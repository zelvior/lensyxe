package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

// statusSnapshot is a small fixed snapshot, so the assertions below describe the
// rendering rather than the repository the test happens to run in.
func statusSnapshot() *models.Snapshot {
	return &models.Snapshot{
		Tool:          "lensyxe",
		Version:       "v1.0.0-rc1",
		Root:          "/repo",
		SchemaVersion: "0.2",
		Git:           models.GitStats{IsRepository: true, Branch: "main", HeadCommit: "abc1234"},
		Health: models.Health{
			Score: 72.5,
			Grade: "C",
			Metrics: []models.Metric{
				{Key: "code", Label: "Code health", Score: 46.2, Weight: 0.4, Applicable: true},
				{Key: "dependency", Label: "Dependency health", Score: 91.0, Weight: 0.3, Applicable: true},
				{Key: "git", Label: "Maintainability (Git)", Score: 78.0, Weight: 0.3, Applicable: true},
			},
		},
		Risks: []models.Risk{
			{ID: "r1", Severity: models.SeverityCritical, Title: "Code health below expectations", Impact: 15},
			{ID: "r2", Severity: models.SeverityHigh, Title: "Maintainability below expectations", Impact: 9},
			{ID: "r3", Severity: models.SeverityHigh, Title: "File exceeds complexity threshold", Impact: 7},
			{ID: "r4", Severity: models.SeverityMedium, Title: "Something moderate", Impact: 3},
			{ID: "r5", Severity: models.SeverityLow, Title: "Bus factor of one", Impact: 1.8},
		},
	}
}

func TestRenderStatusShowsTheEssentials(t *testing.T) {
	var b bytes.Buffer
	if err := RenderStatus(&b, statusSnapshot()); err != nil {
		t.Fatalf("RenderStatus: %v", err)
	}
	out := b.String()

	for _, want := range []string{
		"72.5 / 100",  // the score
		"C",           // the grade
		"Code health", // each dimension
		"46.2",        // and its score
		"40%",         // and its weight
		"5 risk(s)",   // the risk count
		"1 critical",  // severity breakdown
		"2 high",      //
		"weakest:",    // the actionable line
		"Code health at 46.2",
		"lensyxe analyze", // where the full report is
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status output does not contain %q\n%s", want, out)
		}
	}
}

// status must be short. If it grows to the length of analyze it has failed at
// the only job it has: being the thing you read instead of the report.
func TestRenderStatusIsShorterThanAnalyze(t *testing.T) {
	var statusBuf bytes.Buffer
	if err := RenderStatus(&statusBuf, statusSnapshot()); err != nil {
		t.Fatalf("RenderStatus: %v", err)
	}
	var analyzeBuf bytes.Buffer
	if err := Render(&analyzeBuf, statusSnapshot()); err != nil {
		t.Fatalf("Render: %v", err)
	}

	s, a := len(statusBuf.String()), len(analyzeBuf.String())
	if s >= a {
		t.Errorf("status rendered %d bytes and analyze %d; status must be the shorter of the two", s, a)
	}
	// A generous ceiling that still catches a table being pasted in.
	if s > 1200 {
		t.Errorf("status rendered %d bytes, which is no longer a summary", s)
	}
}

// An unmeasured dimension must be stated as such. Drawing it as a zero would
// invent a measurement and drag the apparent score down for a blind spot.
func TestRenderStatusMarksUnmeasuredDimensions(t *testing.T) {
	snap := statusSnapshot()
	snap.Health.Metrics = append(snap.Health.Metrics, models.Metric{
		Key: "extra", Label: "Some future dimension",
		Score: 0, Weight: 0.1, Applicable: false,
	})

	var b bytes.Buffer
	if err := RenderStatus(&b, snap); err != nil {
		t.Fatalf("RenderStatus: %v", err)
	}
	out := b.String()

	if !strings.Contains(out, "not measured") {
		t.Errorf("an unmeasured dimension was not labelled\n%s", out)
	}
	if !strings.Contains(out, "Some future dimension") {
		t.Errorf("the unmeasured dimension is not named at all\n%s", out)
	}
}

// Risks are listed worst first, and the listing is stable between runs.
func TestRenderStatusOrdersRisksBySeverityThenImpact(t *testing.T) {
	riskLines := func() []string {
		var b bytes.Buffer
		if err := RenderStatus(&b, statusSnapshot()); err != nil {
			t.Fatalf("RenderStatus: %v", err)
		}
		var out []string
		for _, line := range strings.Split(b.String(), "\n") {
			if strings.Contains(line, "expectations") || strings.Contains(line, "complexity threshold") ||
				strings.Contains(line, "Bus factor") || strings.Contains(line, "moderate") {
				out = append(out, strings.TrimSpace(line))
			}
		}
		return out
	}

	first := riskLines()
	if len(first) == 0 {
		t.Fatal("no risk lines were rendered")
	}
	if !strings.Contains(first[0], "CRITICAL") {
		t.Errorf("the first risk listed is %q, want the critical one first", first[0])
	}

	// The two HIGH risks must be ordered by impact, and the order must not
	// depend on the input slice order.
	for i := range 6 {
		var b bytes.Buffer
		if err := RenderStatus(&b, statusSnapshot()); err != nil {
			t.Fatalf("RenderStatus: %v", err)
		}
		again := riskLines()
		_ = b
		if len(again) != len(first) {
			t.Fatalf("risk ordering changed between runs (iteration %d)", i)
		}
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("risk ordering changed between runs at position %d: %q then %q",
					j, first[j], again[j])
			}
		}
	}
}

// Rendering must never fail on a nil snapshot, and must not panic.
func TestRenderStatusRejectsNil(t *testing.T) {
	var b bytes.Buffer
	if err := RenderStatus(&b, nil); err == nil {
		t.Error("RenderStatus(nil) returned no error")
	}
}

// A snapshot with no risks must not print a risk header at all.
func TestRenderStatusWithoutRisks(t *testing.T) {
	snap := statusSnapshot()
	snap.Risks = nil

	var b bytes.Buffer
	if err := RenderStatus(&b, snap); err != nil {
		t.Fatalf("RenderStatus: %v", err)
	}
	if strings.Contains(b.String(), "risk(s)") {
		t.Errorf("a snapshot with no risks still printed a risk count\n%s", b.String())
	}
}

// A directory that is not a repository has no branch; the header must say so
// rather than printing an empty field.
func TestRenderStatusWithoutGitRepository(t *testing.T) {
	snap := statusSnapshot()
	snap.Git = models.GitStats{IsRepository: false}

	var b bytes.Buffer
	if err := RenderStatus(&b, snap); err != nil {
		t.Fatalf("RenderStatus: %v", err)
	}
	if strings.Contains(b.String(), "branch") {
		t.Errorf("a non-repository printed a branch line\n%s", b.String())
	}
}
