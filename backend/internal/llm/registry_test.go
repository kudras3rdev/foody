package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func chatBody(content string) string {
	return fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10}}`, content)
}

// stubProvider — фейковый провайдер; hits считает обращения к нему.
type stubProvider struct {
	name    string
	handler http.HandlerFunc
	timeout time.Duration
	price   Price
	noKey   bool
	hits    atomic.Int32
}

func respond(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

// hang держит запрос, пока клиент не отключится. Тело нужно дочитать:
// только после этого сервер замечает обрыв соединения и отменяет r.Context().
func hang(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	<-r.Context().Done()
}

// newTestRegistry собирает конфиг с одной задачей "task", звено цепочки на каждого провайдера.
// Провайдер без handler указывает на закрытый сервер — сетевая ошибка.
func newTestRegistry(t *testing.T, stubs ...*stubProvider) (*Registry, *[]Attempt) {
	t.Helper()
	cfg := &Config{Providers: map[string]Provider{}, Tasks: map[string]Task{}}
	task := Task{Name: "task", PromptVersion: "task_v1", Version: "abc123"}
	for _, s := range stubs {
		var url string
		if s.handler == nil {
			srv := httptest.NewServer(http.NotFoundHandler())
			url = srv.URL
			srv.Close()
		} else {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				s.hits.Add(1)
				s.handler(w, r)
			}))
			t.Cleanup(srv.Close)
			url = srv.URL
		}
		env := "FOODY_TEST_KEY_" + strings.ToUpper(s.name)
		if s.noKey {
			t.Setenv(env, "")
		} else {
			t.Setenv(env, "key-"+s.name)
		}
		cfg.Providers[s.name] = Provider{Name: s.name, BaseURL: url, APIKeyEnv: env}
		timeout := s.timeout
		if timeout == 0 {
			timeout = 5 * time.Second
		}
		task.Chain = append(task.Chain, ChainEntry{Provider: s.name, Model: "model-" + s.name, Timeout: timeout, Price: s.price})
	}
	cfg.Tasks["task"] = task

	var logged []Attempt
	return NewRegistry(cfg, nil, func(a Attempt) { logged = append(logged, a) }), &logged
}

var userMsg = []Message{{Role: "user", Text: "привет"}}

func rejectBad(content string) error {
	if content == "bad" {
		return errors.New("field items is required")
	}
	return nil
}

func TestCallFirstModelSucceeds(t *testing.T) {
	var body []byte
	first := &stubProvider{name: "a", price: Price{Input: 1, Output: 2}, handler: func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		io.WriteString(w, chatBody(`{"ok":true}`))
	}}
	second := &stubProvider{name: "b", handler: respond(200, chatBody("unused"))}
	reg, logged := newTestRegistry(t, first, second)

	res, err := reg.Call(context.Background(), "task", userMsg, rejectBad)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Content != `{"ok":true}` || res.Provider != "a" || res.Model != "model-a" {
		t.Errorf("result = %+v", res)
	}
	if second.hits.Load() != 0 {
		t.Error("second model called after success")
	}
	if !strings.Contains(string(body), `"response_format":{"type":"json_object"}`) || !strings.Contains(string(body), `"model":"model-a"`) {
		t.Errorf("request body = %s", body)
	}

	if len(*logged) != 1 || len(res.Attempts) != 1 {
		t.Fatalf("logged %d, attempts %d, want 1", len(*logged), len(res.Attempts))
	}
	a := (*logged)[0]
	if a.Status != StatusOK || a.Error != "" || a.Task != "task" || a.PromptVersion != "task_v1" || a.ConfigVersion != "abc123" || a.ChainIndex != 0 {
		t.Errorf("attempt = %+v", a)
	}
	if a.TokensIn != 100 || a.TokensOut != 10 || len(a.Raw) == 0 || a.Latency <= 0 || a.Time.IsZero() {
		t.Errorf("attempt metrics = %+v", a)
	}
	if want := (100*1.0 + 10*2.0) / 1e6; a.CostUSD != want {
		t.Errorf("CostUSD = %g, want %g", a.CostUSD, want)
	}
}

func TestCallFallsBackThroughFailures(t *testing.T) {
	stubs := []*stubProvider{
		{name: "nokey", noKey: true, handler: respond(200, chatBody("unused"))},
		{name: "ratelimit", handler: respond(429, `{"error":"slow down"}`)},
		{name: "server", handler: respond(503, `{"error":"overloaded"}`)},
		{name: "client", handler: respond(400, `{"error":"unknown model"}`)},
		{name: "network"},
		{name: "notjson", handler: respond(200, `<html>oops</html>`)},
		{name: "empty", handler: respond(200, `{"choices":[]}`)},
		{name: "invalid", handler: respond(200, chatBody("bad"))},
		{name: "slow", timeout: 50 * time.Millisecond, handler: hang},
		{name: "good", handler: respond(200, chatBody("good"))},
	}
	reg, logged := newTestRegistry(t, stubs...)

	res, err := reg.Call(context.Background(), "task", userMsg, rejectBad)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.Content != "good" || res.Provider != "good" {
		t.Errorf("result = %+v", res)
	}
	if stubs[0].hits.Load() != 0 {
		t.Error("provider without key must not be called")
	}

	// Невалидный ответ повторяется той же моделью один раз, поэтому у "invalid" две попытки.
	want := []struct {
		status string
		index  int
		retry  bool
	}{
		{StatusConfigError, 0, false}, {StatusRateLimited, 1, false}, {StatusServerError, 2, false},
		{StatusClientError, 3, false}, {StatusNetwork, 4, false}, {StatusBadResponse, 5, false},
		{StatusBadResponse, 6, false}, {StatusInvalid, 7, false}, {StatusInvalid, 7, true},
		{StatusTimeout, 8, false}, {StatusOK, 9, false},
	}
	if len(*logged) != len(want) {
		t.Fatalf("logged %d attempts, want %d", len(*logged), len(want))
	}
	for i, a := range *logged {
		if w := want[i]; a.Status != w.status || a.ChainIndex != w.index || a.Retry != w.retry {
			t.Errorf("attempt %d (%s): status %q, index %d, retry %v; want %+v", i, a.Provider, a.Status, a.ChainIndex, a.Retry, w)
		}
		if a.Status != StatusOK && !strings.HasPrefix(a.Error, a.Provider+"/"+a.Model+": ") {
			t.Errorf("attempt %d: error %q lacks model prefix", i, a.Error)
		}
	}
	if inv := (*logged)[7]; !strings.Contains(inv.Error, "field items is required") || string(inv.Raw) != chatBody("bad") {
		t.Errorf("invalid attempt must keep reason and raw response: %+v", inv)
	}
}

func TestCallChainExhausted(t *testing.T) {
	reg, logged := newTestRegistry(t,
		&stubProvider{name: "a", handler: respond(500, "boom")},
		&stubProvider{name: "b", handler: respond(200, chatBody("bad"))},
	)

	res, err := reg.Call(context.Background(), "task", userMsg, rejectBad)
	if !errors.Is(err, ErrChainExhausted) {
		t.Fatalf("err = %v, want ErrChainExhausted", err)
	}
	for _, s := range []string{"a/model-a", "HTTP 500", "b/model-b", "field items is required"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not mention %q", err, s)
		}
	}
	if len(res.Attempts) != 3 || len(*logged) != 3 || res.Content != "" {
		t.Errorf("attempts %d, logged %d, content %q", len(res.Attempts), len(*logged), res.Content)
	}
}

func TestCallStopsOnParentCancel(t *testing.T) {
	second := &stubProvider{name: "b", handler: respond(200, chatBody("unused"))}
	reg, logged := newTestRegistry(t, &stubProvider{name: "a", handler: hang}, second)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := reg.Call(ctx, "task", userMsg, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want parent deadline", err)
	}
	if second.hits.Load() != 0 {
		t.Error("chain continued after parent context was done")
	}
	if len(*logged) != 1 || (*logged)[0].Status != StatusCanceled {
		t.Errorf("logged = %+v, want one canceled attempt", *logged)
	}
}

func TestCallModel(t *testing.T) {
	first := &stubProvider{name: "a", handler: respond(503, "down")}
	second := &stubProvider{name: "b", handler: respond(200, chatBody("from b"))}
	reg, logged := newTestRegistry(t, first, second)

	res, err := reg.CallModel(context.Background(), "task", 1, userMsg, nil)
	if err != nil || res.Content != "from b" || first.hits.Load() != 0 {
		t.Errorf("CallModel(1) = %+v, %v; first hits %d", res, err, first.hits.Load())
	}

	res, err = reg.CallModel(context.Background(), "task", 0, userMsg, nil)
	if err == nil || len(res.Attempts) != 1 || second.hits.Load() != 1 {
		t.Errorf("CallModel(0) must fail without fallback: %+v, %v", res, err)
	}
	if len(*logged) != 2 {
		t.Errorf("logged %d attempts, want 2", len(*logged))
	}

	if _, err := reg.CallModel(context.Background(), "task", 2, userMsg, nil); err == nil {
		t.Error("want out of range error")
	}
}

func TestCallUnknownTask(t *testing.T) {
	reg, _ := newTestRegistry(t, &stubProvider{name: "a", handler: respond(200, chatBody("x"))})
	if _, err := reg.Call(context.Background(), "nope", userMsg, nil); err == nil {
		t.Error("Call: want error for unknown task")
	}
	if _, err := reg.CallModel(context.Background(), "nope", 0, userMsg, nil); err == nil {
		t.Error("CallModel: want error for unknown task")
	}
}

func TestCallRetriesInvalidOnceWithError(t *testing.T) {
	var bodies []string
	first := &stubProvider{name: "a", handler: func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			io.WriteString(w, chatBody("bad"))
			return
		}
		io.WriteString(w, chatBody("fixed"))
	}}
	second := &stubProvider{name: "b", handler: respond(200, chatBody("unused"))}
	reg, logged := newTestRegistry(t, first, second)

	res, err := reg.Call(context.Background(), "task", userMsg, rejectBad)
	if err != nil || res.Content != "fixed" || res.Provider != "a" {
		t.Fatalf("Call = %+v, %v", res, err)
	}
	if second.hits.Load() != 0 {
		t.Error("next model called although retry succeeded")
	}
	if len(*logged) != 2 || (*logged)[0].Status != StatusInvalid || !(*logged)[1].Retry || (*logged)[1].Status != StatusOK {
		t.Errorf("logged = %+v", *logged)
	}

	var retry struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(bodies[1]), &retry); err != nil {
		t.Fatal(err)
	}
	m := retry.Messages
	if len(m) != 3 || m[0].Content != "привет" || m[1].Role != "assistant" || m[1].Content != "bad" ||
		m[2].Role != "user" || !strings.Contains(m[2].Content, "field items is required") {
		t.Errorf("retry messages = %+v", m)
	}
	if len(userMsg) != 1 {
		t.Error("caller's messages were modified")
	}
}

func TestCallModelRetriesInvalid(t *testing.T) {
	reg, logged := newTestRegistry(t, &stubProvider{name: "a", handler: respond(200, chatBody("bad"))})
	_, err := reg.CallModel(context.Background(), "task", 0, userMsg, rejectBad)
	if err == nil || len(*logged) != 2 || !(*logged)[1].Retry {
		t.Errorf("err = %v, logged = %+v; want failed retry", err, *logged)
	}
}

func TestCallJSON(t *testing.T) {
	type answer struct {
		Items []string `json:"items"`
	}
	reg, _ := newTestRegistry(t,
		&stubProvider{name: "a", handler: respond(200, chatBody(`{"items": ["a"], "extra": 1}`))},
		&stubProvider{name: "b", handler: respond(200, chatBody("```json\n{\"items\": [\"b\"]}\n```"))},
	)

	// У "a" лишнее поле в обеих попытках → "b" в обёртке ```json.
	got, res, err := CallJSON[answer](context.Background(), reg, "task", userMsg)
	if err != nil {
		t.Fatalf("CallJSON: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0] != "b" || res.Provider != "b" {
		t.Errorf("got %+v from %s", got, res.Provider)
	}

	got, _, err = CallModelJSON[answer](context.Background(), reg, "task", 0, userMsg)
	if err == nil || got != nil || !strings.Contains(err.Error(), `unknown field "extra"`) {
		t.Errorf("CallModelJSON(0) = %+v, %v", got, err)
	}
}
