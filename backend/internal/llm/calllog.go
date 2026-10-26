package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// callRecord — строка журнала вызовов; поля повторяют таблицу model_calls (NFR-09).
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

// JSONLLog дописывает попытки вызова моделей в JSONL-файл, по строке на попытку.
// На этапе 2 его заменит таблица model_calls.
type JSONLLog struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
	err error
}

// OpenJSONLLog открывает журнал на дозапись, создавая каталоги. Права 0600:
// в сырых ответах описания еды пользователя.
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

// Log пишет попытку; подходит как колбэк NewRegistry. Ошибка записи не
// прерывает вызов модели: первая сохраняется и возвращается из Err и Close.
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

// Err возвращает первую ошибку записи.
func (l *JSONLLog) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.err
}

// Close закрывает файл и возвращает первую ошибку записи или закрытия.
func (l *JSONLLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.f.Close(); err != nil && l.err == nil {
		l.err = fmt.Errorf("close call log: %w", err)
	}
	return l.err
}
