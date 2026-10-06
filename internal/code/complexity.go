// This file implements the lexical complexity estimator and test-file
// detection used by the code analyzer.
//
// Deliberately no AST: parsing Go, TypeScript, and twenty other languages
// correctly would mean either a third-party parser per language or a very
// large maintenance surface. Instead the estimator counts decision tokens and
// block depth while the file is already being streamed for LOC counts, which
// keeps the scan single-pass and allocation-light.
//
// The estimates are honest heuristics, not measurements. Every exported
// function documents exactly what it counts, and each reported number is
// reproducible from the source by hand.
package code

import (
	"strings"

	"github.com/zelvior/lensyxe/pkg/models"
)

// Complexity thresholds, following the conventional McCabe bands: at or below
// 10 is "low", 20 is "moderate", 50 is "high", anything beyond is "very_high".
const (
	ComplexityModerateLimit = 10.0
	ComplexityHighLimit     = 20.0
	ComplexityVeryHighLimit = 50.0
)

// branchKeywords are decision tokens that each add one branch point.
//
// Matched as whole lowercase words, so `ifdef`, `format`, or the identifier
// `caseLabel` never inflate the count.
var branchKeywords = map[string]bool{
	"if": true, "elif": true, "elsif": true, // most languages
	"for": true, "while": true, // iteration
	"case":   true,                               // switch/case and Rust match arms
	"catch":  true,                               // Go, Java, JS, TS, Rust, C#
	"select": true,                               // Go channel select
	"match":  true,                               // Rust, Scala
	"except": true,                               // Python
	"unless": true, "rescue": true, "when": true, // Ruby
	"loop": true, // SQL / PL/pgSQL
}

// shortCircuitOperators add a branch point each because of short-circuit
// evaluation. They are matched as bare substrings; the count can be at most
// slightly overstated by an occurrence inside a string literal, which is an
// acceptable, documented approximation.
var shortCircuitOperators = []string{"&&", "||"}

// definitionKeywords open a callable unit whose body can contain branch
// points. A stripped line whose first word is one of these starts a new
// function scope, which is what lets the estimator attribute branch points per
// function without building a syntax tree.
//
// Declaration keywords are deliberately excluded. `var`, `let`, `const`,
// `type`, `struct`, and `enum` introduce names, not executable bodies; counting
// them as functions would inflate the denominator and make genuinely complex
// code look simple. A `class` is included because its methods are nested
// inside it and would otherwise be unattributed.
var definitionKeywords = map[string]bool{
	"func": true, "fn": true, "def": true, "function": true,
	"sub": true, "proc": true, "method": true, "class": true,
	"impl": true, "trait": true, "interface": true,
	// Modifier-prefixed definitions in other languages.
	"async": true, "export": true, "public": true, "private": true,
	"protected": true, "static": true, "override": true, "abstract": true,
	"defp": true, "deff": true,
}

// complexityScanner accumulates a per-file estimate as lines stream past.
//
// Callers feed one stripped code line at a time, then read the result. The
// scanner holds no file contents, only counters.
type complexityScanner struct {
	// functions counts definition lines seen.
	functions int
	// totalBranches counts every decision token in the file.
	totalBranches int
	// functionBranches counts decision tokens inside the function currently
	// being read. Reset at each definition.
	functionBranches int
	// maxFunctionBranches is the worst single-function branch count observed.
	maxFunctionBranches int
	// nesting is the live block depth from brace counting.
	nesting int
	// maxNesting is the deepest block depth reached.
	maxNesting int
	// braces counts whether any brace at all was seen, which distinguishes
	// brace languages from indentation languages (Python, YAML).
	sawBraces bool
}

// feed consumes one stripped code line: comments and string literals already
// removed by the caller's lexer.
func (c *complexityScanner) feed(stripped string) {
	if stripped == "" {
		return
	}

	// A definition line closes the previous function's accounting and opens a
	// new one.
	if isDefinitionLine(stripped) {
		c.closeFunction()
		c.functions++
	}

	branches := countBranches(stripped)
	c.totalBranches += branches
	c.functionBranches += branches

	if strings.ContainsRune(stripped, '{') || strings.ContainsRune(stripped, '}') {
		c.sawBraces = true
		opens := strings.Count(stripped, "{")
		closes := strings.Count(stripped, "}")
		c.nesting += opens - closes
		if c.nesting < 0 {
			c.nesting = 0 // defensive: unbalanced braces from a partial file
		}
		if c.nesting > c.maxNesting {
			c.maxNesting = c.nesting
		}
	}
}

// closeFunction folds the in-progress branch count into the running maximum.
func (c *complexityScanner) closeFunction() {
	if c.functionBranches > c.maxFunctionBranches {
		c.maxFunctionBranches = c.functionBranches
	}
	c.functionBranches = 0
}

// fileComplexity is the completed per-file estimate.
type fileComplexity struct {
	Functions    int
	BranchPoints int
	MaxNesting   int
	// EstimatedComplexity is the mean estimated cyclomatic complexity per
	// function. With no detectable function it falls back to the file's own
	// branch count so a single large script is not reported as trivial.
	EstimatedComplexity float64
	MaxFunctionComplex  float64
	Level               models.ComplexityLevel
	// Density is EstimatedComplexity per 100 code lines.
	Density float64
}

// estimate finalizes the scan. codeLines is the denominator for density.
func (c *complexityScanner) estimate(codeLines int) fileComplexity {
	c.closeFunction()

	units := c.functions
	if units == 0 {
		// No definitions found: treat the whole file as one unit rather than
		// dividing by zero or reporting a meaningless 1.0.
		units = 1
	}
	estimated := float64(c.totalBranches+units) / float64(units)

	density := 0.0
	if codeLines > 0 {
		density = estimated * 100 / float64(codeLines)
	}

	return fileComplexity{
		Functions:           c.functions,
		BranchPoints:        c.totalBranches,
		MaxNesting:          c.maxNesting,
		EstimatedComplexity: round2(estimated),
		MaxFunctionComplex:  round2(float64(c.maxFunctionBranches) + 1),
		Level:               complexityLevel(estimated),
		Density:             round2(density),
	}
}

// isDefinitionLine reports whether a stripped line opens a definition.
func isDefinitionLine(s string) bool {
	word := firstWord(s)
	if word == "" {
		return false
	}
	return definitionKeywords[word]
}

// firstWord returns the leading identifier-ish token, lowercased.
//
// This handles `func main(`, `async function handler(`, `public async void
// Run(`, and `class Foo {` without needing a per-language parser.
func firstWord(s string) string {
	s = strings.TrimLeft(s, " \t")
	end := 0
	for end < len(s) && isIdentByte(s[end]) {
		end++
	}
	return strings.ToLower(s[:end])
}

// countBranches counts decision tokens on one stripped line.
func countBranches(s string) int {
	n := 0
	for _, word := range splitIdentifiers(s) {
		if branchKeywords[word] {
			n++
		}
	}
	for _, op := range shortCircuitOperators {
		n += strings.Count(s, op)
	}
	return n
}

// splitIdentifiers splits a line into lowercase identifier-ish words.
//
// Non-alphanumeric bytes act as separators, so `if(x)`, `if (x)`,
// `for _, v := range xs`, and `obj.if` are all handled uniformly without a
// regular expression.
func splitIdentifiers(s string) []string {
	words := make([]string, 0, 8)
	start := -1
	for i := 0; i < len(s); i++ {
		if isIdentByte(s[i]) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			words = append(words, strings.ToLower(s[start:i]))
			start = -1
		}
	}
	if start >= 0 {
		words = append(words, strings.ToLower(s[start:]))
	}
	return words
}

func isIdentByte(c byte) bool {
	return c == '_' ||
		(c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9')
}

// complexityLevel buckets an estimated cyclomatic complexity.
func complexityLevel(estimated float64) models.ComplexityLevel {
	switch {
	case estimated <= ComplexityModerateLimit:
		return models.ComplexityLow
	case estimated <= ComplexityHighLimit:
		return models.ComplexityModerate
	case estimated <= ComplexityVeryHighLimit:
		return models.ComplexityHigh
	default:
		return models.ComplexityVeryHigh
	}
}

// testSuffixes are filename suffixes that mark a test file.
var testSuffixes = []string{
	"_test.go", "_test.py", "_test.rb", "_test.rs", "_test.c", "_test.cc",
	"_test.java", "test.java", "test.kt", "tests.cs",
	".test.ts", ".test.tsx", ".test.js", ".test.jsx", ".test.mjs",
	".spec.ts", ".spec.tsx", ".spec.js", ".spec.jsx", ".spec.mjs",
	"_spec.rb",
}

// testPrefixes are filename prefixes that mark a test file.
var testPrefixes = []string{"test_", "spec_"}

// testDirs are directory names whose contents are tests.
var testDirs = []string{
	"test", "tests", "__tests__", "spec", "specs", "testing", "e2e",
}

// isTestPath reports whether a repo-relative path looks like a test file.
//
// Two independent signals are used and either is sufficient:
//   - a conventional test suffix or prefix in the filename
//   - residence in a conventionally named test directory
//
// The check is case-insensitive because CI runs on both case-preserving and
// case-folding filesystems.
func isTestPath(rel string) bool {
	lower := strings.ToLower(filepathSlash(rel))

	// Directory signal: every path segment is checked, not just the base name.
	for _, seg := range strings.Split(lower, "/") {
		for _, dir := range testDirs {
			if seg == dir {
				return true
			}
		}
	}

	base := lower
	if i := strings.LastIndex(lower, "/"); i >= 0 {
		base = lower[i+1:]
	}
	for _, suffix := range testSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	for _, prefix := range testPrefixes {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

// filepathSlash normalizes separators so the suffix checks work on Windows.
func filepathSlash(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}
