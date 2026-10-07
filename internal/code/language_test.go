package code

import (
	"path/filepath"
	"testing"
)

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
