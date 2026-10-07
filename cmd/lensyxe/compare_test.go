package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/internal/compare"
	"github.com/zelvior/lensyxe/pkg/models"
)

func TestCompareRequiresTwoArgs(t *testing.T) {
	if _, _, err := runCLI(t, "compare", "only-one"); err == nil {
		t.Error("expected an error with one argument")
	}
	if _, _, err := runCLI(t, "compare", "a", "b", "c"); err == nil {
		t.Error("expected an error with three arguments")
	}
}

func TestCompareOnNonRepositoryErrors(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runCLI(t, "compare", "HEAD", "HEAD~1", "--compare-root", dir)
	if err == nil {
		t.Fatal("expected an error outside a git repository")
	}
	if !strings.Contains(err.Error(), "git repository") {
		t.Errorf("error = %v, want it to mention the missing repository", err)
	}
}

func TestCompareRejectsUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runCLI(t, "compare", "HEAD", "HEAD~1", "--compare-root", dir, "--format", "xml")
	if err == nil {
		t.Error("expected an error for an unsupported format")
	}
}

// End-to-end compare against a real repository, exercising the archive-export
// path and both renderers.
func TestCompareEndToEnd(t *testing.T) {
	hasGit(t)
	repo := initRepo(t)

	writeIn(t, repo, "main.go", "package main\n\nfunc main() {}\n")
	writeIn(t, repo, "main_test.go", "package main\n\nfunc TestMain(t *testing.T) {}\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "first")

	var big strings.Builder
	big.WriteString("package main\n")
	big.WriteString(branchyBody(60))
	for i := 0; i < 500; i++ {
		big.WriteString("var pad")
		big.WriteString(strings.Repeat("X", i%5))
		big.WriteString(" = 1\n")
	}
	writeIn(t, repo, "big.go", big.String())
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "second")

	headBefore, err := gitOut2(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}

	t.Run("terminal", func(t *testing.T) {
		stdout, _, err := runCLI(t, "compare", "HEAD~1", "HEAD", "--compare-root", repo)
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		for _, want := range []string{
			"lensyxe compare", "HEALTH SCORE", "metric deltas",
			"NEW RISKS", "delta", "Significant regression",
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("compare terminal output missing %q\n%s", want, stdout)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		stdout, _, err := runCLI(t, "compare", "HEAD~1", "HEAD",
			"--compare-root", repo, "--format", "json")
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		var res compare.Result
		if err := json.Unmarshal([]byte(stdout), &res); err != nil {
			t.Fatalf("json: %v\n%s", err, stdout)
		}
		if res.ScoreDelta >= 0 {
			t.Errorf("ScoreDelta = %v, want a negative delta for added complexity", res.ScoreDelta)
		}
		if len(res.Metrics) == 0 {
			t.Error("expected metric deltas")
		}
		if res.SchemaVersion != models.SchemaVersion {
			t.Errorf("schema = %q", res.SchemaVersion)
		}
		// The exported tree for the later revision contains big.go, so it must
		// register as a new risk.
		if len(res.RisksAdded) == 0 {
			t.Errorf("expected new risks, got none (verdict: %s)", res.Verdict)
		}
	})

	t.Run("markdown", func(t *testing.T) {
		stdout, _, err := runCLI(t, "compare", "HEAD~1", "HEAD",
			"--compare-root", repo, "--format", "markdown")
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		if strings.Contains(stdout, "\x1b[") {
			t.Error("compare markdown must not contain ANSI escapes")
		}
		for _, want := range []string{"# Engineering health", "**Delta:", "## Metric deltas"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("compare markdown missing %q", want)
			}
		}
	})

	t.Run("leaves the working tree untouched", func(t *testing.T) {
		headAfter, err := gitOut2(repo, "rev-parse", "HEAD")
		if err != nil {
			t.Fatalf("rev-parse: %v", err)
		}
		if strings.TrimSpace(headAfter) != strings.TrimSpace(headBefore) {
			t.Error("compare moved HEAD")
		}
		status, err := gitOut2(repo, "status", "--porcelain")
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if strings.TrimSpace(status) != "" {
			t.Errorf("compare dirtied the working tree:\n%s", status)
		}
		// big.go must still be present, not checked out away.
		if _, err := os.Stat(filepath.Join(repo, "big.go")); err != nil {
			t.Errorf("compare removed a working-tree file: %v", err)
		}
	})
}
