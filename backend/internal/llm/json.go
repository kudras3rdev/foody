package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// DecodeJSON strictly decodes a model answer into T (NFR-10): exactly one JSON
// object, unknown fields are errors; when *T implements Validate() error the
// task rules are checked too; one ```json ... ``` fence is allowed because
// models add it even in JSON mode
func DecodeJSON[T any](content string) (*T, error) {
	dec := json.NewDecoder(strings.NewReader(stripFence(content)))
	dec.DisallowUnknownFields()

	var v T
	if err := dec.Decode(&v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty response, want JSON object")
		}

		return nil, fmt.Errorf("decode: %w", err)
	}

	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after JSON object")
	}

	if val, ok := any(&v).(interface{ Validate() error }); ok {
		if err := val.Validate(); err != nil {
			return nil, fmt.Errorf("validate: %w", err)
		}
	}

	return &v, nil
}

func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") || !strings.HasSuffix(s, "```") || len(s) < 6 {
		return s
	}

	s = strings.TrimSuffix(s[3:], "```")
	if nl := strings.IndexByte(s, '\n'); nl >= 0 && !strings.ContainsAny(s[:nl], "{[") {
		s = s[nl+1:] // language tag after ```, e.g. "json"
	}

	return strings.TrimSpace(s)
}

// CallJSON calls the task chain and returns the answer decoded into T
func CallJSON[T any](ctx context.Context, r *Registry, task string, msgs []Message) (*T, *Result, error) {
	var out *T

	res, err := r.Call(ctx, task, msgs, decodeInto(&out))

	return out, res, err
}

// CallModelJSON is CallJSON for one chain model without fallback
func CallModelJSON[T any](ctx context.Context, r *Registry, task string, index int, msgs []Message) (*T, *Result, error) {
	var out *T

	res, err := r.CallModel(ctx, task, index, msgs, decodeInto(&out))

	return out, res, err
}

func decodeInto[T any](out **T) Validator {
	return func(content string) error {
		v, err := DecodeJSON[T](content)
		if err != nil {
			return err
		}

		*out = v

		return nil
	}
}
