package llm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const validConfig = `
providers:
  gemini: { base_url: "https://generativelanguage.googleapis.com/v1beta/openai", api_key_env: GEMINI_API_KEY }
  mistral: { base_url: "https://api.mistral.ai/v1", api_key_env: MISTRAL_API_KEY }

tasks:
  label_extraction:
    prompt: ../prompts/label_v1.md
    cross_check: true
    chain:
      - { provider: gemini,  model: flash, timeout: 20s, price: { input: 0.1, output: 0.4 } }
      - { provider: mistral, model: vision }
  meal_recognition:
    prompt: ../prompts/meal_v1.md
    chain:
      - { provider: gemini, model: flash-lite }
`

// writeConfig раскладывает конфиг и промпты как в backend/: config/models.yaml и prompts/*.md.
func writeConfig(t *testing.T, config string, prompts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{"config", "prompts"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range prompts {
		if err := os.WriteFile(filepath.Join(dir, "prompts", name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "config", "models.yaml")
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

var defaultPrompts = map[string]string{
	"label_v1.md": "Извлеки КБЖУ с этикетки.",
	"meal_v1.md":  "Опознай еду.",
}

func TestLoadConfigValid(t *testing.T) {
	path := writeConfig(t, validConfig, defaultPrompts)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if p := cfg.Providers["mistral"]; p.BaseURL != "https://api.mistral.ai/v1" || p.APIKeyEnv != "MISTRAL_API_KEY" {
		t.Errorf("provider mistral = %+v", p)
	}

	label, err := cfg.Task("label_extraction")
	if err != nil {
		t.Fatal(err)
	}
	if label.PromptVersion != "label_v1" {
		t.Errorf("PromptVersion = %q, want label_v1", label.PromptVersion)
	}
	if want := filepath.Join(filepath.Dir(path), "..", "prompts", "label_v1.md"); label.PromptPath != want {
		t.Errorf("PromptPath = %q, want %q", label.PromptPath, want)
	}
	if label.Prompt != defaultPrompts["label_v1.md"] {
		t.Errorf("Prompt = %q", label.Prompt)
	}
	if !label.CrossCheck || len(label.Chain) != 2 {
		t.Fatalf("CrossCheck = %v, chain len = %d", label.CrossCheck, len(label.Chain))
	}
	if e := label.Chain[0]; e.Timeout != 20*time.Second || e.Price != (Price{Input: 0.1, Output: 0.4}) {
		t.Errorf("chain[0] = %+v", e)
	}
	if e := label.Chain[1]; e.Timeout != DefaultTimeout || e.Price != (Price{}) {
		t.Errorf("chain[1] defaults = %+v", e)
	}
	if len(label.Version) != 12 {
		t.Errorf("Version = %q, want 12 hex chars", label.Version)
	}

	meal, _ := cfg.Task("meal_recognition")
	if meal.Version == label.Version {
		t.Error("different tasks have the same version")
	}

	if _, err := cfg.Task("text_parse"); err == nil {
		t.Error("Task(text_parse): want error for missing task")
	}
}

func TestTaskVersion(t *testing.T) {
	load := func(config string, prompts map[string]string) string {
		t.Helper()
		cfg, err := LoadConfig(writeConfig(t, config, prompts))
		if err != nil {
			t.Fatal(err)
		}
		return cfg.Tasks["label_extraction"].Version
	}
	base := load(validConfig, defaultPrompts)

	if v := load(validConfig, defaultPrompts); v != base {
		t.Errorf("same config gives different versions: %s and %s", base, v)
	}

	changed := map[string]string{"label_v1.md": "Другой промпт.", "meal_v1.md": defaultPrompts["meal_v1.md"]}
	if v := load(validConfig, changed); v == base {
		t.Error("prompt text change did not change version")
	}

	swapped := strings.Replace(validConfig, "model: vision", "model: vision-2", 1)
	if v := load(swapped, defaultPrompts); v == base {
		t.Error("model change did not change version")
	}

	timeout := strings.Replace(validConfig, "timeout: 20s", "timeout: 45s", 1)
	if v := load(timeout, defaultPrompts); v != base {
		t.Error("timeout change must not change version")
	}
}

func TestLoadConfigErrors(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		prompts map[string]string
		want    []string
	}{
		{
			name:   "empty file",
			config: "",
			want:   []string{"config is empty"},
		},
		{
			name:   "unknown field",
			config: strings.Replace(validConfig, "    chain:\n      - { provider: gemini, model: flash-lite }", "    chian:\n      - { provider: gemini, model: flash-lite }", 1),
			want:   []string{"field chian not found"},
		},
		{
			name:   "no providers and tasks",
			config: "providers: {}\ntasks: {}\n",
			want:   []string{"providers: at least one provider", "tasks: at least one task"},
		},
		{
			name:   "bad provider",
			config: strings.Replace(validConfig, `{ base_url: "https://api.mistral.ai/v1", api_key_env: MISTRAL_API_KEY }`, `{ base_url: "api.mistral.ai", api_key_env: mistral-key }`, 1),
			want:   []string{"providers.mistral.base_url", "providers.mistral.api_key_env"},
		},
		{
			name:    "missing prompt file",
			config:  validConfig,
			prompts: map[string]string{"label_v1.md": "x"},
			want:    []string{"tasks.meal_recognition.prompt", "no such file"},
		},
		{
			name:    "empty prompt file",
			config:  validConfig,
			prompts: map[string]string{"label_v1.md": "x", "meal_v1.md": " \n"},
			want:    []string{"tasks.meal_recognition.prompt", "is empty"},
		},
		{
			name:   "prompt without version",
			config: strings.Replace(validConfig, "meal_v1.md", "meal.md", 1),
			want:   []string{"tasks.meal_recognition.prompt: file name must look like meal_v1.md"},
		},
		{
			name:   "unknown provider in chain",
			config: strings.Replace(validConfig, "{ provider: gemini, model: flash-lite }", "{ provider: grok, model: flash-lite }", 1),
			want:   []string{`tasks.meal_recognition.chain[0].provider: unknown provider "grok"`},
		},
		{
			name:   "missing model",
			config: strings.Replace(validConfig, "{ provider: gemini, model: flash-lite }", "{ provider: gemini }", 1),
			want:   []string{"tasks.meal_recognition.chain[0].model: required"},
		},
		{
			name:   "empty chain",
			config: strings.Replace(validConfig, "    chain:\n      - { provider: gemini, model: flash-lite }", "    chain: []", 1),
			want:   []string{"tasks.meal_recognition.chain: at least one model"},
		},
		{
			name:   "duplicate model",
			config: strings.Replace(validConfig, "{ provider: mistral, model: vision }", "{ provider: gemini, model: flash }", 1),
			want:   []string{"tasks.label_extraction.chain[1]: duplicate model gemini/flash"},
		},
		{
			name:   "cross check with one model",
			config: strings.Replace(validConfig, "    prompt: ../prompts/meal_v1.md\n", "    prompt: ../prompts/meal_v1.md\n    cross_check: true\n", 1),
			want:   []string{"tasks.meal_recognition.cross_check: needs at least two models"},
		},
		{
			name:   "bad timeout",
			config: strings.Replace(validConfig, "timeout: 20s", "timeout: 20", 1),
			want:   []string{"tasks.label_extraction.chain[0].timeout"},
		},
		{
			name:   "negative timeout",
			config: strings.Replace(validConfig, "timeout: 20s", "timeout: -5s", 1),
			want:   []string{"tasks.label_extraction.chain[0].timeout"},
		},
		{
			name:   "negative price",
			config: strings.Replace(validConfig, "input: 0.1", "input: -0.1", 1),
			want:   []string{"tasks.label_extraction.chain[0].price: must not be negative"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prompts := tt.prompts
			if prompts == nil {
				prompts = defaultPrompts
			}
			cfg, err := LoadConfig(writeConfig(t, tt.config, prompts))
			if err == nil {
				t.Fatalf("want error, got config %+v", cfg)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q\ndoes not contain %q", err, w)
				}
			}
		})
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	_, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil || !strings.Contains(err.Error(), "read model config") {
		t.Errorf("err = %v, want read error", err)
	}
}

// Настоящий конфиг репозитория должен всегда загружаться.
func TestRepoModelsConfigLoads(t *testing.T) {
	cfg, err := LoadConfig("../../config/models.yaml")
	if err != nil {
		t.Fatalf("config/models.yaml: %v", err)
	}
	if _, err := cfg.Task("label_extraction"); err != nil {
		t.Error(err)
	}
}
