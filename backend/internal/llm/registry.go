package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"
)

// model call attempt statuses, the status field of the call log (NFR-09)
const (
	StatusOK          = "ok"
	StatusTimeout     = "timeout"
	StatusRateLimited = "rate_limited"  // HTTP 429
	StatusServerError = "server_error"  // HTTP 5xx
	StatusClientError = "client_error"  // other HTTP 4xx, most likely a config error
	StatusNetwork     = "network_error" // connection, DNS, TLS
	StatusBadResponse = "bad_response"  // 2xx but not a Chat Completions response or empty text
	StatusInvalid     = "invalid"       // answer failed the task validation
	StatusConfigError = "config_error"  // no API key
	StatusCanceled    = "canceled"      // parent context canceled
)

// ErrChainExhausted means no chain model gave a valid answer
var ErrChainExhausted = errors.New("all models in chain failed")

// retryPrompt is the message for a retry after an invalid answer; it does not
// depend on the task, so it lives in code rather than prompts/ (ADR 006)
// it is in Russian like the other prompts
const retryPrompt = "Ответ не прошёл проверку: %v\nИсправь ошибку и верни только JSON целиком, без пояснений." //nolint:gosmopolitan // model prompt

// invalidError means the answer failed task validation, reason is shown to the model on retry
type invalidError struct{ reason error }

func (e *invalidError) Error() string { return "invalid response: " + e.reason.Error() }
func (e *invalidError) Unwrap() error { return e.reason }

// Attempt is one model call attempt, failed ones included (NFR-09)
type Attempt struct {
	Time          time.Time
	Task          string
	ConfigVersion string
	PromptVersion string
	ChainIndex    int
	Retry         bool // retry of the same model after an invalid answer
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

// Result is a task call outcome: the answer of the model that succeeded and all attempts
type Result struct {
	Content  string
	Provider string
	Model    string
	Attempts []Attempt
}

// Validator checks the model answer text, an error sends the request to the next model
type Validator func(content string) error

// Registry picks models per task and walks the fallback chain (NFR-08)
// it knows nothing about the domain: the task passes messages and validation
type Registry struct {
	cfg  *Config
	http *http.Client
	log  func(Attempt)
}

// NewRegistry creates a registry; log receives every attempt, nil disables logging
// nil hc uses the default http.Client
func NewRegistry(cfg *Config, hc *http.Client, log func(Attempt)) *Registry {
	if hc == nil {
		hc = &http.Client{}
	}

	if log == nil {
		log = func(Attempt) {}
	}

	return &Registry{cfg: cfg, http: hc, log: log}
}

// Task returns the configured task, tasks need the prompt text
func (r *Registry) Task(name string) (Task, error) {
	return r.cfg.Task(name)
}

// Call walks the task chain in order and returns the first valid answer
// models are called sequentially (architecture.md, section 8)
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
			return res, fmt.Errorf("task %s: %w", task, ctx.Err())
		}

		errs = append(errs, err)
	}

	return res, fmt.Errorf("task %s: %w: %w", task, ErrChainExhausted, errors.Join(errs...))
}

// CallModel calls one chain model without fallback: for eval of each model
// separately (FR-20) and for an independent label cross-check (FR-05)
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

// tryModel calls a model and retries it once after an invalid answer,
// showing it the answer and the error text (NFR-10, architecture.md 5.3)
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

// attempt makes one call and records it in the log and res.Attempts
// on success it fills res.Content, res.Provider and res.Model
// it returns the answer text even when validation fails, the retry needs it
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

// classify maps an attempt error to a log status; every status except ok
// and canceled moves on to the next model (architecture.md 5.3)
func classify(parent context.Context, err error) string {
	var (
		he *HTTPError
		ie *invalidError
	)
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
