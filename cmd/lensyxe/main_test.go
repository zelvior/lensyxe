package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

// runCLI executes the command tree with args, capturing stdout and stderr.
//
// Every invocation gets a fresh app, so configuration loaded by one command can
// never leak into the next. Several tests assert exactly that.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRootCmd(&app{})
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// runCLIIn runs the command tree with cwd temporarily set to dir.
func runCLIIn(t *testing.T, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	wd, gerr := os.Getwd()
	if gerr != nil {
		t.Fatalf("getwd: %v", gerr)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	return runCLI(t, args...)
}

func TestAnalyzeDefaultsToCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLIIn(t, dir, "analyze")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if !strings.Contains(stdout, "ENGINEERING HEALTH") {
		t.Errorf("expected a health card, got:\n%s", stdout)
	}
}

func TestAnalyzeJSONOutputIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLI(t, "analyze", dir, "--format", "json")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	var snap models.Snapshot
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if snap.SchemaVersion != models.SchemaVersion {
		t.Errorf("schema_version = %q, want %q", snap.SchemaVersion, models.SchemaVersion)
	}
	if snap.Root == "" {
		t.Error("root should be populated")
	}
	if snap.Code.Complexity.WorstFiles == nil {
		t.Error("complexity worst_files should be [] not null")
	}
}

// The v0.1 --format json contract must keep working unchanged.
func TestAnalyzeJSONRetainsPhaseOneContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	stdout, _, err := runCLI(t, "analyze", dir, "--format", "json")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(stdout), &generic); err != nil {
		t.Fatalf("json: %v", err)
	}
	for _, key := range []string{"schema_version", "tool", "root", "code", "git", "dependencies", "health", "findings", "risks"} {
		if _, ok := generic[key]; !ok {
			t.Errorf("top-level key %q missing from JSON output", key)
		}
	}
	code, ok := generic["code"].(map[string]any)
	if !ok {
		t.Fatal("code is not an object")
	}
	// Phase 1 code fields must still be present.
	for _, key := range []string{"files", "total_lines", "code_lines", "max_file_lines", "average_lines", "languages", "hotspots", "bytes", "truncated"} {
		if _, ok := code[key]; !ok {
			t.Errorf("code.%q missing", key)
		}
	}
	deps, ok := generic["dependencies"].(map[string]any)
	if !ok {
		t.Fatal("dependencies is not an object")
	}
	for _, key := range []string{"detected", "ecosystems", "total", "direct", "dev", "locked"} {
		if _, ok := deps[key]; !ok {
			t.Errorf("dependencies.%q missing", key)
		}
	}
}

func TestAnalyzeMarkdownOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLI(t, "analyze", dir, "--format", "markdown")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	for _, want := range []string{"# ", "## Health score", "## Code", "## Risks", "**Recommendation**"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Error("markdown output must not contain ANSI escapes")
	}
}

func TestAnalyzeMarkdownFindingsFlag(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	without, _, err := runCLI(t, "analyze", dir, "--format", "markdown")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	with, _, err := runCLI(t, "analyze", dir, "--format", "markdown", "--findings")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if strings.Contains(without, "## Findings") {
		t.Error("findings should be omitted without --findings")
	}
	if !strings.Contains(with, "## Findings") {
		t.Error("expected a findings section with --findings")
	}
}

// "md" is accepted as an alias so scripts can use the short form.
func TestAnalyzeMarkdownAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := runCLI(t, "analyze", dir, "--format", "md"); err != nil {
		t.Fatalf("md alias should be accepted: %v", err)
	}
}

func TestAnalyzeRejectsUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCLI(t, "analyze", dir, "--format", "yaml"); err == nil {
		t.Fatal("expected an error for an unsupported format")
	}
	if _, _, err := runCLI(t, "analyze", dir, "--format", "YAML"); err == nil {
		t.Fatal("format matching must be case-insensitive in both directions")
	}
}

func TestAnalyzeRejectsTooManyArgs(t *testing.T) {
	if _, _, err := runCLI(t, "analyze", "a", "b"); err == nil {
		t.Fatal("expected an error for more than one positional argument")
	}
}

func TestAnalyzeMissingPathErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if _, _, err := runCLI(t, "analyze", missing); err == nil {
		t.Fatal("expected an error for a nonexistent path")
	}
}
