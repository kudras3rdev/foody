package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"foody/internal/catalog"
	"foody/internal/llm"
)

// TaskLabelExtraction is the task name in config/models.yaml
const TaskLabelExtraction = "label_extraction"

// ErrUnreadableLabel means the model reported no readable nutrition values on the photo
var ErrUnreadableLabel = errors.New("nutrition label is not readable")

// LabelExtractor extracts nutrition values from a label photo (FR-04)
type LabelExtractor interface {
	Extract(ctx context.Context, img llm.Image) (*LabelExtraction, error)
}

// LabelExtraction is the result of reading a label and checking it (FR-05)
type LabelExtraction struct {
	Raw    catalog.RawLabel // as printed, with the quote, from the first model
	Label  catalog.Label    // per 100 g or 100 ml after conversion by code
	Second *catalog.Label   // reading of the second model, nil without cross-check
	Issues []catalog.Issue
	// Attempts holds every model call, failed ones and the cross-check included
	Attempts []llm.Attempt
}

// OK reports that the label passed the checks and the cross-check
// a new item is still shown to the user once (architecture.md 3.3)
func (e *LabelExtraction) OK() bool { return len(e.Issues) == 0 }

type labelExtractor struct {
	reg *llm.Registry
}

func NewLabelExtractor(reg *llm.Registry) LabelExtractor {
	return &labelExtractor{reg: reg}
}

// labelResponse is the model answer schema (prompt label_v2, ADR 006, ADR 007)
// the model copies values as printed, catalog.NormalizeLabel converts them
type labelResponse struct {
	Unreadable string   `json:"unreadable"`
	SourceText string   `json:"source_text"`
	Name       string   `json:"name"`
	Brand      string   `json:"brand"`
	Per        *amount  `json:"per"`
	Energy     *energy  `json:"energy"`
	Protein    *float64 `json:"protein"`
	Fat        *float64 `json:"fat"`
	Carbs      *float64 `json:"carbs"`
	Pack       *amount  `json:"pack"`
	Qualifiers []string `json:"qualifiers"`
}

type amount struct {
	Amount *float64 `json:"amount"`
	Unit   string   `json:"unit"`
}

type energy struct {
	Kcal *float64 `json:"kcal"`
	KJ   *float64 `json:"kj"`
}

// Validate checks the answer structure; catalog.NormalizeLabel and
// catalog.CheckLabel check plausibility, their issues lead to user review
// rather than a retry
func (r *labelResponse) Validate() error {
	if r.Unreadable != "" {
		if r.SourceText != "" || r.Per != nil || r.Energy != nil || r.Protein != nil || r.Fat != nil || r.Carbs != nil {
			return errors.New("unreadable must be the only field; if the nutrition values are readable, omit unreadable and return them")
		}

		return nil
	}

	var errs []error
	if strings.TrimSpace(r.SourceText) == "" {
		errs = append(errs, errors.New("source_text is required: quote the nutrition text from the photo"))
	}

	switch {
	case r.Per == nil:
		errs = append(errs, errors.New("per is required"))
	case r.Per.Amount == nil:
		errs = append(errs, errors.New("per.amount is required"))
	case r.Per.Unit != "g" && r.Per.Unit != "ml":
		errs = append(errs, fmt.Errorf(`per.unit must be "g" or "ml", got %q`, r.Per.Unit))
	}

	if r.Energy == nil || (r.Energy.Kcal == nil && r.Energy.KJ == nil) {
		errs = append(errs, errors.New("energy.kcal or energy.kj is required"))
	}

	for _, f := range []struct {
		name string
		v    *float64
	}{{"protein", r.Protein}, {"fat", r.Fat}, {"carbs", r.Carbs}} {
		if f.v == nil {
			errs = append(errs, fmt.Errorf("%s is required", f.name))
		}
	}

	if r.Pack != nil {
		if r.Pack.Amount == nil {
			errs = append(errs, errors.New("pack.amount is required, or set pack to null"))
		}

		switch r.Pack.Unit {
		case "g", "kg", "ml", "l":
		default:
			errs = append(errs, fmt.Errorf(`pack.unit must be "g", "kg", "ml" or "l", got %q`, r.Pack.Unit))
		}
	}

	return errors.Join(errs...)
}

func (r *labelResponse) raw() catalog.RawLabel {
	raw := catalog.RawLabel{
		SourceText: r.SourceText,
		Name:       r.Name,
		Brand:      r.Brand,
		PerAmount:  *r.Per.Amount,
		PerUnit:    r.Per.Unit,
		Kcal:       r.Energy.Kcal,
		KJ:         r.Energy.KJ,
		Protein:    *r.Protein,
		Fat:        *r.Fat,
		Carbs:      *r.Carbs,
		Qualifiers: r.Qualifiers,
	}
	if r.Pack != nil {
		raw.PackAmount, raw.PackUnit = r.Pack.Amount, r.Pack.Unit
	}

	return raw
}

// Extract reads a label; without cross-check it is a plain chain call
// with cross-check chain models are called in turn until two answer: the
// first gives the reading, the second an independent check (FR-05)
func (x *labelExtractor) Extract(ctx context.Context, img llm.Image) (*LabelExtraction, error) {
	task, err := x.reg.Task(TaskLabelExtraction)
	if err != nil {
		return nil, fmt.Errorf("label extraction: %w", err)
	}

	// the prompt goes in the user message with the photo because some vision
	// models (e.g. Gemma) reject the system role
	msgs := []llm.Message{{Role: "user", Text: task.Prompt, Images: []llm.Image{img}}}
	out := &LabelExtraction{}

	if !task.CrossCheck {
		resp, res, err := llm.CallJSON[labelResponse](ctx, x.reg, TaskLabelExtraction, msgs)

		out.Attempts = res.Attempts
		if err != nil {
			return out, err
		}

		return out, out.finish(resp, nil)
	}

	var (
		got  []*labelResponse
		errs []error
	)

	for i := range task.Chain {
		resp, res, err := llm.CallModelJSON[labelResponse](ctx, x.reg, TaskLabelExtraction, i, msgs)

		out.Attempts = append(out.Attempts, res.Attempts...)

		if ctx.Err() != nil {
			return out, fmt.Errorf("label extraction: %w", ctx.Err())
		}

		if err != nil {
			errs = append(errs, err)
			continue
		}

		got = append(got, resp)
		if len(got) == 2 {
			break
		}
	}

	switch len(got) {
	case 0:
		return out, fmt.Errorf("task %s: %w: %w", TaskLabelExtraction, llm.ErrChainExhausted, errors.Join(errs...))
	case 1:
		if err := out.finish(got[0], nil); err != nil {
			return out, err
		}

		out.Issues = append(out.Issues, catalog.Issue{
			Code:    catalog.IssueCrossUnavailable,
			Message: "cross-check skipped: only one chain model answered",
		})

		return out, nil
	default:
		return out, out.finish(got[0], got[1])
	}
}

// finish builds the result from the main and the optional second reading
func (e *LabelExtraction) finish(first, second *labelResponse) error {
	if first.Unreadable != "" {
		return fmt.Errorf("%w: %s", ErrUnreadableLabel, first.Unreadable)
	}

	e.Raw = first.raw()

	var issues []catalog.Issue

	e.Label, issues = catalog.NormalizeLabel(e.Raw)
	e.Issues = append(issues, catalog.CheckLabel(e.Label)...)

	if second == nil {
		return nil
	}

	if second.Unreadable != "" {
		e.Issues = append(e.Issues, catalog.Issue{
			Code:    catalog.IssueCrossUnavailable,
			Message: "the second model could not read the label: " + second.Unreadable,
		})

		return nil
	}
	// normalization issues of the second answer are skipped, the cross-check compares results
	s, _ := catalog.NormalizeLabel(second.raw())
	e.Second = &s
	e.Issues = append(e.Issues, catalog.CompareLabels(e.Label, s)...)

	return nil
}
