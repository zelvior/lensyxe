package metrics

import (
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

// healthyInputs returns a well-behaved repository: small files, few
// dependencies, active and shared git history.
func healthyInputs() (models.CodeStats, models.GitStats, models.DependencyStats) {
	return models.CodeStats{
			Files:        200,
			CodeLines:    20000,
			AverageLines: 100,
			Languages:    []models.LanguageStat{{Name: "Go", Files: 200, Lines: 20000}},
		},
		models.GitStats{
			IsRepository:       true,
			TotalCommits:       500,
			WindowCommits:      60,
			CommitsPerWeek:     8,
			Authors:            6,
			BusFactor:          4,
			DaysSinceCommit:    2,
			ChurnFiles:         120,
			ChurnConcentration: 0.05,
		},
		models.DependencyStats{
			Detected: true,
			Locked:   true,
			Direct:   5,
			Dev:      3,
			Total:    8,
			Ecosystems: []models.EcosystemStats{
				{Name: "gomod", Lockfile: "go.sum"},
			},
		}
}

func TestComputeIsDeterministic(t *testing.T) {
	c, g, d := healthyInputs()
	first := Compute(c, g, d)
	for i := 0; i < 50; i++ {
		got := Compute(c, g, d)
		if got.Health.Score != first.Health.Score {
			t.Fatalf("run %d: score %v != %v", i, got.Health.Score, first.Health.Score)
		}
		if got.Health.Grade != first.Health.Grade {
			t.Fatalf("run %d: grade %q != %q", i, got.Health.Grade, first.Health.Grade)
		}
	}
}

func TestScoreStaysInRange(t *testing.T) {
	hCode, hGit, hDeps := healthyInputs()
	cases := []struct {
		name string
		code models.CodeStats
		git  models.GitStats
		deps models.DependencyStats
	}{
		{"empty", models.CodeStats{}, models.GitStats{}, models.DependencyStats{}},
		{"healthy", hCode, hGit, hDeps},
		{
			name: "worst case",
			code: models.CodeStats{Files: 1, CodeLines: 50000, AverageLines: 50000,
				Languages: []models.LanguageStat{{Name: "Go"}, {Name: "Rust"}, {Name: "Python"}}},
			git: models.GitStats{IsRepository: true, TotalCommits: 1, CommitsPerWeek: 0,
				Authors: 1, BusFactor: 1, DaysSinceCommit: 3650, ChurnFiles: 1, ChurnConcentration: 1},
			deps: models.DependencyStats{Detected: true, Direct: 500, Total: 500},
		},
		{"no files but git", models.CodeStats{},
			models.GitStats{IsRepository: true, TotalCommits: 10, CommitsPerWeek: 5, DaysSinceCommit: 1},
			models.DependencyStats{Detected: true, Locked: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(tc.code, tc.git, tc.deps)
			if got.Health.Score < 0 || got.Health.Score > 100 {
				t.Fatalf("score %v out of range", got.Health.Score)
			}
			for _, m := range got.Health.Metrics {
				if m.Applicable && (m.Score < 0 || m.Score > 100) {
					t.Fatalf("metric %q score %v out of range", m.Key, m.Score)
				}
			}
		})
	}
}

// Non-applicable metrics must be excluded from the average rather than
// dragging it toward zero.
func TestNonApplicableMetricsAreExcluded(t *testing.T) {
	code, _, deps := healthyInputs()
	withGit := Compute(code, models.GitStats{IsRepository: true, TotalCommits: 50,
		CommitsPerWeek: 6, DaysSinceCommit: 1, BusFactor: 3, ChurnFiles: 50}, deps)
	withoutGit := Compute(code, models.GitStats{}, deps)

	if !withoutGit.Health.Metrics[0].Applicable && withoutGit.Health.Components != 2 {
		t.Fatalf("expected 2 applicable metrics without git, got %d", withoutGit.Health.Components)
	}
	if withGit.Health.Components != 3 {
		t.Fatalf("expected 3 applicable metrics with git, got %d", withGit.Health.Components)
	}

	// Weights of applicable metrics must sum to 1.
	total := 0.0
	for _, m := range withoutGit.Health.Metrics {
		if m.Applicable {
			total += m.Weight
		} else if m.Weight != 0 {
			t.Fatalf("non-applicable metric %q has weight %v", m.Key, m.Weight)
		}
	}
	if total < 0.99 || total > 1.01 {
		t.Fatalf("applicable weights sum to %v", total)
	}
	if withoutGit.Health.Score != withGit.Health.Score && withGit.Health.Score-withGit.Health.Score > 15 {
		t.Fatalf("excluding git changed the score too much: %v vs %v",
			withoutGit.Health.Score, withGit.Health.Score)
	}
}

func TestWorseInputsScoreLower(t *testing.T) {
	goodCode, goodGit, goodDeps := healthyInputs()

	bigFiles := goodCode
	bigFiles.AverageLines = 1200
	bigFiles.Hotspots = []models.Hotspot{{Path: "huge.go", Lines: 12000}}

	badGit := goodGit
	badGit.CommitsPerWeek = 0
	badGit.DaysSinceCommit = 400
	badGit.BusFactor = 1
	badGit.ChurnConcentration = 0.9

	badDeps := goodDeps
	badDeps.Direct = 200
	badDeps.Locked = false

	good := Compute(goodCode, goodGit, goodDeps).Health.Score
	bad := Compute(bigFiles, badGit, badDeps).Health.Score
	if bad >= good {
		t.Fatalf("degraded inputs scored %v, healthy scored %v", bad, good)
	}
}

func TestRisksAreImpactWeighted(t *testing.T) {
	c, g, d := healthyInputs()
	g.BusFactor = 1
	g.CommitsPerWeek = 0
	g.DaysSinceCommit = 500

	scored := Compute(c, g, d)
	if len(scored.Risks) == 0 {
		t.Fatal("expected at least one risk for a degraded git metric")
	}
	for i := 1; i < len(scored.Risks); i++ {
		if scored.Risks[i].Impact > scored.Risks[i-1].Impact {
			t.Fatal("risks are not sorted by impact descending")
		}
	}
}

func TestGradeBoundaries(t *testing.T) {
	cases := map[float64]string{
		100: "A", 90: "A", 89.99: "B", 80: "B", 79.99: "C",
		70: "C", 69.99: "D", 60: "D", 59.99: "F", 0: "F",
	}
	for score, want := range cases {
		if got := models.Grade(score); got != want {
			t.Errorf("Grade(%v) = %q, want %q", score, got, want)
		}
	}
}

func TestWeightsSumToOne(t *testing.T) {
	if sum := WeightCode + WeightDeps + WeightGit; sum != 1.0 {
		t.Fatalf("weights sum to %v", sum)
	}
}

// The size component must be two-sided. Otherwise splitting code into tiny
// files raises the score, which is the easiest possible way to game it.
func TestFileSizeIsPenalizedAtBothExtremes(t *testing.T) {
	build := func(avgLines float64) float64 {
		// 40 files keeps the cohesion component at full credit, isolating
		// the size behavior under test.
		const files = 40
		return scoreCode(models.CodeStats{
			Files:        files,
			CodeLines:    int(avgLines * files),
			AverageLines: avgLines,
			Languages:    []models.LanguageStat{{Name: "Go", Files: files}},
		}).Score
	}

	fragmented := build(4)   // far below the floor
	healthy := build(100)    // inside the band
	oversized := build(1200) // far above the ideal

	if fragmented >= healthy {
		t.Errorf("fragmented code (%v) scored %.1f, healthy scored %.1f: splitting files is not penalized",
			4.0, fragmented, healthy)
	}
	if oversized >= healthy {
		t.Errorf("oversized code (%.0f) scored %.1f, healthy scored %.1f", 1200.0, oversized, healthy)
	}
	if healthy < 99 {
		t.Errorf("a codebase inside the healthy band scored only %.1f", healthy)
	}
}

// Identical logic cut up differently must not produce wildly different scores.
// This is the regression guard for the gameable-score defect.
func TestEquivalentCodeScoresSimilarly(t *testing.T) {
	// Same branching structure, different file granularity.
	branchy := func(perFile int) models.CodeStats {
		files := 4
		perFileLines := perFile
		return models.CodeStats{
			Files:        files,
			CodeLines:    files * perFileLines,
			AverageLines: float64(perFileLines),
			Languages:    []models.LanguageStat{{Name: "Go", Files: files}},
			Hotspots:     []models.Hotspot{},
		}
	}

	coarse := scoreCode(branchy(400)).Score // 4 files of 400 lines
	medium := scoreCode(branchy(120)).Score // 4 files of 120 lines
	tiny := scoreCode(branchy(8)).Score     // 4 files of 8 lines

	// The medium layout is the healthy one and must beat both extremes.
	if medium <= coarse || medium <= tiny {
		t.Errorf("healthy granularity should score highest: medium=%.1f coarse=%.1f tiny=%.1f",
			medium, coarse, tiny)
	}
	// And the two extremes must not be catastrophically far apart, which is
	// what the one-sided version produced (100 vs 2).
	if diff := coarse - tiny; diff > 40 {
		t.Errorf("coarse and fragmented layouts differ by %.1f points; the measure is still granularity-biased", diff)
	}
}

func TestSizeVerdictNamesBothSides(t *testing.T) {
	cases := map[float64]string{
		5:     "files too fragmented",
		100:   "in band",
		1000:  "files too large",
		150:   "in band",
		25:    "in band",
		24.99: "files too fragmented",
	}
	for avg, want := range cases {
		if got := sizeVerdict(avg); got != want {
			t.Errorf("sizeVerdict(%v) = %q, want %q", avg, got, want)
		}
	}
}
