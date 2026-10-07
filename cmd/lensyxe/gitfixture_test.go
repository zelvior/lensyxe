package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hasGit reports whether git is available, skipping the test if not.
func hasGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// initRepo creates a git repository with deterministic identity.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main", ".")
	gitIn(t, dir, "config", "user.name", "Tester")
	gitIn(t, dir, "config", "user.email", "tester@example.com")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// gitIn runs a git command in dir, skipping the test on failure.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git %v failed (%v): %s", args, err, out)
	}
}

// gitOut2 runs git and returns stdout.
func gitOut2(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// writeIn writes a file inside dir.
func writeIn(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// branchyBody returns a function body with n branch points.
func branchyBody(n int) string {
	var b strings.Builder
	b.WriteString("func branchy() {\n")
	for i := 0; i < n; i++ {
		b.WriteString("\tif c")
		b.WriteString(strings.Repeat("x", i%3))
		b.WriteString(" && d")
		b.WriteString(strings.Repeat("y", i%3))
		b.WriteString(" {\n\t\tswitch v {\n\t\tcase 1:\n\t\tdefault:\n\t\t}\n\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}
