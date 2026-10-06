package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

// writeFile writes content to path, creating parent directories.
func writeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestParseNumstatAggregatesPerFile(t *testing.T) {
	out := commitSeparator + "\n" +
		"10\t2\tinternal/a.go\n" +
		"5\t0\tinternal/b.go\n" +
		"-\t-\tassets/logo.png\n" + // binary entry, must be skipped
		commitSeparator + "\n" +
		"1\t1\tinternal/a.go\n"

	entries, added, deleted := parseNumstat(out, commitSeparator)
	if added != 16 || deleted != 3 {
		t.Fatalf("added=%d deleted=%d, want 16/3", added, deleted)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (binary skipped)", len(entries))
	}
	byPath := map[string]models.ChurnEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	a := byPath["internal/a.go"]
	if a.Commits != 2 || a.Added != 11 || a.Deleted != 3 {
		t.Errorf("internal/a.go = %+v, want 2 commits, +11/-3", a)
	}
	if a.Score != a.Commits+a.Added+a.Deleted {
		t.Errorf("Score = %d, inconsistent with components", a.Score)
	}
}

func TestParseNumstatEmptyInput(t *testing.T) {
	entries, added, deleted := parseNumstat("", commitSeparator)
	if len(entries) != 0 || added != 0 || deleted != 0 {
		t.Fatalf("expected zero result, got %d entries +%d/-%d", len(entries), added, deleted)
	}
}

func TestTopChurnOrdersDeterministically(t *testing.T) {
	in := []models.ChurnEntry{
		{Path: "b.go", Score: 10, Added: 5},
		{Path: "a.go", Score: 10, Added: 5},
		{Path: "c.go", Score: 99},
	}
	got := topChurn(in, 10)
	if got[0].Path != "c.go" {
		t.Errorf("first = %q, want c.go (highest score)", got[0].Path)
	}
	// Equal scores must break ties by path ascending.
	if got[1].Path != "a.go" || got[2].Path != "b.go" {
		t.Errorf("tie order = %q,%q, want a.go,b.go", got[1].Path, got[2].Path)
	}
	if limited := topChurn(in, 1); len(limited) != 1 {
		t.Errorf("limit not applied: %d rows", len(limited))
	}
}

func TestChurnConcentration(t *testing.T) {
	even := []models.ChurnEntry{
		{Path: "a", Score: 10}, {Path: "b", Score: 10},
		{Path: "c", Score: 10}, {Path: "d", Score: 10},
	}
	concentrated := []models.ChurnEntry{
		{Path: "a", Score: 97}, {Path: "b", Score: 1},
		{Path: "c", Score: 1}, {Path: "d", Score: 1},
	}

	if got := churnConcentration(even); got > 0.001 {
		t.Errorf("even distribution = %v, want ~0", got)
	}
	if got := churnConcentration(concentrated); got < 0.5 {
		t.Errorf("concentrated distribution = %v, want a high value", got)
	}
	if got := churnConcentration(nil); got != 0 {
		t.Errorf("nil = %v, want 0", got)
	}
	if got := churnConcentration([]models.ChurnEntry{{Path: "solo", Score: 5}}); got != 1 {
		t.Errorf("single file = %v, want 1", got)
	}
}

func TestCountAuthorsAndBusFactor(t *testing.T) {
	out := "ada\nada\nada\ngrace\n"
	counts := countAuthors(out)
	if len(counts) != 2 {
		t.Fatalf("authors = %d, want 2", len(counts))
	}
	if got := topShare(counts); got != 0.75 {
		t.Errorf("topShare = %v, want 0.75", got)
	}
	if got := busFactor(counts, 0.2); got != 2 {
		t.Errorf("busFactor = %d, want 2 (both authors exceed 20%%)", got)
	}
	if got := busFactor(map[string]int{"ada": 100}, 0.2); got != 1 {
		t.Errorf("busFactor = %d, want 1", got)
	}
	if got := busFactor(nil, 0.2); got != 0 {
		t.Errorf("busFactor(nil) = %d, want 0", got)
	}
}

// Analyze must degrade gracefully on a plain directory rather than panicking
// or hanging.
func TestAnalyzeNonRepository(t *testing.T) {
	dir := t.TempDir()
	res, err := Analyze(context.Background(), dir, DefaultConfig())
	if err == nil {
		t.Fatal("expected ErrNoRepository for a plain directory")
	}
	if res.Stats.IsRepository {
		t.Error("IsRepository should stay false")
	}
	if res.Stats.Note == "" {
		t.Error("expected a note explaining the degradation")
	}
}

// End-to-end against a real repository created in a temp dir. Skipped when git
// is unavailable so the suite stays portable.
func TestAnalyzeRealRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
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

	run("init", "-q", ".")
	run("config", "user.name", "Tester")
	run("config", "user.email", "tester@example.com")
	// Two commits, both today, so the window metrics are populated.
	for i, body := range []string{"package p\nfunc a() {}\n", "package p\nfunc a() {}\nfunc b() {}\n"} {
		name := filepath.Join(dir, "main.go")
		if err := writeFile(name, body); err != nil {
			t.Fatalf("write: %v", err)
		}
		run("add", ".")
		run("commit", "-q", "-m", string(rune('a'+i)))
	}

	cfg := DefaultConfig()
	cfg.Timeout = 30 * time.Second
	res, err := Analyze(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !res.Stats.IsRepository {
		t.Fatal("IsRepository = false in a real repository")
	}
	if res.Stats.TotalCommits != 2 {
		t.Errorf("TotalCommits = %d, want 2", res.Stats.TotalCommits)
	}
	if res.Stats.Authors != 1 {
		t.Errorf("Authors = %d, want 1", res.Stats.Authors)
	}
	if res.Stats.BusFactor != 1 {
		t.Errorf("BusFactor = %d, want 1", res.Stats.BusFactor)
	}
	if res.Stats.HeadCommit == "" {
		t.Error("HeadCommit should be populated")
	}
	if len(res.Stats.HeadCommit) > 12 {
		t.Errorf("HeadCommit = %q, want a short SHA", res.Stats.HeadCommit)
	}
	if res.Stats.ChurnFiles == 0 {
		t.Error("expected churn entries for a repository with commits")
	}
	if res.Stats.DaysSinceCommit != 0 {
		t.Errorf("DaysSinceCommit = %d, want 0 for a commit made now", res.Stats.DaysSinceCommit)
	}
	if res.Stats.LastCommitAt.IsZero() {
		t.Error("LastCommitAt should be parsed")
	}
	if res.Stats.Note != "" {
		t.Logf("note: %s", res.Stats.Note)
	}
}

// An empty repository (no commits) must not fail the scan.
func TestAnalyzeRepositoryWithoutCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init failed (%v): %s", err, out)
	}

	res, err := Analyze(context.Background(), dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Stats.TotalCommits != 0 {
		t.Errorf("TotalCommits = %d, want 0", res.Stats.TotalCommits)
	}
	if len(res.Findings) == 0 {
		t.Error("expected a finding noting the absence of commits")
	}
}

func TestShortenSHAAndAtoi(t *testing.T) {
	if got := shortenSHA("0123456789abcdef0123"); got != "0123456789ab" {
		t.Errorf("shortenSHA = %q", got)
	}
	if got := shortenSHA("abc"); got != "abc" {
		t.Errorf("shortenSHA = %q, want unchanged", got)
	}
	if got := atoi(" 42\n"); got != 42 {
		t.Errorf("atoi = %d", got)
	}
	if got := atoi("not-a-number"); got != 0 {
		t.Errorf("atoi = %d, want 0 on parse failure", got)
	}
}

func TestNormalizePath(t *testing.T) {
	if got := normalizePath("internal\\a.go"); got != "internal/a.go" {
		t.Errorf("normalizePath = %q, want forward slashes", got)
	}
	if got := normalizePath("   "); got != "" {
		t.Errorf("normalizePath = %q, want empty", got)
	}
}

func TestRunSurfacesStderr(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	_, err := run(context.Background(), t.TempDir(), 10*time.Second, "rev-parse", "--verify", "nope")
	if err == nil {
		t.Fatal("expected an error for an unknown ref")
	}
	if !strings.Contains(err.Error(), "git rev-parse") {
		t.Errorf("error should name the failing command, got %v", err)
	}
}
