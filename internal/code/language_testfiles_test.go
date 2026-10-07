package code

import (
	"os"
	"path/filepath"
	"testing"
)

// The per-language breakdown reported zero test files for every language.
//
// langTests was declared and passed to buildLanguages but never incremented, so
// `analyze` reported 0 test files for a repository with 115 of them. The bug was
// invisible for two reasons: the overall TestFiles counter was computed
// separately and was correct, so the summary looked right; and Aggregate() has
// always counted per-language tests correctly, so the monorepo path disagreed
// with the main one rather than being checked against it.
func TestLanguageBreakdownCountsTestFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("main.go", "package main\n\nfunc main() {}\n")
	write("main_test.go", "package main\n\nimport \"testing\"\n\nfunc TestMain(t *testing.T) {}\n")
	write("lib/lib.go", "package lib\n\nfunc F() {}\n")
	write("lib/lib_test.go", "package lib\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) {}\n")

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	byName := map[string]int{}
	for _, l := range res.Stats.Languages {
		byName[l.Name] = l.TestFiles
	}

	if got := byName["Go"]; got != 2 {
		t.Errorf("Go test files = %d, want 2 (main_test.go and lib_test.go); "+
			"the per-language tally is not being incremented", got)
	}
	if res.Stats.TestFiles != 2 {
		t.Errorf("overall TestFiles = %d, want 2", res.Stats.TestFiles)
	}
}

// A language with no tests must report zero rather than inheriting another's
// count. The map is keyed by language, so this is the failure mode a shared
// counter would have.
func TestLanguageWithNoTestsReportsZero(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.ts"), []byte("export const x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	for _, l := range res.Stats.Languages {
		if l.TestFiles != 0 {
			t.Errorf("%s reports %d test files in a tree with none", l.Name, l.TestFiles)
		}
	}
}

// The two code paths that build a language breakdown must agree.
//
// Aggregate() and Analyze() are separate implementations of the same roll-up,
// and they disagreed here for as long as the bug existed. Asserting they produce
// the same per-language test counts is what stops the next divergence from
// needing a human to notice.
func TestAggregateAndAnalyzeAgreeOnLanguageTestFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package a\n\nfunc A() {}\n")
	write("a_test.go", "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n")
	write("b.go", "package b\n\nfunc B() {}\n")

	res, err := Analyze(dir, DefaultConfig(), nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	agg := Aggregate(res.PerFile, nil, DefaultConfig(), "")

	fromAnalyze := map[string]int{}
	for _, l := range res.Stats.Languages {
		fromAnalyze[l.Name] = l.TestFiles
	}
	fromAggregate := map[string]int{}
	for _, l := range agg.Stats.Languages {
		fromAggregate[l.Name] = l.TestFiles
	}

	if len(fromAnalyze) != len(fromAggregate) {
		t.Fatalf("language count differs: analyze=%v aggregate=%v", fromAnalyze, fromAggregate)
	}
	for name, want := range fromAnalyze {
		if fromAggregate[name] != want {
			t.Errorf("%s test files: analyze=%d aggregate=%d; the two roll-ups "+
				"must agree", name, want, fromAggregate[name])
		}
	}
}
