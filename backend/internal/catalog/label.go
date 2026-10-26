package catalog

import (
	"fmt"
	"math"
	"strings"
)

// Basis is the unit nutrition values refer to: per 100 g or per 100 ml (ADR 001)
type Basis string

const (
	BasisGram Basis = "g"
	BasisML   Basis = "ml"
)

// Per100 holds nutrition values per 100 units of the basis
type Per100 struct {
	Kcal    float64
	Protein float64
	Fat     float64
	Carbs   float64
}

// Label is the nutrition data of a packaged product (FR-04)
type Label struct {
	Name     string
	Brand    string
	Basis    Basis
	Per100   Per100
	PackSize *float64 // in basis units, nil when not printed
}

// codes of failed label checks
const (
	IssueNegative         = "negative_value"
	IssueEnergyMismatch   = "energy_mismatch"
	IssueMacrosOver100    = "macros_over_100"
	IssueKcalOver900      = "kcal_over_900"
	IssueBadBasis         = "bad_basis"
	IssueBadPackSize      = "bad_pack_size"
	IssueCrossMismatch    = "cross_check_mismatch"
	IssueCrossUnavailable = "cross_check_unavailable"
	IssueBadPer           = "bad_per"               // serving size missing or not in g/ml
	IssuePerPortion       = "per_portion_converted" // label is not per 100 g/ml, converted by code
	IssueNoEnergy         = "no_energy"             // neither kcal nor kJ printed
	IssueEnergyUnits      = "kcal_kj_mismatch"      // printed kcal and kJ disagree
	IssuePackUnit         = "pack_unit_mismatch"    // pack is in mass while basis is volume, or vice versa
	IssueQualified        = "qualified_values"      // "no more than", ranges, hard to read digits
)

// Issue is a failed check; a label with issues goes to user review (FR-05)
type Issue struct {
	Code    string
	Message string
}

// check tolerances (FR-05, ADR 001, architecture.md 3.3)
const (
	energyRelTol = 0.15 // 4*P + 9*F + 4*C against kcal
	energyAbsTol = 5.0  // kcal, for zero and low calorie products
	maxMacros    = 100.0
	maxKcal      = 900.0
	crossRelTol  = 0.02 // cross-check of two models
	crossAbsTol  = 0.1  // for zeros and small values
	kjPerKcal    = 4.184
	unitsRelTol  = 0.02 // kcal against kJ/4.184
	unitsAbsTol  = 1.0  // kcal, rounding on the label
	eps          = 1e-9 // float error at tolerance boundaries
)

// RawLabel is the label data as printed (ADR 007): serving size, units and
// energy without conversion; only NormalizeLabel converts
type RawLabel struct {
	SourceText string // verbatim label fragment with nutrition values
	Name       string
	Brand      string
	PerAmount  float64 // "per 100 g" gives 100, "per 30 g serving" gives 30
	PerUnit    string  // g or ml
	Kcal       *float64
	KJ         *float64
	Protein    float64
	Fat        float64
	Carbs      float64
	PackAmount *float64 // nil when not visible on the photo
	PackUnit   string   // g, kg, ml or l
	Qualifiers []string // model notes such as "fat: no more than 0.5 g"
}

var packUnits = map[string]struct {
	basis Basis
	mult  float64
}{
	"g": {BasisGram, 1}, "kg": {BasisGram, 1000},
	"ml": {BasisML, 1}, "l": {BasisML, 1000},
}

// NormalizeLabel converts printed values to a Label per 100 basis units and
// returns the issues that require user review
// FR-05 checks (CheckLabel) run separately on the result
func NormalizeLabel(r RawLabel) (Label, []Issue) {
	var issues []Issue

	add := func(code, format string, args ...any) {
		issues = append(issues, Issue{Code: code, Message: fmt.Sprintf(format, args...)})
	}

	l := Label{Name: r.Name, Brand: r.Brand, Basis: Basis(r.PerUnit)}
	scale := 1.0

	switch {
	case l.Basis != BasisGram && l.Basis != BasisML:
		add(IssueBadPer, "serving unit %q, want g or ml", r.PerUnit)
	case r.PerAmount <= 0:
		add(IssueBadPer, "serving size %g %s, must be positive", r.PerAmount, r.PerUnit)
	case math.Abs(r.PerAmount-100) > eps:
		scale = 100 / r.PerAmount

		add(IssuePerPortion, "label gives values per %g %s, converted to per 100", r.PerAmount, r.PerUnit)
	}

	var kcal float64

	switch {
	case r.Kcal != nil:
		kcal = *r.Kcal

		if r.KJ != nil {
			fromKJ := *r.KJ / kjPerKcal

			if tol := math.Max(unitsRelTol*kcal, unitsAbsTol); math.Abs(kcal-fromKJ) > tol+eps {
				add(IssueEnergyUnits, "%g kcal, but %g kJ is %.1f kcal", kcal, *r.KJ, fromKJ)
			}
		}
	case r.KJ != nil:
		kcal = round(*r.KJ/kjPerKcal, 1)
	default:
		add(IssueNoEnergy, "label has neither kcal nor kJ")
	}

	l.Per100 = Per100{
		Kcal:    round(kcal*scale, 2),
		Protein: round(r.Protein*scale, 2),
		Fat:     round(r.Fat*scale, 2),
		Carbs:   round(r.Carbs*scale, 2),
	}

	if r.PackAmount != nil {
		u, ok := packUnits[r.PackUnit]

		switch {
		case !ok:
			add(IssuePackUnit, "pack unit %q, want g, kg, ml or l", r.PackUnit)
		case u.basis != l.Basis:
			add(IssuePackUnit, "pack is %g %s while values are per 100 %s, cannot convert without density", *r.PackAmount, r.PackUnit, r.PerUnit)
		default:
			size := round(*r.PackAmount*u.mult, 2)
			l.PackSize = &size
		}
	}

	if len(r.Qualifiers) > 0 {
		add(IssueQualified, "model notes: %s", strings.Join(r.Qualifiers, "; "))
	}

	return l, issues
}

func round(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))

	return math.Round(v*p) / p
}

// CheckLabel checks a label against FR-05 and returns every failed check
func CheckLabel(l Label) []Issue {
	var issues []Issue

	add := func(code, format string, args ...any) {
		issues = append(issues, Issue{Code: code, Message: fmt.Sprintf(format, args...)})
	}

	p := l.Per100

	if l.Basis != BasisGram && l.Basis != BasisML {
		add(IssueBadBasis, "basis %q, want g or ml", l.Basis)
	}

	for _, v := range []struct {
		name string
		val  float64
	}{{"kcal", p.Kcal}, {"protein", p.Protein}, {"fat", p.Fat}, {"carbs", p.Carbs}} {
		if v.val < 0 {
			add(IssueNegative, "%s: negative value %g", v.name, v.val)
		}
	}

	formula := 4*p.Protein + 9*p.Fat + 4*p.Carbs

	if tol := math.Max(energyRelTol*p.Kcal, energyAbsTol); math.Abs(p.Kcal-formula) > tol+eps {
		add(IssueEnergyMismatch, "label says %g kcal, but 4*P + 9*F + 4*C gives %.1f (tolerance %.1f)", p.Kcal, formula, tol)
	}

	if sum := p.Protein + p.Fat + p.Carbs; sum > maxMacros+eps {
		add(IssueMacrosOver100, "P + F + C = %g per 100 %s, over %g", sum, l.Basis, maxMacros)
	}

	if p.Kcal > maxKcal+eps {
		add(IssueKcalOver900, "%g kcal per 100 %s, over %g", p.Kcal, l.Basis, maxKcal)
	}

	if l.PackSize != nil && *l.PackSize <= 0 {
		add(IssueBadPackSize, "pack size %g, must be positive", *l.PackSize)
	}

	return issues
}

// CompareLabels cross-checks two independent readings of the same label
// numbers differ when the difference is over max(2%, 0.1)
// name and brand are not compared: models spell them differently and they do
// not affect nutrition math
func CompareLabels(a, b Label) []Issue {
	var issues []Issue

	add := func(format string, args ...any) {
		issues = append(issues, Issue{Code: IssueCrossMismatch, Message: fmt.Sprintf(format, args...)})
	}

	if a.Basis != b.Basis {
		add("basis: %q and %q", a.Basis, b.Basis)
	}

	for _, v := range []struct {
		name string
		x, y float64
	}{
		{"kcal", a.Per100.Kcal, b.Per100.Kcal},
		{"protein", a.Per100.Protein, b.Per100.Protein},
		{"fat", a.Per100.Fat, b.Per100.Fat},
		{"carbs", a.Per100.Carbs, b.Per100.Carbs},
	} {
		if differ(v.x, v.y) {
			add("%s: %g and %g", v.name, v.x, v.y)
		}
	}

	switch {
	case (a.PackSize == nil) != (b.PackSize == nil):
		add("pack size: %s and %s", fmtPack(a.PackSize), fmtPack(b.PackSize))
	case a.PackSize != nil && differ(*a.PackSize, *b.PackSize):
		add("pack size: %g and %g", *a.PackSize, *b.PackSize)
	}

	return issues
}

func differ(x, y float64) bool {
	tol := math.Max(crossRelTol*math.Max(math.Abs(x), math.Abs(y)), crossAbsTol)

	return math.Abs(x-y) > tol+eps
}

func fmtPack(p *float64) string {
	if p == nil {
		return "not printed"
	}

	return fmt.Sprintf("%g", *p)
}
