package code

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

func TestIsTestPath(t *testing.T) {
	yes := []string{
		"main_test.go",
		"pkg/thing_test.go",
		"util_test.py",
		"widget.test.ts",
		"widget.spec.js",
		"foo.test.tsx",
		"test_helper.py",
		"spec_helper.rb",
		"tests/anything.rb",
		"__tests__/component.ts",
		"src/e2e/flow.ts",
		"tests/e2e/flow.py",
		"TEST_UPPER.GO", // case-insensitive
	}
	for _, p := range yes {
		if !isTestPath(p) {
			t.Errorf("isTestPath(%q) = false, want true", p)
		}
	}

	no := []string{
		"main.go",
		"internal/testdata.go", // "testdata" is not "test"
		"latest.go",            // prefix must be "test_", not "test"
		"contest.go",
		"src/protest.ts",
		"mytest/thing.go",
	}
	for _, p := range no {
		if isTestPath(p) {
			t.Errorf("isTestPath(%q) = true, want false", p)
		}
	}
}

func TestComplexityLevelBands(t *testing.T) {
	cases := []struct {
		score float64
		want  models.ComplexityLevel
	}{
		{1, models.ComplexityLow},
		{10, models.ComplexityLow},
		{10.1, models.ComplexityModerate},
		{20, models.ComplexityModerate},
		{20.1, models.ComplexityHigh},
		{50, models.ComplexityHigh},
		{50.1, models.ComplexityVeryHigh},
		{500, models.ComplexityVeryHigh},
	}
	for _, tc := range cases {
		if got := complexityLevel(tc.score); got != tc.want {
			t.Errorf("complexityLevel(%v) = %v, want %v", tc.score, got, tc.want)
		}
	}
}

func TestComplexityLevelRankIsOrdered(t *testing.T) {
	order := []models.ComplexityLevel{
		models.ComplexityLow,
		models.ComplexityModerate,
		models.ComplexityHigh,
		models.ComplexityVeryHigh,
	}
	for i := 1; i < len(order); i++ {
		if order[i].Rank() <= order[i-1].Rank() {
			t.Errorf("%v rank %d should exceed %v rank %d",
				order[i], order[i].Rank(), order[i-1], order[i-1].Rank())
		}
	}
}

func TestCountBranches(t *testing.T) {
	cases := []struct {
		name string
		line string
		want int
	}{
		{"no branches", "x := 1", 0},
		{"if", "if a {", 1},
		{"else if counts once", "} else if b {", 1},
		{"and operator", "if a && b {", 2},
		{"or operator", "if a || b {", 2},
		{"both operators", "if a && b || c {", 3},
		{"for", "for i := 0; i < 10; i++ {", 1},
		{"case", "case 1:", 1},
		{"catch", "} catch (e) {", 1},
		{"select", "select {", 1},
		{"identifier containing if", "format(x)", 0},
		{"identifier containing for", "perform()", 0},
		{"word ifdef", "#ifdef DEBUG", 0},
		{"multiple on one line", "if a { for x { } }", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countBranches(tc.line); got != tc.want {
				t.Errorf("countBranches(%q) = %d, want %d", tc.line, got, tc.want)
			}
		})
	}
}

func TestIsDefinitionLine(t *testing.T) {
	yes := []string{
		"func main() {",
		"func (r *T) Method() {",
		"def handler(event):",
		"function render() {",
		"class Foo {",
		"public async void Run() {",
		"async function go() {",
		"impl Foo {",
	}
	for _, line := range yes {
		if !isDefinitionLine(line) {
			t.Errorf("isDefinitionLine(%q) = false, want true", line)
		}
	}
	// Declaration keywords introduce names rather than executable bodies, so
	// counting them would inflate the denominator and make complex code look
	// simple.
	no := []string{
		"x := 1",
		"if a {",
		"return nil",
		"struct Point {",
		"var x = 1",
		"const y = 2",
		"let z = 3",
		"type Alias = int",
		"",
		"}",
	}
	for _, line := range no {
		if isDefinitionLine(line) {
			t.Errorf("isDefinitionLine(%q) = true, want false", line)
		}
	}
}

func TestFirstWord(t *testing.T) {
	cases := map[string]string{
		"func main() {":      "func",
		"  def handler()":    "def",
		"public async Run()": "public",
		"x := 1":             "x",
		"":                   "",
		"   ":                "",
		"123":                "123",
	}
	for in, want := range cases {
		if got := firstWord(in); got != want {
			t.Errorf("firstWord(%q) = %q, want %q", in, got, want)
		}
	}
}

// A GitHub Actions workflow scored 47 (high band) as complexity purely from
// `if:`/`for:`/`when:` mapping keys and the shell inside `run: |` blocks. The
// estimator is lexical, so it cannot tell a declarative key from a keyword.
// Declarative languages must therefore be excluded rather than scored.
func TestDeclarativeLanguagesAreNotScored(t *testing.T) {
	for _, lang := range []string{"YAML"} {
		if hasControlFlow(lang) {
			t.Errorf("%s must not be scored for complexity", lang)
		}
	}
	for _, lang := range []string{"Go", "Python", "TypeScript", "Shell", "SQL", "Ruby", "Rust"} {
		if !hasControlFlow(lang) {
			t.Errorf("%s must be scored for complexity", lang)
		}
	}
}

// An unscored language must be absent from the complexity aggregate rather
// than reported as low complexity, which would claim a measurement nobody
// made.
func TestYAMLIsExcludedFromComplexitySummary(t *testing.T) {
	dir := t.TempDir()
	// A workflow whose tokens look exactly like decision keywords.
	wf := "name: CI\non: [push]\njobs:\n  build:\n    runs-on: ubuntu-latest\n" +
		"    steps:\n      - if: a\n      - for: b\n      - when: c\n" +
		"      - case: d\n      - loop: e\n      - match: f\n"
	writeFile(t, filepath.Join(dir, "action.yml"), wf)
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	for _, f := range res.Stats.Complexity.WorstFiles {
		if f.Language == "YAML" {
			t.Errorf("YAML must not appear in the complexity summary: %+v", f)
		}
	}
	if res.Stats.Complexity.MaxComplexityFile == "action.yml" {
		t.Errorf("a YAML workflow must not be the worst-complexity file: %+v",
			res.Stats.Complexity)
	}

	// The YAML file is still counted as code, just not as complexity.
	if res.Stats.Files != 2 {
		t.Errorf("Files = %d, want 2", res.Stats.Files)
	}
	var yamlLines int
	for _, l := range res.Stats.Languages {
		if l.Name == "YAML" {
			yamlLines = l.Lines
		}
	}
	if yamlLines == 0 {
		t.Error("YAML must still be counted for line totals")
	}
}

// A large declarative file must never be confirmed as a hotspot on a
// complexity factor it was never measured against.
func TestYAMLHotspotCannotBeConfirmed(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("name: CI\non: [push]\njobs:\n  build:\n    steps:\n")
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&b, "      - uses: actions/checkout@v%d\n", i%10)
	}
	writeFile(t, filepath.Join(dir, "action.yml"), b.String())

	res, err := Analyze(dir, DefaultConfig(), map[string]int{"action.yml": 5000})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	for _, h := range res.Stats.Hotspots {
		if h.Language != "YAML" {
			continue
		}
		if h.Confirmed {
			t.Errorf("a YAML file must never be a confirmed hotspot: %+v", h)
		}
		// And the rationale must say complexity was not measured rather
		// than claiming it was low.
		if !strings.Contains(h.Rationale, "not measured") {
			t.Errorf("rationale must disclose that complexity was not measured: %q", h.Rationale)
		}
	}
}

func TestScannerTracksNestingAndFunctions(t *testing.T) {
	var c complexityScanner
	lines := []string{
		"func a() {",
		"if x {",
		"for y {",
		"}",
		"}",
		"}",
		"func b() {",
		"}",
	}
	for _, l := range lines {
		c.feed(l)
	}
	got := c.estimate(20)
	if got.Functions != 2 {
		t.Errorf("functions = %d, want 2", got.Functions)
	}
	if got.MaxNesting != 3 {
		t.Errorf("max nesting = %d, want 3", got.MaxNesting)
	}
	if got.BranchPoints != 2 {
		t.Errorf("branch points = %d, want 2", got.BranchPoints)
	}
}

// A file with no detectable definitions must not divide by zero, and must not
// be reported as trivially simple either.
func TestEstimateWithoutFunctions(t *testing.T) {
	var c complexityScanner
	c.feed("if a {")
	c.feed("if b {")
	c.feed("}")
	c.feed("}")

	got := c.estimate(10)
	if got.Functions != 0 {
		t.Errorf("functions = %d, want 0", got.Functions)
	}
	if got.EstimatedComplexity < 1 {
		t.Errorf("estimated complexity = %v, want at least 1", got.EstimatedComplexity)
	}
	if got.Level == "" {
		t.Error("level must always be set")
	}
}

// Unbalanced braces from a truncated file must not push nesting negative.
func TestScannerHandlesUnbalancedBraces(t *testing.T) {
	var c complexityScanner
	c.feed("func a() {}")
	c.feed("}}}")
	got := c.estimate(5)
	if got.MaxNesting < 0 {
		t.Errorf("max nesting = %d, must not be negative", got.MaxNesting)
	}
}

func TestEstimateDensity(t *testing.T) {
	var c complexityScanner
	c.feed("func a() {")
	c.feed("if x { }")
	c.feed("}")
	got := c.estimate(50)
	if got.Density <= 0 {
		t.Errorf("density = %v, want positive", got.Density)
	}
	// Zero code lines must yield zero density rather than a NaN.
	var d complexityScanner
	d.feed("func a() {}")
	if got := d.estimate(0).Density; got != 0 {
		t.Errorf("density with 0 lines = %v, want 0", got)
	}
}
