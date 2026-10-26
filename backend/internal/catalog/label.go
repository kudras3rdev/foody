package catalog

import (
	"fmt"
	"math"
	"strings"
)

// Basis — база КБЖУ позиции: на 100 г или на 100 мл (ADR 001).
type Basis string

const (
	BasisGram Basis = "g"
	BasisML   Basis = "ml"
)

// Per100 — КБЖУ на 100 единиц базы.
type Per100 struct {
	Kcal    float64
	Protein float64
	Fat     float64
	Carbs   float64
}

// Label — данные этикетки товара (FR-04).
type Label struct {
	Name     string
	Brand    string
	Basis    Basis
	Per100   Per100
	PackSize *float64 // в единицах базы; nil — на этикетке не указан
}

// Коды непройденных проверок этикетки.
const (
	IssueNegative         = "negative_value"
	IssueEnergyMismatch   = "energy_mismatch"
	IssueMacrosOver100    = "macros_over_100"
	IssueKcalOver900      = "kcal_over_900"
	IssueBadBasis         = "bad_basis"
	IssueBadPackSize      = "bad_pack_size"
	IssueCrossMismatch    = "cross_check_mismatch"
	IssueCrossUnavailable = "cross_check_unavailable"
	IssueBadPer           = "bad_per"               // порция не указана или не в г/мл
	IssuePerPortion       = "per_portion_converted" // на этикетке не на 100 г/мл, пересчитано кодом
	IssueNoEnergy         = "no_energy"             // нет ни ккал, ни кДж
	IssueEnergyUnits      = "kcal_kj_mismatch"      // ккал и кДж на этикетке не сходятся
	IssuePackUnit         = "pack_unit_mismatch"    // упаковка в массе, а база — в объёме, или наоборот
	IssueQualified        = "qualified_values"      // «не более», диапазоны, плохо видные цифры
)

// Issue — непройденная проверка; этикетка с проблемами уходит на подтверждение (FR-05).
type Issue struct {
	Code    string
	Message string
}

// Допуски проверок (FR-05, ADR 001, architecture.md 3.3).
const (
	energyRelTol = 0.15 // формула 4·Б + 9·Ж + 4·У против калорийности
	energyAbsTol = 5.0  // ккал: для нулевых и малокалорийных продуктов
	maxMacros    = 100.0
	maxKcal      = 900.0
	crossRelTol  = 0.02 // сверка двух моделей
	crossAbsTol  = 0.1  // для нулей и малых значений
	kjPerKcal    = 4.184
	unitsRelTol  = 0.02 // ккал против кДж/4.184
	unitsAbsTol  = 1.0  // ккал: округление на этикетке
	eps          = 1e-9 // погрешность float на границах допусков
)

// RawLabel — данные этикетки как напечатаны (ADR 007): порция, единицы
// и энергия без пересчёта. Пересчитывает только NormalizeLabel.
type RawLabel struct {
	SourceText string // дословный фрагмент этикетки с КБЖУ
	Name       string
	Brand      string
	PerAmount  float64 // «на 100 г» → 100, «на порцию 30 г» → 30
	PerUnit    string  // g или ml
	Kcal       *float64
	KJ         *float64
	Protein    float64
	Fat        float64
	Carbs      float64
	PackAmount *float64 // nil — на фото не видно
	PackUnit   string   // g, kg, ml или l
	Qualifiers []string // пометки модели: «жиры: не более 0,5 г»
}

var packUnits = map[string]struct {
	basis Basis
	mult  float64
}{
	"g": {BasisGram, 1}, "kg": {BasisGram, 1000},
	"ml": {BasisML, 1}, "l": {BasisML, 1000},
}

// NormalizeLabel переводит напечатанное в Label на 100 единиц базы и
// возвращает проблемы, из-за которых этикетку нужно подтвердить.
// Проверки FR-05 (CheckLabel) вызываются отдельно, на результате.
func NormalizeLabel(r RawLabel) (Label, []Issue) {
	var issues []Issue
	add := func(code, format string, args ...any) {
		issues = append(issues, Issue{Code: code, Message: fmt.Sprintf(format, args...)})
	}
	l := Label{Name: r.Name, Brand: r.Brand, Basis: Basis(r.PerUnit)}

	scale := 1.0
	switch {
	case l.Basis != BasisGram && l.Basis != BasisML:
		add(IssueBadPer, "единица порции %q, ожидается g или ml", r.PerUnit)
	case r.PerAmount <= 0:
		add(IssueBadPer, "порция %g %s, должна быть больше нуля", r.PerAmount, r.PerUnit)
	case math.Abs(r.PerAmount-100) > eps:
		scale = 100 / r.PerAmount
		add(IssuePerPortion, "на этикетке значения на %g %s, пересчитано на 100", r.PerAmount, r.PerUnit)
	}

	var kcal float64
	switch {
	case r.Kcal != nil:
		kcal = *r.Kcal
		if r.KJ != nil {
			fromKJ := *r.KJ / kjPerKcal
			if tol := math.Max(unitsRelTol*kcal, unitsAbsTol); math.Abs(kcal-fromKJ) > tol+eps {
				add(IssueEnergyUnits, "%g ккал, а %g кДж — это %.1f ккал", kcal, *r.KJ, fromKJ)
			}
		}
	case r.KJ != nil:
		kcal = round(*r.KJ/kjPerKcal, 1)
	default:
		add(IssueNoEnergy, "на этикетке нет ни ккал, ни кДж")
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
			add(IssuePackUnit, "единица упаковки %q, ожидается g, kg, ml или l", r.PackUnit)
		case u.basis != l.Basis:
			add(IssuePackUnit, "упаковка %g %s, а значения на 100 %s: без плотности не пересчитать", *r.PackAmount, r.PackUnit, r.PerUnit)
		default:
			size := round(*r.PackAmount*u.mult, 2)
			l.PackSize = &size
		}
	}

	if len(r.Qualifiers) > 0 {
		add(IssueQualified, "пометки модели: %s", strings.Join(r.Qualifiers, "; "))
	}
	return l, issues
}

func round(v float64, digits int) float64 {
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}

// CheckLabel проверяет этикетку по FR-05 и возвращает все непройденные проверки.
func CheckLabel(l Label) []Issue {
	var issues []Issue
	add := func(code, format string, args ...any) {
		issues = append(issues, Issue{Code: code, Message: fmt.Sprintf(format, args...)})
	}
	p := l.Per100

	if l.Basis != BasisGram && l.Basis != BasisML {
		add(IssueBadBasis, "база %q, ожидается g или ml", l.Basis)
	}
	for _, v := range []struct {
		name string
		val  float64
	}{{"ккал", p.Kcal}, {"белки", p.Protein}, {"жиры", p.Fat}, {"углеводы", p.Carbs}} {
		if v.val < 0 {
			add(IssueNegative, "%s: отрицательное значение %g", v.name, v.val)
		}
	}

	formula := 4*p.Protein + 9*p.Fat + 4*p.Carbs
	if tol := math.Max(energyRelTol*p.Kcal, energyAbsTol); math.Abs(p.Kcal-formula) > tol+eps {
		add(IssueEnergyMismatch, "калорийность %g, а по формуле 4·Б + 9·Ж + 4·У выходит %.1f (допуск ±%.1f)", p.Kcal, formula, tol)
	}
	if sum := p.Protein + p.Fat + p.Carbs; sum > maxMacros+eps {
		add(IssueMacrosOver100, "Б + Ж + У = %g на 100 %s, больше %g", sum, l.Basis, maxMacros)
	}
	if p.Kcal > maxKcal+eps {
		add(IssueKcalOver900, "калорийность %g на 100 %s, больше %g", p.Kcal, l.Basis, maxKcal)
	}
	if l.PackSize != nil && *l.PackSize <= 0 {
		add(IssueBadPackSize, "размер упаковки %g, должен быть больше нуля", *l.PackSize)
	}
	return issues
}

// CompareLabels сверяет два независимых извлечения одной этикетки. Числа
// расходятся, если разница больше max(2%, 0.1). Название и бренд не сверяются:
// модели пишут их по-разному, а на подсчёт КБЖУ они не влияют.
func CompareLabels(a, b Label) []Issue {
	var issues []Issue
	add := func(format string, args ...any) {
		issues = append(issues, Issue{Code: IssueCrossMismatch, Message: fmt.Sprintf(format, args...)})
	}
	if a.Basis != b.Basis {
		add("база: %q и %q", a.Basis, b.Basis)
	}
	for _, v := range []struct {
		name string
		x, y float64
	}{
		{"ккал", a.Per100.Kcal, b.Per100.Kcal},
		{"белки", a.Per100.Protein, b.Per100.Protein},
		{"жиры", a.Per100.Fat, b.Per100.Fat},
		{"углеводы", a.Per100.Carbs, b.Per100.Carbs},
	} {
		if differ(v.x, v.y) {
			add("%s: %g и %g", v.name, v.x, v.y)
		}
	}
	switch {
	case (a.PackSize == nil) != (b.PackSize == nil):
		add("размер упаковки: %s и %s", fmtPack(a.PackSize), fmtPack(b.PackSize))
	case a.PackSize != nil && differ(*a.PackSize, *b.PackSize):
		add("размер упаковки: %g и %g", *a.PackSize, *b.PackSize)
	}
	return issues
}

func differ(x, y float64) bool {
	tol := math.Max(crossRelTol*math.Max(math.Abs(x), math.Abs(y)), crossAbsTol)
	return math.Abs(x-y) > tol+eps
}

func fmtPack(p *float64) string {
	if p == nil {
		return "не указан"
	}
	return fmt.Sprintf("%g", *p)
}
