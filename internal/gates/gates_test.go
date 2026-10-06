package gates

import (
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/internal/config"
	"github.com/zelvior/lensyxe/pkg/models"
)

// th builds a threshold set with only the named rules enabled.
//
// It starts from config.NewThresholds so unset numeric rules are genuinely
// unset, then applies one rule. Using a helper keeps every case from having to
// remember the Unset sentinel.
func th(mutate func(*config.Thresholds)) config.Thresholds {
	out := config.NewThresholds()
	mutate(&out)
	return out
}

// snap builds a snapshot with the fields the gates read.
func snap(score float64, complexity float64) *models.Snapshot {
	return &models.Snapshot{
		Health: models.Health{Score: score, Grade: models.Grade(score)},
		Code: models.CodeStats{
			SourceFiles: 20, HasTests: true,
			Complexity: models.ComplexitySummary{
				Measured: true, AverageComplexity: complexity, MaxComplexity: complexity,
			},
			Hotspots: []models.Hotspot{},
		},
		Dependencies: models.DependencyStats{},
		Risks:        []models.Risk{},
	}
}

func TestNoThresholdsMeansNoBreaches(t *testing.T) {
	none := config.NewThresholds()
	if got := Evaluate(snap(10, 99), none, Baseline{}); len(got) != 0 {
		t.Errorf("breaches = %+v, want none", got)
	}
}

func TestAny(t *testing.T) {
	if Any(config.NewThresholds()) {
		t.Error("an unset threshold set must report false")
	}
	configured := []config.Thresholds{
		th(func(c *config.Thresholds) { c.MinHealthScore = 50 }),
		th(func(c *config.Thresholds) { c.MaxHealthDrop = 1 }),
		th(func(c *config.Thresholds) { c.MaxComplexityIncrease = 1 }),
		th(func(c *config.Thresholds) { c.MaxRiskCount = 1 }),
		th(func(c *config.Thresholds) { c.MaxHotspots = 1 }),
		th(func(c *config.Thresholds) { c.FailOnDrift = true }),
		th(func(c *config.Thresholds) { c.RequireTests = true }),
	}
	for _, t2 := range configured {
		if !Any(t2) {
			t.Errorf("Any(%+v) = false, want true", t2)
		}
	}
}

// A maximum of zero means "allow none". It must be honored, not read as unset.
func TestZeroMaximumIsConfigured(t *testing.T) {
	zeroRisks := th(func(c *config.Thresholds) { c.MaxRiskCount = 0 })
	s := snap(90, 5)
	if got := Evaluate(s, zeroRisks, Baseline{}); len(got) != 0 {
		t.Errorf("a clean repo passes a zero-risk gate, got %+v", got)
	}
	s.Risks = []models.Risk{{ID: "one"}}
	got := Evaluate(s, zeroRisks, Baseline{})
	if len(got) != 1 || got[0].Rule != "max_risk_count" {
		t.Fatalf("one risk must breach max_risk_count: 0, got %+v", got)
	}

	zeroHotspots := th(func(c *config.Thresholds) { c.MaxHotspots = 0 })
	s2 := snap(90, 5)
	s2.Code.Hotspots = []models.Hotspot{{Path: "a.go", Confirmed: true}}
	if got := Evaluate(s2, zeroHotspots, Baseline{}); len(got) != 1 {
		t.Errorf("one confirmed hotspot must breach max_hotspots: 0, got %+v", got)
	}
}

func TestMinHealthScore(t *testing.T) {
	limit := th(func(c *config.Thresholds) { c.MinHealthScore = 70 })
	if got := Evaluate(snap(75, 5), limit, Baseline{}); len(got) != 0 {
		t.Errorf("75 above 70 should pass, got %+v", got)
	}
	got := Evaluate(snap(65, 5), limit, Baseline{})
	if len(got) != 1 || got[0].Rule != "min_health_score" {
		t.Fatalf("breaches = %+v", got)
	}
	if !strings.Contains(got[0].Message, "65.0") || !strings.Contains(got[0].Message, "70.0") {
		t.Errorf("message must cite both numbers, got %q", got[0].Message)
	}
	// Exactly at the threshold passes.
	if got := Evaluate(snap(70, 5), limit, Baseline{}); len(got) != 0 {
		t.Errorf("the threshold is inclusive, got %+v", got)
	}
}

// A first run has no baseline, so a drop rule cannot fire.
func TestMaxHealthDropRequiresBaseline(t *testing.T) {
	limit := th(func(c *config.Thresholds) { c.MaxHealthDrop = 5 })
	if got := Evaluate(snap(10, 5), limit, Baseline{HasPrevious: false}); len(got) != 0 {
		t.Errorf("a first run must not fail a drop rule, got %+v", got)
	}
	base := Baseline{HasPrevious: true, Score: 90}
	if got := Evaluate(snap(88, 5), limit, base); len(got) != 0 {
		t.Errorf("a 2-point drop is within 5, got %+v", got)
	}
	got := Evaluate(snap(80, 5), limit, base)
	if len(got) != 1 || got[0].Rule != "max_health_drop" {
		t.Fatalf("breaches = %+v", got)
	}
	if got[0].Actual != 10 {
		t.Errorf("Actual = %v, want 10", got[0].Actual)
	}
}

func TestMaxComplexityIncrease(t *testing.T) {
	limit := th(func(c *config.Thresholds) { c.MaxComplexityIncrease = 1.0 })
	base := Baseline{HasPrevious: true, AverageComplexity: 4}

	if got := Evaluate(snap(80, 4.5), limit, base); len(got) != 0 {
		t.Errorf("a 0.5 rise is within 1.0, got %+v", got)
	}
	got := Evaluate(snap(80, 6), limit, base)
	if len(got) != 1 || got[0].Rule != "max_complexity_increase" {
		t.Fatalf("breaches = %+v", got)
	}
	if !strings.Contains(got[0].Message, "4.00 -> 6.00") {
		t.Errorf("message should show both values, got %q", got[0].Message)
	}
}

// Without a baseline the rule measures the absolute value, which still catches
// a codebase that is complex to begin with.
func TestMaxComplexityIncreaseWithoutBaseline(t *testing.T) {
	limit := th(func(c *config.Thresholds) { c.MaxComplexityIncrease = 1.0 })
	if got := Evaluate(snap(80, 8), limit, Baseline{}); len(got) != 1 {
		t.Errorf("an absolute complexity of 8 should breach, got %+v", got)
	}
	if got := Evaluate(snap(80, 0.5), limit, Baseline{}); len(got) != 0 {
		t.Errorf("low complexity should pass, got %+v", got)
	}
}

func TestMaxRiskCount(t *testing.T) {
	limit := th(func(c *config.Thresholds) { c.MaxRiskCount = 2 })
	s := snap(80, 5)
	s.Risks = []models.Risk{{ID: "a"}, {ID: "b"}}
	if got := Evaluate(s, limit, Baseline{}); len(got) != 0 {
		t.Errorf("exactly at the limit should pass, got %+v", got)
	}
	s.Risks = append(s.Risks, models.Risk{ID: "c"})
	if got := Evaluate(s, limit, Baseline{}); len(got) != 1 {
		t.Errorf("3 risks should breach a limit of 2, got %+v", got)
	}
}

// Only confirmed hotspots count: a size-only candidate is not yet a problem.
func TestMaxHotspotsCountsOnlyConfirmed(t *testing.T) {
	limit := th(func(c *config.Thresholds) { c.MaxHotspots = 1 })
	s := snap(80, 5)
	s.Code.Hotspots = []models.Hotspot{
		{Path: "a.go", Lines: 900, Confirmed: false},
		{Path: "b.go", Lines: 900, Confirmed: false},
		{Path: "c.go", Lines: 900, Confirmed: false},
	}
	if got := Evaluate(s, limit, Baseline{}); len(got) != 0 {
		t.Errorf("unconfirmed candidates must not count, got %+v", got)
	}
	s.Code.Hotspots[0].Confirmed = true
	s.Code.Hotspots[1].Confirmed = true
	got := Evaluate(s, limit, Baseline{})
	if len(got) != 1 || got[0].Actual != 2 {
		t.Errorf("breaches = %+v", got)
	}
}

func TestFailOnDrift(t *testing.T) {
	limit := th(func(c *config.Thresholds) { c.FailOnDrift = true })
	s := snap(80, 5)
	if got := Evaluate(s, limit, Baseline{}); len(got) != 0 {
		t.Errorf("no drift should pass, got %+v", got)
	}
	s.Dependencies.Drift = true
	s.Dependencies.Ecosystems = []models.EcosystemStats{
		{Name: "npm", Drift: true},
		{Name: "gomod", Drift: true},
	}
	got := Evaluate(s, limit, Baseline{})
	if len(got) != 1 {
		t.Fatalf("breaches = %+v", got)
	}
	if !strings.Contains(got[0].Message, "npm and gomod") {
		t.Errorf("message should name both ecosystems, got %q", got[0].Message)
	}
}

// A repository with no production code has no test-coverage opinion.
func TestRequireTestsSkipsWhenNoSourceFiles(t *testing.T) {
	limit := th(func(c *config.Thresholds) { c.RequireTests = true })
	s := snap(80, 5)
	s.Code.SourceFiles = 0
	s.Code.HasTests = false
	if got := Evaluate(s, limit, Baseline{}); len(got) != 0 {
		t.Errorf("no source files means no test requirement, got %+v", got)
	}
}

func TestRequireTests(t *testing.T) {
	limit := th(func(c *config.Thresholds) { c.RequireTests = true })
	s := snap(80, 5)
	s.Code.HasTests = false
	got := Evaluate(s, limit, Baseline{})
	if len(got) != 1 || got[0].Rule != "require_tests" {
		t.Fatalf("breaches = %+v", got)
	}
	if !strings.Contains(got[0].Message, "20") {
		t.Errorf("message should cite the source file count, got %q", got[0].Message)
	}
}

// Multiple breaches must all be reported, not just the first, so a single run
// tells the whole story.
func TestMultipleBreachesAreAllReported(t *testing.T) {
	limit := th(func(c *config.Thresholds) {
		c.MinHealthScore = 90
		c.MaxHealthDrop = 10
		c.MaxRiskCount = 1
		c.RequireTests = true
		c.FailOnDrift = true
	})
	s := snap(50, 5)
	s.Code.HasTests = false
	s.Dependencies.Drift = true
	s.Risks = []models.Risk{{ID: "a"}, {ID: "b"}, {ID: "c"}}

	got := Evaluate(s, limit, Baseline{HasPrevious: true, Score: 95, AverageComplexity: 4})
	rules := map[string]bool{}
	for _, b := range got {
		rules[b.Rule] = true
	}
	for _, want := range []string{
		"min_health_score", "max_health_drop",
		"max_risk_count", "fail_on_drift", "require_tests",
	} {
		if !rules[want] {
			t.Errorf("missing breach %q in %+v", want, got)
		}
	}
}

// Ordering must be stable so output does not shuffle between runs.
func TestBreachesAreSortedByRule(t *testing.T) {
	limit := th(func(c *config.Thresholds) {
		c.MaxRiskCount = 1
		c.MinHealthScore = 90
		c.RequireTests = true
	})
	s := snap(50, 5)
	s.Code.HasTests = false
	s.Risks = []models.Risk{{ID: "a"}, {ID: "b"}}

	got := Evaluate(s, limit, Baseline{})
	for i := 1; i < len(got); i++ {
		if got[i].Rule < got[i-1].Rule {
			t.Fatalf("breaches not sorted by rule: %+v", got)
		}
	}
}

func TestEvaluateNilSnapshot(t *testing.T) {
	limit := th(func(c *config.Thresholds) { c.MinHealthScore = 90 })
	if got := Evaluate(nil, limit, Baseline{}); len(got) != 0 {
		t.Errorf("breaches = %+v, want none", got)
	}
}

// A rule at exactly the limit passes: thresholds are inclusive upper bounds.
func TestLimitsAreInclusive(t *testing.T) {
	limit := th(func(c *config.Thresholds) {
		c.MaxHealthDrop = 10
		c.MaxComplexityIncrease = 2
		c.MaxRiskCount = 3
		c.MaxHotspots = 2
	})
	s := snap(80, 6)
	s.Risks = []models.Risk{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	s.Code.Hotspots = []models.Hotspot{
		{Path: "a.go", Confirmed: true}, {Path: "b.go", Confirmed: true},
	}
	base := Baseline{HasPrevious: true, Score: 90, AverageComplexity: 4}
	// 10-point drop, +2 complexity, 3 risks, 2 confirmed hotspots: all at limit.
	if got := Evaluate(s, limit, base); len(got) != 0 {
		t.Errorf("values exactly at the limit should pass, got %+v", got)
	}
}
