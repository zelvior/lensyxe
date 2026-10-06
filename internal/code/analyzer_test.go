package code

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

// writeFile creates path (and parents) with the given content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestAnalyzeCountsLinesAndExcludesComments(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), `package main

// a line comment
/* a block
   comment */
func main() {
	println("code") // trailing comment
}
`)
	writeFile(t, filepath.Join(dir, "util_test.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "README.md"), "# not counted\n")

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Stats.Files != 2 {
		t.Fatalf("files = %d, want 2 (markdown must be ignored)", res.Stats.Files)
	}
	// 5 code lines: package, func main, println, closing brace, plus the
	// single line of util_test.go. Comments and the blank line are excluded.
	if res.Stats.CodeLines != 5 {
		t.Errorf("code lines = %d, want 5", res.Stats.CodeLines)
	}
	if res.Stats.TotalLines < res.Stats.CodeLines {
		t.Errorf("physical lines %d < code lines %d", res.Stats.TotalLines, res.Stats.CodeLines)
	}
	if len(res.Stats.Languages) != 1 || res.Stats.Languages[0].Name != "Go" {
		t.Fatalf("languages = %+v, want a single Go entry", res.Stats.Languages)
	}
}

func TestAnalyzeIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	// Many same-size files force ties so sort stability is exercised.
	for _, name := range []string{"c.go", "a.go", "b.go", "d.go"} {
		writeFile(t, filepath.Join(dir, name), "package p\nfunc f() {}\nfunc g() {}\n")
	}
	first, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	for i := 0; i < 10; i++ {
		got, err := Analyze(dir, DefaultConfig(), nil)
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if !reflect.DeepEqual(got.Stats, first.Stats) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, got.Stats, first.Stats)
		}
	}
}

func TestAnalyzeIgnoresConfiguredDirs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "app.go"), "package p\n")
	writeFile(t, filepath.Join(dir, "vendor", "dep.go"), "package d\n")
	writeFile(t, filepath.Join(dir, "node_modules", "x.js"), "var a = 1;\n")

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Stats.Files != 1 {
		t.Fatalf("files = %d, want 1 (vendor and node_modules pruned)", res.Stats.Files)
	}
}

func TestAnalyzeRespectsSizeCap(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.MaxFileBytes = 32
	writeFile(t, filepath.Join(dir, "small.go"), "package p\n")
	writeFile(t, filepath.Join(dir, "big.go"), "package p\n// padding padding padding padding padding padding\n")

	res, err := Analyze(dir, cfg, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !res.Stats.Truncated {
		t.Error("expected Truncated to be set when the size cap is hit")
	}
	if res.Stats.Files != 1 {
		t.Errorf("files = %d, want 1", res.Stats.Files)
	}
}

func TestAnalyzeEmptyDirectory(t *testing.T) {
	res, err := Analyze(t.TempDir(), DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Stats.Files != 0 {
		t.Errorf("files = %d, want 0", res.Stats.Files)
	}
	if res.Stats.Hotspots == nil || res.Stats.Languages == nil {
		t.Error("slices must be non-nil so JSON emits [] instead of null")
	}
	if len(res.Findings) == 0 {
		t.Error("expected an informational finding for an empty tree")
	}
}

func TestAnalyzeRejectsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "x.go")
	writeFile(t, file, "package p\n")

	if _, err := Analyze(file, DefaultConfig(), nil); err == nil {
		t.Fatal("expected an error when the target is a file, not a directory")
	}
}

func TestLanguageOrdering(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package p\nfunc a() {}\nfunc b() {}\nfunc c() {}\n")
	writeFile(t, filepath.Join(dir, "a.py"), "x = 1\ny = 2\n")

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Stats.Languages) != 2 {
		t.Fatalf("languages = %+v", res.Stats.Languages)
	}
	if res.Stats.Languages[0].Name != "Go" {
		t.Errorf("expected Go first (more lines), got %+v", res.Stats.Languages)
	}
}

func TestTestFileDetection(t *testing.T) {
	dir := t.TempDir()
	tests := []string{
		"main.go",
		"main_test.go",
		"widget.test.ts",
		"widget.spec.js",
		"test_helper.py",
		"tests/legacy.rb",
		"__tests__/foo.ts",
	}
	for _, name := range tests {
		writeFile(t, filepath.Join(dir, name), "x = 1\n")
	}

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Stats.Files != len(tests) {
		t.Fatalf("files = %d, want %d", res.Stats.Files, len(tests))
	}
	// Only main.go is production code.
	if res.Stats.SourceFiles != 1 || res.Stats.TestFiles != len(tests)-1 {
		t.Errorf("source=%d test=%d, want 1 and %d",
			res.Stats.SourceFiles, res.Stats.TestFiles, len(tests)-1)
	}
	if !res.Stats.HasTests {
		t.Error("HasTests should be true")
	}
	want := float64(len(tests)-1) / float64(len(tests))
	if res.Stats.TestFileRatio != round2(want) {
		t.Errorf("TestFileRatio = %v, want %v", res.Stats.TestFileRatio, round2(want))
	}
}

func TestNoTestsFlagged(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.go"), "package main\n")

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Stats.HasTests {
		t.Error("HasTests should be false")
	}
	if res.Stats.TestFileRatio != 0 {
		t.Errorf("TestFileRatio = %v, want 0", res.Stats.TestFileRatio)
	}
	found := false
	for _, f := range res.Findings {
		if f.Title == "No test files detected" {
			found = true
		}
	}
	if !found {
		t.Error("expected a 'No test files detected' finding")
	}
}

// branchyFunction returns a single function body with n decision tokens,
// written so the estimator sees exactly n branches.
func branchyFunction(name string, branches int) string {
	var b strings.Builder
	b.WriteString("func " + name + "() {\n")
	for i := 0; i < branches; i++ {
		b.WriteString("\tif cond")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(" {\n\t\tswitch v {\n\t\tcase 1:\n\t\tdefault:\n\t\t}\n\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// paddedFunction returns a function with one branch plus filler lines, used to
// reach a line count without raising complexity.
func paddedFunction(name string, filler int) string {
	var b strings.Builder
	b.WriteString("func " + name + "() {\n")
	for i := 0; i < filler; i++ {
		b.WriteString("\tvalue")
		b.WriteString(strings.Repeat("X", i%4))
		b.WriteString(" = 1\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// A file is only a confirmed hotspot when size, churn, AND complexity all
// cross their thresholds. Each missing factor must prevent confirmation.
func TestHotspotClassification(t *testing.T) {
	dir := t.TempDir()

	// Large AND complex: one function holding 40 branch points plus filler to
	// clear the 500-line size bar.
	var hot strings.Builder
	hot.WriteString("package p\n")
	hot.WriteString(branchyFunction("hot", 40))
	for i := 0; i < 470; i++ {
		hot.WriteString("var pad")
		hot.WriteString(strings.Repeat("X", i%5))
		hot.WriteString(" = 1\n")
	}
	writeFile(t, filepath.Join(dir, "hot.go"), hot.String())

	// Large but simple: same size, no decision tokens.
	var flat strings.Builder
	flat.WriteString("package p\n")
	for i := 0; i < 620; i++ {
		flat.WriteString("var flat")
		flat.WriteString(strings.Repeat("X", i%5))
		flat.WriteString(" = 1\n")
	}
	writeFile(t, filepath.Join(dir, "flat.go"), flat.String())

	churn := map[string]int{"hot.go": 120, "flat.go": 5}

	res, err := Analyze(dir, DefaultConfig(), churn)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Stats.Hotspots) != 2 {
		t.Fatalf("hotspots = %d, want 2:\n%+v", len(res.Stats.Hotspots), res.Stats.Hotspots)
	}

	byPath := map[string]models.Hotspot{}
	for _, h := range res.Stats.Hotspots {
		byPath[h.Path] = h
	}
	hotSpot, ok := byPath["hot.go"]
	if !ok {
		t.Fatalf("hot.go missing from hotspots: %+v", res.Stats.Hotspots)
	}
	if !hotSpot.Confirmed {
		t.Errorf("hot.go should be confirmed; got classification %q, complexity %.1f, lines %d, churn %d",
			hotSpot.Classification, hotSpot.Complexity, hotSpot.Lines, hotSpot.Churn)
	}
	if hotSpot.Level.Rank() < models.ComplexityHigh.Rank() {
		t.Errorf("hot.go level = %v, want at least high", hotSpot.Level)
	}
	if byPath["flat.go"].Confirmed {
		t.Errorf("flat.go must not be confirmed: classification %q, complexity %.1f",
			byPath["flat.go"].Classification, byPath["flat.go"].Complexity)
	}
}

// Each of the three factors must be individually necessary.
func TestHotspotRequiresAllThreeFactors(t *testing.T) {
	cases := []struct {
		name       string
		churn      int
		complexify bool
		want       bool
	}{
		{name: "all three met", churn: 50, complexify: true, want: true},
		{name: "churn missing", churn: 5, complexify: true, want: false},
		{name: "complexity missing", churn: 50, complexify: false, want: false},
		{name: "both churn and complexity missing", churn: 0, complexify: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var b strings.Builder
			b.WriteString("package p\n")
			if tc.complexify {
				b.WriteString(branchyFunction("hot", 40))
			}
			for i := 0; i < 620; i++ {
				b.WriteString("var pad")
				b.WriteString(strings.Repeat("X", i%5))
				b.WriteString(" = 1\n")
			}
			writeFile(t, filepath.Join(dir, "f.go"), b.String())

			res, err := Analyze(dir, DefaultConfig(), map[string]int{"f.go": tc.churn})
			if err != nil {
				t.Fatalf("Analyze: %v", err)
			}
			if len(res.Stats.Hotspots) == 0 {
				t.Fatal("expected a size-based candidate")
			}
			got := res.Stats.Hotspots[0].Confirmed
			if got != tc.want {
				t.Errorf("confirmed = %v, want %v (classification %q, complexity %.1f, churn %d)",
					got, tc.want, res.Stats.Hotspots[0].Classification,
					res.Stats.Hotspots[0].Complexity, res.Stats.Hotspots[0].Churn)
			}
		})
	}
}

// Confirmed hotspots must sort ahead of unconfirmed ones.
func TestHotspotOrderingPutsConfirmedFirst(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 900; i++ {
		b.WriteString("var x = 1\n")
	}
	writeFile(t, filepath.Join(dir, "huge_flat.go"), b.String())

	var c strings.Builder
	c.WriteString("package p\n")
	for i := 0; i < 520; i++ {
		c.WriteString("if a && b { if c || d { for x := range y { } } }\n")
	}
	writeFile(t, filepath.Join(dir, "medium_complex.go"), c.String())

	res, err := Analyze(dir, DefaultConfig(), map[string]int{"medium_complex.go": 90})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Stats.Hotspots) < 2 {
		t.Fatalf("expected both files as hotspot candidates: %+v", res.Stats.Hotspots)
	}
	if !res.Stats.Hotspots[0].Confirmed {
		t.Errorf("first hotspot should be the confirmed one, got %+v", res.Stats.Hotspots[0])
	}
}

// Without churn data nothing can be confirmed, since churn is one of the three
// required factors.
func TestHotspotsUnconfirmedWithoutChurn(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 600; i++ {
		b.WriteString("if a && b { for x := range y { } }\n")
	}
	writeFile(t, filepath.Join(dir, "big.go"), b.String())

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Stats.Hotspots) == 0 {
		t.Fatal("expected a size-based candidate")
	}
	for _, h := range res.Stats.Hotspots {
		if h.Confirmed {
			t.Errorf("%s confirmed without churn data", h.Path)
		}
	}
}

// ApplyChurn must reclassify from retained records without re-walking the tree.
func TestApplyChurnReclassifies(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 600; i++ {
		b.WriteString("if a && b { for x := range y { } }\n")
	}
	writeFile(t, filepath.Join(dir, "big.go"), b.String())

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	for _, h := range res.Stats.Hotspots {
		if h.Confirmed {
			t.Fatal("precondition: nothing should be confirmed yet")
		}
	}

	updated := res.ApplyChurn(map[string]int{"big.go": 100}, DefaultConfig())
	if len(updated.Stats.Hotspots) != 1 {
		t.Fatalf("hotspots = %d, want 1", len(updated.Stats.Hotspots))
	}
	h := updated.Stats.Hotspots[0]
	if !h.Confirmed {
		t.Errorf("expected confirmation after churn join, got %+v", h)
	}
	if h.Churn != 100 {
		t.Errorf("churn = %d, want 100", h.Churn)
	}
	if !strings.Contains(h.Classification, "confirmed") {
		t.Errorf("classification = %q, want it to include 'confirmed'", h.Classification)
	}
}

func TestChurnMap(t *testing.T) {
	entries := []models.ChurnEntry{
		{Path: "a.go", Added: 10, Deleted: 5},
		{Path: "b.go", Added: 1, Deleted: 0},
	}
	got := ChurnMap(entries)
	if got["a.go"] != 15 || got["b.go"] != 1 {
		t.Errorf("ChurnMap = %v", got)
	}
	if ChurnMap(nil) != nil {
		t.Error("ChurnMap(nil) should return nil")
	}
}

func TestComplexitySummary(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 20; i++ {
		b.WriteString("func f() {\n\tif a && b {\n\t\tif c || d {\n\t\t\tfor x := range y {\n\t\t\t}\n\t\t}\n\t}\n}\n")
	}
	writeFile(t, filepath.Join(dir, "c.go"), b.String())
	writeFile(t, filepath.Join(dir, "d.go"), "package p\nfunc g() {}\n")

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	cx := res.Stats.Complexity
	if !cx.Measured {
		t.Fatal("complexity should be measured")
	}
	if cx.Functions < 20 {
		t.Errorf("functions = %d, want at least 20", cx.Functions)
	}
	if cx.BranchPoints == 0 {
		t.Error("expected branch points to be counted")
	}
	if cx.MaxNesting < 3 {
		t.Errorf("max nesting = %d, want at least 3", cx.MaxNesting)
	}
	if cx.MaxComplexityFile == "" {
		t.Error("expected a worst-complexity file to be named")
	}
	if len(cx.WorstFiles) == 0 {
		t.Error("expected worst-complexity file rows")
	}
	// WorstFiles must be sorted descending by complexity.
	for i := 1; i < len(cx.WorstFiles); i++ {
		if cx.WorstFiles[i-1].EstimatedComplexity < cx.WorstFiles[i].EstimatedComplexity {
			t.Error("WorstFiles is not sorted descending")
		}
	}
	// The simple file must rank below the branchy one.
	if cx.WorstFiles[0].Path != "c.go" {
		t.Errorf("worst file = %q, want c.go", cx.WorstFiles[0].Path)
	}
}

// Turning the estimator off must leave every complexity number at zero rather
// than reporting a fabricated value.
func TestComplexityCanBeDisabled(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package p\nfunc f() { if a { if b { } } }\n")

	cfg := DefaultConfig()
	cfg.EnableComplexity = false
	res, err := Analyze(dir, cfg, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Stats.Complexity.Measured {
		t.Error("Measured should be false when the estimator is disabled")
	}
	if res.Stats.Complexity.AverageComplexity != 0 {
		t.Errorf("average complexity = %v, want 0", res.Stats.Complexity.AverageComplexity)
	}
}

func TestClassifyLineStripsTrailingComments(t *testing.T) {
	rule := rules["Go"]
	cases := []struct {
		name     string
		in       string
		inBlock  bool
		wantSkip bool
		wantHas  string
	}{
		{name: "plain", in: "x := 1", wantHas: "x := 1"},
		{name: "trailing comment", in: "x := 1 // note", wantHas: "x := 1"},
		{name: "whole comment", in: "// note", wantSkip: true},
		{name: "blank", in: "   ", wantSkip: true},
		{name: "block single line", in: "/* note */", wantSkip: true},
		{name: "block then code", in: "/* note */ x := 1", wantSkip: true},
		{name: "in block", in: "still comment", inBlock: true, wantSkip: true},
		{name: "block ends then code", in: "*/ x := 1", inBlock: true, wantHas: "x := 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, skip, _ := classifyLine(tc.in, rule, true, tc.inBlock)
			if skip != tc.wantSkip {
				t.Errorf("skip = %v, want %v (got %q)", skip, tc.wantSkip, got)
			}
			if !tc.wantSkip && got != tc.wantHas {
				t.Errorf("stripped = %q, want %q", got, tc.wantHas)
			}
		})
	}
}

// Branch keywords inside comments must not count toward complexity.
func TestCommentsDoNotInflateComplexity(t *testing.T) {
	dir := t.TempDir()
	withComments := "package p\n// if for while case catch\n/* if for while */\nfunc f() {}\n"
	writeFile(t, filepath.Join(dir, "a.go"), withComments)

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := res.Stats.Complexity.BranchPoints; got != 0 {
		t.Errorf("branch points = %d, want 0 (comments must not count)", got)
	}
}

// A brace inside a string literal is not a block delimiter. Without blanking
// string contents, a file that merely mentions "{" and "}" in string form
// reports a runaway nesting depth.
func TestStringLiteralsDoNotCountAsBlocks(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	b.WriteString("func f() {\n")
	for i := 0; i < 40; i++ {
		b.WriteString("\tif strings.ContainsRune(s, '{') {\n")
		b.WriteString("\t\tif strings.ContainsRune(s, '}') {\n")
		b.WriteString("\t\t}\n")
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	writeFile(t, filepath.Join(dir, "a.go"), b.String())

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	// Real nesting is 3 (function, if, if). The literal braces must not add to it.
	if got := res.Stats.Complexity.MaxNesting; got != 3 {
		t.Errorf("MaxNesting = %d, want 3 (string braces must not count)", got)
	}
}

// Branch keywords inside string literals must not count either.
func TestStringLiteralsDoNotCountAsBranches(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.go"), "package p\nvar doc = \"if for while case &&\"\n")

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := res.Stats.Complexity.BranchPoints; got != 0 {
		t.Errorf("branch points = %d, want 0 (string contents must not count)", got)
	}
}

func TestBlankStringLiterals(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", `a := 1`, `a := 1`},
		{"blanks contents", `s := "hello"`, `s := "     "`},
		{"keeps surrounding tokens", `x := "v" + y`, `x := " " + y`},
		{"handles escape", `s := "a\"b"`, `s := "    "`},
		{"unterminated blanks to eol", `s := "abc`, `s := "   `},
		{"two literals", `m["{"] + n["}"]`, `m[" "] + n[" "]`},
		{"raw backtick", "s := `{`", "s := ` `"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := blankStringLiterals(tc.in, dqB)
			if got != tc.want {
				t.Errorf("blankStringLiterals(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// Brace balance must be preserved either way.
			if strings.Count(got, "{") != strings.Count(tc.in, "{") &&
				strings.Count(got, "{") != 0 {
				t.Errorf("unexpected brace introduced: %q", got)
			}
		})
	}
}

// Comment markers inside string literals must not be treated as comments.
// Getting this backwards truncates the line mid-literal and leaves the brace
// unbalanced, which silently inflates MaxNesting for the whole file.
func TestCommentMarkersInsideStringsAreNotComments(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	b.WriteString("func f() {\n")
	for i := 0; i < 20; i++ {
		b.WriteString("\tm := map[string]string{\"//\": \"/*\"}\n")
		b.WriteString("\t_ = m\n")
	}
	b.WriteString("}\n")
	writeFile(t, filepath.Join(dir, "a.go"), b.String())

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := res.Stats.Complexity.MaxNesting; got != 1 {
		t.Errorf("MaxNesting = %d, want 1 (comment markers in strings are not comments)", got)
	}
}

func TestBlankStringLiteralsNoDelims(t *testing.T) {
	// A language with no declared delimiters must pass text through untouched.
	in := `s := "unchanged"`
	if got := blankStringLiterals(in, nil); got != in {
		t.Errorf("got %q, want the input unchanged", got)
	}
}

// Unconfirmed candidates are weak evidence and must not be reported at medium
// severity, where a dozen of them would drown out real findings.
func TestUnconfirmedHotspotFindingsAreLowSeverity(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < 40; i++ {
		b.WriteString("var x")
		b.WriteString(strings.Repeat("X", i%5))
		b.WriteString(" = 1\n")
	}
	writeFile(t, filepath.Join(dir, "a.go"), b.String())

	cfg := DefaultConfig()
	cfg.HotspotThreshold = 10
	res, err := Analyze(dir, cfg, nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Stats.Hotspots) != 1 {
		t.Fatalf("precondition: expected one candidate, got %d", len(res.Stats.Hotspots))
	}
	for _, f := range res.Findings {
		if f.Subject == "a.go" {
			if f.Severity != models.SeverityLow {
				t.Errorf("unconfirmed candidate severity = %v, want low", f.Severity)
			}
			return
		}
	}
	t.Fatal("expected a finding for the candidate file")
}
