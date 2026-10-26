package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const okResponse = `{
  "choices": [{"message": {"role": "assistant", "content": "{\"items\": []}"}, "finish_reason": "stop"}],
  "usage": {"prompt_tokens": 120, "completion_tokens": 8}
}`

// fakeProvider answers with status and body and keeps the last request
func fakeProvider(t *testing.T, status int, body string, header http.Header) (*httptest.Server, *capturedRequest) {
	t.Helper()

	got := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Path = r.URL.Path
		got.Auth = r.Header.Get("Authorization")
		got.Body, _ = io.ReadAll(r.Body)

		maps.Copy(w.Header(), header)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return srv, got
}

type capturedRequest struct {
	Path string
	Auth string
	Body []byte
}

func TestCompleteSuccess(t *testing.T) {
	srv, got := fakeProvider(t, http.StatusOK, okResponse, nil)
	temp := 0.0
	c := NewClient(srv.URL+"/v1/", "secret", nil)

	resp, err := c.Complete(context.Background(), Request{
		Model: "flash",
		Messages: []Message{
			{Role: "system", Text: "Ты — счётчик КБЖУ."},
			{Role: "user", Text: "что на фото?", Images: []Image{{MIME: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff}}}},
		},
		JSONMode:    true,
		Temperature: &temp,
		MaxTokens:   500,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if got.Path != "/v1/chat/completions" {
		t.Errorf("path = %q", got.Path)
	}

	if got.Auth != "Bearer secret" {
		t.Errorf("Authorization = %q", got.Auth)
	}

	want := `{"model":"flash","messages":[` +
		`{"role":"system","content":"Ты — счётчик КБЖУ."},` +
		`{"role":"user","content":[{"type":"text","text":"что на фото?"},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,/9j/"}}]}],` +
		`"response_format":{"type":"json_object"},"temperature":0,"max_tokens":500}`
	if string(got.Body) != want {
		t.Errorf("request body\n got: %s\nwant: %s", got.Body, want)
	}

	if resp.Content != `{"items": []}` || resp.FinishReason != "stop" {
		t.Errorf("content = %q, finish = %q", resp.Content, resp.FinishReason)
	}

	if resp.TokensIn != 120 || resp.TokensOut != 8 {
		t.Errorf("tokens = %d/%d", resp.TokensIn, resp.TokensOut)
	}

	if string(resp.Raw) != okResponse {
		t.Errorf("Raw = %q", resp.Raw)
	}

	if resp.Latency <= 0 {
		t.Errorf("Latency = %v", resp.Latency)
	}
}

func TestCompleteOmitsOptionalFields(t *testing.T) {
	srv, got := fakeProvider(t, http.StatusOK, okResponse, nil)

	_, err := NewClient(srv.URL, "k", nil).Complete(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: "user", Text: "привет"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	var body map[string]any
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatal(err)
	}

	for _, k := range []string{"response_format", "temperature", "max_tokens"} {
		if _, ok := body[k]; ok {
			t.Errorf("field %q must be omitted, body = %s", k, got.Body)
		}
	}
}

func TestCompleteHTTPError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		header     http.Header
		retryAfter time.Duration
	}{
		{name: "rate limit", status: http.StatusTooManyRequests, header: http.Header{"Retry-After": {"7"}}, retryAfter: 7 * time.Second},
		{name: "server error", status: http.StatusServiceUnavailable},
		{name: "bad request", status: http.StatusBadRequest, header: http.Header{"Retry-After": {"Wed, 21 Oct 2026 07:28:00 GMT"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"error":{"message":"nope"}}`
			srv, _ := fakeProvider(t, tt.status, body, tt.header)

			resp, err := NewClient(srv.URL, "k", nil).Complete(context.Background(), Request{Model: "m"})

			var he *HTTPError
			if !errors.As(err, &he) {
				t.Fatalf("err = %v, want *HTTPError", err)
			}

			if he.StatusCode != tt.status || string(he.Body) != body || he.RetryAfter != tt.retryAfter {
				t.Errorf("HTTPError = %+v", he)
			}

			if resp == nil || string(resp.Raw) != body || resp.Latency <= 0 {
				t.Errorf("response for log = %+v", resp)
			}
		})
	}
}

func TestCompleteBadBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
	}{
		{name: "no choices", body: `{"choices":[]}`, want: ErrEmptyResponse},
		{name: "null content", body: `{"choices":[{"message":{"content":null}}]}`, want: ErrEmptyResponse},
		{name: "not json", body: `<html>bad gateway</html>`, want: ErrBadResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeProvider(t, http.StatusOK, tt.body, nil)

			resp, err := NewClient(srv.URL, "k", nil).Complete(context.Background(), Request{Model: "m"})
			if err == nil {
				t.Fatal("want error")
			}

			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}

			if string(resp.Raw) != tt.body {
				t.Errorf("Raw = %q, want body kept for log", resp.Raw)
			}
		})
	}
}

func TestCompleteTooLarge(t *testing.T) {
	srv, _ := fakeProvider(t, http.StatusOK, strings.Repeat("x", maxResponseBytes+10), nil)

	_, err := NewClient(srv.URL, "k", nil).Complete(context.Background(), Request{Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("err = %v, want size error", err)
	}
}

func TestCompleteContextTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs before srv.Close

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	resp, err := NewClient(srv.URL, "k", nil).Complete(ctx, Request{Model: "m"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}

	if resp.Latency < 50*time.Millisecond {
		t.Errorf("Latency = %v", resp.Latency)
	}
}

func TestProviderAPIKey(t *testing.T) {
	p := Provider{Name: "gemini", APIKeyEnv: "FOODY_TEST_KEY"}

	t.Setenv("FOODY_TEST_KEY", "")

	if _, err := p.APIKey(); err == nil {
		t.Error("want error for unset key")
	}

	t.Setenv("FOODY_TEST_KEY", "abc")

	if k, err := p.APIKey(); err != nil || k != "abc" {
		t.Errorf("APIKey = %q, %v", k, err)
	}
}
