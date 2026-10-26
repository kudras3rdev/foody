package catalog

import (
	"testing"
)

func ptr(v float64) *float64 { return &v }

// yogurt is a typical label: 4*2.8 + 9*2.4 + 4*11.5 = 78.8 against 79 kcal
func yogurt() Label {
	return Label{Name: "Йогурт", Basis: BasisGram, Per100: Per100{Kcal: 79, Protein: 2.8, Fat: 2.4, Carbs: 11.5}, PackSize: ptr(150)}
}

func codes(issues []Issue) []string {
	var c []string
	for _, i := range issues {
		c = append(c, i.Code)
	}

	return c
}

func equalCodes(got []Issue, want []string) bool {
	c := codes(got)
	if len(c) != len(want) {
		return false
	}

	for i := range c {
		if c[i] != want[i] {
			return false
		}
	}

	return true
}

func TestCheckLabel(t *testing.T) {
	tests := []struct {
		name  string
		label func(l *Label)
		want  []string
	}{
		{name: "typical yogurt", label: func(l *Label) {}},
		{name: "no pack size", label: func(l *Label) { l.PackSize = nil }},
		{name: "zero kcal drink", label: func(l *Label) {
			l.Basis, l.Per100 = BasisML, Per100{}
		}},
		{name: "diet drink within absolute tolerance", label: func(l *Label) {
			// 4*0.9 = 3.6 against 1 kcal: 15% of 1 is 0.15, but the absolute tolerance is 5 kcal
			l.Basis, l.Per100 = BasisML, Per100{Kcal: 1, Carbs: 0.9}
		}},
		{name: "zero kcal but sugar", label: func(l *Label) {
			l.Basis, l.Per100 = BasisML, Per100{Kcal: 0, Carbs: 10}
		}, want: []string{IssueEnergyMismatch}},
		{name: "tolerance is relative to label kcal", label: func(l *Label) {
			// formula 100, kcal 115: difference 15, tolerance 0.15*115 = 17.25
			l.Per100 = Per100{Kcal: 115, Carbs: 25}
		}},
		{name: "exact boundary of 15 percent", label: func(l *Label) {
			// formula 85, kcal 100, tolerance 15
			l.Per100 = Per100{Kcal: 100, Carbs: 21.25}
		}},
		{name: "just over 15 percent", label: func(l *Label) {
			l.Per100 = Per100{Kcal: 100, Carbs: 21.2}
		}, want: []string{IssueEnergyMismatch}},
		{name: "kj read as kcal", label: func(l *Label) {
			l.Per100.Kcal = 330 // 79 kcal = 330 kJ
		}, want: []string{IssueEnergyMismatch}},
		{name: "macros over 100", label: func(l *Label) {
			l.Per100 = Per100{Kcal: 420, Protein: 5, Fat: 0, Carbs: 100}
		}, want: []string{IssueMacrosOver100}},
		{name: "pure oil is fine", label: func(l *Label) {
			l.Per100 = Per100{Kcal: 899, Fat: 99.9}
		}},
		{name: "kcal over 900", label: func(l *Label) {
			l.Per100 = Per100{Kcal: 905, Fat: 100}
		}, want: []string{IssueKcalOver900}},
		{name: "negative values", label: func(l *Label) {
			l.Per100 = Per100{Kcal: 79, Protein: -2.8, Fat: 2.4, Carbs: 11.5}
		}, want: []string{IssueNegative, IssueEnergyMismatch}},
		{name: "bad basis", label: func(l *Label) { l.Basis = "portion" }, want: []string{IssueBadBasis}},
		{name: "zero pack size", label: func(l *Label) { l.PackSize = ptr(0) }, want: []string{IssueBadPackSize}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := yogurt()
			tt.label(&l)

			got := CheckLabel(l)
			if !equalCodes(got, tt.want) {
				t.Errorf("CheckLabel = %+v, want codes %v", got, tt.want)
			}
		})
	}
}

func TestCheckLabelMessage(t *testing.T) {
	l := yogurt()
	l.Per100.Kcal = 330

	got := CheckLabel(l)
	if len(got) != 1 || got[0].Message != "label says 330 kcal, but 4*P + 9*F + 4*C gives 78.8 (tolerance 49.5)" {
		t.Errorf("message = %+v", got)
	}
}

func TestCompareLabels(t *testing.T) {
	tests := []struct {
		name   string
		change func(b *Label)
		want   int
	}{
		{name: "identical", change: func(b *Label) {}},
		{name: "name and brand ignored", change: func(b *Label) { b.Name, b.Brand = "Activia клубника", "Danone" }},
		{name: "within 2 percent", change: func(b *Label) { b.Per100.Kcal = 80.5 }},
		{name: "over 2 percent", change: func(b *Label) { b.Per100.Kcal = 81 }, want: 1},
		{name: "small values within 0.1", change: func(b *Label) { b.Per100.Fat = 2.5 }},
		{name: "decimal shift", change: func(b *Label) { b.Per100.Protein = 28 }, want: 1},
		{name: "basis differs", change: func(b *Label) { b.Basis = BasisML }, want: 1},
		{name: "pack size missing in one", change: func(b *Label) { b.PackSize = nil }, want: 1},
		{name: "pack size differs", change: func(b *Label) { b.PackSize = ptr(120) }, want: 1},
		{name: "several fields", change: func(b *Label) { b.Per100.Kcal, b.Per100.Fat = 100, 5 }, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, b := yogurt(), yogurt()
			tt.change(&b)

			got := CompareLabels(a, b)
			if len(got) != tt.want {
				t.Errorf("CompareLabels = %+v, want %d issues", got, tt.want)
			}

			for _, i := range got {
				if i.Code != IssueCrossMismatch {
					t.Errorf("code = %q", i.Code)
				}
			}
		})
	}
}

func TestCompareLabelsZeroValues(t *testing.T) {
	a := Label{Basis: BasisML, Per100: Per100{Kcal: 0, Fat: 0}}

	b := Label{Basis: BasisML, Per100: Per100{Kcal: 0.1, Fat: 0.1}}
	if got := CompareLabels(a, b); len(got) != 0 {
		t.Errorf("0 vs 0.1 must match, got %+v", got)
	}
}

// blini is the golden set label 001: inline text with kJ and kcal
func blini() RawLabel {
	return RawLabel{
		SourceText: "Пищевая ценность 100 г продукта: белки – 6,4 г; жиры – 2,5 г; углеводы – 21,7 г. Энергетическая ценность: 570,2 кДж / 134,9 ккал.",
		Name:       "Блинчики с творогом", PerAmount: 100, PerUnit: "g",
		Kcal: ptr(134.9), KJ: ptr(570.2), Protein: 6.4, Fat: 2.5, Carbs: 21.7,
		PackAmount: ptr(800), PackUnit: "g",
	}
}

func TestNormalizeLabel(t *testing.T) {
	tests := []struct {
		name   string
		change func(r *RawLabel)
		want   Label
		issues []string
	}{
		{
			name:   "per 100 g as printed",
			change: func(r *RawLabel) {},
			want:   Label{Basis: BasisGram, Per100: Per100{Kcal: 134.9, Protein: 6.4, Fat: 2.5, Carbs: 21.7}, PackSize: ptr(800)},
		},
		{
			name:   "only kj",
			change: func(r *RawLabel) { r.Kcal = nil },
			want:   Label{Basis: BasisGram, Per100: Per100{Kcal: 136.3, Protein: 6.4, Fat: 2.5, Carbs: 21.7}, PackSize: ptr(800)},
		},
		{
			name:   "only kcal",
			change: func(r *RawLabel) { r.KJ = nil },
			want:   Label{Basis: BasisGram, Per100: Per100{Kcal: 134.9, Protein: 6.4, Fat: 2.5, Carbs: 21.7}, PackSize: ptr(800)},
		},
		{
			name:   "no energy",
			change: func(r *RawLabel) { r.Kcal, r.KJ = nil, nil },
			want:   Label{Basis: BasisGram, Per100: Per100{Protein: 6.4, Fat: 2.5, Carbs: 21.7}, PackSize: ptr(800)},
			issues: []string{IssueNoEnergy},
		},
		{
			name: "kcal and kj disagree",
			// 330 kJ is 78.9 kcal, not 134.9: the model read the wrong column
			change: func(r *RawLabel) { r.KJ = ptr(330) },
			want:   Label{Basis: BasisGram, Per100: Per100{Kcal: 134.9, Protein: 6.4, Fat: 2.5, Carbs: 21.7}, PackSize: ptr(800)},
			issues: []string{IssueEnergyUnits},
		},
		{
			name: "tiny kcal within absolute tolerance",
			// "Dobry Cola sugar free" (003): 1.4 kJ / 0.3 kcal
			change: func(r *RawLabel) {
				r.PerUnit, r.Kcal, r.KJ, r.Protein, r.Fat, r.Carbs = "ml", ptr(0.3), ptr(1.4), 0, 0, 0.1
				r.PackAmount, r.PackUnit = ptr(1.5), "l"
			},
			want: Label{Basis: BasisML, Per100: Per100{Kcal: 0.3, Carbs: 0.1}, PackSize: ptr(1500)},
		},
		{
			name: "per portion converted",
			change: func(r *RawLabel) {
				r.PerAmount, r.Kcal, r.KJ, r.Protein, r.Fat, r.Carbs = 30, ptr(120), nil, 3, 4.5, 16.5
			},
			want:   Label{Basis: BasisGram, Per100: Per100{Kcal: 400, Protein: 10, Fat: 15, Carbs: 55}, PackSize: ptr(800)},
			issues: []string{IssuePerPortion},
		},
		{
			name:   "kg pack",
			change: func(r *RawLabel) { r.PackAmount, r.PackUnit = ptr(0.8), "kg" },
			want:   Label{Basis: BasisGram, Per100: Per100{Kcal: 134.9, Protein: 6.4, Fat: 2.5, Carbs: 21.7}, PackSize: ptr(800)},
		},
		{
			name:   "no pack size",
			change: func(r *RawLabel) { r.PackAmount, r.PackUnit = nil, "" },
			want:   Label{Basis: BasisGram, Per100: Per100{Kcal: 134.9, Protein: 6.4, Fat: 2.5, Carbs: 21.7}},
		},
		{
			name:   "pack in ml for per 100 g",
			change: func(r *RawLabel) { r.PackAmount, r.PackUnit = ptr(500), "ml" },
			want:   Label{Basis: BasisGram, Per100: Per100{Kcal: 134.9, Protein: 6.4, Fat: 2.5, Carbs: 21.7}},
			issues: []string{IssuePackUnit},
		},
		{
			name:   "unknown pack unit",
			change: func(r *RawLabel) { r.PackUnit = "шт" },
			want:   Label{Basis: BasisGram, Per100: Per100{Kcal: 134.9, Protein: 6.4, Fat: 2.5, Carbs: 21.7}},
			issues: []string{IssuePackUnit},
		},
		{
			name:   "bad per unit",
			change: func(r *RawLabel) { r.PerUnit, r.PackAmount = "portion", nil },
			want:   Label{Basis: "portion", Per100: Per100{Kcal: 134.9, Protein: 6.4, Fat: 2.5, Carbs: 21.7}},
			issues: []string{IssueBadPer},
		},
		{
			name:   "zero per amount",
			change: func(r *RawLabel) { r.PerAmount = 0 },
			want:   Label{Basis: BasisGram, Per100: Per100{Kcal: 134.9, Protein: 6.4, Fat: 2.5, Carbs: 21.7}, PackSize: ptr(800)},
			issues: []string{IssueBadPer},
		},
		{
			name:   "qualifiers",
			change: func(r *RawLabel) { r.Qualifiers = []string{"жиры: не более 2,5 г"} },
			want:   Label{Basis: BasisGram, Per100: Per100{Kcal: 134.9, Protein: 6.4, Fat: 2.5, Carbs: 21.7}, PackSize: ptr(800)},
			issues: []string{IssueQualified},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := blini()
			tt.change(&r)

			got, issues := NormalizeLabel(r)
			if got.Basis != tt.want.Basis || got.Per100 != tt.want.Per100 || !samePack(got.PackSize, tt.want.PackSize) {
				t.Errorf("label = %+v pack %v, want %+v pack %v", got, fmtPack(got.PackSize), tt.want, fmtPack(tt.want.PackSize))
			}

			if !equalCodes(issues, tt.issues) {
				t.Errorf("issues = %+v, want %v", issues, tt.issues)
			}
		})
	}
}

func samePack(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}

// golden set references pass FR-05 checks after normalization
func TestGoldenLabelsPassChecks(t *testing.T) {
	for name, r := range map[string]RawLabel{
		"001 blini":     blini(),
		"002 nestle":    {PerAmount: 100, PerUnit: "g", Kcal: ptr(365), KJ: ptr(1528), Protein: 9.8, Fat: 5.1, Carbs: 66},
		"003 cola":      {PerAmount: 100, PerUnit: "ml", Kcal: ptr(0.3), KJ: ptr(1.4), Carbs: 0.1, PackAmount: ptr(1.5), PackUnit: "l"},
		"004 savushkin": {PerAmount: 100, PerUnit: "g", Kcal: ptr(66.8), KJ: ptr(281.4), Protein: 8, Fat: 2, Carbs: 4.2, PackAmount: ptr(250), PackUnit: "g"},
	} {
		l, issues := NormalizeLabel(r)

		issues = append(issues, CheckLabel(l)...)
		if len(issues) > 0 {
			t.Errorf("%s: %+v", name, issues)
		}
	}
}
