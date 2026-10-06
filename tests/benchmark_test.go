package tests

import (
	"context"
	"flag"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/internal/analyzer"
	"github.com/zelvior/lensyxe/internal/code"
)

// Performance targets.
//
// These are regression guards, not aspirational numbers. A benchmark that
// merely prints a duration tells you a change was slower only if you happen to
// remember last month's figure; a benchmark that fails tells you immediately.
//
// # Why there are several budgets
//
// The 200 ms target applies to the analysis proper: the code walk, the
// dependency parse, and the scorer. That is the part whose cost grows with the
// repository, and it is the part worth guarding.
//
// It is not applied end to end, because most of an end-to-end run on Windows is
// git subprocess spawn time. Measured on this codebase, a 40-file fixture
// analyzes in roughly 900 ms end to end and roughly 26 ms of actual analysis:
// 86% of the wall clock is the git analyzer, which shells out several times and
// pays process-creation cost per call. That overhead is an operating-system
// property, not an Lensyxe one. A 200 ms end-to-end budget would pass on Linux
// and fail on a Windows developer machine, which is precisely the flakiness a
// performance gate must not have.
//
// So the budgets are separated:
//
//	Code walk, small fixture:   < 200 ms    scales with files
//	End to end, small fixture:  < 3000 ms   dominated by git spawn cost
//	End to end, medium fixture: < 15000 ms  dominated by git spawn cost
//	Allocated per source file:  < 512 KiB   guards the working set
//
// Each is a flag so a slower environment can relax it without editing the test:
//
//	go test ./tests -budget.code-walk=500
//
// A budget of 0 disables its assertion.
var (
	budgetCodeWalk = flag.Duration("budget.code-walk", 200*time.Millisecond,
		"ceiling for the code walk over the small fixture; 0 disables")
	budgetEndToEnd = flag.Duration("budget.end-to-end", 3*time.Second,
		"ceiling for a full analysis of the small fixture; 0 disables")
	budgetMediumEndToEnd = flag.Duration("budget.medium-end-to-end", 15*time.Second,
		"ceiling for a full analysis of the medium fixture; 0 disables")
	budgetBytesPerFile = flag.Uint64("budget.bytes-per-file", 512*1024,
		"ceiling for allocated bytes per source file; 0 disables")
)

// benchConfig builds an analyzer config for a target.
func benchConfig(target string) analyzer.Config {
	cfg := analyzer.DefaultConfig(target)
	// A generous timeout: a benchmark that fails on the deadline would report
	// a timeout rather than a duration, which is useless.
	cfg.Timeout = 5 * time.Minute
	return cfg
}

// BenchmarkAnalyzeSmallRepo measures a small single-package repository.
//
// ReportAllocs is on for every benchmark here. Wall-clock time alone hides the
// regression that matters most in a CLI: an analysis that stops allocating per
// line stays fast in microseconds while its resident memory grows.
func BenchmarkAnalyzeSmallRepo(b *testing.B) {
	dir := smallFixture(b)
	cfg := benchConfig(dir)

	// One warm-up run so the first measured iteration is not paying for lazily
	// initialized state inside the analyzers.
	if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
		b.Fatalf("warm-up analysis failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
			b.Fatalf("analysis %d failed: %v", i, err)
		}
	}
}

// BenchmarkAnalyzeMediumRepo measures a larger multi-package service.
func BenchmarkAnalyzeMediumRepo(b *testing.B) {
	dir := mediumFixture(b)
	cfg := benchConfig(dir)

	if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
		b.Fatalf("warm-up analysis failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
			b.Fatalf("analysis %d failed: %v", i, err)
		}
	}
}

// BenchmarkAnalyzeLargeRepo measures a monorepo-scale repository.
//
// It exists to confirm the analyzer scales roughly linearly. A quadratic walk
// would show up here as a run time far out of proportion to the file count, and
// that is the failure mode the medium benchmark alone would miss.
func BenchmarkAnalyzeLargeRepo(b *testing.B) {
	if testing.Short() {
		b.Skip("large fixture generation is slow; skipped under -short")
	}
	dir := largeFixture(b)
	cfg := benchConfig(dir)

	if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
		b.Fatalf("warm-up analysis failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
			b.Fatalf("analysis %d failed: %v", i, err)
		}
	}
}

// TestCodeWalkMeetsBudget is the 200 ms target, applied to the part of the
// pipeline it can meaningfully govern.
//
// This is the budget that matters. It measures the filesystem walk, the LOC
// count, and the lexical complexity pass over a 40-file fixture, which is the
// work whose cost grows with the repository. The git analyzer is excluded
// deliberately: its cost is subprocess spawn time, which is an operating-system
// property and does not scale with the code.
func TestCodeWalkMeetsBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("performance budget is not meaningful under -short")
	}
	if *budgetCodeWalk == 0 {
		t.Skip("code-walk budget disabled")
	}

	dir := smallFixture(t)

	walkCfg := code.DefaultConfig()
	walkCfg.HotspotThreshold = benchConfig(dir).HotspotThreshold
	walkCfg.EnableComplexity = true

	// Warm up: the first walk pays for lazily built path tables.
	if _, err := code.Analyze(dir, walkCfg, nil); err != nil {
		t.Fatalf("warm-up failed: %v", err)
	}

	start := time.Now()
	res, err := code.Analyze(dir, walkCfg, nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("code walk failed: %v", err)
	}

	// A walk that returns nothing in 1 ms has not measured anything.
	if res.Stats.Files == 0 {
		t.Fatal("the walk reported no files")
	}

	t.Logf("walked %d files (%d code lines) in %s",
		res.Stats.Files, res.Stats.CodeLines, elapsed.Round(time.Millisecond))

	if elapsed > *budgetCodeWalk {
		t.Errorf("code walk over %d files took %s, budget is %s. "+
			"This is the budget that tracks repository size, so a breach here is a "+
			"real regression. Run `go test -bench=BenchmarkAnalyzeSmallRepo -benchmem ./tests/`",
			res.Stats.Files, elapsed.Round(time.Millisecond), *budgetCodeWalk)
	}
}

// TestAnalyzeSmallRepoMeetsEndToEndBudget is the whole-pipeline smoke guard.
//
// The ceiling is deliberately loose because most of it is git subprocess spawn
// cost, which differs by an order of magnitude between a warm Linux runner and a
// Windows laptop. Tightening it would make the test flaky across environments,
// which is worse than having no guard at all.
func TestAnalyzeSmallRepoMeetsEndToEndBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("performance budget is not meaningful under -short")
	}
	if *budgetEndToEnd == 0 {
		t.Skip("end-to-end budget disabled")
	}

	dir := smallFixture(t)
	cfg := benchConfig(dir)

	// Warm up first: the first analysis pays for lazily initialized state, and
	// charging that to the user experience would be wrong, since the CLI
	// process runs exactly one analysis.
	if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
		t.Fatalf("warm-up failed: %v", err)
	}

	start := time.Now()
	snap, err := analyzer.Scan(context.Background(), cfg, "bench")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("analysis failed: %v", err)
	}

	// Sanity check the output before judging its speed. A benchmark that
	// returns an empty snapshot in 2 ms is not a fast analyzer.
	if snap.Code.Files == 0 {
		t.Fatal("analysis reported no files; the fixture or analyzer is broken")
	}
	if snap.Health.Score <= 0 {
		t.Fatalf("analysis produced no score: %+v", snap.Health)
	}

	t.Logf("analyzed %d files (%d code lines) in %s, score %.1f "+
		"(most of this is git subprocess spawn cost; see TestProfileAnalysisCost)",
		snap.Code.Files, snap.Code.CodeLines, elapsed.Round(time.Millisecond),
		snap.Health.Score)

	if elapsed > *budgetEndToEnd {
		t.Errorf("end-to-end analysis took %s, budget is %s "+
			"(raise it with -budget.end-to-end if this machine is unusually slow)",
			elapsed.Round(time.Millisecond), *budgetEndToEnd)
	}
}

func TestAnalyzeMediumRepoMeetsEndToEndBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("performance budget is not meaningful under -short")
	}
	if *budgetMediumEndToEnd == 0 {
		t.Skip("medium budget disabled")
	}

	dir := mediumFixture(t)
	cfg := benchConfig(dir)

	if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
		t.Fatalf("warm-up failed: %v", err)
	}

	start := time.Now()
	snap, err := analyzer.Scan(context.Background(), cfg, "bench")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("analysis failed: %v", err)
	}
	if snap.Code.Files == 0 {
		t.Fatal("analysis reported no files")
	}

	t.Logf("analyzed %d files (%d code lines) in %s, score %.1f",
		snap.Code.Files, snap.Code.CodeLines, elapsed.Round(time.Millisecond),
		snap.Health.Score)

	if elapsed > *budgetMediumEndToEnd {
		t.Errorf("medium analysis took %s, budget is %s",
			elapsed.Round(time.Millisecond), *budgetMediumEndToEnd)
	}
}

// TestAnalysisMemoryIsBoundedPerFile guards the per-file working set.
//
// The analyzer streams files and keeps one record per file, so allocated bytes
// should grow with the file count and not with the total bytes read. A
// regression that buffers file contents, or retains the per-file complexity
// detail for every file instead of the capped summary, would show up here while
// staying comfortably inside any wall-clock budget.
//
// The measurement is total allocation rather than peak resident memory because
// Go makes peak RSS awkward to observe portably from inside the process.
func TestAnalysisMemoryIsBoundedPerFile(t *testing.T) {
	if testing.Short() {
		t.Skip("allocation budget is not meaningful under -short")
	}
	if *budgetBytesPerFile == 0 {
		t.Skip("allocation budget disabled")
	}

	dir := smallFixture(t)
	cfg := benchConfig(dir)

	// Warm up, then measure a single analysis with allocation tracking.
	if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
		t.Fatalf("warm-up failed: %v", err)
	}

	var stats memStats
	total := testing.AllocsPerRun(1, func() {
		stats = measure(func() {
			if _, err := analyzer.Scan(context.Background(), cfg, "bench"); err != nil {
				t.Fatalf("analysis failed: %v", err)
			}
		})
	})

	snap, err := analyzer.Scan(context.Background(), cfg, "bench")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Code.Files == 0 {
		t.Fatal("no files analyzed")
	}

	perFile := stats.totalAlloc / uint64(snap.Code.Files)
	t.Logf("%d files: %s total allocated (%.0f allocations), %d bytes/file",
		snap.Code.Files,
		humanBytes(stats.totalAlloc),
		total,
		perFile)

	if perFile > *budgetBytesPerFile {
		t.Errorf("analysis allocated %d bytes per file, budget is %d. "+
			"The analyzer should retain one small record per file, not file contents.",
			perFile, *budgetBytesPerFile)
	}
}

// TestAnalysisIsDeterministicAcrossRuns is a benchmark-suite guard.
//
// Performance work is where nondeterminism usually creeps in: a map iterated
// in random order, a timestamp folded into a score, a cache that changes
// behaviour on the second run. All three are invisible to a duration and fatal
// to the tool's core promise, so the benchmark package asserts it.
func TestAnalysisIsDeterministicAcrossRuns(t *testing.T) {
	dir := smallFixture(t)
	cfg := benchConfig(dir)

	first, err := analyzer.Scan(context.Background(), cfg, "bench")
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		got, err := analyzer.Scan(context.Background(), cfg, "bench")
		if err != nil {
			t.Fatalf("run %d failed: %v", i, err)
		}
		if got.Health.Score != first.Health.Score {
			t.Errorf("run %d scored %.2f, first run scored %.2f",
				i, got.Health.Score, first.Health.Score)
		}
		if got.Health.Grade != first.Health.Grade {
			t.Errorf("run %d graded %q, first run graded %q",
				i, got.Health.Grade, first.Health.Grade)
		}
		if len(got.Risks) != len(first.Risks) {
			t.Errorf("run %d found %d risks, first run found %d",
				i, len(got.Risks), len(first.Risks))
		}
		// DurationMS is expected to differ; GeneratedAt with it. Everything
		// else must not.
		for j := range first.Risks {
			if j >= len(got.Risks) {
				break
			}
			if got.Risks[j].ID != first.Risks[j].ID {
				t.Errorf("run %d risk %d is %q, first run had %q",
					i, j, got.Risks[j].ID, first.Risks[j].ID)
			}
		}
	}
}

// BenchmarkRiskEngine isolates scoring from the filesystem walk.
//
// Once the scan dominates the profile, a regression in the scorer is invisible
// in the end-to-end numbers. This benchmark runs the pure part on its own so a
// change there is visible.
func BenchmarkRiskEngine(b *testing.B) {
	dir := smallFixture(b)
	cfg := benchConfig(dir)

	snap, err := analyzer.Scan(context.Background(), cfg, "bench")
	if err != nil {
		b.Fatalf("fixture analysis failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		assess := riskAssess
		_ = assess(snap.Code, snap.Git, snap.Dependencies, snap.Health)
	}
}

// BenchmarkRenderJSON isolates serialization, which matters because the
// dashboard and every pipeline consume it.
func BenchmarkRenderJSON(b *testing.B) {
	dir := smallFixture(b)
	cfg := benchConfig(dir)

	snap, err := analyzer.Scan(context.Background(), cfg, "bench")
	if err != nil {
		b.Fatalf("fixture analysis failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var buf discardWriter
		if err := renderJSON(&buf, snap); err != nil {
			b.Fatalf("render: %v", err)
		}
		if buf.n == 0 {
			b.Fatal("renderer produced nothing")
		}
	}
}

// BenchmarkMonorepoBreakdown measures the per-package scoring path, which is
// the one designed specifically to avoid a second filesystem pass. If that
// design regresses into N walks, this number is where it shows.
func BenchmarkMonorepoBreakdown(b *testing.B) {
	dir := workspaceFixture(b)

	base := benchConfig(dir)
	base.DetectWorkspace = true

	if _, err := analyzer.Scan(context.Background(), base, "bench"); err != nil {
		b.Fatalf("fixture analysis failed: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := analyzer.Scan(context.Background(), base, "bench"); err != nil {
			b.Fatalf("analysis %d failed: %v", i, err)
		}
	}
}
