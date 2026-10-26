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

// runLabel reads nutrition labels from photos through the label_extraction chain
// and prints the quote, nutrition, checks and model attempts to debug the prompt
func runLabel(ctx context.Context, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("label", flag.ContinueOnError)
	fs.SetOutput(stdout)
	configPath := fs.String("config", "config/models.yaml", "model config")
	envPath := fs.String("env", "../.env", "file with API keys")
	logPath := fs.String("log", "../data/logs/model_calls.jsonl", "model call log")

	fs.Usage = func() {
		fmt.Fprintln(stdout, "usage: foody label [flags] <photo>...\n\nrun from backend/, flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}

	if fs.NArg() == 0 {
		fs.Usage()

		return errors.New("no photos given")
	}

	if err := loadDotEnv(*envPath); err != nil {
		return fmt.Errorf("load env: %w", err)
	}

	cfg, err := llm.LoadConfig(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	callLog, err := llm.OpenJSONLLog(*logPath)
	if err != nil {
		return fmt.Errorf("open call log: %w", err)
	}

	x := tasks.NewLabelExtractor(llm.NewRegistry(cfg, nil, callLog.Log))

	var failed int

	for _, path := range fs.Args() {
		if err := labelPhoto(ctx, x, path, stdout); err != nil {
			fmt.Fprintf(stdout, "error: %v\n", err)

			failed++
		}

		fmt.Fprintln(stdout)

		if ctx.Err() != nil {
			break
		}
	}

	fmt.Fprintf(stdout, "call log: %s\n", *logPath)

	if err := callLog.Close(); err != nil {
		return fmt.Errorf("close call log: %w", err)
	}

	if failed > 0 {
		return fmt.Errorf("failed to read %d of %d photos", failed, fs.NArg())
	}

	return nil
}

func labelPhoto(ctx context.Context, x tasks.LabelExtractor, path string, w io.Writer) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read photo: %w", err)
	}

	header := fmt.Sprintf("%s: %d KB", filepath.Base(path), len(data)/1024)
	if c, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		header += fmt.Sprintf(", %dx%d", c.Width, c.Height)
	}

	fmt.Fprintf(w, "== %s\n", header)

	res, err := x.Extract(ctx, llm.Image{MIME: http.DetectContentType(data), Data: data})
	if res != nil {
		defer printAttempts(w, res.Attempts)
	}

	if errors.Is(err, llm.ErrChainExhausted) {
		return errors.New("no model in the chain answered, see the model calls below")
	}

	if err != nil {
		return fmt.Errorf("extract label: %w", err)
	}

	fmt.Fprintf(w, "quote:     %q\n", res.Raw.SourceText)
	fmt.Fprintf(w, "item:      %s\n", strings.Trim(res.Raw.Name+" / "+res.Raw.Brand, " /"))
	fmt.Fprintf(w, "nutrition: %s\n", fmtLabel(res.Label))

	if res.Second != nil {
		fmt.Fprintf(w, "cross:     %s\n", fmtLabel(*res.Second))
	}

	if res.OK() {
		fmt.Fprintln(w, "checks:    passed")

		return nil
	}

	fmt.Fprintln(w, "checks:    needs review")

	for _, i := range res.Issues {
		fmt.Fprintf(w, "  - %s: %s\n", i.Code, i.Message)
	}

	return nil
}

func fmtLabel(l catalog.Label) string {
	s := fmt.Sprintf("per 100 %s: %g kcal, protein %g, fat %g, carbs %g", l.Basis, l.Per100.Kcal, l.Per100.Protein, l.Per100.Fat, l.Per100.Carbs)
	if l.PackSize != nil {
		s += fmt.Sprintf(", pack %g %s", *l.PackSize, l.Basis)
	}

	return s
}

func printAttempts(w io.Writer, attempts []llm.Attempt) {
	fmt.Fprintln(w, "model calls:")

	for _, a := range attempts {
		line := fmt.Sprintf("  #%d %s/%s  %s", a.ChainIndex, a.Provider, a.Model, a.Status)
		if a.Retry {
			line += " (retry)"
		}

		if a.Latency > 0 {
			line += fmt.Sprintf("  %.1fs  %d/%d tokens", a.Latency.Seconds(), a.TokensIn, a.TokensOut)
		}

		if a.Status != llm.StatusOK {
			line += "  " + shorten(a.Error, 160)
		}

		fmt.Fprintln(w, line)
	}
}

func shorten(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}

	return s
}
