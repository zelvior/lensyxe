package blast

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// analyzeSpec runs the whole computation against a hand-built commit set.
//
// Testing through this rather than through git means the coupling arithmetic
// can be checked against cases whose answers are known by hand, with no
// repository, no clock, and no subprocess involved.
func analyzeSpec(t *testing.T, cfg Config, changeset []string, specs ...[]string) Result {
	t.Helper()
	return analyze("/repo", cfg, commits(specs...), changeset)
}

// commits builds a synthetic commit set.
func commits(specs ...[]string) []commitFiles {
	out := make([]commitFiles, len(specs))
	for i, files := range specs {
		out[i] = commitFiles{sha: fmt.Sprintf("sha%d", i), files: files}
	}
	return out
}

func findPair(got Result, a, b string) *Pair {
	for i := range got.Pairs {
		p := got.Pairs[i]
		if (p.A == a && p.B == b) || (p.A == b && p.B == a) {
			return &got.Pairs[i]
		}
	}
	return nil
}

func TestCouplingIsSharedOverTheRarerFile(t *testing.T) {
	// a and b always change together; c changes on its own twice.
	got := analyzeSpec(t, DefaultConfig(), []string{"a"},
		[]string{"a", "b"},
		[]string{"a", "b"},
		[]string{"a", "b"},
		[]string{"c"},
		[]string{"c"},
	)

	ab := findPair(got, "a", "b")
	if ab == nil {
		t.Fatalf("the a/b pair was not reported: %+v", got.Pairs)
	}
	if ab.SharedCommits != 3 {
		t.Errorf("shared_commits = %d, want 3", ab.SharedCommits)
	}
	if ab.CommitsA != 3 || ab.CommitsB != 3 {
		t.Errorf("per-file counts = %d/%d, want 3/3", ab.CommitsA, ab.CommitsB)
	}
	if ab.Coupling != 1 {
		t.Errorf("coupling = %v, want 1", ab.Coupling)
	}

	// Files sharing no commits must not be coupled.
	for _, name := range []string{"a", "b"} {
		if p := findPair(got, name, "c"); p != nil {
			t.Errorf("%s and c share no commits but were coupled: %+v", name, p)
		}
	}
}

// The rare-file denominator is the point: if one file changes every commit and
// the other changes once, they share one commit, which is 1.0 coupling.
func TestCouplingNormalisesByTheRarerFile(t *testing.T) {
	cfg := DefaultConfig()
	// The evidence floor is lowered so a single shared commit is admitted; this
	// test is about the denominator, not the sample size.
	cfg.MinSharedCommits = 1

	got := analyzeSpec(t, cfg, []string{"common"},
		[]string{"common", "rare"},
		[]string{"common"},
		[]string{"common"},
		[]string{"common"},
	)

	p := findPair(got, "common", "rare")
	if p == nil {
		t.Fatalf("the pair was not reported: %+v", got.Pairs)
	}
	if p.Coupling != 1 {
		t.Errorf("coupling = %v, want 1: every time rare changed, common did too", p.Coupling)
	}
	if p.CommitsA != 4 || p.CommitsB != 1 {
		t.Errorf("counts = %d/%d, want 4/1", p.CommitsA, p.CommitsB)
	}
}

// The floor on shared commits is what stops a young repository from reporting
// every pair as perfectly coupled.
func TestPairsBelowTheEvidenceFloorAreDropped(t *testing.T) {
	got := analyzeSpec(t, DefaultConfig(), []string{"a"},
		[]string{"a", "b"},
		[]string{"c", "d"}, // only one shared commit
	)
	if len(got.Pairs) != 0 {
		t.Errorf("a pair seen in one commit was reported: %+v", got.Pairs)
	}
}

// Weak coupling is dropped rather than reported with a caveat.
//
// The denominator is min(commitsA, commitsB), so coupling only falls below 1
// when the pair misses at least one commit that the rarer file was in. Here y
// changes 4 times, only 2 of them alongside x, so coupling is 2/4 = 0.5.
func TestWeakCouplingIsDropped(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2
	cfg.CouplingThreshold = 0.9

	got := analyzeSpec(t, cfg, []string{"x"},
		[]string{"x", "y"}, // 1 shared
		[]string{"x", "y"}, // 2 shared
		[]string{"y"},      // y alone
		[]string{"y"},      // y alone
		[]string{"x"},      // x alone
	)
	if p := findPair(got, "x", "y"); p != nil {
		t.Errorf("coupling %v was below the threshold but reported: %+v", p.Coupling, p)
	}
}

// The min-denominator means full coupling whenever the rarer file never changes
// alone. This is the intended behaviour, not an off-by-one, and it is what makes
// the measure readable: 1.0 means "every time this file changed, the other one
// did too".
func TestRarerFileNeverChangingAloneGivesFullCoupling(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 1

	got := analyzeSpec(t, cfg, []string{"x"},
		[]string{"x", "y"},
		[]string{"x"},
		[]string{"x"},
		[]string{"x", "y"},
	)
	p := findPair(got, "x", "y")
	if p == nil {
		t.Fatalf("pair not reported: %+v", got.Pairs)
	}
	if p.CommitsB != 2 {
		t.Fatalf("y changed %d times, want 2", p.CommitsB)
	}
	if p.Coupling != 1 {
		t.Errorf("coupling = %v, want 1", p.Coupling)
	}
}

// A file listed twice in one commit counts once.
func TestDuplicatePathsInACommitCountOnce(t *testing.T) {
	got := analyzeSpec(t, DefaultConfig(), []string{"a"},
		[]string{"a", "b", "b"},
		[]string{"a", "b"},
		[]string{"a", "b"},
	)
	for _, p := range got.Pairs {
		if p.A == p.B {
			t.Errorf("a file was paired with itself: %+v", p)
		}
	}
	if p := findPair(got, "a", "b"); p == nil || p.SharedCommits != 3 {
		t.Errorf("shared_commits = %+v, want 3", p)
	}
}

// Coupling must stay within 0..1 or a score above 100 would be meaningless.
func TestCouplingIsClampedToOne(t *testing.T) {
	got := analyzeSpec(t, DefaultConfig(), []string{"a"},
		[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"},
	)
	for _, p := range got.Pairs {
		if p.Coupling < 0 || p.Coupling > 1 {
			t.Errorf("coupling %v is out of range: %+v", p.Coupling, p)
		}
	}
}

// Deterministic output: identical history must give identical bytes, or the
// tool cannot be diffed between runs.
func TestOutputIsDeterministic(t *testing.T) {
	specs := [][]string{
		{"a", "b"}, {"b", "c"}, {"a", "c"}, {"d", "e"}, {"a", "b", "c"},
	}
	first := formatResult(analyzeSpec(t, DefaultConfig(), []string{"a"}, specs...))
	for i := 0; i < 20; i++ {
		again := formatResult(analyzeSpec(t, DefaultConfig(), []string{"a"}, specs...))
		if again != first {
			t.Fatalf("run %d differs:\n%s\n---\n%s", i, first, again)
		}
	}
}

// Seeds must be the changeset's files. An earlier version seeded from every
// coupled file, which made each one zero-hop and therefore excluded, so the
// prediction list was always empty.
func TestExpandsFromTheChangesetNotFromEveryCoupledFile(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2

	got := analyzeSpec(t, cfg, []string{"a"},
		[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"},
	)

	if len(got.Predictions) != 1 {
		t.Fatalf("got %d predictions, want exactly 1: %+v",
			len(got.Predictions), got.Predictions)
	}
	p := got.Predictions[0]
	if p.Path != "b" {
		t.Errorf("predicted %q, want b", p.Path)
	}
	if p.Hops != 1 {
		t.Errorf("hops = %d, want 1", p.Hops)
	}
	if p.Via != "a" {
		t.Errorf("via = %q, want a", p.Via)
	}
	if p.InChangeset {
		t.Error("b was marked as part of the changeset")
	}
}

// A changeset file must never be predicted as something to look at.
func TestChangesetFilesAreNeverPredicted(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2
	got := analyzeSpec(t, cfg, []string{"a", "b"},
		[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"},
	)
	for _, p := range got.Predictions {
		if p.Path == "a" || p.Path == "b" {
			t.Errorf("changeset file %q was predicted: %+v", p.Path, p)
		}
	}
}

// An empty changeset predicts nothing rather than everything.
func TestEmptyChangesetPredictsNothing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2
	got := analyzeSpec(t, cfg, nil,
		[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"},
	)
	if len(got.Predictions) != 0 {
		t.Errorf("an empty changeset produced predictions: %+v", got.Predictions)
	}
}

// A one-hop prediction outranks a two-hop one, so "most likely first" means
// something.
func TestDirectCouplingOutranksIndirect(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2

	got := analyzeSpec(t, cfg, []string{"seed"},
		[]string{"seed", "near"}, []string{"seed", "near"}, []string{"seed", "near"},
		[]string{"near", "far"}, []string{"near", "far"}, []string{"near", "far"},
	)

	var near, far *Prediction
	for i := range got.Predictions {
		switch got.Predictions[i].Path {
		case "near":
			near = &got.Predictions[i]
		case "far":
			far = &got.Predictions[i]
		}
	}
	if near == nil || far == nil {
		t.Fatalf("missing predictions: %+v", got.Predictions)
	}
	if near.Hops >= far.Hops {
		t.Errorf("near is %d hops and far is %d; the closer file must come first",
			near.Hops, far.Hops)
	}
	if far.Via == "" {
		t.Error("an indirect prediction did not record the file it came through")
	}
}

// MaxDistance bounds the walk.
func TestMaxDistanceBoundsTheWalk(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2
	cfg.MaxDistance = 1

	got := analyzeSpec(t, cfg, []string{"a"},
		[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"},
		[]string{"b", "c"}, []string{"b", "c"}, []string{"b", "c"},
	)
	for _, p := range got.Predictions {
		if p.Path == "c" {
			t.Errorf("c is two hops away but was reported at MaxDistance 1: %+v", p)
		}
	}
}

// Insufficient history must be stated, not papered over.
func TestHistoryVerdictGatesOnCommitCount(t *testing.T) {
	cfg := DefaultConfig() // MinCommits 10
	cases := []struct {
		commits int
		want    bool // expect a verdict
	}{
		{0, true},
		{1, true},
		{4, true},
		{9, true},
		{10, false},
		{50, false},
	}
	for _, c := range cases {
		got := historyVerdict(c.commits, cfg)
		if (got != "") != c.want {
			t.Errorf("historyVerdict(%d) = %q, want verdict=%v", c.commits, got, c.want)
		}
	}
}

// The verdict must name the threshold so a reader knows what would change it.
func TestHistoryVerdictNamesTheRequiredDepth(t *testing.T) {
	v := historyVerdict(4, DefaultConfig())
	if !strings.Contains(v, "4 commit") {
		t.Errorf("the verdict does not state the commit count: %q", v)
	}
	if !strings.Contains(v, "10") {
		t.Errorf("the verdict does not name the required depth: %q", v)
	}
	// It must be explicit that this is a data limit, not a finding.
	if !strings.Contains(v, "limit of the data") {
		t.Errorf("the verdict does not distinguish a data limit from a finding: %q", v)
	}
}

// A verdict must name the threshold and the reason.
func TestHistoryVerdictExplainsTheThreshold(t *testing.T) {
	v := historyVerdict(1, DefaultConfig())
	if !strings.Contains(v, "1 commit") {
		t.Errorf("the verdict does not state the commit count: %q", v)
	}
	if !strings.Contains(v, "coupling") {
		t.Errorf("the verdict does not explain what is missing: %q", v)
	}
}

func TestDedupeSorted(t *testing.T) {
	got := dedupeSorted([]string{"b", "a", "b", "", "c"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if dedupeSorted(nil) != nil {
		t.Error("dedupeSorted(nil) should be nil, not an empty slice")
	}
}

// A path containing a space must survive, which is the reason the log format is
// NUL-delimited rather than line-delimited.
func TestParseLogHandlesSpacesInPaths(t *testing.T) {
	raw := "\x00sha1\x002026-01-01T00:00:00Z\nmy file.go\nother.go\n" +
		"\x00sha2\x002026-01-02T00:00:00Z\nmy file.go\n"
	got, first, last := parseLogNUL([]byte(raw))
	if len(got) != 2 {
		t.Fatalf("got %d commits, want 2: %+v", len(got), got)
	}
	if len(got[0].files) != 2 || got[0].files[0] != "my file.go" {
		t.Errorf("a path with a space was split or lost: %+v", got[0].files)
	}
	if got[1].sha != "sha2" {
		t.Errorf("second sha = %q, want sha2", got[1].sha)
	}
	if first.IsZero() || last.IsZero() {
		t.Errorf("timestamps not parsed: %v %v", first, last)
	}
	if !first.Before(last) {
		t.Errorf("first %v is not before last %v", first, last)
	}
}

// A commit with no files carries no co-change evidence and must be skipped, not
// stored as an empty record that would divide every denominator.
func TestParseLogSkipsFileLessCommits(t *testing.T) {
	raw := "\x00sha1\x002026-01-01T00:00:00Z\n" +
		"\x00sha2\x002026-01-02T00:00:00Z\na.go\n"
	got, _, _ := parseLogNUL([]byte(raw))
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1: %+v", len(got), got)
	}
	if got[0].sha != "sha2" {
		t.Errorf("kept %q, want sha2", got[0].sha)
	}
}

// A malformed stream must not panic or invent a commit.
func TestParseLogSurvivesGarbage(t *testing.T) {
	for _, raw := range []string{
		"", "\x00", "\x00\x00", "\x00\x00\x00", "no separators at all",
		"\x00", "\x00sha", "\x00sha\x00", "\x00sha\x00no-newline",
		"\x00\x002026-01-01T00:00:00Z\n",
	} {
		got, _, _ := parseLogNUL([]byte(raw))
		for _, c := range got {
			if c.sha == "" {
				t.Errorf("input %q produced a commit with no sha", raw)
			}
			if len(c.files) == 0 {
				t.Errorf("input %q produced a commit with no files", raw)
			}
		}
	}
}

// A directory that is not a repository is a state to report, not a failure.
func TestAnalyzeOutsideARepository(t *testing.T) {
	res, err := Analyze(context.Background(), t.TempDir(),
		[]string{"a.go"}, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze on a non-repository returned an error: %v", err)
	}
	if res.Commits != 0 {
		t.Errorf("a non-repository reported %d commits", res.Commits)
	}
	if res.InsufficientHistory == "" {
		t.Error("a non-repository gave no explanation")
	}
	if len(res.Predictions) != 0 {
		t.Errorf("a non-repository produced predictions: %+v", res.Predictions)
	}
}

// Against a real repository, the history floor must hold: four commits is not
// enough, so no coupling is reported.
func TestAnalyzeAgainstARealRepositoryGatesOnHistory(t *testing.T) {
	dir := newRepoWithHistory(t, 4)

	res, err := Analyze(context.Background(), dir,
		[]string{"a.go", "b.go"}, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Commits != 4 {
		t.Fatalf("read %d commits, want 4", res.Commits)
	}
	if res.LastCommitAt.IsZero() || res.FirstCommitAt.IsZero() {
		t.Errorf("commit bounds not populated: %v %v", res.FirstCommitAt, res.LastCommitAt)
	}
	if res.InsufficientHistory == "" {
		t.Error("a four-commit repository produced a coupling verdict, " +
			"but four commits is below the ten needed")
	}
	if len(res.Predictions) != 0 {
		t.Errorf("predictions came from insufficient history: %+v", res.Predictions)
	}
}

// Enough history must actually produce predictions.
func TestAnalyzeProducesPredictionsWithEnoughHistory(t *testing.T) {
	dir := newRepoWithHistory(t, 12)

	res, err := Analyze(context.Background(), dir, []string{"a.go"}, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.InsufficientHistory != "" {
		t.Fatalf("twelve commits were rejected as insufficient: %q", res.InsufficientHistory)
	}
	if len(res.Predictions) == 0 {
		t.Fatal("twelve co-changing commits produced no predictions")
	}
	if res.Predictions[0].Path != "b.go" {
		t.Errorf("top prediction = %q, want b.go: %+v",
			res.Predictions[0].Path, res.Predictions)
	}
	if res.Predictions[0].Hops != 1 {
		t.Errorf("top prediction is %d hops away, want 1", res.Predictions[0].Hops)
	}
}

// A file that has never been committed is reported, not silently dropped.
func TestAnalyzeReportsFilesWithNoHistory(t *testing.T) {
	dir := newRepoWithHistory(t, 12)

	res, err := Analyze(context.Background(), dir,
		[]string{"a.go", "brand-new.go"}, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Missing) != 1 || res.Missing[0] != "brand-new.go" {
		t.Errorf("missing = %v, want [brand-new.go]", res.Missing)
	}
	for _, p := range res.Predictions {
		if p.Path == "brand-new.go" {
			t.Error("a never-committed file was predicted as coupled")
		}
	}
}

// The limit must be honoured.
func TestPredictionsRespectTheLimit(t *testing.T) {
	dir := t.TempDir()
	initRepo(t, dir)
	names := []string{"f1.go", "f2.go", "f3.go", "f4.go", "f5.go"}
	for i := 0; i < 12; i++ {
		for _, n := range names {
			write(t, dir, n, fmt.Sprintf("package x\n\nfunc F%d() {}\n", i))
		}
		write(t, dir, "seed.go", fmt.Sprintf("package s\n\nfunc S%d() {}\n", i))
		commit(t, dir, fmt.Sprintf("c%d", i))
	}

	cfg := DefaultConfig()
	cfg.Limit = 2
	res, err := Analyze(context.Background(), dir, []string{"seed.go"}, cfg)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Predictions) > 2 {
		t.Errorf("got %d predictions with Limit 2: %+v",
			len(res.Predictions), res.Predictions)
	}
}

// Pending changes must include untracked files: a brand new file is the case
// with no history at all, and dropping it would drop the file most likely to
// need a second pair of eyes.
func TestPendingChangesIncludesUntrackedFiles(t *testing.T) {
	dir := newRepoWithHistory(t, 3)

	// Modify a tracked file and create a new one.
	write(t, dir, "a.go", "package a\n\nfunc A999() {}\n")
	write(t, dir, "brand-new.go", "package a\n\nfunc New() {}\n")

	got, err := PendingChanges(context.Background(), dir, 30*time.Second)
	if err != nil {
		t.Fatalf("PendingChanges: %v", err)
	}
	want := map[string]bool{"a.go": false, "brand-new.go": false}
	for _, f := range got {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for f, found := range want {
		if !found {
			t.Errorf("%q missing from the pending changeset: %v", f, got)
		}
	}
}

// Staged and unstaged tracked changes must both appear. Comparing against the
// index alone would miss unstaged work, which is most of what is in progress.
func TestPendingChangesIncludesUnstagedChanges(t *testing.T) {
	dir := newRepoWithHistory(t, 3)

	write(t, dir, "a.go", "package a\n\nfunc Changed() {}\n")
	got, err := PendingChanges(context.Background(), dir, 30*time.Second)
	if err != nil {
		t.Fatalf("PendingChanges: %v", err)
	}
	found := false
	for _, f := range got {
		if f == "a.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("an unstaged change was not detected: %v", got)
	}
}

// A clean tree is empty, not an error.
func TestPendingChangesOnACleanTree(t *testing.T) {
	dir := newRepoWithHistory(t, 3)
	got, err := PendingChanges(context.Background(), dir, 30*time.Second)
	if err != nil {
		t.Fatalf("PendingChanges on a clean tree: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a clean tree reported changes: %v", got)
	}
}

// Outside a repository there is no changeset to report, and that is not an
// error worth failing on.
func TestPendingChangesOutsideARepository(t *testing.T) {
	got, err := PendingChanges(context.Background(), t.TempDir(), 30*time.Second)
	if err != nil {
		t.Fatalf("PendingChanges outside a repository: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a non-repository reported changes: %v", got)
	}
}

// Ignored files must not be reported; they are not part of a changeset.
func TestPendingChangesRespectsGitignore(t *testing.T) {
	dir := newRepoWithHistory(t, 3)
	write(t, dir, ".gitignore", "*.log\n")
	commit(t, dir, "ignore")

	write(t, dir, "a.go", "package a\n\nfunc Changed() {}\n")
	write(t, dir, "noise.log", "should not appear\n")

	got, err := PendingChanges(context.Background(), dir, 30*time.Second)
	if err != nil {
		t.Fatalf("PendingChanges: %v", err)
	}
	for _, f := range got {
		if strings.HasSuffix(f, ".log") {
			t.Errorf("a gitignored file was reported as a change: %v", got)
		}
	}
}

// ---- git test helpers ----

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
