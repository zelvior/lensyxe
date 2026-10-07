package compare

import (
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

// snapshotA is the "before" fixture.
func snapshotA() *models.Snapshot {
	return &models.Snapshot{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "test",
		Root:          "/tmp/tree",
		Health: models.Health{
			Score: 87, Grade: "B", Components: 3,
			Summary: "Overall healthy.",
			Metrics: []models.Metric{
				{Key: "code", Label: "Code health", Score: 88, Weight: 0.4, Applicable: true},
				{Key: "dependency", Label: "Dependency health", Score: 90, Weight: 0.3, Applicable: true},
				{Key: "git", Label: "Maintainability (Git)", Score: 83, Weight: 0.3, Applicable: true},
			},
		},
		Code: models.CodeStats{
			Files: 100, SourceFiles: 80, TestFiles: 20, HasTests: true,
			TestFileRatio: 0.2, TestLineRatio: 0.3,
			CodeLines: 8000, TotalLines: 10000, AverageLines: 80,
			Hotspots: []models.Hotspot{},
			Complexity: models.ComplexitySummary{
				Measured: true, Files: 100, AverageComplexity: 4, MaxComplexity: 9,
				MaxComplexityFile: "a.go", MaxNesting: 4,
				WorstFiles: []models.FileComplexity{},
			},
		},
		Dependencies: models.DependencyStats{
			Detected: true, Locked: true, Direct: 10, Dev: 4, Total: 14, Transitive: 60,
			Ecosystems: []models.EcosystemStats{{Name: "npm", Direct: 10, Total: 14, Transitive: 60}},
		},
		Git: models.GitStats{
			IsRepository: false, WindowDays: 90, WindowCommits: 60, Authors: 4, BusFactor: 2,
			CommitsPerWeek: 8, ChurnConcentration: 0.1,
		},
		Risks: []models.Risk{
			{ID: "score.git", Title: "Maintainability (Git) below expectations",
				Severity: models.SeverityMedium, Category: models.CategoryGit, Impact: 5.1},
			{ID: "code.hotspot.old.go", Title: "Confirmed hotspot",
				Severity: models.SeverityHigh, Category: models.CategoryCode, Impact: 8,
				Subject: "old.go"},
			{ID: "dependency.drift.npm", Title: "Missing lockfile",
				Severity: models.SeverityMedium, Category: models.CategoryDependency, Impact: 3,
				Subject: "package.json"},
		},
	}
}

// snapshotB applies a set of changes to snapshotA for the diff tests.
func snapshotB() *models.Snapshot {
	s := snapshotA()
	s.Health.Score = 84
	s.Health.Grade = "B"
	s.Code.CodeLines = 9200
	s.Code.TotalLines = 11500
	s.Code.AverageLines = 92
	s.Code.Files = 102
	s.Code.SourceFiles = 82
	s.Code.TestFiles = 20
	s.Code.TestFileRatio = 0.2
	s.Code.Hotspots = []models.Hotspot{{Path: "new.go", Lines: 700, Confirmed: true}}
	s.Code.Complexity.MaxComplexity = 14
	s.Code.Complexity.AverageComplexity = 4.6
	s.Code.Complexity.MaxNesting = 6
	s.Dependencies.Direct = 13
	s.Dependencies.Total = 17
	s.Dependencies.Transitive = 95
	s.Git.WindowCommits = 52
	s.Git.Authors = 5
	s.Git.BusFactor = 3
	s.Git.ChurnConcentration = 0.22
	s.Risks = []models.Risk{
		// Unchanged: same severity and impact, so it must not be reported.
		{ID: "dependency.drift.npm", Title: "Missing lockfile",
			Severity: models.SeverityMedium, Category: models.CategoryDependency, Impact: 3,
			Subject: "package.json"},
		// Changed: worsened.
		{ID: "score.git", Title: "Maintainability (Git) below expectations",
			Severity: models.SeverityHigh, Category: models.CategoryGit, Impact: 7.2},
		// Added.
		{ID: "code.hotspot.new.go", Title: "Confirmed hotspot",
			Severity: models.SeverityCritical, Category: models.CategoryCode, Impact: 11,
			Subject: "new.go"},
	}
	return s
}

func TestBuildComputesScoreDeltaAndGrade(t *testing.T) {
	res := build(Config{}, "test", "/repo",
		RevisionInfo{Ref: "v1", ShortSHA: "aaa"},
		RevisionInfo{Ref: "v2", ShortSHA: "bbb"},
		snapshotA(), snapshotB())

	if res.ScoreA != 87 || res.ScoreB != 84 {
		t.Errorf("scores = %v -> %v, want 87 -> 84", res.ScoreA, res.ScoreB)
	}
	if res.ScoreDelta != -3 {
		t.Errorf("ScoreDelta = %v, want -3", res.ScoreDelta)
	}
	if res.GradeChange != "B -> B (-3.0)" {
		t.Errorf("GradeChange = %q", res.GradeChange)
	}
	if !strings.Contains(res.Verdict, "regression") {
		t.Errorf("Verdict = %q, want it to mention a regression", res.Verdict)
	}
}

func TestBuildDiffRisks(t *testing.T) {
	res := build(Config{}, "test", "/repo",
		RevisionInfo{}, RevisionInfo{}, snapshotA(), snapshotB())

	// Added: only in B.
	if len(res.RisksAdded) != 1 || res.RisksAdded[0].ID != "code.hotspot.new.go" {
		t.Errorf("RisksAdded = %s", ids(res.RisksAdded))
	}
	// Resolved: only in A.
	if len(res.RisksResolved) != 1 || res.RisksResolved[0].ID != "code.hotspot.old.go" {
		t.Errorf("RisksResolved = %s", ids(res.RisksResolved))
	}
	// Changed: present in both with different severity or impact.
	if len(res.RisksChanged) != 1 || res.RisksChanged[0].ID != "score.git" {
		t.Fatalf("RisksChanged = %+v", res.RisksChanged)
	}
	c := res.RisksChanged[0]
	if c.Severity != models.SeverityMedium || c.After != models.SeverityHigh {
		t.Errorf("severity = %s -> %s", c.Severity, c.After)
	}
	if c.ImpactA != 5.1 || c.ImpactB != 7.2 || c.Delta != 2.1 {
		t.Errorf("impact = %v -> %v (delta %v)", c.ImpactA, c.ImpactB, c.Delta)
	}
	// Higher impact is worse, so Better must be -1.
	if c.Better != -1 {
		t.Errorf("Better = %d, want -1 for a worsening risk", c.Better)
	}
}

// An identical snapshot on both sides must produce no metric rows and no risk
// changes, only a zero delta.
func TestBuildIdenticalSnapshots(t *testing.T) {
	a, b := snapshotA(), snapshotA()
	res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, a, b)

	if res.ScoreDelta != 0 {
		t.Errorf("ScoreDelta = %v, want 0", res.ScoreDelta)
	}
	if len(res.Metrics) != 0 {
		t.Errorf("expected no metric deltas, got %d", len(res.Metrics))
	}
	if len(res.RisksAdded)+len(res.RisksResolved)+len(res.RisksChanged) != 0 {
		t.Error("expected no risk changes")
	}
	if res.Verdict != "No net change in health score." {
		t.Errorf("Verdict = %q", res.Verdict)
	}
}

// Lower-is-better metrics must be marked as regressions when they increase.
func TestMetricDirectionalSignals(t *testing.T) {
	res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, snapshotA(), snapshotB())

	byLabel := map[string]MetricDelta{}
	for _, m := range res.Metrics {
		byLabel[m.Label] = m
	}

	cases := []struct {
		label      string
		wantBetter int
		wantDelta  float64
	}{
		// More direct dependencies is worse.
		{"Direct deps", -1, 3},
		// Higher average complexity is worse.
		{"Avg complexity", -1, 0.6},
		// Higher churn concentration is worse.
		{"Churn concentration", -1, 0.12},
		// More authors means broader knowledge distribution.
		{"Authors", 1, 1},
		// Volume metrics are reported but never judged: labelling a jump in
		// LOC "improved" would be misleading.
		{"Code lines", 0, 1200},
		{"Source files", 0, 2},
	}
	for _, tc := range cases {
		m, ok := byLabel[tc.label]
		if !ok {
			t.Errorf("metric %q missing from the delta table", tc.label)
			continue
		}
		if m.Better != tc.wantBetter {
			t.Errorf("%s: Better = %d, want %d", tc.label, m.Better, tc.wantBetter)
		}
		if m.Delta != tc.wantDelta {
			t.Errorf("%s: Delta = %v, want %v", tc.label, m.Delta, tc.wantDelta)
		}
		if m.Display == "" {
			t.Errorf("%s: Display must be populated", tc.label)
		}
	}
}

// Unchanged metrics must be omitted: a comparison should surface movement, not
// restate the whole profile.
func TestUnchangedMetricsAreOmitted(t *testing.T) {
	res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, snapshotA(), snapshotB())
	for _, m := range res.Metrics {
		if m.Label == "Test files" { // 20 in both snapshots
			t.Error("unchanged metric 'Test files' should not appear in the delta table")
		}
		if m.Label == "Test file ratio" {
			t.Error("unchanged metric 'Test file ratio' should not appear")
		}
	}
}

// Ordering must be deterministic across repeated builds.
func TestBuildIsDeterministic(t *testing.T) {
	a, b := snapshotA(), snapshotB()
	first := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, a, b)
	for i := 0; i < 25; i++ {
		got := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, a, b)
		if len(got.Metrics) != len(first.Metrics) {
			t.Fatalf("run %d metric count differs", i)
		}
		for j := range got.Metrics {
			if got.Metrics[j].Label != first.Metrics[j].Label {
				t.Fatalf("run %d position %d: %q vs %q", i, j, got.Metrics[j].Label, first.Metrics[j].Label)
			}
		}
		if len(got.RisksAdded) != len(first.RisksAdded) {
			t.Fatalf("run %d added-risk count differs", i)
		}
	}
}

func TestMetricDeltasSortedByMagnitude(t *testing.T) {
	res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, snapshotA(), snapshotB())
	for i := 1; i < len(res.Metrics); i++ {
		prev := abs(res.Metrics[i-1].Delta)
		cur := abs(res.Metrics[i].Delta)
		if prev < cur {
			t.Fatalf("metrics not sorted by magnitude: %v then %v", prev, cur)
		}
	}
}

func TestCountConfirmed(t *testing.T) {
	s := snapshotA()
	if got := countConfirmed(s); got != 0 {
		t.Errorf("countConfirmed = %d, want 0", got)
	}
	s.Code.Hotspots = []models.Hotspot{
		{Path: "a", Confirmed: true},
		{Path: "b", Confirmed: false},
		{Path: "c", Confirmed: true},
	}
	if got := countConfirmed(s); got != 2 {
		t.Errorf("countConfirmed = %d, want 2", got)
	}
}

// Non-nil empty slices keep JSON output stable ("[]" instead of null).
func TestDiffProducesNonNilSlices(t *testing.T) {
	a := snapshotA()
	res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, a, a)
	if res.RisksAdded == nil || res.RisksResolved == nil || res.RisksChanged == nil {
		t.Error("risk diff slices must be non-nil")
	}
	if res.Metrics == nil {
		t.Error("Metrics must be non-nil")
	}
}
