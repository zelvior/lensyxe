package code

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

// branchyFunction returns a single function body with n decision tokens,
// written so the estimator sees exactly n branches.
func branchyFunction(name string, branches int) string {
	var b strings.Builder
	b.WriteString("func " + name + "() {\n")
	for i := 0; i < branches; i++ {
		b.WriteString("\tif cond")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(" {\n\t\tswitch v {\n\t\tcase 1:\n\t\tdefault:\n\t\t}\n\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// A file is only a confirmed hotspot when size, churn, AND complexity all
// cross their thresholds. Each missing factor must prevent confirmation.
func TestHotspotClassification(t *testing.T) {
	dir := t.TempDir()

	// Large AND complex: one function holding 40 branch points plus filler to
	// clear the 500-line size bar.
	var hot strings.Builder
	hot.WriteString("package p\n")
	hot.WriteString(branchyFunction("hot", 40))
	for i := 0; i < 470; i++ {
		hot.WriteString("var pad")
		hot.WriteString(strings.Repeat("X", i%5))
		hot.WriteString(" = 1\n")
	}
	writeFile(t, filepath.Join(dir, "hot.go"), hot.String())

	// Large but simple: same size, no decision tokens.
	var flat strings.Builder
	flat.WriteString("package p\n")
	for i := 0; i < 620; i++ {
		flat.WriteString("var flat")
		flat.WriteString(strings.Repeat("X", i%5))
		flat.WriteString(" = 1\n")
	}
	writeFile(t, filepath.Join(dir, "flat.go"), flat.String())

	churn := map[string]int{"hot.go": 120, "flat.go": 5}

	res, err := Analyze(dir, DefaultConfig(), churn)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Stats.Hotspots) != 2 {
		t.Fatalf("hotspots = %d, want 2:\n%+v", len(res.Stats.Hotspots), res.Stats.Hotspots)
	}

	byPath := map[string]models.Hotspot{}
	for _, h := range res.Stats.Hotspots {
		byPath[h.Path] = h
	}
	hotSpot, ok := byPath["hot.go"]
	if !ok {
		t.Fatalf("hot.go missing from hotspots: %+v", res.Stats.Hotspots)
	}
	if !hotSpot.Confirmed {
		t.Errorf("hot.go should be confirmed; got classification %q, complexity %.1f, lines %d, churn %d",
			hotSpot.Classification, hotSpot.Complexity, hotSpot.Lines, hotSpot.Churn)
	}
	if hotSpot.Level.Rank() < models.ComplexityHigh.Rank() {
		t.Errorf("hot.go level = %v, want at least high", hotSpot.Level)
	}
	if byPath["flat.go"].Confirmed {
		t.Errorf("flat.go must not be confirmed: classification %q, complexity %.1f",
			byPath["flat.go"].Classification, byPath["flat.go"].Complexity)
	}
}

// Each of the three factors must be individually necessary.
func TestHotspotRequiresAllThreeFactors(t *testing.T) {
	cases := []struct {
		name       string
		churn      int
		complexify bool
		want       bool
	}{
		{name: "all three met", churn: 50, complexify: true, want: true},
		{name: "churn missing", churn: 5, complexify: true, want: false},
		{name: "complexity missing", churn: 50, complexify: false, want: false},
		{name: "both churn and complexity missing", churn: 0, complexify: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var b strings.Builder
			b.WriteString("package p\n")
			if tc.complexify {
				b.WriteString(branchyFunction("hot", 40))
			}
			for i := 0; i < 620; i++ {
				b.WriteString("var pad")
				b.WriteString(strings.Repeat("X", i%5))
				b.WriteString(" = 1\n")
			}
			writeFile(t, filepath.Join(dir, "f.go"), b.String())

			res, err := Analyze(dir, DefaultConfig(), map[string]int{"f.go": tc.churn})
			if err != nil {
				t.Fatalf("Analyze: %v", err)
			}
			if len(res.Stats.Hotspots) == 0 {
				t.Fatal("expected a size-based candidate")
			}
			got := res.Stats.Hotspots[0].Confirmed
			if got != tc.want {
				t.Errorf("confirmed = %v, want %v (classification %q, complexity %.1f, churn %d)",
					got, tc.want, res.Stats.Hotspots[0].Classification,
					res.Stats.Hotspots[0].Complexity, res.Stats.Hotspots[0].Churn)
			}
		})
	}
}

// Confirmed hotspots must sort ahead of unconfirmed ones.
func TestHotspotOrderingPutsConfirmedFirst(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 900; i++ {
		b.WriteString("var x = 1\n")
	}
	writeFile(t, filepath.Join(dir, "huge_flat.go"), b.String())

	var c strings.Builder
	c.WriteString("package p\n")
	for i := 0; i < 520; i++ {
		c.WriteString("if a && b { if c || d { for x := range y { } } }\n")
	}
	writeFile(t, filepath.Join(dir, "medium_complex.go"), c.String())

	res, err := Analyze(dir, DefaultConfig(), map[string]int{"medium_complex.go": 90})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Stats.Hotspots) < 2 {
		t.Fatalf("expected both files as hotspot candidates: %+v", res.Stats.Hotspots)
	}
	if !res.Stats.Hotspots[0].Confirmed {
		t.Errorf("first hotspot should be the confirmed one, got %+v", res.Stats.Hotspots[0])
	}
}

// Without churn data nothing can be confirmed, since churn is one of the three
// required factors.
func TestHotspotsUnconfirmedWithoutChurn(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 600; i++ {
		b.WriteString("if a && b { for x := range y { } }\n")
	}
	writeFile(t, filepath.Join(dir, "big.go"), b.String())

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Stats.Hotspots) == 0 {
		t.Fatal("expected a size-based candidate")
	}
	for _, h := range res.Stats.Hotspots {
		if h.Confirmed {
			t.Errorf("%s confirmed without churn data", h.Path)
		}
	}
}

// ApplyChurn must reclassify from retained records without re-walking the tree.
func TestApplyChurnReclassifies(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 600; i++ {
		b.WriteString("if a && b { for x := range y { } }\n")
	}
	writeFile(t, filepath.Join(dir, "big.go"), b.String())

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	for _, h := range res.Stats.Hotspots {
		if h.Confirmed {
			t.Fatal("precondition: nothing should be confirmed yet")
		}
	}

	updated := res.ApplyChurn(map[string]int{"big.go": 100}, DefaultConfig())
	if len(updated.Stats.Hotspots) != 1 {
		t.Fatalf("hotspots = %d, want 1", len(updated.Stats.Hotspots))
	}
	h := updated.Stats.Hotspots[0]
	if !h.Confirmed {
		t.Errorf("expected confirmation after churn join, got %+v", h)
	}
	if h.Churn != 100 {
		t.Errorf("churn = %d, want 100", h.Churn)
	}
	if !strings.Contains(h.Classification, "confirmed") {
		t.Errorf("classification = %q, want it to include 'confirmed'", h.Classification)
	}
}

func TestChurnMap(t *testing.T) {
	entries := []models.ChurnEntry{
		{Path: "a.go", Added: 10, Deleted: 5},
		{Path: "b.go", Added: 1, Deleted: 0},
	}
	got := ChurnMap(entries)
	if got["a.go"] != 15 || got["b.go"] != 1 {
		t.Errorf("ChurnMap = %v", got)
	}
	if ChurnMap(nil) != nil {
		t.Error("ChurnMap(nil) should return nil")
	}
}
