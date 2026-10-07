package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/internal/gap"
)

func gapResult() *gap.Result {
	return &gap.Result{
		Root: "/repo",
		Profile: gap.ProfileSummary{
			Source: gap.SourcePprof, Path: "cpu.pprof",
			Observations: 1000, Attributed: 900, Coverage: 0.9,
			Attribution: gap.AttributionFileAndFunction,
			WindowStart: "2026-10-06T10:00:00Z",
			WindowEnd:   "2026-10-06T10:01:00Z",
			Notes:       []string{"profile covers one minute"},
		},
		Files:   120,
		Matched: 40,
		Hot: []gap.Entry{{
			Path: "internal/hot/handler.go", Static: 82.5, CriticalRisk: 241.5,
			Hits: 50000, ComplexityScore: 44, CodeLines: 410, Churn: 300,
			Bucket: gap.BucketHot,
		}},
		Debt: []gap.Entry{{
			Path: "internal/debt/legacy.go", Static: 61, Hits: 0,
			ComplexityScore: 40, CodeLines: 480, Churn: 220,
			Bucket: gap.BucketDebt, Note: "no runtime evidence",
		}},
		Phantom: []gap.Entry{{
			Path: "internal/ghost/unreached.go", Static: 44, Hits: 0,
			ComplexityScore: 30, CodeLines: 210, Churn: 90,
			Bucket: gap.BucketPhantom, Phantom: true,
		}},
	}
}

func TestRenderGapShowsAllThreeSections(t *testing.T) {
	var b bytes.Buffer
	if err := RenderGap(&b, gapResult(), "table"); err != nil {
		t.Fatalf("RenderGap: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		"🔥 CRITICAL PATH HOTSPOTS",
		"💤 DEPRIORITIZED DEBT",
		"👻 PHANTOM CODE CANDIDATES",
		"internal/hot/handler.go",
		"internal/debt/legacy.go",
		"internal/ghost/unreached.go",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// The runtime evidence governs every number below it, so it must be printed and
// must not be visually buried under the findings.
func TestCoverageIsPrintedBeforeTheFindings(t *testing.T) {
	var b bytes.Buffer
	if err := RenderGap(&b, gapResult(), "table"); err != nil {
		t.Fatalf("RenderGap: %v", err)
	}
	out := b.String()
	coverageAt := strings.Index(out, "resolved to source")
	hotAt := strings.Index(out, "CRITICAL PATH HOTSPOTS")
	if coverageAt < 0 {
		t.Fatal("the coverage figure is not printed")
	}
	if coverageAt > hotAt {
		t.Error("the coverage figure appears after the findings it qualifies")
	}
	if !strings.Contains(out, "90%") {
		t.Errorf("the resolved percentage is missing:\n%s", out)
	}
}

// The formula and its consequence at zero hits are documented in the output,
// because a reader seeing a 0.0 risk on the debt and phantom lists needs to know
// that is arithmetic rather than a bug.
func TestFormulaIsStated(t *testing.T) {
	var b bytes.Buffer
	if err := RenderGap(&b, gapResult(), "table"); err != nil {
		t.Fatalf("RenderGap: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "log10") {
		t.Error("the formula is not stated")
	}
	if !strings.Contains(out, "log10(1) = 0") {
		t.Error("the zero-hits consequence is not explained")
	}
}

// The claim that code never ran must be qualified: it is the easiest thing in the
// output to be wrong by accident.
func TestPhantomSectionStatesItsLimit(t *testing.T) {
	var b bytes.Buffer
	if err := RenderGap(&b, gapResult(), "table"); err != nil {
		t.Fatalf("RenderGap: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		"feature flag",
		"Absence is only",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the phantom caveat is missing %q:\n%s", want, out)
		}
	}
}

func TestStaleProfileIsSurfaced(t *testing.T) {
	res := gapResult()
	res.Profile.Stale = "the profile records service.version=1.4.2 but the analysed " +
		"tree reports 1.0.0; the static and runtime halves are describing different builds"

	var b bytes.Buffer
	if err := RenderGap(&b, res, "table"); err != nil {
		t.Fatalf("RenderGap: %v", err)
	}
	if !strings.Contains(b.String(), "different builds") {
		t.Errorf("the stale warning was not surfaced:\n%s", b.String())
	}
}

// Loader notes must be printed exactly once. They were previously appended to
// both the profile summary and the warning list.
func TestNotesAreNotDuplicated(t *testing.T) {
	var b bytes.Buffer
	if err := RenderGap(&b, gapResult(), "table"); err != nil {
		t.Fatalf("RenderGap: %v", err)
	}
	out := b.String()
	if n := strings.Count(out, "profile covers one minute"); n != 1 {
		t.Errorf("the loader note appears %d times, want 1:\n%s", n, out)
	}
}

// The label column must not run into its value.
func TestEvidenceFieldsAreAligned(t *testing.T) {
	var b bytes.Buffer
	if err := RenderGap(&b, gapResult(), "table"); err != nil {
		t.Fatalf("RenderGap: %v", err)
	}
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.Contains(line, "observations") && strings.Contains(line, "resolved") {
			if !strings.Contains(line, " ") {
				t.Errorf("label and value ran together: %q", line)
			}
		}
	}
}

func TestGapJSONRoundTrips(t *testing.T) {
	var b bytes.Buffer
	if err := RenderGap(&b, gapResult(), "json"); err != nil {
		t.Fatalf("RenderGap: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, b.String())
	}
	for _, want := range []string{"root", "profile", "hot", "debt", "phantom", "entries"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%q missing from JSON output", want)
		}
	}
}

func TestGapUnknownFormatIsRejected(t *testing.T) {
	var b bytes.Buffer
	if err := RenderGap(&b, gapResult(), "csv"); err == nil {
		t.Error("an unknown format was accepted")
	}
}

func TestRenderGapRejectsNil(t *testing.T) {
	var b bytes.Buffer
	if err := RenderGap(&b, nil, "table"); err == nil {
		t.Error("RenderGap(nil) returned no error")
	}
}

// With no evidence at all the report must say so rather than implying the code
// is idle because it is good.
func TestEmptyResultSaysNothingWasObserved(t *testing.T) {
	res := &gap.Result{Root: "/repo", Profile: gap.ProfileSummary{
		Source: gap.SourcePprof, Coverage: 0, Attribution: gap.AttributionNone,
	}}
	var b bytes.Buffer
	if err := RenderGap(&b, res, "table"); err != nil {
		t.Fatalf("RenderGap: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "No file is both complicated and observed running") {
		t.Errorf("the empty hot section is not explained:\n%s", out)
	}
}
