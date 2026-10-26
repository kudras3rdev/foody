package tasks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"foody/internal/catalog"
	"foody/internal/llm"
)

const yogurtJSON = `{"source_text": "Пищевая ценность на 100 г: белки 2,8 г, жиры 2,4 г, углеводы 11,5 г; 79 ккал", "name": "Йогурт", "brand": "Активиа", "per": {"amount": 100, "unit": "g"}, "energy": {"kcal": 79, "kj": null}, "protein": 2.8, "fat": 2.4, "carbs": 11.5, "pack": {"amount": 150, "unit": "g"}, "qualifiers": []}`

// fakeModel is a fake provider with one model, it answers with content or the status code
type fakeModel struct {
	content string
	status  int
	hits    atomic.Int32
	body    atomic.Value // last request
}

func reply(content string) *fakeModel { return &fakeModel{content: content} }
func failing(status int) *fakeModel   { return &fakeModel{status: status} }

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.hits.Add(1)

	b, _ := io.ReadAll(r.Body)
	f.body.Store(string(b))

	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}

	fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`, f.content)
}

// newExtractor writes a config with the label_extraction task, each fakeModel is a separate provider
func newExtractor(t *testing.T, crossCheck bool, models ...*fakeModel) LabelExtractor {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "label_v1.md"), []byte("ПРОМПТ ЭТИКЕТКИ"), 0o644); err != nil {
		t.Fatal(err)
	}

	var providers, chain strings.Builder

	for i, m := range models {
		srv := httptest.NewServer(m)
		t.Cleanup(srv.Close)

		env := fmt.Sprintf("FOODY_TEST_KEY_%d", i)
		t.Setenv(env, "key")
		fmt.Fprintf(&providers, "  p%d: { base_url: %q, api_key_env: %s }\n", i, srv.URL, env)
		fmt.Fprintf(&chain, "      - { provider: p%d, model: m%d }\n", i, i)
	}

	yaml := fmt.Sprintf("providers:\n%stasks:\n  label_extraction:\n    prompt: label_v1.md\n    cross_check: %v\n    chain:\n%s",
		providers.String(), crossCheck, chain.String())

	path := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := llm.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	return NewLabelExtractor(llm.NewRegistry(cfg, nil, nil))
}

var photo = llm.Image{MIME: "image/jpeg", Data: []byte{0xff, 0xd8, 0xff}}

func issueCodes(issues []catalog.Issue) string {
	var c []string
	for _, i := range issues {
		c = append(c, i.Code)
	}

	return strings.Join(c, ",")
}

func TestExtractSingleModel(t *testing.T) {
	m := reply("```json\n" + yogurtJSON + "\n```")
	x := newExtractor(t, false, m, reply("unused"))

	got, err := x.Extract(context.Background(), photo)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	pack := 150.0

	want := catalog.Label{Name: "Йогурт", Brand: "Активиа", Basis: catalog.BasisGram,
		Per100: catalog.Per100{Kcal: 79, Protein: 2.8, Fat: 2.4, Carbs: 11.5}, PackSize: &pack}
	if got.Label.Name != want.Name || got.Label.Brand != want.Brand || got.Label.Basis != want.Basis ||
		got.Label.Per100 != want.Per100 || *got.Label.PackSize != pack {
		t.Errorf("Label = %+v", got.Label)
	}

	if !strings.HasPrefix(got.Raw.SourceText, "Пищевая ценность на 100 г") {
		t.Errorf("SourceText = %q", got.Raw.SourceText)
	}

	if !got.OK() || got.Second != nil || len(got.Attempts) != 1 {
		t.Errorf("OK %v, Second %v, attempts %d", got.OK(), got.Second, len(got.Attempts))
	}

	body := m.body.Load().(string)
	if !strings.Contains(body, "ПРОМПТ ЭТИКЕТКИ") || !strings.Contains(body, "data:image/jpeg;base64,/9j/") {
		t.Errorf("request must carry prompt and photo: %s", body)
	}
}

func TestExtractDrinkWithoutPackSize(t *testing.T) {
	x := newExtractor(t, false, reply(`{"source_text": "в 100 мл: 0 ккал", "name": "Burn", "brand": "", "per": {"amount": 100, "unit": "ml"}, "energy": {"kcal": 0, "kj": null}, "protein": 0, "fat": 0, "carbs": 0, "pack": null, "qualifiers": []}`))

	got, err := x.Extract(context.Background(), photo)
	if err != nil {
		t.Fatal(err)
	}

	if got.Label.Basis != catalog.BasisML || got.Label.PackSize != nil || !got.OK() {
		t.Errorf("got %+v, issues %v", got.Label, got.Issues)
	}
}

// code converts units: kJ to kcal, liters to ml (ADR 007)
func TestExtractConvertsUnits(t *testing.T) {
	x := newExtractor(t, false, reply(`{"source_text": "в 100 мл: углеводы 11,2 г; 188 кДж", "name": "Вольт", "brand": "", "per": {"amount": 100, "unit": "ml"}, "energy": {"kcal": null, "kj": 188}, "protein": 0, "fat": 0, "carbs": 11.2, "pack": {"amount": 0.449, "unit": "l"}, "qualifiers": []}`))

	got, err := x.Extract(context.Background(), photo)
	if err != nil {
		t.Fatal(err)
	}

	if got.Label.Per100.Kcal != 44.9 || got.Label.PackSize == nil || *got.Label.PackSize != 449 || !got.OK() {
		t.Errorf("label = %+v pack %v, issues %v", got.Label, got.Label.PackSize, got.Issues)
	}
}

func TestExtractPerPortionNeedsReview(t *testing.T) {
	portion := strings.Replace(yogurtJSON, `"amount": 100, "unit": "g"`, `"amount": 50, "unit": "g"`, 1)

	got, err := newExtractor(t, false, reply(portion)).Extract(context.Background(), photo)
	if err != nil {
		t.Fatal(err)
	}

	if got.Label.Per100.Kcal != 158 || issueCodes(got.Issues) != catalog.IssuePerPortion {
		t.Errorf("label = %+v, issues %v", got.Label, got.Issues)
	}
}

func TestExtractChecksLabel(t *testing.T) {
	kj := strings.Replace(yogurtJSON, `"kcal": 79`, `"kcal": 330`, 1)
	x := newExtractor(t, false, reply(kj))

	got, err := x.Extract(context.Background(), photo)
	if err != nil {
		t.Fatal(err)
	}

	if got.OK() || issueCodes(got.Issues) != catalog.IssueEnergyMismatch {
		t.Errorf("issues = %v", got.Issues)
	}
}

func TestExtractCrossCheck(t *testing.T) {
	tests := []struct {
		name       string
		models     []*fakeModel
		wantIssues string
		wantHits   []int32
		wantKcal   float64
	}{
		{
			name:     "agree",
			models:   []*fakeModel{reply(yogurtJSON), reply(strings.Replace(yogurtJSON, `"Йогурт"`, `"Activia"`, 1)), reply("unused")},
			wantHits: []int32{1, 1, 0}, wantKcal: 79,
		},
		{
			name:       "disagree",
			models:     []*fakeModel{reply(yogurtJSON), reply(strings.Replace(yogurtJSON, `"kcal": 79`, `"kcal": 90`, 1))},
			wantIssues: catalog.IssueCrossMismatch,
			wantHits:   []int32{1, 1}, wantKcal: 79,
		},
		{
			name:     "first model down",
			models:   []*fakeModel{failing(503), reply(yogurtJSON), reply(yogurtJSON)},
			wantHits: []int32{1, 1, 1}, wantKcal: 79,
		},
		{
			name:       "only one model answers",
			models:     []*fakeModel{failing(429), reply(yogurtJSON), failing(500)},
			wantIssues: catalog.IssueCrossUnavailable,
			wantHits:   []int32{1, 1, 1}, wantKcal: 79,
		},
		{
			name:       "second unreadable",
			models:     []*fakeModel{reply(yogurtJSON), reply(`{"unreadable": "блик"}`)},
			wantIssues: catalog.IssueCrossUnavailable,
			wantHits:   []int32{1, 1}, wantKcal: 79,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := newExtractor(t, true, tt.models...)

			got, err := x.Extract(context.Background(), photo)
			if err != nil {
				t.Fatalf("Extract: %v", err)
			}

			if codes := issueCodes(got.Issues); codes != tt.wantIssues {
				t.Errorf("issues = %q (%v), want %q", codes, got.Issues, tt.wantIssues)
			}

			if got.Label.Per100.Kcal != tt.wantKcal {
				t.Errorf("kcal = %g", got.Label.Per100.Kcal)
			}

			for i, m := range tt.models {
				if h := m.hits.Load(); h != tt.wantHits[i] {
					t.Errorf("model %d hits = %d, want %d", i, h, tt.wantHits[i])
				}
			}
		})
	}
}

func TestExtractInvalidSchemaFallsBack(t *testing.T) {
	noCarbs := strings.Replace(yogurtJSON, `, "carbs": 11.5`, "", 1)
	bad := reply(noCarbs)
	x := newExtractor(t, false, bad, reply(yogurtJSON))

	got, err := x.Extract(context.Background(), photo)
	if err != nil {
		t.Fatal(err)
	}

	if bad.hits.Load() != 2 {
		t.Errorf("invalid model hits = %d, want 2 (answer + retry)", bad.hits.Load())
	}

	if len(got.Attempts) != 3 || !strings.Contains(got.Attempts[0].Error, "carbs is required") {
		t.Errorf("attempts = %+v", got.Attempts)
	}

	if !strings.Contains(bad.body.Load().(string), "carbs is required") {
		t.Error("retry must tell the model what was wrong")
	}
}

func TestExtractSchemaErrors(t *testing.T) {
	for name, content := range map[string]string{
		"bad per unit":         strings.Replace(yogurtJSON, `"amount": 100, "unit": "g"`, `"amount": 100, "unit": "portion"`, 1),
		"no per":               `{"source_text": "x", "energy": {"kcal": 1, "kj": null}, "protein": 0, "fat": 0, "carbs": 0}`,
		"no energy":            strings.Replace(yogurtJSON, `"kcal": 79, "kj": null`, `"kcal": null, "kj": null`, 1),
		"no source text":       strings.Replace(yogurtJSON, `"Пищевая ценность на 100 г: белки 2,8 г, жиры 2,4 г, углеводы 11,5 г; 79 ккал"`, `""`, 1),
		"bad pack unit":        strings.Replace(yogurtJSON, `"amount": 150, "unit": "g"`, `"amount": 1, "unit": "шт"`, 1),
		"kcal string":          strings.Replace(yogurtJSON, `"kcal": 79`, `"kcal": "79"`, 1),
		"extra field":          strings.Replace(yogurtJSON, `"brand"`, `"sugar": 5, "brand"`, 1),
		"unreadable with data": strings.Replace(yogurtJSON, `"name"`, `"unreadable": "нет названия", "name"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newExtractor(t, false, reply(content)).Extract(context.Background(), photo)
			if !errors.Is(err, llm.ErrChainExhausted) {
				t.Errorf("err = %v, want chain exhausted", err)
			}
		})
	}
}

func TestExtractUnreadable(t *testing.T) {
	for _, cross := range []bool{false, true} {
		x := newExtractor(t, cross, reply(`{"unreadable": "на фото нет таблицы КБЖУ"}`), reply(yogurtJSON))

		_, err := x.Extract(context.Background(), photo)
		if !errors.Is(err, ErrUnreadableLabel) || !strings.Contains(err.Error(), "нет таблицы") {
			t.Errorf("cross_check=%v: err = %v", cross, err)
		}
	}
}

func TestExtractAllModelsFail(t *testing.T) {
	for _, cross := range []bool{false, true} {
		x := newExtractor(t, cross, failing(500), failing(429))

		got, err := x.Extract(context.Background(), photo)
		if !errors.Is(err, llm.ErrChainExhausted) {
			t.Errorf("cross_check=%v: err = %v", cross, err)
		}

		if len(got.Attempts) != 2 {
			t.Errorf("cross_check=%v: attempts = %d, want 2", cross, len(got.Attempts))
		}
	}
}
