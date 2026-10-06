package risk

import (
	"strings"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

// cleanSnapshot is a well-behaved repository: small files, tests present,
// locked dependencies, healthy shared history.
func cleanSnapshot() (models.CodeStats, models.GitStats, models.DependencyStats, models.Health) {
	code := models.CodeStats{
		Files: 100, SourceFiles: 80, TestFiles: 20, HasTests: true,
		TestFileRatio: 0.2, CodeLines: 8000, AverageLines: 80,
		Languages: []models.LanguageStat{{Name: "Go", Files: 100, Lines: 8000, TestFiles: 20}},
		Complexity: models.ComplexitySummary{
			Measured: true, Files: 100, Functions: 800,
			AverageComplexity: 4, MaxComplexity: 9, MaxComplexityFile: "a.go",
			WorstFiles: []models.FileComplexity{},
		},
	}
	git := models.GitStats{
		IsRepository: true, TotalCommits: 400, WindowDays: 90, WindowCommits: 60,
		CommitsPerWeek: 8, Authors: 6, BusFactor: 4, TopAuthorShare: 0.2,
		DaysSinceCommit: 2, ChurnFiles: 90, ChurnConcentration: 0.1,
		Churn: []models.ChurnEntry{{Path: "a.go", Score: 20, Commits: 3}},
	}
	deps := models.DependencyStats{
		Detected: true, Locked: true, Direct: 6, Dev: 4, Total: 10, Transitive: 30,
		Ecosystems: []models.EcosystemStats{
			{Name: "npm", Manifest: "package.json", Lockfile: "package-lock.json",
				Direct: 6, Dev: 4, Total: 10, Transitive: 30},
		},
	}
	// Metric scores sit above 90 so the dimension-deficit rule (which fires at a
	// deficit of 10 or more) stays silent. This snapshot represents a repository
	// with nothing worth flagging.
	health := models.Health{
		Score: 94, Grade: "A", Components: 3,
		Metrics: []models.Metric{
			{Key: "code", Label: "Code health", Score: 94, Weight: 0.4, Applicable: true},
			{Key: "dependency", Label: "Dependency health", Score: 97, Weight: 0.3, Applicable: true},
			{Key: "git", Label: "Maintainability (Git)", Score: 92, Weight: 0.3, Applicable: true},
		},
	}
	return code, git, deps, health
}

func TestAssessCleanRepositoryHasNoRisks(t *testing.T) {
	code, git, deps, health := cleanSnapshot()
	got := Assess(code, git, deps, health)
	if len(got) != 0 {
		t.Fatalf("expected no risks for a clean repo, got %d:\n%+v", len(got), summarize(got))
	}
}

// Every risk must carry evidence. A risk without numbers is an assertion, not
// a measurement, and the whole point of this package is evidence-first output.
func TestEveryRiskHasEvidence(t *testing.T) {
	code, git, deps, health := cleanSnapshot()

	code.Hotspots = []models.Hotspot{{
		Path: "internal/engine.go", Lines: 900, Churn: 220,
		Complexity: 40, Level: models.ComplexityVeryHigh,
		Confirmed: true, Classification: "size,churn,complexity,confirmed",
	}}
	code.Complexity.VeryHighFunctions = 6
	code.Complexity.WorstFiles = []models.FileComplexity{{
		Path: "internal/engine.go", EstimatedComplexity: 40, Level: models.ComplexityVeryHigh,
		BranchPoints: 300, MaxNesting: 9, Functions: 8, Lines: 900,
	}}
	code.TestFiles = 0
	code.HasTests = false
	code.TestFileRatio = 0
	deps.Locked = false
	deps.Drift = true
	deps.DriftReason = []string{"package.json declares 6 direct dependencies with no lockfile"}
	deps.Ecosystems[0].Lockfile = ""
	deps.Ecosystems[0].Drift = true
	deps.Ecosystems[0].DriftReason = "package.json declares 6 direct dependencies with no lockfile"
	git.BusFactor = 1
	git.TopAuthorShare = 1
	git.ChurnConcentration = 0.8
	git.DaysSinceCommit = 400
	health.Metrics[0].Score = 40 // force a dimension deficit

	risks := Assess(code, git, deps, health)
	if len(risks) < 6 {
		t.Fatalf("expected a risk from every rule, got %d:\n%s", len(risks), summarize(risks))
	}
	for _, r := range risks {
		if len(r.Evidence) == 0 {
			t.Errorf("risk %q (%s) has no evidence", r.ID, r.Title)
		}
		if r.ID == "" {
			t.Errorf("risk %q has an empty ID", r.Title)
		}
		if r.Title == "" || r.Detail == "" {
			t.Errorf("risk %q is missing a title or detail", r.ID)
		}
	}
}

// Risk IDs must be unique: `lensyxe compare` matches on them, so duplicates
// would make a diff silently drop a risk.
func TestRiskIDsAreUnique(t *testing.T) {
	code, git, deps, health := cleanSnapshot()
	code.Hotspots = []models.Hotspot{
		{Path: "a.go", Lines: 900, Churn: 200, Complexity: 40,
			Level: models.ComplexityVeryHigh, Confirmed: true},
		{Path: "b.go", Lines: 800, Churn: 100, Complexity: 30,
			Level: models.ComplexityVeryHigh, Confirmed: true},
	}
	seen := map[string]bool{}
	for _, r := range Assess(code, git, deps, health) {
		if seen[r.ID] {
			t.Errorf("duplicate risk ID %q", r.ID)
		}
		seen[r.ID] = true
	}
}

func TestClassifyBands(t *testing.T) {
	cases := []struct {
		impact float64
		want   models.Severity
	}{
		{0.5, models.SeverityLow},
		{MediumImpact, models.SeverityMedium},
		{MediumImpact + 0.01, models.SeverityMedium},
		{HighImpact, models.SeverityHigh},
		{CriticalImpact, models.SeverityCritical},
		{50, models.SeverityCritical},
		{0, models.SeverityLow},
	}
	for _, tc := range cases {
		if got := classify(tc.impact); got != tc.want {
			t.Errorf("classify(%v) = %v, want %v", tc.impact, got, tc.want)
		}
	}
}

// Sorting must be total so repeated runs produce identical output.
func TestAssessIsDeterministic(t *testing.T) {
	code, git, deps, health := degradedSnapshot()

	first := Assess(code, git, deps, health)
	if len(first) == 0 {
		t.Fatal("precondition: expected risks")
	}
	for i := 0; i < 25; i++ {
		got := Assess(code, git, deps, health)
		if len(got) != len(first) {
			t.Fatalf("run %d produced %d risks, want %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j].ID != first[j].ID {
				t.Fatalf("run %d position %d: ID %q, want %q", i, j, got[j].ID, first[j].ID)
			}
		}
	}
}

func TestRiskOrderIsSeverityThenImpact(t *testing.T) {
	code, git, deps, health := degradedSnapshot()
	risks := Assess(code, git, deps, health)
	for i := 1; i < len(risks); i++ {
		prev, cur := risks[i-1], risks[i]
		switch {
		case prev.Severity.Rank() < cur.Severity.Rank():
			t.Fatalf("severity out of order at %d: %v then %v", i, prev.Severity, cur.Severity)
		case prev.Severity == cur.Severity && prev.Impact < cur.Impact:
			t.Fatalf("impact out of order at %d within %v: %v then %v",
				i, prev.Severity, prev.Impact, cur.Impact)
		}
	}
}

// Only fully confirmed hotspots may produce a high-severity code risk. A large
// file that never changes is a note, not a risk.
func TestUnconfirmedHotspotsProduceNoRisk(t *testing.T) {
	code, git, deps, health := cleanSnapshot()
	code.Hotspots = []models.Hotspot{{
		Path: "docs.go", Lines: 2000, Churn: 0, Complexity: 2,
		Level: models.ComplexityLow, Confirmed: false,
		Classification: "size",
	}}

	for _, r := range Assess(code, git, deps, health) {
		if strings.HasPrefix(r.ID, "code.hotspot.") {
			t.Fatalf("unconfirmed hotspot produced a risk: %+v", r)
		}
	}
}

func TestConfirmedHotspotProducesRiskWithFileSubject(t *testing.T) {
	code, git, deps, health := cleanSnapshot()
	code.Hotspots = []models.Hotspot{{
		Path: "internal/engine.go", Lines: 900, Churn: 220,
		Complexity: 40, Level: models.ComplexityVeryHigh, Confirmed: true,
		Classification: "size,churn,complexity,confirmed",
		Rationale:      "900 code lines; over every threshold.",
	}}

	risks := Assess(code, git, deps, health)
	found := false
	for _, r := range risks {
		if r.ID != "code.hotspot.internal/engine.go" {
			continue
		}
		found = true
		if r.Subject != "internal/engine.go" {
			t.Errorf("Subject = %q", r.Subject)
		}
		if !strings.Contains(r.Detail, "900") {
			t.Errorf("detail should cite the LOC count, got %q", r.Detail)
		}
		if r.Recommendation == "" {
			t.Error("expected a recommendation")
		}
	}
	if !found {
		t.Fatalf("no risk for the confirmed hotspot: %s", summarize(risks))
	}
}

func TestTestingRisks(t *testing.T) {
	cases := []struct {
		name       string
		testFiles  int
		sourceFile int
		hasTests   bool
		wantID     string
	}{
		{"no tests at all", 0, 50, false, "testing.none"},
		{"ratio below threshold", 3, 50, true, "testing.ratio"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, git, deps, health := cleanSnapshot()
			code.TestFiles = tc.testFiles
			code.SourceFiles = tc.sourceFile
			code.HasTests = tc.hasTests
			code.TestFileRatio = float64(tc.testFiles) / float64(tc.testFiles+tc.sourceFile)

			found := false
			for _, r := range Assess(code, git, deps, health) {
				if r.ID == tc.wantID {
					found = true
					if r.Category != models.CategoryTesting {
						t.Errorf("category = %q, want testing", r.Category)
					}
				}
			}
			if !found {
				t.Errorf("expected risk %q", tc.wantID)
			}
		})
	}
}

// A repository with no production code has no meaningful test ratio, so the
// testing rules must stay silent rather than reporting 0%.
func TestTestingRiskSkipsWhenNoSourceFiles(t *testing.T) {
	code, git, deps, health := cleanSnapshot()
	code.SourceFiles = 0
	code.Files = 0
	code.HasTests = false
	code.TestFileRatio = 0

	for _, r := range Assess(code, git, deps, health) {
		if strings.HasPrefix(r.ID, "testing.") {
			t.Fatalf("testing risk on a repo with no source files: %+v", r)
		}
	}
}

func TestDependencyDriftRisk(t *testing.T) {
	code, git, deps, health := cleanSnapshot()
	deps.Locked = false
	deps.Drift = true
	deps.DriftReason = []string{"package.json declares 6 direct dependencies with no lockfile"}
	deps.Ecosystems[0].Lockfile = ""
	deps.Ecosystems[0].Drift = true
	deps.Ecosystems[0].DriftReason = deps.DriftReason[0]

	found := false
	for _, r := range Assess(code, git, deps, health) {
		if r.ID == "dependency.drift.npm" {
			found = true
			if !strings.Contains(r.Detail, "lockfile") {
				t.Errorf("detail should mention the lockfile, got %q", r.Detail)
			}
		}
	}
	if !found {
		t.Fatal("expected a dependency.drift.npm risk")
	}
}

// A large transitive multiplier is a maintenance signal: each direct dependency
// extends the update surface.
func TestDeepTransitiveTreeRisk(t *testing.T) {
	code, git, deps, health := cleanSnapshot()
	deps.Ecosystems[0].Transitive = 400
	deps.Ecosystems[0].Direct = 10

	found := false
	for _, r := range Assess(code, git, deps, health) {
		if r.ID == "dependency.transitive.npm" {
			found = true
			if !strings.Contains(r.Detail, "40.0x") {
				t.Errorf("detail should cite the multiplier, got %q", r.Detail)
			}
		}
	}
	if !found {
		t.Fatal("expected a deep transitive tree risk")
	}
}

func TestGitRisks(t *testing.T) {
	t.Run("bus factor of one", func(t *testing.T) {
		_, git, deps, health := cleanSnapshot()
		git.BusFactor = 1
		git.TopAuthorShare = 0.95
		found := false
		for _, r := range Assess(cleanCode(), git, deps, health) {
			if r.ID == "git.bus_factor" {
				found = true
			}
		}
		if !found {
			t.Error("expected a git.bus_factor risk")
		}
	})

	t.Run("churn concentration", func(t *testing.T) {
		_, git, deps, health := cleanSnapshot()
		git.ChurnConcentration = 0.9
		found := false
		for _, r := range Assess(cleanCode(), git, deps, health) {
			if r.ID == "git.churn_concentration" {
				found = true
			}
		}
		if !found {
			t.Error("expected a git.churn_concentration risk")
		}
	})

	t.Run("stale repository", func(t *testing.T) {
		_, git, deps, health := cleanSnapshot()
		git.DaysSinceCommit = 500
		found := false
		for _, r := range Assess(cleanCode(), git, deps, health) {
			if r.ID == "git.stale" {
				found = true
			}
		}
		if !found {
			t.Error("expected a git.stale risk")
		}
	})

	t.Run("healthy history is silent", func(t *testing.T) {
		_, git, deps, health := cleanSnapshot()
		for _, r := range Assess(cleanCode(), git, deps, health) {
			if strings.HasPrefix(r.ID, "git.") {
				t.Errorf("unexpected git risk on healthy history: %+v", r)
			}
		}
	})

	t.Run("non-git directory is silent", func(t *testing.T) {
		_, _, deps, health := cleanSnapshot()
		for _, r := range Assess(cleanCode(), models.GitStats{}, deps, health) {
			if strings.HasPrefix(r.ID, "git.") {
				t.Errorf("git risk without a repository: %+v", r)
			}
		}
	})
}

// Dimension risks explain the score, so they must appear whenever a metric is
// materially below full marks.
func TestDimensionRiskForScoreDeficit(t *testing.T) {
	code, git, deps, health := cleanSnapshot()
	health.Metrics[0].Score = 45
	health.Metrics[0].Weight = 0.4
	health.Metrics[0].Detail = "size 45, hotspots 20% of lines, cohesion 60"

	found := false
	for _, r := range Assess(code, git, deps, health) {
		if r.ID != "score.code" {
			continue
		}
		found = true
		// (100-45) * 0.4 = 22, above the 15-point cap, so it clamps.
		if r.Impact != 15 {
			t.Errorf("impact = %v, want 15 (clamped from 22)", r.Impact)
		}
	}
	if !found {
		t.Fatal("expected a score.code risk")
	}
}

func TestNonApplicableMetricsProduceNoRisk(t *testing.T) {
	code, git, deps, health := cleanSnapshot()
	health.Metrics = []models.Metric{
		{Key: "code", Label: "Code health", Score: 0, Weight: 0.4, Applicable: false},
	}
	for _, r := range Assess(code, git, deps, health) {
		if r.ID == "score.code" {
			t.Fatalf("risk for a non-applicable metric: %+v", r)
		}
	}
}

// Impact must stay bounded so no single risk can claim the whole score.
func TestImpactIsCapped(t *testing.T) {
	code, git, deps, health := degradedSnapshot()
	// Drive one hotspot far past any threshold.
	code.Hotspots = []models.Hotspot{{
		Path: "monster.go", Lines: 500000, Churn: 90000, Complexity: 9999,
		Level: models.ComplexityVeryHigh, Confirmed: true,
	}}
	for _, r := range Assess(code, git, deps, health) {
		if r.Impact > 15 {
			t.Errorf("risk %q impact = %v, exceeds the cap", r.ID, r.Impact)
		}
		if r.Impact < 0 {
			t.Errorf("risk %q has negative impact", r.ID)
		}
	}
}

func TestCapImpact(t *testing.T) {
	cases := map[float64]float64{
		-5: 0, 0: 0, 1.234: 1.23, 15: 15, 100: 15, 14.999: 15,
	}
	for in, want := range cases {
		if got := capImpact(in); got != want {
			t.Errorf("capImpact(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestFormatComplexity(t *testing.T) {
	cases := map[float64]string{1: "1", 4: "4", 4.5: "4.5", 12.25: "12.2"}
	for in, want := range cases {
		if got := formatComplexity(in); got != want {
			t.Errorf("formatComplexity(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestStaleRiskUsesCommitDate(t *testing.T) {
	code, git, deps, health := cleanSnapshot()
	git.DaysSinceCommit = 400
	git.LastCommitAt = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, r := range Assess(code, git, deps, health) {
		if r.ID != "git.stale" {
			continue
		}
		found := false
		for _, e := range r.Evidence {
			if strings.Contains(e.Detail, "2025-01-01") {
				found = true
			}
		}
		if !found {
			t.Error("expected the last commit date in the evidence")
		}
	}
}

// cleanCode returns the code stats from the clean snapshot.
func cleanCode() models.CodeStats {
	code, _, _, _ := cleanSnapshot()
	return code
}

// degradedSnapshot returns inputs that trip every rule at once.
func degradedSnapshot() (models.CodeStats, models.GitStats, models.DependencyStats, models.Health) {
	code, git, deps, health := cleanSnapshot()
	code.TestFiles = 0
	code.HasTests = false
	code.TestFileRatio = 0
	code.Complexity.VeryHighFunctions = 8
	code.Hotspots = []models.Hotspot{{
		Path: "internal/engine.go", Lines: 900, Churn: 200,
		Complexity: 45, Level: models.ComplexityVeryHigh, Confirmed: true,
	}}
	code.Complexity.WorstFiles = []models.FileComplexity{{
		Path: "internal/engine.go", EstimatedComplexity: 45,
		Level: models.ComplexityVeryHigh, BranchPoints: 300,
		MaxNesting: 9, Functions: 7, Lines: 900,
	}}
	code.Complexity.MaxComplexity = 45
	code.Complexity.MaxComplexityFile = "internal/engine.go"
	code.Complexity.Files = 100
	deps.Locked = false
	deps.Ecosystems[0].Lockfile = ""
	deps.Ecosystems[0].Drift = true
	deps.Ecosystems[0].DriftReason = "missing lockfile"
	git.BusFactor = 1
	git.TopAuthorShare = 1
	git.ChurnConcentration = 0.85
	git.DaysSinceCommit = 400
	health.Metrics[0].Score = 35
	return code, git, deps, health
}

// summarize renders risks for test failure messages.
func summarize(risks []models.Risk) string {
	var b strings.Builder
	for _, r := range risks {
		b.WriteString("\n  " + r.ID + " [" + string(r.Severity) + "] impact=" +
			formatComplexity(r.Impact) + " " + r.Title)
	}
	return b.String()
}
