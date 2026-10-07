package code

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
