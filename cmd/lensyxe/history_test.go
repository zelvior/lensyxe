package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// History must be recorded beside the ANALYZED tree, not beside the working
// directory. Recording against the cwd meant `lensyxe analyze /other/repo`
// wrote its history into whatever directory the user happened to be in.
func TestAnalyzePersistsHistoryBesideTarget(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	elsewhere := t.TempDir()

	stdout, stderr, err := runCLIIn(t, elsewhere, "analyze", target)
	if err != nil {
		t.Fatalf("analyze: %v\n%s", stderr, stdout)
	}

	wantDB := filepath.Join(target, ".lensyxe", "history.db")
	if _, err := os.Stat(wantDB); err != nil {
		t.Fatalf("expected history at %s: %v", wantDB, err)
	}
	// Nothing must be written into the invocation directory.
	if _, err := os.Stat(filepath.Join(elsewhere, ".lensyxe")); err == nil {
		t.Error("history was written into the working directory instead of the target")
	}

	// And `history` must find it, even from an unrelated directory. The
	// "N snapshot(s)" note goes to stderr, so the timeline itself is the
	// signal that a run was recorded.
	out, stderr, err := runCLIIn(t, elsewhere, "history", target)
	if err != nil {
		t.Fatalf("history: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "Engineering health timeline") {
		t.Errorf("expected a timeline on stdout, got:\n%s", out)
	}
	if !strings.Contains(out, "first run") {
		t.Errorf("expected the recorded run to appear, got:\n%s", out)
	}
	if !strings.Contains(stderr, "1 snapshot") {
		t.Errorf("expected the snapshot count on stderr, got:\n%s", stderr)
	}
}

func TestAnalyzeNoPersistSkipsDatabase(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := runCLI(t, "analyze", target, "--no-persist"); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".lensyxe")); err == nil {
		t.Error("--no-persist must not create a database")
	}
}
