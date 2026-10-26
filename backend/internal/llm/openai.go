package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxResponseBytes limits the provider response body: the VPS has 2 GB RAM (NFR-11)
const maxResponseBytes = 4 << 20

// Message is a chat message, images go only in user messages
type Message struct {
	Role   string // "system", "user" or "assistant"
	Text   string
	Images []Image
}

type Image struct {
	MIME string // e.g. "image/jpeg"
	Data []byte
}

type Request struct {
	Model       string
	Messages    []Message
	JSONMode    bool     // response_format: json_object
	Temperature *float64 // nil uses the provider default
	MaxTokens   int      // 0 means no limit
}

// Response is a call result; Complete returns it on errors too because
// the latency and raw response go to the call log (NFR-09)
type Response struct {
	Content      string
	FinishReason string
	TokensIn     int
	TokensOut    int
	Latency      time.Duration
	Raw          []byte // HTTP response body as is
}

// HTTPError is a provider response with a non-2xx status
type HTTPError struct {
	StatusCode int
	Body       []byte
	RetryAfter time.Duration // from the Retry-After header, 0 when absent
}

func (e *HTTPError) Error() string {
	body := string(e.Body)
	if len(body) > 300 {
		body = body[:300] + "…"
	}

	return fmt.Sprintf("provider returned HTTP %d: %s", e.StatusCode, body)
}

var (
	// ErrBadResponse means HTTP 2xx but the body is not a Chat Completions response
	ErrBadResponse = errors.New("bad provider response")
	// ErrEmptyResponse means HTTP 2xx but the answer has no text
	ErrEmptyResponse = errors.New("provider returned no content")
)

// Client calls an OpenAI-compatible Chat Completions API (D-06)
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient creates a client; the call context sets the timeout, nil hc uses the default http.Client
func NewClient(baseURL, apiKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{}
	}

	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, http: hc}
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []contentPart
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content *string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	body, err := json.Marshal(buildChatRequest(req))
	if err != nil {
		return &Response{}, fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return &Response{}, fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	start := time.Now()

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return &Response{Latency: time.Since(start)}, fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseBytes+1))

	resp := &Response{Latency: time.Since(start), Raw: raw}
	if err != nil {
		return resp, fmt.Errorf("read response: %w", err)
	}

	if len(raw) > maxResponseBytes {
		return resp, fmt.Errorf("%w: response exceeds %d bytes", ErrBadResponse, maxResponseBytes)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode > 299 {
		return resp, &HTTPError{
			StatusCode: httpResp.StatusCode,
			Body:       raw,
			RetryAfter: parseRetryAfter(httpResp.Header.Get("Retry-After")),
		}
	}

	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return resp, fmt.Errorf("%w: decode: %w", ErrBadResponse, err)
	}

	resp.TokensIn = cr.Usage.PromptTokens

	resp.TokensOut = cr.Usage.CompletionTokens
	if len(cr.Choices) == 0 || cr.Choices[0].Message.Content == nil || *cr.Choices[0].Message.Content == "" {
		return resp, ErrEmptyResponse
	}

	resp.Content = *cr.Choices[0].Message.Content
	resp.FinishReason = cr.Choices[0].FinishReason

	return resp, nil
}

func buildChatRequest(req Request) chatRequest {
	cr := chatRequest{
		Model:       req.Model,
		Messages:    make([]chatMessage, 0, len(req.Messages)),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}
	if req.JSONMode {
		cr.ResponseFormat = &responseFormat{Type: "json_object"}
	}

	for _, m := range req.Messages {
		// text without images goes as a string, every provider accepts that
		if len(m.Images) == 0 {
			cr.Messages = append(cr.Messages, chatMessage{Role: m.Role, Content: m.Text})
			continue
		}

		parts := make([]contentPart, 0, len(m.Images)+1)
		if m.Text != "" {
			parts = append(parts, contentPart{Type: "text", Text: m.Text})
		}

		for _, img := range m.Images {
			url := "data:" + img.MIME + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
			parts = append(parts, contentPart{Type: "image_url", ImageURL: &imageURL{URL: url}})
		}

		cr.Messages = append(cr.Messages, chatMessage{Role: m.Role, Content: parts})
	}

	return cr
}

// parseRetryAfter understands seconds only, LLM providers do not send dates
func parseRetryAfter(v string) time.Duration {
	s, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || s < 0 {
		return 0
	}

	return time.Duration(s) * time.Second
}
