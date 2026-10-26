package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// callRecord is one call log line, fields mirror the model_calls table (NFR-09)
type callRecord struct {
	Time          time.Time `json:"time"`
	Task          string    `json:"task"`
	ConfigVersion string    `json:"config_version"`
	PromptVersion string    `json:"prompt_version"`
	ChainIndex    int       `json:"chain_index"`
	Retry         bool      `json:"retry"`
	Provider      string    `json:"provider"`
	Model         string    `json:"model"`
	Status        string    `json:"status"`
	Error         string    `json:"error,omitempty"`
	LatencyMS     int64     `json:"latency_ms"`
	TokensIn      int       `json:"tokens_in"`
	TokensOut     int       `json:"tokens_out"`
	CostUSD       float64   `json:"cost_usd"`
	RawResponse   string    `json:"raw_response"`
}

// JSONLLog appends model call attempts to a JSONL file, one line per attempt
// the model_calls table replaces it at stage 2
type JSONLLog struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
	err error
}

// OpenJSONLLog opens the log for appending and creates directories
// file mode is 0600 because raw responses describe the user's food
func OpenJSONLLog(path string) (*JSONLLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create call log dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open call log: %w", err)
	}

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)

	return &JSONLLog{f: f, enc: enc}, nil
}

// Log writes an attempt and fits as the NewRegistry callback
// a write error does not stop the model call: the first one is kept and
// returned from Err and Close
func (l *JSONLLog) Log(a Attempt) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.err != nil {
		return
	}

	err := l.enc.Encode(callRecord{
		Time:          a.Time.UTC(),
		Task:          a.Task,
		ConfigVersion: a.ConfigVersion,
		PromptVersion: a.PromptVersion,
		ChainIndex:    a.ChainIndex,
		Retry:         a.Retry,
		Provider:      a.Provider,
		Model:         a.Model,
		Status:        a.Status,
		Error:         a.Error,
		LatencyMS:     a.Latency.Milliseconds(),
		TokensIn:      a.TokensIn,
		TokensOut:     a.TokensOut,
		CostUSD:       a.CostUSD,
		RawResponse:   string(a.Raw),
	})
	if err != nil {
		l.err = fmt.Errorf("write call log: %w", err)
	}
}

// Err returns the first write error
func (l *JSONLLog) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.err
}

// Close closes the file and returns the first write or close error
func (l *JSONLLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.f.Close(); err != nil && l.err == nil {
		l.err = fmt.Errorf("close call log: %w", err)
	}

	return l.err
}
