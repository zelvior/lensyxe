package decay

import (
	"context"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

func daysAgo(n int) time.Time { return now.AddDate(0, 0, -n) }

// touch builds one commit.
func touch(days int, author string, files ...string) commitTouch {
	return commitTouch{date: daysAgo(days), author: author, files: files}
}

func defaultCfg() Config {
	cfg := DefaultConfig()
	cfg.WindowDays = 90
	return cfg
}

func TestOrphansAreFilesUntouchedInTheWindow(t *testing.T) {
	res := analyze("/repo", defaultCfg(), []commitTouch{
		touch(200, "a", "old.go"),
		touch(10, "a", "busy.go"),
	}, now)

	// Window is 90 days, so old.go at 200 days is an orphan and busy.go is not.
	if len(res.Orphans) != 1 || res.Orphans[0].Path != "old.go" {
		t.Fatalf("orphans = %+v, want only old.go", res.Orphans)
	}
	if res.Orphans[0].AgeDays != 200 {
		t.Errorf("age = %d, want 200", res.Orphans[0].AgeDays)
	}
	if !res.Orphans[0].Tracked {
		t.Error("an orphan was not marked as tracked")
	}
}

// Orphans must be oldest first: that is the order worth acting in.
func TestOrphansAreOldestFirst(t *testing.T) {
	res := analyze("/repo", defaultCfg(), []commitTouch{
		touch(200, "a", "b.go"),
		touch(400, "a", "a.go"),
		touch(300, "a", "c.go"),
	}, now)
	if len(res.Orphans) != 3 {
		t.Fatalf("orphans = %+v, want 3", res.Orphans)
	}
	for i := 1; i < len(res.Orphans); i++ {
		if res.Orphans[i-1].AgeDays < res.Orphans[i].AgeDays {
			t.Errorf("orphans are not oldest first: %+v", res.Orphans)
			break
		}
	}
}

func TestSilosRequireADominantContributor(t *testing.T) {
	res := analyze("/repo", defaultCfg(), []commitTouch{
		touch(200, "ann", "silo.go"),
		touch(190, "ann", "silo.go"),
		touch(180, "ann", "silo.go"),
		// shared.go has three contributors, so no single one dominates.
		touch(200, "ann", "shared.go"),
		touch(190, "bob", "shared.go"),
		touch(180, "cy", "shared.go"),
	}, now)

	if len(res.Silos) != 1 {
		t.Fatalf("silos = %+v, want only silo.go", res.Silos)
	}
	s := res.Silos[0]
	if s.Path != "silo.go" || s.Author != "ann" {
		t.Errorf("silo = %+v, want silo.go by ann", s)
	}
	if s.Share != 1 {
		t.Errorf("share = %v, want 1", s.Share)
	}
	if s.AuthorCommits != 3 || s.TotalCommits != 3 {
		t.Errorf("counts = %d/%d, want 3/3", s.AuthorCommits, s.TotalCommits)
	}
}

// A file with one commit by one author is not a silo: there is no history to be
// dominated, and reporting it would flood the list with new files.
func TestSingleCommitFilesAreNotSilos(t *testing.T) {
	res := analyze("/repo", defaultCfg(), []commitTouch{
		touch(200, "ann", "new.go"),
	}, now)
	if len(res.Silos) != 0 {
		t.Errorf("a one-commit file was reported as a silo: %+v", res.Silos)
	}
}

// A share below the threshold is not a silo, and the counts must be reported
// explicitly rather than left for the reader to divide.
func TestSiloShareBelowThresholdIsNotReported(t *testing.T) {
	history := []commitTouch{
		touch(200, "ann", "s.go"), touch(190, "ann", "s.go"),
		touch(180, "bob", "s.go"),
	}
	res := analyze("/repo", defaultCfg(), history, now)
	// 2 of 3 commits is 0.67, below the 0.9 default.
	if len(res.Silos) != 0 {
		t.Errorf("a 0.67 share was reported as a silo: %+v", res.Silos[0])
	}

	// Lower the threshold and the same file is reported, with both counts.
	cfg := defaultCfg()
	cfg.SiloShare = 0.6
	res = analyze("/repo", cfg, history, now)
	if len(res.Silos) != 1 {
		t.Fatalf("silos = %+v, want one", res.Silos)
	}
	s := res.Silos[0]
	if s.AuthorCommits != 2 || s.TotalCommits != 3 {
		t.Errorf("counts = %d/%d, want 2/3", s.AuthorCommits, s.TotalCommits)
	}
	if s.Share < 0.66 || s.Share > 0.67 {
		t.Errorf("share = %v, want ~0.667", s.Share)
	}
}

// Two commits by one author is arithmetic, not a knowledge silo. Without this
// floor every young file in every repository reports as a 100% silo and the
// finding says nothing.
func TestSilosNeedEnoughCommitsToMeanSomething(t *testing.T) {
	cfg := defaultCfg()
	cfg.MinSiloCommits = 3

	res := analyze("/repo", cfg, []commitTouch{
		touch(200, "ann", "two.go"),
		touch(190, "ann", "two.go"),
	}, now)
	if len(res.Silos) != 0 {
		t.Errorf("a two-commit file was reported as a silo: %+v", res.Silos[0])
	}

	// The same file at three commits is reported.
	res = analyze("/repo", cfg, []commitTouch{
		touch(200, "ann", "three.go"),
		touch(190, "ann", "three.go"),
		touch(180, "ann", "three.go"),
	}, now)
	if len(res.Silos) != 1 {
		t.Fatalf("a three-commit single-author file was not reported: %+v", res.Silos)
	}
}

// The half-life gate is the point of the package: over a short history no
// half-life is reported and the reason is stated.
// The gate must key off the depth of the whole history, which means a fixture
// with one old commit no longer exercises it: an earlier version of this test
// added a 300-day-old commit to produce an orphan, which quietly extended the
// history past the 180-day threshold and let the half-life through.
func TestHalfLifeIsGatedOnHistoryDepth(t *testing.T) {
	cfg := defaultCfg()
	cfg.WindowDays = 2 // so the day-2 commit counts as out of window

	res := analyze("/repo", cfg, []commitTouch{
		// Three commits so f.go clears MinSiloCommits and can appear as a silo,
		// all inside the same two days so the history stays short.
		touch(1, "a", "f.go"),
		touch(2, "a", "f.go"),
		touch(2, "a", "f.go"),
		// Touched only on the window boundary, which counts as outside it, so
		// this file is a genuine orphan while the history stays two days deep.
		touch(2, "a", "g.go"),
	}, now)

	if res.HistoryDays != 2 {
		t.Fatalf("history depth = %d days, want 2", res.HistoryDays)
	}
	if res.HalfLife != nil {
		t.Errorf("a two-day history produced a half-life: %+v", res.HalfLife)
	}
	if res.HalfLifeUnavailable == "" {
		t.Fatal("no explanation for the missing half-life")
	}
	for _, want := range []string{"2 day", "180", "limit of the data"} {
		if !strings.Contains(res.HalfLifeUnavailable, want) {
			t.Errorf("the explanation is missing %q: %s", want, res.HalfLifeUnavailable)
		}
	}

	// The findings that do not need a curve must survive the gate: refusing to
	// fit is not a reason to withhold what can be measured.
	if len(res.Orphans) == 0 {
		t.Errorf("orphans were suppressed by the half-life gate: %+v", res)
	}
	if len(res.Silos) != 1 || res.Silos[0].Path != "f.go" {
		t.Errorf("silos were suppressed by the half-life gate: %+v", res.Silos)
	}
}

// Enough history must produce a half-life, with both rates visible so the
// number can be checked.
func TestHalfLifeIsFittedWithEnoughHistory(t *testing.T) {
	cfg := defaultCfg()
	cfg.MinHistoryDays = 30
	// Three commits a day for 100 days, then one a day for 100 days: activity
	// fell by two thirds, so the half-life is a real number.
	var commits []commitTouch
	for d := 200; d >= 101; d-- {
		commits = append(commits, touch(d, "a", "f.go"))
	}
	for d := 100; d >= 1; d-- {
		commits = append(commits, touch(d, "a", "f.go"))
	}
	_ = commits

	// Build the uneven series explicitly instead: 3 per day early, 1 per day late.
	var uneven []commitTouch
	for d := 200; d >= 101; d-- {
		for k := 0; k < 3; k++ {
			uneven = append(uneven, commitTouch{
				date: daysAgo(d).Add(time.Duration(k) * time.Hour), author: "a",
				files: []string{"f.go"},
			})
		}
	}
	for d := 100; d >= 1; d-- {
		uneven = append(uneven, touch(d, "a", "f.go"))
	}

	res := analyze("/repo", cfg, uneven, now)
	if res.HalfLifeUnavailable != "" {
		t.Fatalf("a 200-day history was rejected: %s", res.HalfLifeUnavailable)
	}
	if res.HalfLife == nil {
		t.Fatal("no half-life was produced")
	}
	h := res.HalfLife
	if h.PriorPerDay <= 0 || h.RecentPerDay <= 0 {
		t.Errorf("rates not reported: %+v", h)
	}
	if h.PriorPerDay <= h.RecentPerDay {
		t.Errorf("prior rate %v should exceed recent %v: the series is not decaying",
			h.PriorPerDay, h.RecentPerDay)
	}
	if h.Days <= 0 {
		t.Errorf("half-life = %v, want positive", h.Days)
	}
	if h.Days > float64(res.HistoryDays) {
		t.Errorf("half-life %v exceeds the %d-day history it was fitted over",
			h.Days, res.HistoryDays)
	}
}

// Rising activity has no decay, and the result must say so rather than
// reporting a negative or infinite half-life.
func TestRisingActivityReportsNoDecay(t *testing.T) {
	cfg := defaultCfg()
	cfg.MinHistoryDays = 30
	var uneven []commitTouch
	for d := 100; d >= 51; d-- {
		uneven = append(uneven, touch(d, "a", "f.go"))
	}
	for d := 50; d >= 1; d-- {
		for k := 0; k < 5; k++ {
			uneven = append(uneven, commitTouch{
				date: daysAgo(d).Add(time.Duration(k) * time.Hour), author: "a",
				files: []string{"f.go"},
			})
		}
	}
	res := analyze("/repo", cfg, uneven, now)
	if res.HalfLife == nil {
		t.Fatal("no half-life produced for rising activity")
	}
	if res.HalfLife.Days != 0 {
		t.Errorf("half-life = %v for rising activity, want 0 (no decay)",
			res.HalfLife.Days)
	}
}

func TestNoCommitsIsHandled(t *testing.T) {
	res := analyze("/repo", defaultCfg(), nil, now)
	if res.Note == "" {
		t.Error("no commits gave no explanation")
	}
	if res.HalfLifeUnavailable == "" {
		t.Error("no commits gave no half-life explanation")
	}
	if len(res.Orphans) != 0 || len(res.Silos) != 0 {
		t.Errorf("no commits produced findings: %+v", res)
	}
}

func TestLimitsAreRespected(t *testing.T) {
	cfg := defaultCfg()
	cfg.Limit = 2
	var commits []commitTouch
	for i, name := range []string{"a.go", "b.go", "c.go", "d.go"} {
		commits = append(commits, touch(200+i, "a", name))
	}
	res := analyze("/repo", cfg, commits, now)
	if len(res.Orphans) > 2 {
		t.Errorf("orphans = %d, limit 2", len(res.Orphans))
	}
}

func TestOutputIsDeterministic(t *testing.T) {
	commits := []commitTouch{
		touch(200, "ann", "x.go"), touch(190, "ann", "x.go"),
		touch(180, "bob", "y.go"), touch(10, "ann", "z.go"),
	}
	first := format(analyze("/repo", defaultCfg(), commits, now))
	for i := 0; i < 20; i++ {
		if again := format(analyze("/repo", defaultCfg(), commits, now)); again != first {
			t.Fatalf("run %d differs:\n%s\n---\n%s", i, first, again)
		}
	}
}

// The half-life arithmetic must use a correct logarithm. An earlier version
// hand-rolled ln via an atanh series with the wrong denominators and was wrong
// by several percent on every value, which is exactly the kind of quiet error a
// test comparing against a known logarithm catches.
func TestHalfLifeArithmeticUsesTheCorrectLogarithm(t *testing.T) {
	cfg := defaultCfg()
	cfg.MinHistoryDays = 30

	// Three commits a day early, one a day late: ratio 1/3.
	// ln(0.5)/ln(1/3) = 0.63093 halves, and each half is (span/2) days.
	var uneven []commitTouch
	for d := 200; d >= 101; d-- {
		for k := 0; k < 3; k++ {
			uneven = append(uneven, commitTouch{
				date:   daysAgo(d).Add(time.Duration(k) * time.Hour),
				author: "a", files: []string{"f.go"},
			})
		}
	}
	for d := 100; d >= 1; d-- {
		uneven = append(uneven, touch(d, "a", "f.go"))
	}

	res := analyze("/repo", cfg, uneven, now)
	if res.HalfLife == nil {
		t.Fatal("no half-life produced")
	}
	h := res.HalfLife
	// Each half is span/2 = 100 days, so Days = 0.63093 * 100 = 63.09.
	want := math.Log(0.5) / math.Log(1.0/3.0) * 100
	if math.Abs(h.Days-want) > 0.5 {
		t.Errorf("half-life = %v, want ~%.2f (rates %v vs %v)",
			h.Days, want, h.PriorPerDay, h.RecentPerDay)
	}
}

// ---- against a real repository ----

func TestAnalyzeRealRepositoryGatesShortHistory(t *testing.T) {
	dir := newRepo(t, 4)
	res, err := Analyze(context.Background(), dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Commits != 4 {
		t.Fatalf("read %d commits, want 4", res.Commits)
	}
	if res.HalfLifeUnavailable == "" {
		t.Error("a four-commit repository produced a half-life")
	}
	if res.HalfLife != nil {
		t.Errorf("half-life reported from insufficient history: %+v", res.HalfLife)
	}
}

func TestAnalyzeRealRepositoryOutsideAGitRepo(t *testing.T) {
	res, err := Analyze(context.Background(), t.TempDir(), DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze outside a repository: %v", err)
	}
	if res.Commits != 0 || res.Note == "" {
		t.Errorf("a non-repository was not handled: %+v", res)
	}
}

func TestParseLogHandlesSpacesAndSkipsEmpty(t *testing.T) {
	raw := "\x002026-01-01T00:00:00Z\x00ann\nmy file.go\n\n" +
		"\x002026-01-02T00:00:00Z\x00bob\nother.go\n"
	got := parseLog([]byte(raw))
	if len(got) != 2 {
		t.Fatalf("got %d commits, want 2: %+v", len(got), got)
	}
	if got[0].author != "ann" {
		t.Errorf("author = %q, want ann", got[0].author)
	}
	if len(got[0].files) != 1 || got[0].files[0] != "my file.go" {
		t.Errorf("files = %+v, want [my file.go]", got[0].files)
	}
	if got[1].date.Day() != 2 {
		t.Errorf("second commit date = %v", got[1].date)
	}
}

func TestParseLogSurvivesGarbage(t *testing.T) {
	for _, raw := range []string{"", "\x00", "\x00\x00", "\x00\x00\x00", "\x00no-newline"} {
		for _, c := range parseLog([]byte(raw)) {
			if len(c.files) == 0 {
				t.Errorf("input %q produced a file-less commit", raw)
			}
		}
	}
}

// ---- helpers ----

func newRepo(t *testing.T, commits int) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "t@example.com")
	git(t, dir, "config", "user.name", "T")
	git(t, dir, "config", "commit.gpgsign", "false")
	git(t, dir, "config", "core.autocrlf", "false")
	for i := 0; i < commits; i++ {
		p := filepath.Join(dir, "f.go")
		if err := os.WriteFile(p,
			[]byte("package f\n\nfunc F"+itoa(i)+"() {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-q", "-m", "c"+itoa(i))
	}
	return dir
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func trim(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }

func format(r Result) string {
	var b strings.Builder
	for _, o := range r.Orphans {
		b.WriteString("orphan " + o.Path + " " + itoa(o.AgeDays) + "\n")
	}
	for _, s := range r.Silos {
		b.WriteString("silo " + s.Path + " " + s.Author + " " + trim(s.Share) + "\n")
	}
	if r.HalfLife != nil {
		b.WriteString("half " + trim(r.HalfLife.Days) + "\n")
	}
	b.WriteString(r.HalfLifeUnavailable + "\n")
	return b.String()
}
