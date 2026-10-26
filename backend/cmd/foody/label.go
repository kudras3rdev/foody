package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"foody/internal/catalog"
	"foody/internal/llm"
	"foody/internal/tasks"
)

// runLabel читает этикетки с фото через цепочку label_extraction и печатает
// цитату, КБЖУ, проверки и попытки моделей — для отладки промпта на своих фото.
func runLabel(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("label", flag.ContinueOnError)
	fs.SetOutput(stdout)
	configPath := fs.String("config", "config/models.yaml", "конфиг моделей")
	envPath := fs.String("env", "../.env", "файл с ключами API")
	logPath := fs.String("log", "../data/logs/model_calls.jsonl", "журнал вызовов моделей")
	fs.Usage = func() {
		fmt.Fprintln(stdout, "usage: foody label [flags] <фото>...\n\nЗапускать из backend/. Флаги:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return errors.New("нет фото")
	}

	if err := loadDotEnv(*envPath); err != nil {
		return err
	}
	cfg, err := llm.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	callLog, err := llm.OpenJSONLLog(*logPath)
	if err != nil {
		return err
	}
	x := tasks.NewLabelExtractor(llm.NewRegistry(cfg, nil, callLog.Log))

	var failed int
	for _, path := range fs.Args() {
		if err := labelPhoto(ctx, x, path, stdout); err != nil {
			fmt.Fprintf(stdout, "Ошибка: %v\n", err)
			failed++
		}
		fmt.Fprintln(stdout)
		if ctx.Err() != nil {
			break
		}
	}
	fmt.Fprintf(stdout, "Журнал вызовов: %s\n", *logPath)
	if err := callLog.Close(); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("не прочитано фото: %d из %d", failed, fs.NArg())
	}
	return nil
}

func labelPhoto(ctx context.Context, x tasks.LabelExtractor, path string, w io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("%s: %d КБ", filepath.Base(path), len(data)/1024)
	if c, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		header += fmt.Sprintf(", %d×%d", c.Width, c.Height)
	}
	fmt.Fprintf(w, "== %s\n", header)

	res, err := x.Extract(ctx, llm.Image{MIME: http.DetectContentType(data), Data: data})
	if res != nil {
		defer printAttempts(w, res.Attempts)
	}
	if errors.Is(err, llm.ErrChainExhausted) {
		return errors.New("ни одна модель цепочки не дала ответ, причины — в вызовах моделей ниже")
	}
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "Цитата:   «%s»\n", res.Raw.SourceText)
	fmt.Fprintf(w, "Позиция:  %s\n", strings.Trim(res.Raw.Name+" · "+res.Raw.Brand, " ·"))
	fmt.Fprintf(w, "КБЖУ:     %s\n", fmtLabel(res.Label))
	if res.Second != nil {
		fmt.Fprintf(w, "Сверка:   %s\n", fmtLabel(*res.Second))
	}
	if res.OK() {
		fmt.Fprintln(w, "Проверки: ✓ пройдены")
		return nil
	}
	fmt.Fprintln(w, "Проверки: ⚠ нужно подтверждение")
	for _, i := range res.Issues {
		fmt.Fprintf(w, "  - %s: %s\n", i.Code, i.Message)
	}
	return nil
}

func fmtLabel(l catalog.Label) string {
	unit := map[catalog.Basis]string{catalog.BasisGram: "г", catalog.BasisML: "мл"}[l.Basis]
	s := fmt.Sprintf("на 100 %s: %g ккал · Б %g · Ж %g · У %g", unit, l.Per100.Kcal, l.Per100.Protein, l.Per100.Fat, l.Per100.Carbs)
	if l.PackSize != nil {
		s += fmt.Sprintf(" · упаковка %g %s", *l.PackSize, unit)
	}
	return s
}

func printAttempts(w io.Writer, attempts []llm.Attempt) {
	fmt.Fprintln(w, "Вызовы моделей:")
	for _, a := range attempts {
		line := fmt.Sprintf("  #%d %s/%s  %s", a.ChainIndex, a.Provider, a.Model, a.Status)
		if a.Retry {
			line += " (повтор)"
		}
		if a.Latency > 0 {
			line += fmt.Sprintf("  %.1f с  %d/%d ток.", a.Latency.Seconds(), a.TokensIn, a.TokensOut)
		}
		if a.Status != llm.StatusOK {
			line += "  " + shorten(a.Error, 160)
		}
		fmt.Fprintln(w, line)
	}
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
