package compare

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

// snapshotA is the "before" fixture.
func snapshotA() *models.Snapshot {
	return &models.Snapshot{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "test",
		Root:          "/tmp/tree",
		Health: models.Health{
			Score: 87, Grade: "B", Components: 3,
			Summary: "Overall healthy.",
			Metrics: []models.Metric{
				{Key: "code", Label: "Code health", Score: 88, Weight: 0.4, Applicable: true},
				{Key: "dependency", Label: "Dependency health", Score: 90, Weight: 0.3, Applicable: true},
				{Key: "git", Label: "Maintainability (Git)", Score: 83, Weight: 0.3, Applicable: true},
			},
		},
		Code: models.CodeStats{
			Files: 100, SourceFiles: 80, TestFiles: 20, HasTests: true,
			TestFileRatio: 0.2, TestLineRatio: 0.3,
			CodeLines: 8000, TotalLines: 10000, AverageLines: 80,
			Hotspots: []models.Hotspot{},
			Complexity: models.ComplexitySummary{
				Measured: true, Files: 100, AverageComplexity: 4, MaxComplexity: 9,
				MaxComplexityFile: "a.go", MaxNesting: 4,
				WorstFiles: []models.FileComplexity{},
			},
		},
		Dependencies: models.DependencyStats{
			Detected: true, Locked: true, Direct: 10, Dev: 4, Total: 14, Transitive: 60,
			Ecosystems: []models.EcosystemStats{{Name: "npm", Direct: 10, Total: 14, Transitive: 60}},
		},
		Git: models.GitStats{
			IsRepository: false, WindowDays: 90, WindowCommits: 60, Authors: 4, BusFactor: 2,
			CommitsPerWeek: 8, ChurnConcentration: 0.1,
		},
		Risks: []models.Risk{
			{ID: "score.git", Title: "Maintainability (Git) below expectations",
				Severity: models.SeverityMedium, Category: models.CategoryGit, Impact: 5.1},
			{ID: "code.hotspot.old.go", Title: "Confirmed hotspot",
				Severity: models.SeverityHigh, Category: models.CategoryCode, Impact: 8,
				Subject: "old.go"},
			{ID: "dependency.drift.npm", Title: "Missing lockfile",
				Severity: models.SeverityMedium, Category: models.CategoryDependency, Impact: 3,
				Subject: "package.json"},
		},
	}
}

// snapshotB applies a set of changes to snapshotA for the diff tests.
func snapshotB() *models.Snapshot {
	s := snapshotA()
	s.Health.Score = 84
	s.Health.Grade = "B"
	s.Code.CodeLines = 9200
	s.Code.TotalLines = 11500
	s.Code.AverageLines = 92
	s.Code.Files = 102
	s.Code.SourceFiles = 82
	s.Code.TestFiles = 20
	s.Code.TestFileRatio = 0.2
	s.Code.Hotspots = []models.Hotspot{{Path: "new.go", Lines: 700, Confirmed: true}}
	s.Code.Complexity.MaxComplexity = 14
	s.Code.Complexity.AverageComplexity = 4.6
	s.Code.Complexity.MaxNesting = 6
	s.Dependencies.Direct = 13
	s.Dependencies.Total = 17
	s.Dependencies.Transitive = 95
	s.Git.WindowCommits = 52
	s.Git.Authors = 5
	s.Git.BusFactor = 3
	s.Git.ChurnConcentration = 0.22
	s.Risks = []models.Risk{
		// Unchanged: same severity and impact, so it must not be reported.
		{ID: "dependency.drift.npm", Title: "Missing lockfile",
			Severity: models.SeverityMedium, Category: models.CategoryDependency, Impact: 3,
			Subject: "package.json"},
		// Changed: worsened.
		{ID: "score.git", Title: "Maintainability (Git) below expectations",
			Severity: models.SeverityHigh, Category: models.CategoryGit, Impact: 7.2},
		// Added.
		{ID: "code.hotspot.new.go", Title: "Confirmed hotspot",
			Severity: models.SeverityCritical, Category: models.CategoryCode, Impact: 11,
			Subject: "new.go"},
	}
	return s
}

func TestBuildComputesScoreDeltaAndGrade(t *testing.T) {
	res := build(Config{}, "test", "/repo",
		RevisionInfo{Ref: "v1", ShortSHA: "aaa"},
		RevisionInfo{Ref: "v2", ShortSHA: "bbb"},
		snapshotA(), snapshotB())

	if res.ScoreA != 87 || res.ScoreB != 84 {
		t.Errorf("scores = %v -> %v, want 87 -> 84", res.ScoreA, res.ScoreB)
	}
	if res.ScoreDelta != -3 {
		t.Errorf("ScoreDelta = %v, want -3", res.ScoreDelta)
	}
	if res.GradeChange != "B -> B (-3.0)" {
		t.Errorf("GradeChange = %q", res.GradeChange)
	}
	if !strings.Contains(res.Verdict, "regression") {
		t.Errorf("Verdict = %q, want it to mention a regression", res.Verdict)
	}
}

func TestBuildDiffRisks(t *testing.T) {
	res := build(Config{}, "test", "/repo",
		RevisionInfo{}, RevisionInfo{}, snapshotA(), snapshotB())

	// Added: only in B.
	if len(res.RisksAdded) != 1 || res.RisksAdded[0].ID != "code.hotspot.new.go" {
		t.Errorf("RisksAdded = %s", ids(res.RisksAdded))
	}
	// Resolved: only in A.
	if len(res.RisksResolved) != 1 || res.RisksResolved[0].ID != "code.hotspot.old.go" {
		t.Errorf("RisksResolved = %s", ids(res.RisksResolved))
	}
	// Changed: present in both with different severity or impact.
	if len(res.RisksChanged) != 1 || res.RisksChanged[0].ID != "score.git" {
		t.Fatalf("RisksChanged = %+v", res.RisksChanged)
	}
	c := res.RisksChanged[0]
	if c.Severity != models.SeverityMedium || c.After != models.SeverityHigh {
		t.Errorf("severity = %s -> %s", c.Severity, c.After)
	}
	if c.ImpactA != 5.1 || c.ImpactB != 7.2 || c.Delta != 2.1 {
		t.Errorf("impact = %v -> %v (delta %v)", c.ImpactA, c.ImpactB, c.Delta)
	}
	// Higher impact is worse, so Better must be -1.
	if c.Better != -1 {
		t.Errorf("Better = %d, want -1 for a worsening risk", c.Better)
	}
}

// An identical snapshot on both sides must produce no metric rows and no risk
// changes, only a zero delta.
func TestBuildIdenticalSnapshots(t *testing.T) {
	a, b := snapshotA(), snapshotA()
	res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, a, b)

	if res.ScoreDelta != 0 {
		t.Errorf("ScoreDelta = %v, want 0", res.ScoreDelta)
	}
	if len(res.Metrics) != 0 {
		t.Errorf("expected no metric deltas, got %d", len(res.Metrics))
	}
	if len(res.RisksAdded)+len(res.RisksResolved)+len(res.RisksChanged) != 0 {
		t.Error("expected no risk changes")
	}
	if res.Verdict != "No net change in health score." {
		t.Errorf("Verdict = %q", res.Verdict)
	}
}

// Lower-is-better metrics must be marked as regressions when they increase.
func TestMetricDirectionalSignals(t *testing.T) {
	res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, snapshotA(), snapshotB())

	byLabel := map[string]MetricDelta{}
	for _, m := range res.Metrics {
		byLabel[m.Label] = m
	}

	cases := []struct {
		label      string
		wantBetter int
		wantDelta  float64
	}{
		// More direct dependencies is worse.
		{"Direct deps", -1, 3},
		// Higher average complexity is worse.
		{"Avg complexity", -1, 0.6},
		// Higher churn concentration is worse.
		{"Churn concentration", -1, 0.12},
		// More authors means broader knowledge distribution.
		{"Authors", 1, 1},
		// Volume metrics are reported but never judged: labelling a jump in
		// LOC "improved" would be misleading.
		{"Code lines", 0, 1200},
		{"Source files", 0, 2},
	}
	for _, tc := range cases {
		m, ok := byLabel[tc.label]
		if !ok {
			t.Errorf("metric %q missing from the delta table", tc.label)
			continue
		}
		if m.Better != tc.wantBetter {
			t.Errorf("%s: Better = %d, want %d", tc.label, m.Better, tc.wantBetter)
		}
		if m.Delta != tc.wantDelta {
			t.Errorf("%s: Delta = %v, want %v", tc.label, m.Delta, tc.wantDelta)
		}
		if m.Display == "" {
			t.Errorf("%s: Display must be populated", tc.label)
		}
	}
}

// Unchanged metrics must be omitted: a comparison should surface movement, not
// restate the whole profile.
func TestUnchangedMetricsAreOmitted(t *testing.T) {
	res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, snapshotA(), snapshotB())
	for _, m := range res.Metrics {
		if m.Label == "Test files" { // 20 in both snapshots
			t.Error("unchanged metric 'Test files' should not appear in the delta table")
		}
		if m.Label == "Test file ratio" {
			t.Error("unchanged metric 'Test file ratio' should not appear")
		}
	}
}

// Ordering must be deterministic across repeated builds.
func TestBuildIsDeterministic(t *testing.T) {
	a, b := snapshotA(), snapshotB()
	first := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, a, b)
	for i := 0; i < 25; i++ {
		got := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, a, b)
		if len(got.Metrics) != len(first.Metrics) {
			t.Fatalf("run %d metric count differs", i)
		}
		for j := range got.Metrics {
			if got.Metrics[j].Label != first.Metrics[j].Label {
				t.Fatalf("run %d position %d: %q vs %q", i, j, got.Metrics[j].Label, first.Metrics[j].Label)
			}
		}
		if len(got.RisksAdded) != len(first.RisksAdded) {
			t.Fatalf("run %d added-risk count differs", i)
		}
	}
}

func TestMetricDeltasSortedByMagnitude(t *testing.T) {
	res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, snapshotA(), snapshotB())
	for i := 1; i < len(res.Metrics); i++ {
		prev := abs(res.Metrics[i-1].Delta)
		cur := abs(res.Metrics[i].Delta)
		if prev < cur {
			t.Fatalf("metrics not sorted by magnitude: %v then %v", prev, cur)
		}
	}
}

func TestCountConfirmed(t *testing.T) {
	s := snapshotA()
	if got := countConfirmed(s); got != 0 {
		t.Errorf("countConfirmed = %d, want 0", got)
	}
	s.Code.Hotspots = []models.Hotspot{
		{Path: "a", Confirmed: true},
		{Path: "b", Confirmed: false},
		{Path: "c", Confirmed: true},
	}
	if got := countConfirmed(s); got != 2 {
		t.Errorf("countConfirmed = %d, want 2", got)
	}
}

// Non-nil empty slices keep JSON output stable ("[]" instead of null).
func TestDiffProducesNonNilSlices(t *testing.T) {
	a := snapshotA()
	res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, a, a)
	if res.RisksAdded == nil || res.RisksResolved == nil || res.RisksChanged == nil {
		t.Error("risk diff slices must be non-nil")
	}
	if res.Metrics == nil {
		t.Error("Metrics must be non-nil")
	}
}

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

// SafeJoin must refuse archive entries that escape the destination directory.
func TestSafeJoinRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	good := []string{
		"a.go",
		"internal/b.go",
		"deep/nested/c.txt",
	}
	for _, name := range good {
		if _, err := safeJoin(base, name); err != nil {
			t.Errorf("safeJoin(%q) errored: %v", name, err)
		}
	}
	bad := []string{
		"../escape.txt",
		"../../escape.txt",
		"/absolute.txt",
		"internal/../../escape.txt",
	}
	for _, name := range bad {
		if _, err := safeJoin(base, name); err == nil {
			t.Errorf("safeJoin(%q) should have been rejected", name)
		}
	}
}

func TestShorten(t *testing.T) {
	if got := shorten("0123456789abcdef"); got != "0123456789ab" {
		t.Errorf("shorten = %q", got)
	}
	if got := shorten("abc"); got != "abc" {
		t.Errorf("shorten = %q, want unchanged", got)
	}
}

func TestAbs(t *testing.T) {
	if abs(-3.5) != 3.5 || abs(2.5) != 2.5 {
		t.Error("abs is broken")
	}
}

func TestVerdictBands(t *testing.T) {
	cases := []struct {
		scoreA, scoreB float64
		wantContains   string
	}{
		{90, 80, "Significant regression"},
		{80, 78, "Slight regression"},
		{80, 90, "Significant improvement"},
		{80, 81, "Slight improvement"},
		{80, 80, "No net change"},
	}
	for _, tc := range cases {
		a, b := snapshotA(), snapshotA()
		a.Health.Score = tc.scoreA
		b.Health.Score = tc.scoreB
		res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, a, b)
		if !strings.Contains(res.Verdict, tc.wantContains) {
			t.Errorf("verdict for %v->%v = %q, want it to contain %q",
				tc.scoreA, tc.scoreB, res.Verdict, tc.wantContains)
		}
	}
}

// Temp extraction dirs must be cleaned up so repeated runs do not fill the
// system temp directory.
func TestExportRevisionCreatesAndCleansTree(t *testing.T) {
	if !hasGit(t) {
		t.Skip("git not installed")
	}
	repo := initRepo(t)
	writeIn(t, repo, "a.go", "package a\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "one")

	tree, cleanup, err := exportRevision(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatalf("exportRevision: %v", err)
	}
	if _, err := statFile(filepath.Join(tree, "a.go")); err != nil {
		t.Errorf("expected a.go in the exported tree: %v", err)
	}
	// The tar itself must not remain in the tree.
	if _, err := statFile(filepath.Join(tree, "tree.tar")); err == nil {
		t.Error("the intermediate tar should be removed after extraction")
	}

	cleanup()
	if _, err := statFile(tree); err == nil {
		t.Error("cleanup should have removed the temp tree")
	}
}

// A large archive entry is refused rather than exhausting the temp directory.
func TestExtractTarRejectsOversizedEntry(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "tree.tar")
	writeTar(t, tarPath, tarEntry{name: "big.bin", size: maxArchiveFileBytes + 1})

	err := extractTar(tarPath, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("expected an error for an oversized archive entry")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %v, want it to mention the size cap", err)
	}
}

func TestExtractTarSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "tree.tar")
	writeTar(t, tarPath,
		tarEntry{name: "ok.go", body: "package p\n", mode: 0o644},
		tarEntry{name: "link", linkname: "ok.go", mode: 0o777, symlink: true},
	)
	out := filepath.Join(dir, "out")
	if err := extractTar(tarPath, out); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	if _, err := statFile(filepath.Join(out, "ok.go")); err != nil {
		t.Errorf("regular file should be extracted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(out, "link")); err == nil {
		t.Error("symlinks must be skipped, not created")
	}
}

func TestExtractTarRejectsTraversalEntry(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "tree.tar")
	writeTar(t, tarPath, tarEntry{name: "../escaped.go", body: "package p\n", mode: 0o644})

	err := extractTar(tarPath, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("expected a traversal entry to be rejected")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error = %v, want it to mention the escape", err)
	}
	if _, err := statFile(filepath.Join(dir, "escaped.go")); err == nil {
		t.Error("a file escaped the extraction directory")
	}
}

// ids renders risk IDs for assertion failure messages.
func ids(risks []models.Risk) string {
	if len(risks) == 0 {
		return "(none)"
	}
	out := make([]string, 0, len(risks))
	for _, r := range risks {
		out = append(out, r.ID)
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// branchyBody returns a function body with n branch points.
func branchyBody(n int) string {
	var b strings.Builder
	b.WriteString("func branchy() {\n")
	for i := 0; i < n; i++ {
		b.WriteString(fmt.Sprintf("\tif c%d && d%d {\n\t\tswitch v {\n\t\tcase 1:\n\t\tdefault:\n\t\t}\n\t}\n", i, i))
	}
	b.WriteString("}\n")
	return b.String()
}

// hasGit reports whether git is available, skipping the test if not.
func hasGit(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	return true
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

// statFile is a thin os.Stat wrapper for readability in assertions.
func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }

// tarEntry describes one archive member for writeTar.
type tarEntry struct {
	name     string
	body     string
	size     int64
	mode     int64
	linkname string
	symlink  bool
}

// writeTar builds a tar archive at path containing the given entries.
func writeTar(t *testing.T, path string, entries ...tarEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar: %v", err)
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode}
		switch {
		case e.symlink:
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.linkname
		case e.size > 0:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = e.size
		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Size > 0 {
			// Pad with zeroes so the declared size matches the payload.
			payload := []byte(e.body)
			if int64(len(payload)) < hdr.Size {
				payload = append(payload, make([]byte, hdr.Size-int64(len(payload)))...)
			}
			if _, err := tw.Write(payload); err != nil {
				t.Fatalf("write body: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
}
