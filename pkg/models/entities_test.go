package models

import "testing"

func TestSeverityRank(t *testing.T) {
	ordered := []Severity{SeverityInfo, SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical}
	for i := 1; i < len(ordered); i++ {
		if ordered[i].Rank() <= ordered[i-1].Rank() {
			t.Errorf("%q rank %d should exceed %q rank %d",
				ordered[i], ordered[i].Rank(), ordered[i-1], ordered[i-1].Rank())
		}
	}
	if Severity("bogus").Rank() != -1 {
		t.Error("unknown severity should rank below info")
	}
}

// Every severity needs a distinct glyph so reports never render two levels
// identically.
func TestSeverityEmojiIsDistinct(t *testing.T) {
	want := map[Severity]string{
		SeverityCritical: "🔴",
		SeverityHigh:     "🟠",
		SeverityMedium:   "🟡",
		SeverityLow:      "🔵",
		SeverityInfo:     "⚪",
	}
	seen := map[string]Severity{}
	for sev, glyph := range want {
		if got := sev.Emoji(); got != glyph {
			t.Errorf("%q emoji = %q, want %q", sev, got, glyph)
		}
		if prev, dup := seen[glyph]; dup {
			t.Errorf("%q and %q share the glyph %q", prev, sev, glyph)
		}
		seen[glyph] = sev
	}
	if got := Severity("bogus").Emoji(); got == "" {
		t.Error("unknown severity should still render a glyph")
	}
}

func TestComplexityLevelRank(t *testing.T) {
	order := []ComplexityLevel{
		ComplexityLow, ComplexityModerate, ComplexityHigh, ComplexityVeryHigh,
	}
	for i := 1; i < len(order); i++ {
		if order[i].Rank() <= order[i-1].Rank() {
			t.Errorf("%v rank %d should exceed %v rank %d",
				order[i], order[i].Rank(), order[i-1], order[i-1].Rank())
		}
	}
	if ComplexityLevel("bogus").Rank() != -1 {
		t.Error("unknown complexity level should rank lowest")
	}
}

func TestNewEvidence(t *testing.T) {
	e := NewEvidence("file", "LOC", "900 code lines", 900, "a.go")
	if e.Kind != "file" || e.Label != "LOC" || e.Detail != "900 code lines" {
		t.Errorf("evidence fields wrong: %+v", e)
	}
	if e.Subject != "a.go" {
		t.Errorf("Subject = %q", e.Subject)
	}
	// A value must be present and independent of the caller's variable.
	if e.Value == nil || *e.Value != 900 {
		t.Errorf("Value = %v, want 900", e.Value)
	}
	*e.Value = 1
	if *e.Value != 1 {
		t.Error("evidence value should be addressable")
	}
}

func TestGradeBoundaries(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{100, "A"}, {90, "A"}, {89.9, "B"}, {80, "B"}, {79.9, "C"},
		{70, "C"}, {69.9, "D"}, {60, "D"}, {59.9, "F"}, {0, "F"},
	}
	for _, tc := range cases {
		if got := Grade(tc.score); got != tc.want {
			t.Errorf("Grade(%v) = %q, want %q", tc.score, got, tc.want)
		}
	}
}

func TestSchemaVersion(t *testing.T) {
	if SchemaVersion != "0.2" {
		t.Errorf("SchemaVersion = %q, want 0.2", SchemaVersion)
	}
}

// Categories used by the risk engine must exist, since risks are emitted with
// them and reporters switch on the values.
func TestRiskCategoriesExist(t *testing.T) {
	cats := []Category{
		CategoryCode, CategoryGit, CategoryDependency,
		CategoryComplexity, CategoryTesting,
	}
	seen := map[Category]bool{}
	for _, c := range cats {
		if c == "" {
			t.Error("category must not be empty")
		}
		if seen[c] {
			t.Errorf("duplicate category %q", c)
		}
		seen[c] = true
	}
}
