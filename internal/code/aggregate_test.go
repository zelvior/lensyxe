package code

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

// rec builds a FileRecord for aggregate tests.
func rec(path, lang string, lines int, isTest bool) FileRecord {
	return FileRecord{
		Path:       path,
		Language:   lang,
		CodeLines:  lines,
		TotalLines: lines + 3, // a couple of blank and comment lines
		Bytes:      int64(lines * 12),
		IsTest:     isTest,
	}
}

// measured returns a record with a complexity measurement attached.
func measured(r FileRecord, complexity float64, level models.ComplexityLevel) FileRecord {
	r.ComplexityMeasured = true
	r.ComplexityScore = complexity
	r.Complexity = level
	r.Functions = 4
	r.BranchPoints = 12
	r.MaxNesting = 3
	r.MaxFunctionComplexity = complexity * 1.5
	return r
}

func TestAggregateEmpty(t *testing.T) {
	res := Aggregate(nil, nil, DefaultConfig(), "")

	if res.Stats.Files != 0 {
		t.Errorf("Files = %d, want 0", res.Stats.Files)
	}
	// The slices must not be nil: the JSON contract and the dashboard both
	// iterate them, and a nil serializes as null rather than [].
	if res.Stats.Languages == nil || res.Stats.Hotspots == nil ||
		res.Stats.Complexity.WorstFiles == nil {
		t.Error("an empty aggregate must still return non-nil slices")
	}
	if res.PerFile == nil {
		t.Error("PerFile must be non-nil so ApplyChurn can reclassify")
	}
	// Nothing was measured, so the summary must say so rather than implying a
	// zero-cost codebase.
	if res.Stats.Complexity.Measured {
		t.Error("an empty aggregate must not report complexity as measured")
	}
}

func TestAggregateTotals(t *testing.T) {
	records := []FileRecord{
		rec("a.go", "Go", 100, false),
		rec("b.go", "Go", 200, false),
		rec("c_test.go", "Go", 50, true),
	}

	res := Aggregate(records, nil, DefaultConfig(), "")

	s := res.Stats
	if s.Files != 3 {
		t.Errorf("Files = %d, want 3", s.Files)
	}
	if s.CodeLines != 350 {
		t.Errorf("CodeLines = %d, want 350", s.CodeLines)
	}
	if s.TotalLines != 359 {
		t.Errorf("TotalLines = %d, want 359", s.TotalLines)
	}
	// BlankOrComment must be derived, not left at zero: a report claiming 359
	// physical lines contain no blanks is an impossible number.
	if s.BlankOrComment != 9 {
		t.Errorf("BlankOrComment = %d, want 9", s.BlankOrComment)
	}
	if s.SourceFiles != 2 || s.TestFiles != 1 {
		t.Errorf("source/test = %d/%d, want 2/1", s.SourceFiles, s.TestFiles)
	}
	if !s.HasTests {
		t.Error("HasTests must be true")
	}
	if s.TestFileRatio != 0.33 {
		t.Errorf("TestFileRatio = %v, want 0.33", s.TestFileRatio)
	}
	if s.AverageLines != 116.67 {
		t.Errorf("AverageLines = %v, want 116.67", s.AverageLines)
	}
	if s.MaxFileLines != 200 {
		t.Errorf("MaxFileLines = %d, want 200", s.MaxFileLines)
	}
}

func TestAggregateLanguages(t *testing.T) {
	records := []FileRecord{
		rec("a.go", "Go", 100, false),
		rec("b.go", "Go", 100, false),
		rec("c.ts", "TypeScript", 40, false),
		rec("d.ts", "TypeScript", 40, true),
	}

	res := Aggregate(records, nil, DefaultConfig(), "")
	if len(res.Stats.Languages) != 2 {
		t.Fatalf("Languages = %d, want 2", len(res.Stats.Languages))
	}

	// Sorted by lines descending, then name ascending.
	byName := map[string]models.LanguageStat{}
	for _, l := range res.Stats.Languages {
		byName[l.Name] = l
	}

	if got := byName["Go"]; got.Files != 2 || got.Lines != 200 || got.TestFiles != 0 {
		t.Errorf("Go = %+v", got)
	}
	if got := byName["TypeScript"]; got.Files != 2 || got.Lines != 80 || got.TestFiles != 1 {
		t.Errorf("TypeScript = %+v", got)
	}
	// Go has more lines, so it must come first.
	if res.Stats.Languages[0].Name != "Go" {
		t.Errorf("languages must sort by lines desc, got %+v", res.Stats.Languages)
	}
}

// A declarative language must be absent from the complexity aggregate rather
// than counted as trivial, exactly as in the main walk.
func TestAggregateExcludesUnmeasuredLanguages(t *testing.T) {
	records := []FileRecord{
		measured(rec("a.go", "Go", 100, false), 4.0, models.ComplexityLow),
		rec("workflow.yml", "YAML", 400, false), // not measured
	}

	res := Aggregate(records, nil, DefaultConfig(), "")

	// The YAML file still counts as a file, which changes the average size.
	if res.Stats.Files != 2 {
		t.Errorf("Files = %d, want 2: an unmeasured language is still source", res.Stats.Files)
	}
	// But it must not appear in the complexity summary.
	for _, f := range res.Stats.Complexity.WorstFiles {
		if f.Language == "YAML" {
			t.Errorf("YAML must not appear in the complexity summary: %+v", f)
		}
	}
	if res.Stats.Complexity.MaxComplexityFile == "workflow.yml" {
		t.Error("an unmeasured file must not be the worst-complexity file")
	}
	// Only the measured file contributes.
	if res.Stats.Complexity.Files != 1 {
		t.Errorf("complexity Files = %d, want 1", res.Stats.Complexity.Files)
	}
}

func TestAggregateComplexitySummary(t *testing.T) {
	records := []FileRecord{
		measured(rec("a.go", "Go", 200, false), 5.0, models.ComplexityLow),
		measured(rec("b.go", "Go", 200, false), 12.0, models.ComplexityModerate),
		measured(rec("c.go", "Go", 200, false), 45.0, models.ComplexityHigh),
		measured(rec("d.go", "Go", 200, false), 60.0, models.ComplexityVeryHigh),
	}

	res := Aggregate(records, nil, DefaultConfig(), "")
	cx := res.Stats.Complexity

	if !cx.Measured {
		t.Error("complexity must report as measured")
	}
	if cx.Files != 4 {
		t.Errorf("Files = %d, want 4", cx.Files)
	}
	if cx.Functions != 16 {
		t.Errorf("Functions = %d, want 16 (4 per file)", cx.Functions)
	}
	if cx.BranchPoints != 48 {
		t.Errorf("BranchPoints = %d, want 48", cx.BranchPoints)
	}
	if cx.MaxComplexity != 60 {
		t.Errorf("MaxComplexity = %v, want 60", cx.MaxComplexity)
	}
	if cx.MaxComplexityFile != "d.go" {
		t.Errorf("MaxComplexityFile = %q, want d.go", cx.MaxComplexityFile)
	}
	if cx.VeryHighFunctions != 1 || cx.HighFunctions != 1 {
		t.Errorf("band counts = %d very high / %d high, want 1/1",
			cx.VeryHighFunctions, cx.HighFunctions)
	}
	if cx.AverageComplexity != 30.5 {
		t.Errorf("AverageComplexity = %v, want 30.5", cx.AverageComplexity)
	}
	// Worst first.
	if len(cx.WorstFiles) != 4 || cx.WorstFiles[0].Path != "d.go" {
		t.Errorf("WorstFiles must sort worst first, got %+v", cx.WorstFiles)
	}
	// Density is per 100 code lines of the SUBSET, so a package's figure is
	// comparable with the repository's.
	for _, f := range cx.WorstFiles {
		if f.Density <= 0 {
			t.Errorf("density for %s is %v; it must be recomputed from the subset", f.Path, f.Density)
		}
	}
}

func TestAggregateHotspots(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HotspotThreshold = 100

	records := []FileRecord{
		measured(rec("big.go", "Go", 300, false), 8.0, models.ComplexityLow),
		measured(rec("small.go", "Go", 20, false), 1.0, models.ComplexityLow),
	}
	churn := map[string]int{"big.go": 10}

	res := Aggregate(records, churn, cfg, "")

	if len(res.Stats.Hotspots) != 1 {
		t.Fatalf("Hotspots = %+v, want one", res.Stats.Hotspots)
	}
	h := res.Stats.Hotspots[0]
	if h.Path != "big.go" {
		t.Errorf("Path = %q", h.Path)
	}
	if h.Churn != 10 {
		t.Errorf("Churn = %d, want 10: the churn map must be joined", h.Churn)
	}
	if h.Complexity != 8 {
		t.Errorf("Complexity = %v, want 8", h.Complexity)
	}
	// Complexity is `low`, so it cannot be confirmed even at 300 lines.
	if h.Confirmed {
		t.Error("a low-complexity file must not be a confirmed hotspot")
	}
	if res.Stats.Complexity.Measured && res.Stats.Hotspots[0].Level != models.ComplexityLow {
		t.Errorf("hotspot level = %q", h.Level)
	}
}

// The prefix is how a monorepo package's package-relative paths become
// repo-relative ones, which is what the churn map is keyed by.
func TestAggregatePrefixesPaths(t *testing.T) {
	cfg := DefaultConfig()
	cfg.HotspotThreshold = 100

	records := []FileRecord{
		measured(rec("index.go", "Go", 300, false), 9.0, models.ComplexityLow),
	}

	// The churn map is keyed by the repo-relative path, so without the prefix
	// the churn lookup misses and the hotspot loses its churn.
	churn := map[string]int{"packages/core/index.go": 25}

	res := Aggregate(records, churn, cfg, "packages/core")

	if len(res.Stats.Hotspots) != 1 {
		t.Fatalf("Hotspots = %+v", res.Stats.Hotspots)
	}
	h := res.Stats.Hotspots[0]
	if h.Path != "packages/core/index.go" {
		t.Errorf("Path = %q, want the prefixed path", h.Path)
	}
	if h.Churn != 25 {
		t.Errorf("Churn = %d, want 25: the prefix must be applied before the churn join", h.Churn)
	}
	if len(res.PerFile) != 1 || res.PerFile[0].Path != "packages/core/index.go" {
		t.Errorf("PerFile paths must be prefixed too, got %+v", res.PerFile)
	}
}

// An empty prefix must not produce a leading separator on every path.
func TestAggregateEmptyPrefixLeavesPathsAlone(t *testing.T) {
	records := []FileRecord{rec("a/b.go", "Go", 10, false)}
	res := Aggregate(records, nil, DefaultConfig(), "")
	if res.PerFile[0].Path != "a/b.go" {
		t.Errorf("Path = %q, want the original path", res.PerFile[0].Path)
	}
}

// Aggregate must agree with a real walk, since a monorepo package score is
// only trustworthy if re-aggregating a walk's records reproduces the same
// numbers the walk itself produced.
func TestAggregateReproducesARealWalk(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"main.go": "package main\n\nfunc main() {}\n",
		"a.go":    "package main\n\nfunc A(x int) int {\n\tif x > 1 {\n\t\treturn x\n\t}\n\treturn 0\n}\n",
		"b.go":    "package main\n\nfunc B() int {\n\tfor i := 0; i < 3; i++ {\n\t}\n\treturn 1\n}\n",
		"a_test.go": "package main\n\nimport \"testing\"\n\n" +
			"func TestA(t *testing.T) {\n\tif A(2) != 2 {\n\t\tt.Fail()\n\t}\n}\n",
	}
	for name, body := range files {
		writeFile(t, filepath.Join(dir, name), body)
	}

	cfg := DefaultConfig()
	walk, err := Analyze(dir, cfg, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	// Re-aggregate exactly the records the walk produced.
	agg := Aggregate(walk.PerFile, nil, cfg, "")

	if agg.Stats.Files != walk.Stats.Files {
		t.Errorf("Files = %d, walk said %d", agg.Stats.Files, walk.Stats.Files)
	}
	if agg.Stats.CodeLines != walk.Stats.CodeLines {
		t.Errorf("CodeLines = %d, walk said %d", agg.Stats.CodeLines, walk.Stats.CodeLines)
	}
	if agg.Stats.AverageLines != walk.Stats.AverageLines {
		t.Errorf("AverageLines = %v, walk said %v",
			agg.Stats.AverageLines, walk.Stats.AverageLines)
	}
	if agg.Stats.TestFileRatio != walk.Stats.TestFileRatio {
		t.Errorf("TestFileRatio = %v, walk said %v",
			agg.Stats.TestFileRatio, walk.Stats.TestFileRatio)
	}
	if agg.Stats.AverageLines == 0 {
		t.Error("the fixture should have produced a non-zero average")
	}
}

// countLines is the complexity-free scan used where only LOC matters.
func TestCountLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.go")
	// Five physical lines: package, blank, comment, blank, func.
	writeFile(t, path, "package p\n\n// a comment\n\nfunc F() {}\n")

	physical, code, err := countLines(path, "Go")
	if err != nil {
		t.Fatalf("countLines: %v", err)
	}
	if physical != 5 {
		t.Errorf("physical = %d, want 5", physical)
	}
	// Blank lines and the comment are excluded from the code count.
	if code != 2 {
		t.Errorf("code = %d, want 2 (package + func)", code)
	}
}

func TestCountLinesMissingFile(t *testing.T) {
	if _, _, err := countLines(filepath.Join(t.TempDir(), "absent.go"), "Go"); err == nil {
		t.Error("a missing file must be reported")
	}
}

// An unreadable file must be an error rather than being counted as empty.
//
// The permission-bit approach is POSIX-only: Windows largely ignores mode 0o000,
// so the test skips there rather than asserting something untrue.
func TestCountLinesUnreadableFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce POSIX permission bits")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission bits")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "locked.go")
	writeFile(t, path, "package p\n")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Skipf("cannot change permissions: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	if _, _, err := countLines(path, "Go"); err == nil {
		t.Error("an unreadable file must be reported rather than counted as empty")
	}
}
