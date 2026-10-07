package blast

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

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
