package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	got, err := parseDotEnv([]byte(`
# provider keys
GEMINI_API_KEY=abc
export GROQ_API_KEY = def
MISTRAL_API_KEY="with # hash"
OPENROUTER_API_KEY='single'
EMPTY=
COMMENTED=value # explanation
`))
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"GEMINI_API_KEY": "abc", "GROQ_API_KEY": "def", "MISTRAL_API_KEY": "with # hash",
		"OPENROUTER_API_KEY": "single", "EMPTY": "", "COMMENTED": "value",
	}
	if len(got) != len(want) {
		t.Errorf("got %v", got)
	}

	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	if _, err := parseDotEnv([]byte("no equals sign")); err == nil {
		t.Error("want error for line without =")
	}
}

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("FOODY_T_NEW=file\nFOODY_T_SET=file\nFOODY_T_EMPTY=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("FOODY_T_SET", "shell")
	t.Setenv("FOODY_T_EMPTY", "")
	t.Setenv("FOODY_T_NEW", "")
	os.Unsetenv("FOODY_T_NEW")

	if err := loadDotEnv(path); err != nil {
		t.Fatal(err)
	}

	for k, v := range map[string]string{"FOODY_T_NEW": "file", "FOODY_T_SET": "shell", "FOODY_T_EMPTY": "file"} {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}

	if err := loadDotEnv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("missing file: %v", err)
	}
}
