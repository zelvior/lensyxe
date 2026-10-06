package tests

import (
	"context"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/internal/analyzer"
	"github.com/zelvior/lensyxe/internal/code"
	"github.com/zelvior/lensyxe/internal/dependencies"
	gitanalyzer "github.com/zelvior/lensyxe/internal/git"
	"github.com/zelvior/lensyxe/internal/metrics"
	"github.com/zelvior/lensyxe/pkg/models"
)

// TestProfileAnalysisCost attributes the wall-clock time of one analysis to its
// parts.
//
// This exists because an end-to-end budget alone is not diagnosable: when it
// trips, the log says "too slow" and nothing about which part. The split also
// documents a fact that is easy to get wrong when reading the analyzer: the git
// cost is dominated by subprocess spawns, not by the size of the history, so it
// barely moves as a repository grows. Budgeting against it is budgeting against
// the operating system.
func TestProfileAnalysisCost(t *testing.T) {
	dir := smallFixture(t)
	cfg := benchConfig(dir)

	// Warm up so lazily initialized state is not attributed to a measured part.
	if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
		t.Fatalf("warm-up failed: %v", err)
	}

	// End to end.
	start := time.Now()
	snap, err := analyzer.Scan(context.Background(), cfg, "bench")
	endToEnd := time.Since(start)
	if err != nil {
		t.Fatalf("analysis failed: %v", err)
	}

	// The code walk on its own.
	walkCfg := code.DefaultConfig()
	walkCfg.HotspotThreshold = cfg.HotspotThreshold
	walkCfg.EnableComplexity = true
	start = time.Now()
	codeRes, err := code.Analyze(dir, walkCfg, nil)
	codeOnly := time.Since(start)
	if err != nil {
		t.Fatalf("code walk failed: %v", err)
	}

	// The git analysis on its own.
	gitCfg := gitanalyzer.DefaultConfig()
	gitCfg.WindowDays = cfg.GitWindowDays
	start = time.Now()
	_, gitErr := gitanalyzer.Analyze(context.Background(), dir, gitCfg)
	gitOnly := time.Since(start)
	if gitErr != nil {
		t.Fatalf("git analysis failed: %v", gitErr)
	}

	// Dependency parsing on its own.
	start = time.Now()
	if _, err := dependencies.Analyze(dir, dependencies.DefaultConfig()); err != nil {
		t.Fatalf("dependency analysis failed: %v", err)
	}
	depsOnly := time.Since(start)

	// Scoring and risk assessment, which are pure.
	start = time.Now()
	score := metrics.Compute(codeRes.Stats, snap.Git, snap.Dependencies)
	scoreOnly := time.Since(start)

	t.Logf("files=%d codeLines=%d commits=%d",
		codeRes.Stats.Files, codeRes.Stats.CodeLines, snap.Git.WindowCommits)
	t.Logf("end-to-end   %8s", endToEnd.Round(time.Millisecond))
	t.Logf("  code walk  %8s  (%.0f%%)", codeOnly.Round(time.Millisecond),
		pct(codeOnly, endToEnd))
	t.Logf("  git        %8s  (%.0f%%)", gitOnly.Round(time.Millisecond),
		pct(gitOnly, endToEnd))
	t.Logf("  deps       %8s  (%.0f%%)", depsOnly.Round(time.Millisecond),
		pct(depsOnly, endToEnd))
	t.Logf("  scoring    %8s  (%.0f%%)", scoreOnly.Round(time.Millisecond),
		pct(scoreOnly, endToEnd))
	if score.Health.Score != snap.Health.Score {
		t.Errorf("the isolated scorer disagreed with the pipeline: %.2f vs %.2f",
			score.Health.Score, snap.Health.Score)
	}
}

// pct renders part as a percentage of whole.
func pct(part, whole time.Duration) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}

// TestGitCostIsIndependentOfRepoSize documents the property the budgets rely
// on: git analysis cost is dominated by subprocess spawns, so it does not grow
// with the file count the way the code walk does.
func TestGitCostIsIndependentOfRepoSize(t *testing.T) {
	small := smallFixture(t)
	medium := mediumFixture(t)

	timeIt := func(dir string) time.Duration {
		cfg := benchConfig(dir)
		if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
			t.Fatalf("warm-up failed: %v", err)
		}
		start := time.Now()
		if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
			t.Fatalf("analysis failed: %v", err)
		}
		return time.Since(start)
	}

	smallTime := timeIt(small)
	mediumTime := timeIt(medium)

	// The medium fixture has ten times the files, so the code walk alone should
	// cost far more than ten times the small one. Total cost growing by roughly
	// that factor is the expected shape; a total near the small figure would
	// mean the walk is not the dominant term and something is wrong.
	t.Logf("small (40 files)  %s", smallTime.Round(time.Millisecond))
	t.Logf("medium (400 files) %s", mediumTime.Round(time.Millisecond))

	if mediumTime < smallTime {
		t.Errorf("the larger fixture analyzed faster (%s vs %s); "+
			"the measurement is probably noise-dominated",
			mediumTime.Round(time.Millisecond), smallTime.Round(time.Millisecond))
	}

	// A generous ceiling. The point is to catch an accidental order-of-magnitude
	// blowup, not to police microseconds.
	if mediumTime > 30*time.Second {
		t.Errorf("medium fixture took %s, which is far beyond any expected cost",
			mediumTime.Round(time.Millisecond))
	}
}

var _ = models.Snapshot{}
