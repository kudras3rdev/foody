package llm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultTimeout — таймаут вызова модели, если в звене цепочки он не задан.
const DefaultTimeout = 30 * time.Second

var (
	envNameRe    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	promptFileRe = regexp.MustCompile(`^([a-z0-9]+(?:_[a-z0-9]+)*_v[0-9]+)\.md$`)
)

// Config — провайдеры и цепочки моделей по задачам (config/models.yaml, NFR-07).
type Config struct {
	Providers map[string]Provider
	Tasks     map[string]Task
}

type Provider struct {
	Name      string
	BaseURL   string
	APIKeyEnv string // имя переменной окружения с ключом; ключ читается при вызове
}

var errNoAPIKey = errors.New("API key is not set")

// APIKey читает ключ провайдера из окружения.
func (p Provider) APIKey() (string, error) {
	key := os.Getenv(p.APIKeyEnv)
	if key == "" {
		return "", fmt.Errorf("provider %s: %w: env %s is empty", p.Name, errNoAPIKey, p.APIKeyEnv)
	}
	return key, nil
}

type Task struct {
	Name          string
	PromptPath    string // путь к файлу промпта, относительные пути — от каталога конфига
	PromptVersion string // имя файла промпта без расширения, например "meal_v1"
	Prompt        string
	CrossCheck    bool // вторая модель цепочки извлекает независимо для сверки (FR-05)
	Chain         []ChainEntry
	Version       string // хэш задачи (модели + промпт) для recognition_runs.config_version
}

type ChainEntry struct {
	Provider string
	Model    string
	Timeout  time.Duration
	Price    Price
}

// Price — цена в долларах за 1M токенов; ноль для бесплатных уровней.
type Price struct {
	Input  float64
	Output float64
}

// Task возвращает задачу по имени или ошибку, если её нет в конфиге.
func (c *Config) Task(name string) (Task, error) {
	t, ok := c.Tasks[name]
	if !ok {
		return Task{}, fmt.Errorf("task %q is not configured", name)
	}
	return t, nil
}

type rawConfig struct {
	Providers map[string]rawProvider `yaml:"providers"`
	Tasks     map[string]rawTask     `yaml:"tasks"`
}

type rawProvider struct {
	BaseURL   string `yaml:"base_url"`
	APIKeyEnv string `yaml:"api_key_env"`
}

type rawTask struct {
	Prompt     string          `yaml:"prompt"`
	CrossCheck bool            `yaml:"cross_check"`
	Chain      []rawChainEntry `yaml:"chain"`
}

type rawChainEntry struct {
	Provider string    `yaml:"provider"`
	Model    string    `yaml:"model"`
	Timeout  string    `yaml:"timeout"`
	Price    *rawPrice `yaml:"price"`
}

type rawPrice struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
}

// LoadConfig читает и валидирует конфиг моделей. Все ошибки валидации
// возвращаются вместе, чтобы исправить конфиг за один проход.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read model config: %w", err)
	}
	cfg, err := parseConfig(data, filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("model config %s: %w", path, err)
	}
	return cfg, nil
}

func parseConfig(data []byte, baseDir string) (*Config, error) {
	var raw rawConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("config is empty")
		}
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	cfg := &Config{
		Providers: make(map[string]Provider, len(raw.Providers)),
		Tasks:     make(map[string]Task, len(raw.Tasks)),
	}

	if len(raw.Providers) == 0 {
		fail("providers: at least one provider is required")
	}
	for _, name := range slices.Sorted(maps.Keys(raw.Providers)) {
		p := raw.Providers[name]
		if u, err := url.Parse(p.BaseURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			fail("providers.%s.base_url: want absolute http(s) URL, got %q", name, p.BaseURL)
		}
		if !envNameRe.MatchString(p.APIKeyEnv) {
			fail("providers.%s.api_key_env: want env variable name like GEMINI_API_KEY, got %q", name, p.APIKeyEnv)
		}
		cfg.Providers[name] = Provider{Name: name, BaseURL: p.BaseURL, APIKeyEnv: p.APIKeyEnv}
	}

	if len(raw.Tasks) == 0 {
		fail("tasks: at least one task is required")
	}
	for _, name := range slices.Sorted(maps.Keys(raw.Tasks)) {
		rt := raw.Tasks[name]
		prefix := "tasks." + name
		t := Task{Name: name, CrossCheck: rt.CrossCheck}

		if rt.Prompt == "" {
			fail("%s.prompt: required", prefix)
		} else if m := promptFileRe.FindStringSubmatch(filepath.Base(rt.Prompt)); m == nil {
			fail("%s.prompt: file name must look like meal_v1.md, got %q", prefix, filepath.Base(rt.Prompt))
		} else {
			t.PromptVersion = m[1]
			t.PromptPath = rt.Prompt
			if !filepath.IsAbs(t.PromptPath) {
				t.PromptPath = filepath.Join(baseDir, t.PromptPath)
			}
			text, err := os.ReadFile(t.PromptPath)
			switch {
			case err != nil:
				fail("%s.prompt: %w", prefix, err)
			case strings.TrimSpace(string(text)) == "":
				fail("%s.prompt: file %s is empty", prefix, t.PromptPath)
			default:
				t.Prompt = string(text)
			}
		}

		if len(rt.Chain) == 0 {
			fail("%s.chain: at least one model is required", prefix)
		}
		if rt.CrossCheck && len(rt.Chain) < 2 {
			fail("%s.cross_check: needs at least two models in chain", prefix)
		}
		seen := make(map[string]bool, len(rt.Chain))
		for i, rc := range rt.Chain {
			at := fmt.Sprintf("%s.chain[%d]", prefix, i)
			e := ChainEntry{Provider: rc.Provider, Model: rc.Model, Timeout: DefaultTimeout}
			if rc.Provider == "" {
				fail("%s.provider: required", at)
			} else if _, ok := raw.Providers[rc.Provider]; !ok {
				fail("%s.provider: unknown provider %q", at, rc.Provider)
			}
			if rc.Model == "" {
				fail("%s.model: required", at)
			}
			if key := rc.Provider + "/" + rc.Model; seen[key] {
				fail("%s: duplicate model %s", at, key)
			} else {
				seen[key] = true
			}
			if rc.Timeout != "" {
				d, err := time.ParseDuration(rc.Timeout)
				if err != nil || d <= 0 {
					fail("%s.timeout: want positive duration like 30s, got %q", at, rc.Timeout)
				}
				e.Timeout = d
			}
			if rc.Price != nil {
				if rc.Price.Input < 0 || rc.Price.Output < 0 {
					fail("%s.price: must not be negative", at)
				}
				e.Price = Price{Input: rc.Price.Input, Output: rc.Price.Output}
			}
			t.Chain = append(t.Chain, e)
		}

		t.Version = taskVersion(t)
		cfg.Tasks[name] = t
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return cfg, nil
}

// taskVersion хэширует то, что влияет на результат задачи: модели цепочки,
// сверку и текст промпта. Таймауты и цены в хэш не входят.
func taskVersion(t Task) string {
	type model struct{ Provider, Model string }
	v := struct {
		PromptVersion string
		Prompt        string
		CrossCheck    bool
		Chain         []model
	}{PromptVersion: t.PromptVersion, Prompt: t.Prompt, CrossCheck: t.CrossCheck}
	for _, e := range t.Chain {
		v.Chain = append(v.Chain, model{e.Provider, e.Model})
	}
	b, _ := json.Marshal(v) // структура из строк и bool — Marshal не падает
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}
