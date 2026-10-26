package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// Статусы попытки вызова модели — поле status в журнале (NFR-09).
const (
	StatusOK          = "ok"
	StatusTimeout     = "timeout"
	StatusRateLimited = "rate_limited"  // HTTP 429
	StatusServerError = "server_error"  // HTTP 5xx
	StatusClientError = "client_error"  // прочие HTTP 4xx — скорее всего ошибка конфига
	StatusNetwork     = "network_error" // соединение, DNS, TLS
	StatusBadResponse = "bad_response"  // 2xx, но не ответ Chat Completions или пустой текст
	StatusInvalid     = "invalid"       // ответ не прошёл проверку задачи
	StatusConfigError = "config_error"  // нет ключа API
	StatusCanceled    = "canceled"      // отменён внешний контекст
)

// ErrChainExhausted — ни одна модель цепочки не дала валидный ответ.
var ErrChainExhausted = errors.New("all models in chain failed")

// retryPrompt — сообщение для повтора после невалидного ответа. Оно не зависит
// от задачи, поэтому живёт в коде, а не в prompts/ (ADR 006).
const retryPrompt = "Ответ не прошёл проверку: %v\nИсправь ошибку и верни только JSON целиком, без пояснений."

// invalidError — ответ модели не прошёл проверку задачи; reason показывается модели при повторе.
type invalidError struct{ reason error }

func (e *invalidError) Error() string { return "invalid response: " + e.reason.Error() }
func (e *invalidError) Unwrap() error { return e.reason }

// Attempt — одна попытка вызова модели, включая неудачные (NFR-09).
type Attempt struct {
	Time          time.Time
	Task          string
	ConfigVersion string
	PromptVersion string
	ChainIndex    int
	Retry         bool // повтор той же модели после невалидного ответа
	Provider      string
	Model         string
	Status        string
	Error         string
	Latency       time.Duration
	TokensIn      int
	TokensOut     int
	CostUSD       float64
	Raw           []byte
}

// Result — итог вызова задачи: текст ответа модели, которая справилась, и все попытки.
type Result struct {
	Content  string
	Provider string
	Model    string
	Attempts []Attempt
}

// Validator проверяет текст ответа модели; ошибка отправляет запрос следующей модели.
type Validator func(content string) error

// Registry выбирает модели по задаче и проходит цепочку fallback (NFR-08).
// О предметной области не знает: сообщения и проверку ответа передаёт задача.
type Registry struct {
	cfg  *Config
	http *http.Client
	log  func(Attempt)
}

// NewRegistry создаёт реестр. log получает каждую попытку; nil — без журнала.
// hc == nil — http.Client по умолчанию.
func NewRegistry(cfg *Config, hc *http.Client, log func(Attempt)) *Registry {
	if hc == nil {
		hc = &http.Client{}
	}
	if log == nil {
		log = func(Attempt) {}
	}
	return &Registry{cfg: cfg, http: hc, log: log}
}

// Task возвращает задачу из конфига: задаче нужен текст промпта.
func (r *Registry) Task(name string) (Task, error) {
	return r.cfg.Task(name)
}

// Call проходит цепочку задачи по порядку и возвращает первый валидный ответ.
// Модели вызываются последовательно (architecture.md, раздел 8).
func (r *Registry) Call(ctx context.Context, task string, msgs []Message, validate Validator) (*Result, error) {
	t, err := r.cfg.Task(task)
	if err != nil {
		return &Result{}, err
	}
	res := &Result{}
	var errs []error
	for i := range t.Chain {
		err := r.tryModel(ctx, t, i, msgs, validate, res)
		if err == nil {
			return res, nil
		}
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		errs = append(errs, err)
	}
	return res, fmt.Errorf("task %s: %w: %w", task, ErrChainExhausted, errors.Join(errs...))
}

// CallModel вызывает одну модель цепочки без fallback: для eval по каждой
// модели отдельно (FR-20) и независимой сверки этикетки второй моделью (FR-05).
func (r *Registry) CallModel(ctx context.Context, task string, index int, msgs []Message, validate Validator) (*Result, error) {
	t, err := r.cfg.Task(task)
	if err != nil {
		return &Result{}, err
	}
	if index < 0 || index >= len(t.Chain) {
		return &Result{}, fmt.Errorf("task %s: chain index %d out of range [0, %d)", task, index, len(t.Chain))
	}
	res := &Result{}
	return res, r.tryModel(ctx, t, index, msgs, validate, res)
}

// tryModel вызывает модель и при невалидном ответе повторяет её один раз,
// показав ей её ответ и текст ошибки (NFR-10, architecture.md 5.3).
func (r *Registry) tryModel(ctx context.Context, t Task, i int, msgs []Message, validate Validator, res *Result) error {
	content, err := r.attempt(ctx, t, i, msgs, validate, false, res)
	var ie *invalidError
	if !errors.As(err, &ie) {
		return err
	}
	retry := append(slices.Clip(msgs),
		Message{Role: "assistant", Text: content},
		Message{Role: "user", Text: fmt.Sprintf(retryPrompt, ie.reason)},
	)
	_, err = r.attempt(ctx, t, i, retry, validate, true, res)
	return err
}

// attempt делает один вызов, пишет его в журнал и в res.Attempts;
// при успехе заполняет res.Content, res.Provider и res.Model.
// Возвращает текст ответа и тогда, когда он не прошёл проверку: он нужен для повтора.
func (r *Registry) attempt(ctx context.Context, t Task, i int, msgs []Message, validate Validator, retry bool, res *Result) (content string, err error) {
	e := t.Chain[i]
	a := Attempt{
		Time:          time.Now(),
		Task:          t.Name,
		ConfigVersion: t.Version,
		PromptVersion: t.PromptVersion,
		ChainIndex:    i,
		Retry:         retry,
		Provider:      e.Provider,
		Model:         e.Model,
	}
	defer func() {
		a.Status = classify(ctx, err)
		if err != nil {
			err = fmt.Errorf("%s/%s: %w", e.Provider, e.Model, err)
			a.Error = err.Error()
		}
		res.Attempts = append(res.Attempts, a)
		r.log(a)
	}()

	key, err := r.cfg.Providers[e.Provider].APIKey()
	if err != nil {
		return "", err
	}

	callCtx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	resp, err := NewClient(r.cfg.Providers[e.Provider].BaseURL, key, r.http).Complete(callCtx, Request{
		Model:    e.Model,
		Messages: msgs,
		JSONMode: true,
	})
	a.Latency, a.Raw = resp.Latency, resp.Raw
	a.TokensIn, a.TokensOut = resp.TokensIn, resp.TokensOut
	a.CostUSD = (float64(resp.TokensIn)*e.Price.Input + float64(resp.TokensOut)*e.Price.Output) / 1e6
	if err != nil {
		return "", err
	}
	if validate != nil {
		if verr := validate(resp.Content); verr != nil {
			return resp.Content, &invalidError{reason: verr}
		}
	}
	res.Content, res.Provider, res.Model = resp.Content, e.Provider, e.Model
	return resp.Content, nil
}

// classify сводит ошибку попытки к статусу журнала. Все статусы, кроме
// ok и canceled, ведут к следующей модели (architecture.md, 5.3).
func classify(parent context.Context, err error) string {
	var he *HTTPError
	var ie *invalidError
	switch {
	case err == nil:
		return StatusOK
	case parent.Err() != nil:
		return StatusCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return StatusTimeout
	case errors.As(err, &he):
		switch {
		case he.StatusCode == http.StatusTooManyRequests:
			return StatusRateLimited
		case he.StatusCode >= 500:
			return StatusServerError
		default:
			return StatusClientError
		}
	case errors.As(err, &ie):
		return StatusInvalid
	case errors.Is(err, ErrBadResponse), errors.Is(err, ErrEmptyResponse):
		return StatusBadResponse
	case errors.Is(err, errNoAPIKey):
		return StatusConfigError
	default:
		return StatusNetwork
	}
}
