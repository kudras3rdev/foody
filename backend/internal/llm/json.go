package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// DecodeJSON строго разбирает ответ модели в T (NFR-10): ровно один JSON-объект,
// неизвестные поля — ошибка. Если *T реализует Validate() error, проверяются и
// правила задачи. Допускается одна обёртка ```json … ```, которую модели
// добавляют даже в JSON-режиме.
func DecodeJSON[T any](content string) (*T, error) {
	dec := json.NewDecoder(strings.NewReader(stripFence(content)))
	dec.DisallowUnknownFields()
	var v T
	if err := dec.Decode(&v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("empty response, want JSON object")
		}
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected data after JSON object")
	}
	if val, ok := any(&v).(interface{ Validate() error }); ok {
		if err := val.Validate(); err != nil {
			return nil, err
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
		s = s[nl+1:] // строка языка после ```: "json"
	}
	return strings.TrimSpace(s)
}

// CallJSON вызывает задачу по цепочке и возвращает ответ, разобранный в T.
func CallJSON[T any](ctx context.Context, r *Registry, task string, msgs []Message) (*T, *Result, error) {
	var out *T
	res, err := r.Call(ctx, task, msgs, decodeInto(&out))
	return out, res, err
}

// CallModelJSON — CallJSON для одной модели цепочки без fallback.
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
