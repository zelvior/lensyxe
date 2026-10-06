package cognitive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// source is a small Go program written inline so the expected figures can be
// read off by a person rather than recorded from a previous run.
const source = `package sample

import "fmt"

// straightforward declares one variable and uses it straight away.
func straightforward(n int) int {
	sum := 0
	for i := 0; i < n; i++ {
		sum += i
	}
	return sum
}

// sprawling declares a value and does not read it for a long way.
func sprawling(n int) int {
	first := compute(n)
	second := compute(n + 1)
	third := compute(n + 2)
	fourth := compute(n + 3)
	fifth := compute(n + 4)
	sixth := compute(n + 5)
	_ = second
	_ = third
	_ = fourth
	_ = fifth
	_ = sixth
	return first
}

func compute(n int) int { return n * 2 }

// nested is deeply nested.
func nested(items []int) int {
	total := 0
	for _, a := range items {
		if a > 0 {
			for b := 0; b < a; b++ {
				if b%2 == 0 {
					for c := 0; c < b; c++ {
						if c > 1 {
							total++
						}
					}
				}
			}
		}
	}
	return total
}

// talkative calls many different helpers.
func talkative(s string) {
	fmt.Println(s)
	fmt.Sprintf("%s", s)
	strings.TrimSpace(s)
	strings.ToUpper(s)
}
`

func measure(t *testing.T, src string) File {
	t.Helper()
	return measureFile("sample.go", "Go", MethodAST, []byte(src), DefaultConfig())
}

func fnNamed(f File, name string) *Function {
	for i := range f.Worst {
		if f.Worst[i].Name == name {
			return &f.Worst[i]
		}
	}
	return nil
}

// Lifetime is the line distance from a declaration to its last use. This
// example is laid out so every expected span can be counted by hand:
//
//	line 4: a := 1        declared
//	line 5: _ = a         last use, 1 line later
//	line 7: b := 2        declared
//	line 8,9,10: filler
//	line 11: _ = b        last use, 4 lines later
const lifetimeSample = `package p

func f() int {
	a := 1
	_ = a

	b := 2
	_ = 0
	_ = 0
	_ = 0
	_ = b
	return 0
}
`

func TestLifetimeIsTheDistanceToTheLastUse(t *testing.T) {
	f := measureFile("p.go", "Go", MethodAST, []byte(lifetimeSample), DefaultConfig())
	if f.Error != "" {
		t.Fatalf("measure failed: %s", f.Error)
	}
	fn := f.Worst[0]

	if fn.Variables != 2 {
		t.Fatalf("measured %d variables, want 2:\n%+v", fn.Variables, fn)
	}
	// Spans of 1 and 4. The median of {1,4} is 2 (rounded down) and the max 4.
	if fn.MedianLifetime != 2 {
		t.Errorf("median lifetime = %d, want 2 (spans of 1 and 4)", fn.MedianLifetime)
	}
	if fn.MaxLifetime != 4 {
		t.Errorf("max lifetime = %d, want 4", fn.MaxLifetime)
	}
}

// A variable declared and never used has no span and must not be counted as a
// zero-span variable, which would drag the median down.
func TestUnusedVariablesAreNotCountedAsZeroSpan(t *testing.T) {
	src := `package p

func f() int {
	used := 1
	_ = used
	never := 2
	return 0
}
`
	f := measureFile("p.go", "Go", MethodAST, []byte(src), DefaultConfig())
	fn := f.Worst[0]
	if fn.Variables != 1 {
		t.Errorf("measured %d variables, want 1 (the unused one has no span):\n%+v",
			fn.Variables, fn)
	}
	if fn.MaxLifetime != 1 {
		t.Errorf("max lifetime = %d, want 1", fn.MaxLifetime)
	}
}

// A file that cannot be parsed is reported, not dropped.

// The whole point of the lifetime measure: a value declared and not read for a
// long way must score worse than one read immediately.
func TestLongLifetimeScoresAboveShortLifetime(t *testing.T) {
	f := measure(t, source)
	short := fnNamed(f, "straightforward")
	long := fnNamed(f, "sprawling")
	if short == nil || long == nil {
		t.Fatalf("missing functions: %+v", f.Worst)
	}
	if long.MaxLifetime <= short.MaxLifetime {
		t.Errorf("sprawling max lifetime %d is not above straightforward %d",
			long.MaxLifetime, short.MaxLifetime)
	}
	if long.Score <= short.Score {
		t.Errorf("sprawling scored %.1f, not above straightforward %.1f",
			long.Score, short.Score)
	}
}

// Nesting must be measured from the tree, not from indentation, so the depth of
// a genuinely nested function is found.
func TestScopeDepthFollowsNesting(t *testing.T) {
	f := measure(t, source)
	fn := fnNamed(f, "nested")
	if fn == nil {
		t.Fatalf("nested not measured: %+v", f.Worst)
	}
	if fn.ScopeDepth < 3 {
		t.Errorf("scope depth = %d, want at least 3 for four nested loops:\n%+v",
			fn.ScopeDepth, *fn)
	}

	flat := fnNamed(f, "straightforward")
	if flat == nil {
		t.Fatal("straightforward not measured")
	}
	if flat.ScopeDepth >= fn.ScopeDepth {
		t.Errorf("flat function depth %d is not below nested %d",
			flat.ScopeDepth, fn.ScopeDepth)
	}
}

// Call targets must be counted.
func TestContextSwitchDensityCountsCalls(t *testing.T) {
	f := measure(t, source)
	fn := fnNamed(f, "talkative")
	if fn == nil {
		t.Fatalf("talkative not measured: %+v", f.Worst)
	}
	if fn.ContextSwitches < 3 {
		t.Errorf("context switches = %d, want at least 3:\n%+v",
			fn.ContextSwitches, *fn)
	}
	if fn.Density <= 0 {
		t.Errorf("density = %v, want positive", fn.Density)
	}
}

// Determinism: identical input must give an identical score, or the index is
// not reproducible and cannot be compared between runs.
func TestScoringIsDeterministic(t *testing.T) {
	cfg := DefaultConfig()
	first := measureFile("s.go", "Go", MethodAST, []byte(source), cfg).Score
	for i := 0; i < 50; i++ {
		again := measureFile("s.go", "Go", MethodAST, []byte(source), cfg).Score
		if again != first {
			t.Fatalf("run %d scored %v, first was %v", i, again, first)
		}
	}
}

// The index is defined by its weights, so weights that do not describe a
// weighted sum must be refused rather than silently renormalised.
func TestWeightsMustSumToOne(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DensityWeight = 0.9 // total 1.55
	dir := t.TempDir()
	if _, err := Analyze(dir, cfg); err == nil {
		t.Fatal("Analyze accepted weights that do not sum to 1")
	} else if !strings.Contains(err.Error(), "sum to 1") {
		t.Errorf("the error does not explain the problem: %v", err)
	}
}

// Saturation points must be positive or the components divide by zero.
func TestSaturationPointsMustBePositive(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.LifetimeSaturation = 0 },
		func(c *Config) { c.DensitySaturation = 0 },
		func(c *Config) { c.DepthSaturation = 0 },
	} {
		cfg := DefaultConfig()
		mutate(&cfg)
		if _, err := Analyze(t.TempDir(), cfg); err == nil {
			t.Errorf("Analyze accepted a zero saturation point: %+v", cfg)
		}
	}
}

// A score must never leave 0..100, including for absurd input.
func TestScoreStaysInRange(t *testing.T) {
	cfg := DefaultConfig()
	for _, c := range []struct {
		lifetime, density float64
		depth             int
	}{
		{0, 0, 0},
		{-500, -1, -3},
		{100000, 10000, 900},
	} {
		got := score(c.lifetime, c.density, c.depth, cfg)
		if got < 0 || got > 100 {
			t.Errorf("score(%v,%v,%v) = %v, out of range", c.lifetime, c.density, c.depth, got)
		}
	}
}

// A file that cannot be parsed is reported, not dropped: a syntax error is
// itself worth knowing about.
func TestUnparseableFileIsReportedNotDropped(t *testing.T) {
	f := measureFile("bad.go", "Go", MethodAST, []byte("package bad\nfunc ("), DefaultConfig())
	if f.Error == "" {
		t.Error("a file that cannot be parse reported no error")
	}
	if f.Method != MethodAST {
		t.Errorf("method = %q, want ast", f.Method)
	}
}

// The lexical path must say so, and must not claim a lifetime it cannot see.
func TestLexicalMeasurementIsLabelledAndOmitsLifetime(t *testing.T) {
	js := []byte("function outer() {\n  if (x) {\n    for (;;) {\n      helper();\n    }\n  }\n}\n")
	f := measureFile("a.js", "JavaScript", MethodLexical, js, DefaultConfig())

	if f.Method != MethodLexical {
		t.Errorf("method = %q, want lexical", f.Method)
	}
	if f.Error == "" {
		t.Error("a lexically measured file did not say that lifetime was excluded")
	}
	if !strings.Contains(f.Error, "not observable") {
		t.Errorf("the exclusion is not explained: %q", f.Error)
	}
	if f.MedianLifetime != 0 || f.MaxLifetime != 0 {
		t.Errorf("a lexical measurement reported a lifetime: %+v", f)
	}
	if f.MaxScopeDepth < 2 {
		t.Errorf("nesting depth = %d, want at least 2:\n%+v", f.MaxScopeDepth, f)
	}
}

// Methods must never be blended into one number. A repository median over
// approximate and exact measurements would be a number describing neither.
func TestLexicalFilesAreNotCountedAsAnalyzed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "good.go", source)
	write(t, dir, "script.js", "function f() { helper(); }\n")

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Analyzed != 1 {
		t.Errorf("analyzed = %d, want 1 (only the Go file is exact)", res.Analyzed)
	}
	if res.Failed != 1 {
		t.Errorf("failed = %d, want 1 (the JS file is not measurable exactly)", res.Failed)
	}
	// The Go median must come from the Go file alone, so it has to equal that
	// file's score exactly.
	var goScore float64
	for _, f := range res.Files {
		if f.Method == MethodAST {
			goScore = f.Score
		}
	}
	if res.MedianScore != goScore {
		t.Errorf("median %v does not equal the only exact measurement %v: "+
			"an approximate file reached the aggregate", res.MedianScore, goScore)
	}
}

// An empty directory is not an error and must not claim a score.
func TestAnalyzeEmptyDirectory(t *testing.T) {
	res, err := Analyze(t.TempDir(), DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.MedianScore != 0 || res.WorstScore != 0 {
		t.Errorf("an empty directory produced scores: %+v", res)
	}
}

// Dependency and output directories must be pruned, or node_modules would
// dominate every figure.
func TestVendorDirectoriesArePruned(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "app.go", source)
	write(t, dir, "node_modules/dep/index.js", "function f(){}\n")
	write(t, dir, ".git/hooks/pre-commit.go", "package main\n")

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	for _, f := range res.Files {
		if strings.Contains(f.Path, "node_modules") || strings.Contains(f.Path, ".git") {
			t.Errorf("a pruned directory was measured: %s", f.Path)
		}
	}
	if res.Analyzed != 1 {
		t.Errorf("analyzed = %d, want 1", res.Analyzed)
	}
}

// Files are ranked worst first, and ties break on path so output is stable.
func TestFilesAreRankedWorstFirst(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "clean.go", `package a
func f() int { x := 1; return x }
`)
	write(t, dir, "gnarled.go", source)

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Files) != 2 {
		t.Fatalf("got %d files, want 2", len(res.Files))
	}
	if res.Files[0].Score < res.Files[1].Score {
		t.Errorf("files are not ranked worst first: %+v", res.Files)
	}
	if res.WorstScore != res.Files[0].Score {
		t.Errorf("WorstScore %v does not match the top file %v",
			res.WorstScore, res.Files[0].Score)
	}
}

// The drill-down must be bounded and worst-first, so the list is usable.
func TestWorstFunctionsAreBoundedAndRanked(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "many.go", manyFunctions(12))

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	f := res.Files[0]
	if len(f.Worst) > worstFunctionsKept {
		t.Errorf("kept %d functions, cap is %d", len(f.Worst), worstFunctionsKept)
	}
	for i := 1; i < len(f.Worst); i++ {
		if f.Worst[i-1].Score < f.Worst[i].Score {
			t.Errorf("functions are not ranked worst first: %+v", f.Worst)
			break
		}
	}
}

// manyFunctions builds a file with count functions of varying length.
func manyFunctions(count int) string {
	var b strings.Builder
	b.WriteString("package many\n\n")
	for i := 0; i < count; i++ {
		b.WriteString("func f")
		b.WriteString(itoa(i))
		b.WriteString("(n int) int {\n")
		b.WriteString("\tx := n\n")
		for j := 0; j < i*3; j++ {
			b.WriteString("\t_ = x + ")
			b.WriteString(itoa(j))
			b.WriteString("\n")
		}
		b.WriteString("\treturn x\n}\n\n")
	}
	return b.String()
}

func medianOfIsCorrect(t *testing.T) {
	if got := medianOf(nil); got != 0 {
		t.Errorf("medianOf(nil) = %d, want 0", got)
	}
	if got := medianOf([]int{5}); got != 5 {
		t.Errorf("medianOf([5]) = %d, want 5", got)
	}
	if got := medianOf([]int{1, 2, 3}); got != 2 {
		t.Errorf("medianOf([1,2,3]) = %d, want 2", got)
	}
	if got := medianOf([]int{1, 2, 3, 4}); got != 2 {
		t.Errorf("medianOf([1,2,3,4]) = %d, want 2 (rounded down)", got)
	}
}

func TestMedianOf(t *testing.T) { medianOfIsCorrect(t) }

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
