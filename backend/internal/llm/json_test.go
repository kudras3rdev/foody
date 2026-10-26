package llm

import (
	"errors"
	"strings"
	"testing"
)

type sample struct {
	Name  string   `json:"name"`
	Kcal  *float64 `json:"kcal"`
	Items []string `json:"items"`
}

func (s *sample) Validate() error {
	if s.Kcal == nil {
		return errors.New("kcal is required")
	}
	if *s.Kcal < 0 {
		return errors.New("kcal must be >= 0")
	}
	return nil
}

func TestDecodeJSONValid(t *testing.T) {
	for name, content := range map[string]string{
		"plain":             `{"name": "кола", "kcal": 42, "items": ["a"]}`,
		"spaces":            "\n  {\"name\": \"кола\", \"kcal\": 42, \"items\": [\"a\"]}  \n",
		"fence json":        "```json\n{\"name\": \"кола\", \"kcal\": 42, \"items\": [\"a\"]}\n```",
		"fence no language": "```\n{\"name\": \"кола\", \"kcal\": 42, \"items\": [\"a\"]}\n```",
		"fence one line":    "```{\"name\": \"кола\", \"kcal\": 42, \"items\": [\"a\"]}```",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeJSON[sample](content)
			if err != nil {
				t.Fatalf("DecodeJSON: %v", err)
			}
			if got.Name != "кола" || *got.Kcal != 42 || len(got.Items) != 1 {
				t.Errorf("got %+v", got)
			}
		})
	}
}

func TestDecodeJSONInvalid(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "  ", "empty response"},
		{"not json", "Конечно! Вот ответ: кола", "invalid character"},
		{"unknown field", `{"name": "кола", "kcal": 42, "sugar": 10}`, `unknown field "sugar"`},
		{"wrong type", `{"name": "кола", "kcal": "42"}`, "cannot unmarshal string"},
		{"trailing data", `{"name": "кола", "kcal": 42} и ещё текст`, "unexpected data after JSON"},
		{"two objects", `{"kcal": 1} {"kcal": 2}`, "unexpected data after JSON"},
		{"truncated", `{"name": "кола", "kcal": 4`, "unexpected EOF"},
		{"validate missing", `{"name": "кола"}`, "kcal is required"},
		{"validate range", `{"name": "кола", "kcal": -1}`, "kcal must be >= 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeJSON[sample](tt.content)
			if err == nil {
				t.Fatalf("want error, got %+v", got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %q, want %q", err, tt.want)
			}
		})
	}
}

func TestDecodeJSONWithoutValidate(t *testing.T) {
	got, err := DecodeJSON[struct {
		A int `json:"a"`
	}](`{"a": 1}`)
	if err != nil || got.A != 1 {
		t.Errorf("got %+v, %v", got, err)
	}
}
