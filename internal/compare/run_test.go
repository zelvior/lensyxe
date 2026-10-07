package compare

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

func TestRunRejectsMissingArgs(t *testing.T) {
	noop := func(context.Context, string) (*models.Snapshot, error) { return nil, nil }
	if _, err := Run(context.Background(), Config{A: "only-one", Analyzer: noop}, "test"); err == nil {
		t.Error("expected an error when revB is missing")
	}
	if _, err := Run(context.Background(), Config{A: "a", B: "b"}, "test"); err == nil {
		t.Error("expected an error when no analyzer is configured")
	}
}

func TestRunRejectsSameCommit(t *testing.T) {
	if !hasGit(t) {
		t.Skip("git not installed")
	}
	repo := initRepo(t)
	writeIn(t, repo, "a.go", "package a\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "one")

	noop := func(context.Context, string) (*models.Snapshot, error) { return nil, nil }
	cfg := Config{Root: repo, A: "HEAD", B: "HEAD", Analyzer: noop}
	if _, err := Run(context.Background(), cfg, "test"); err == nil {
		t.Fatal("expected an error when both refs resolve to the same commit")
	}
}

func TestRunNonRepository(t *testing.T) {
	noop := func(context.Context, string) (*models.Snapshot, error) { return nil, nil }
	cfg := Config{Root: t.TempDir(), A: "HEAD", B: "HEAD~1", Analyzer: noop}
	_, err := Run(context.Background(), cfg, "test")
	if err == nil {
		t.Fatal("expected ErrNotRepository")
	}
	if !errors.Is(err, ErrNotRepository) {
		t.Errorf("error = %v, want ErrNotRepository", err)
	}
}

func TestRunRejectsUnresolvableRef(t *testing.T) {
	if !hasGit(t) {
		t.Skip("git not installed")
	}
	repo := initRepo(t)
	writeIn(t, repo, "a.go", "package a\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "one")

	noop := func(context.Context, string) (*models.Snapshot, error) { return nil, nil }
	cfg := Config{Root: repo, A: "no-such-ref", B: "HEAD", Analyzer: noop}
	if _, err := Run(context.Background(), cfg, "test"); err == nil {
		t.Fatal("expected an error for an unknown ref")
	}
}

// End-to-end against a real repository: two revisions with a real regression
// between them. This is the integration test that proves the read-only export
// path works.
func TestRunEndToEnd(t *testing.T) {
	if !hasGit(t) {
		t.Skip("git not installed")
	}
	repo := initRepo(t)

	// Revision one: small, tested, locked.
	writeIn(t, repo, "go.mod", "module demo\n\ngo 1.22\n")
	writeIn(t, repo, "go.sum", "example.com/dep v1.0.0 h1:x=\n")
	writeIn(t, repo, "main.go", "package main\n\nfunc main() {}\n")
	writeIn(t, repo, "main_test.go", "package main\n\nfunc TestMain(t *testing.T) {}\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "first")

	// Revision two: a large complex file and no tests.
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

	// Analyze each exported tree with a stub that inspects the filesystem, so
	// the test exercises the export path rather than re-analyzing the repo.
	analyze := func(_ context.Context, treePath string) (*models.Snapshot, error) {
		snap := &models.Snapshot{
			SchemaVersion: models.SchemaVersion,
			Tool:          "lensyxe",
			Version:       "test",
			Root:          treePath,
			Health:        models.Health{Score: 50, Grade: "F", Components: 1},
			Code:          models.CodeStats{},
		}
		if _, err := statFile(filepath.Join(treePath, "big.go")); err == nil {
			snap.Code.CodeLines = 700
			snap.Health.Score = 62
			snap.Health.Metrics = []models.Metric{
				{Key: "code", Label: "Code health", Score: 62, Weight: 1, Applicable: true},
			}
			snap.Risks = []models.Risk{
				{ID: "code.hotspot.big.go", Title: "Confirmed hotspot",
					Severity: models.SeverityHigh, Category: models.CategoryCode,
					Impact: 7, Subject: "big.go"},
			}
		} else {
			snap.Code.CodeLines = 40
			snap.Health.Score = 88
			snap.Health.Metrics = []models.Metric{
				{Key: "code", Label: "Code health", Score: 88, Weight: 1, Applicable: true},
			}
		}
		return snap, nil
	}

	cfg := Config{Root: repo, A: "HEAD~1", B: "HEAD", Analyzer: analyze, Timeout: 60}
	res, err := Run(context.Background(), cfg, "test")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ScoreA != 88 || res.ScoreB != 62 {
		t.Errorf("scores = %v -> %v, want 88 -> 62", res.ScoreA, res.ScoreB)
	}
	if res.ScoreDelta != -26 {
		t.Errorf("ScoreDelta = %v, want -26", res.ScoreDelta)
	}
	if len(res.RisksAdded) != 1 || res.RisksAdded[0].ID != "code.hotspot.big.go" {
		t.Errorf("RisksAdded = %s", ids(res.RisksAdded))
	}
	if res.A.Commit == res.B.Commit {
		t.Error("the two revisions resolved to the same commit")
	}
	if res.A.ShortSHA == "" || res.B.ShortSHA == "" {
		t.Error("both revisions should have a short SHA")
	}

	// The repository must be left untouched: same HEAD, clean working tree.
	head, err := gitOut2(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	if strings.TrimSpace(head) != res.B.Commit {
		t.Errorf("HEAD moved during compare: %s vs %s", strings.TrimSpace(head), res.B.Commit)
	}
	status, err := gitOut2(repo, "status", "--porcelain")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if strings.TrimSpace(status) != "" {
		t.Errorf("working tree was dirtied by compare:\n%s", status)
	}
	// main_test.go must still exist in the working tree.
	if _, err := statFile(filepath.Join(repo, "main_test.go")); err != nil {
		t.Errorf("compare removed a working-tree file: %v", err)
	}
}
