package code

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

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
