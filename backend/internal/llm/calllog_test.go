package llm

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readLog(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("line %q is not JSON: %v", sc.Text(), err)
		}
		lines = append(lines, m)
	}
	return lines
}

func TestJSONLLogWritesAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "nested", "model_calls.jsonl")
	l, err := OpenJSONLLog(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 26, 12, 30, 0, 0, time.FixedZone("MSK", 3*3600))
	l.Log(Attempt{
		Time: at, Task: "label_extraction", ConfigVersion: "abc123", PromptVersion: "label_v1",
		ChainIndex: 1, Retry: true, Provider: "openrouter", Model: "m:free", Status: StatusOK,
		Latency: 1500 * time.Millisecond, TokensIn: 300, TokensOut: 40, CostUSD: 0.0001,
		Raw: []byte(`{"choices":[{"message":{"content":"<ok> & \"кола\""}}]}`),
	})
	l.Log(Attempt{Task: "label_extraction", Status: StatusRateLimited, Error: "openrouter/m: HTTP 429"})
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readLog(t, path)
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	first := lines[0]
	want := map[string]any{
		"time": "2026-10-26T09:30:00Z", "task": "label_extraction", "config_version": "abc123",
		"prompt_version": "label_v1", "chain_index": 1.0, "retry": true, "provider": "openrouter",
		"model": "m:free", "status": "ok", "latency_ms": 1500.0, "tokens_in": 300.0, "tokens_out": 40.0,
		"cost_usd": 0.0001, "raw_response": `{"choices":[{"message":{"content":"<ok> & \"кола\""}}]}`,
	}
	for k, v := range want {
		if first[k] != v {
			t.Errorf("%s = %#v, want %#v", k, first[k], v)
		}
	}
	if _, ok := first["error"]; ok {
		t.Error("error must be omitted for successful attempt")
	}
	if lines[1]["status"] != "rate_limited" || lines[1]["error"] != "openrouter/m: HTTP 429" {
		t.Errorf("second line = %v", lines[1])
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %v, want 0600", perm)
	}
}

func TestJSONLLogAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model_calls.jsonl")
	for i := range 2 {
		l, err := OpenJSONLLog(path)
		if err != nil {
			t.Fatal(err)
		}
		l.Log(Attempt{Task: "t", ChainIndex: i})
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
	}
	lines := readLog(t, path)
	if len(lines) != 2 || lines[1]["chain_index"] != 1.0 {
		t.Errorf("lines = %v, want two appended records", lines)
	}
}

func TestJSONLLogKeepsWriteError(t *testing.T) {
	l, err := OpenJSONLLog(filepath.Join(t.TempDir(), "model_calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	l.f.Close() // запись в закрытый файл падает
	l.Log(Attempt{Task: "t"})
	if l.Err() == nil {
		t.Fatal("Err: want write error")
	}
	if err := l.Close(); err == nil {
		t.Error("Close: want first write error")
	}
}

func TestRegistryWritesJSONLLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model_calls.jsonl")
	l, err := OpenJSONLLog(path)
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := newTestRegistry(t,
		&stubProvider{name: "a", handler: respond(503, "down")},
		&stubProvider{name: "b", handler: respond(200, chatBody("ok"))},
	)
	reg.log = l.Log

	if _, err := reg.Call(t.Context(), "task", userMsg, nil); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	lines := readLog(t, path)
	if len(lines) != 2 || lines[0]["status"] != "server_error" || lines[0]["raw_response"] != "down" || lines[1]["status"] != "ok" {
		t.Errorf("lines = %v", lines)
	}
}
