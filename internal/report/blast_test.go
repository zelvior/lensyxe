package report

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/internal/blast"
)

func blastResult() *blast.Result {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	return &blast.Result{
		Root:          "/repo",
		Commits:       30,
		FirstCommitAt: now.AddDate(0, -1, 0),
		LastCommitAt:  now,
		Changeset:     []string{"a.go", "b.go"},
		Predictions: []blast.Prediction{
			{Path: "c.go", Coupling: 0.67, Via: "a.go", Hops: 1, SharedCommits: 4},
			{Path: "d.go", Coupling: 0.5, Via: "c.go", Hops: 2, SharedCommits: 2},
		},
		Pairs: []blast.Pair{
			{A: "a.go", B: "c.go", SharedCommits: 4, CommitsA: 9, CommitsB: 6, Coupling: 0.67},
			{A: "e.go", B: "f.go", SharedCommits: 2, CommitsA: 2, CommitsB: 2, Coupling: 1},
		},
	}
}

// A wrong format verb prints %!s(int=4) rather than failing, so it can reach a
// release. Assert on the absence of the marker, not the presence of the number.
func TestRenderBlastHasNoFormatErrors(t *testing.T) {
	var b bytes.Buffer
	if err := RenderBlast(&b, blastResult()); err != nil {
		t.Fatalf("RenderBlast: %v", err)
	}
	if strings.Contains(b.String(), "%!") {
		t.Errorf("output contains a formatting error:\n%s", b.String())
	}
}

func TestRenderBlastShowsTheEssentials(t *testing.T) {
	var b bytes.Buffer
	if err := RenderBlast(&b, blastResult()); err != nil {
		t.Fatalf("RenderBlast: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		"30 commit",    // history depth, which governs trust
		"a.go", "b.go", // the changeset
		"PREDICTED AFFECTED",
		"c.go", "d.go", // both predictions
		"1 hop", "2 hops", // distance
		"4 commit",         // evidence count
		"via a.go",         // provenance
		"not a dependency", // the caveat
	} {
		if !strings.Contains(out, want) {
			t.Errorf("blast output is missing %q:\n%s", want, out)
		}
	}
}

// The insufficient-history verdict must appear instead of any prediction, and
// must not be phrased as a finding about the code.
func TestRenderBlastStatesInsufficientHistoryInsteadOfPredictions(t *testing.T) {
	res := blastResult()
	res.Predictions = nil
	res.Pairs = nil
	res.InsufficientHistory = "only 4 commit(s) of history, and co-change coupling " +
		"needs at least 10. No coupling is reported."

	var b bytes.Buffer
	if err := RenderBlast(&b, res); err != nil {
		t.Fatalf("RenderBlast: %v", err)
	}
	out := b.String()

	if !strings.Contains(out, "NO COUPLING REPORTED") {
		t.Errorf("the verdict header is missing:\n%s", out)
	}
	if !strings.Contains(out, "needs at least 10") {
		t.Errorf("the threshold is not named:\n%s", out)
	}
	if strings.Contains(out, "PREDICTED AFFECTED") {
		t.Errorf("predictions were rendered despite the verdict:\n%s", out)
	}
}

// A never-committed file is a finding in itself and must be marked.
func TestRenderBlastMarksFilesWithNoHistory(t *testing.T) {
	res := blastResult()
	res.Changeset = []string{"a.go", "brand-new.go"}
	res.Missing = []string{"brand-new.go"}
	res.Predictions = nil

	var b bytes.Buffer
	if err := RenderBlast(&b, res); err != nil {
		t.Fatalf("RenderBlast: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "brand-new.go") {
		t.Errorf("the never-committed file is not shown:\n%s", out)
	}
	if !strings.Contains(out, "never committed") {
		t.Errorf("the never-committed file is not explained:\n%s", out)
	}
}

// An empty prediction list must say so rather than printing an empty heading.
func TestRenderBlastWithNoPredictions(t *testing.T) {
	res := blastResult()
	res.Predictions = nil
	res.Pairs = nil

	var b bytes.Buffer
	if err := RenderBlast(&b, res); err != nil {
		t.Fatalf("RenderBlast: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "None") {
		t.Errorf("no-prediction case is not explained:\n%s", out)
	}
	if strings.Contains(out, "STRONGEST COUPLINGS") {
		t.Errorf("the couplings section rendered with no pairs:\n%s", out)
	}
}

// The rule must start on its own line, or it runs into the text above it.
func TestRenderBlastRuleStartsOnItsOwnLine(t *testing.T) {
	var b bytes.Buffer
	if err := RenderBlast(&b, blastResult()); err != nil {
		t.Fatalf("RenderBlast: %v", err)
	}
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, "---") && !strings.HasPrefix(strings.TrimSpace(line), "---") {
			t.Errorf("a rule runs into preceding text: %q", line)
		}
	}
}

func TestRenderBlastRejectsNil(t *testing.T) {
	var b bytes.Buffer
	if err := RenderBlast(&b, nil); err == nil {
		t.Error("RenderBlast(nil) returned no error")
	}
}

// The bar is fixed width so the numbers stay aligned.
func TestCouplingBarIsFixedWidth(t *testing.T) {
	plain := strings.NewReplacer().Replace(couplingBar(0))
	_ = plain
	for _, v := range []float64{0, 0.25, 0.5, 1} {
		bar := stripANSI(couplingBar(v))
		if len(bar) != 12 {
			t.Errorf("couplingBar(%v) = %q, length %d, want 12", v, bar, len(bar))
		}
	}
	// Out-of-range input must not overflow the bar.
	for _, v := range []float64{-1, 1.5, 99} {
		if got := len(stripANSI(couplingBar(v))); got != 12 {
			t.Errorf("couplingBar(%v) length %d, want 12", v, got)
		}
	}
}

// stripANSI removes SGR sequences so a styled string's length is comparable.
func stripANSI(s string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape && (r == 'm' || r == 'K'):
			inEscape = false
		case !inEscape:
			b.WriteRune(r)
		}
	}
	return b.String()
}
