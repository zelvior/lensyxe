package blast

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initRepo(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "t@example.com")
	gitRun(t, dir, "config", "user.name", "Test")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	// A core.autocrlf off keeps the committed bytes identical to what is
	// written, so file listings are stable across platforms.
	gitRun(t, dir, "config", "core.autocrlf", "false")
}

func newRepoWithHistory(t *testing.T, n int) string {
	t.Helper()
	dir := t.TempDir()
	initRepo(t, dir)
	for i := 0; i < n; i++ {
		write(t, dir, "a.go", fmt.Sprintf("package a\n\nfunc A%d() {}\n", i))
		write(t, dir, "b.go", fmt.Sprintf("package b\n\nfunc B%d() {}\n", i))
		commit(t, dir, fmt.Sprintf("c%d", i))
	}
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, msg string) {
	t.Helper()
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", msg)
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// formatResult renders a Result deterministically for the comparison test.
func formatResult(r Result) string {
	var b strings.Builder
	for _, p := range r.Pairs {
		fmt.Fprintf(&b, "pair %s|%s|%d|%s\n", p.A, p.B, p.SharedCommits, trim(p.Coupling))
	}
	for _, p := range r.Predictions {
		fmt.Fprintf(&b, "pred %s|%s|%d|%s\n", p.Path, p.Via, p.Hops, trim(p.Coupling))
	}
	return b.String()
}

// trim renders a coupling to two decimals so float formatting cannot make two
// identical results look different.
func trim(v float64) string { return fmt.Sprintf("%.2f", v) }
