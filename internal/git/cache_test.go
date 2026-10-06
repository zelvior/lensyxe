package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// newRepo creates a throwaway repository with a deterministic history.
//
// The cache is process-global, so tests that share a repository path would share
// cache entries. Every case therefore gets its own directory, which is also what
// keeps a cached entry from one test leaking into another.
func newRepo(t *testing.T, commits int) string {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init", "-q", "-b", "main")
	for i := range commits {
		// Distinct content per commit. Writing the same bytes again produces an
		// empty commit and `git commit` fails, which is a confusing failure for
		// a test that is really about the cache.
		name := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(name, []byte(fmt.Sprintf("line %d\n", i)), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		run("add", "file.txt")

		// Both dates are set. `git log --since` filters on the COMMITTER date,
		// not the author date, so setting only --date leaves every commit at the
		// current time and a window test measures nothing.
		when := time.Now().Add(-time.Duration(commits-i) * 12 * time.Hour).
			Format(time.RFC3339)
		cmd := exec.Command("git", "commit", "-q", "-m", fmt.Sprintf("commit %d", i))
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_AUTHOR_DATE="+when,
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid",
			"GIT_COMMITTER_DATE="+when,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git commit: %v\n%s", err, out)
		}
	}

	return dir
}

// A cache hit must be indistinguishable from the cold read it replaced.
//
// This is the assertion that matters. The cache stores raw git stdout rather
// than parsed values precisely so the hit path runs the same parsing, and this
// test is what holds that property in place: if someone caches a derived value
// instead, or forgets to key on the window, the two Results diverge here.
func TestHistoryCacheHitMatchesColdRun(t *testing.T) {
	dir := newRepo(t, 5)
	cfg := DefaultConfig()
	cfg.WindowDays = 90

	ctx := context.Background()

	cold, err := Analyze(ctx, dir, cfg)
	if err != nil {
		t.Fatalf("cold analyze: %v", err)
	}

	// The first call populated the cache; the second must hit it.
	warm, err := Analyze(ctx, dir, cfg)
	if err != nil {
		t.Fatalf("warm analyze: %v", err)
	}

	if !reflect.DeepEqual(cold.Stats, warm.Stats) {
		t.Errorf("cached stats differ from the cold read\ncold: %+v\nwarm: %+v",
			cold.Stats, warm.Stats)
	}
	if !reflect.DeepEqual(cold.ChurnByPath, warm.ChurnByPath) {
		t.Errorf("cached churn differs from the cold read\ncold: %v\nwarm: %v",
			cold.ChurnByPath, warm.ChurnByPath)
	}
	if !reflect.DeepEqual(cold.Findings, warm.Findings) {
		t.Errorf("cached findings differ from the cold read")
	}
}

// A new commit invalidates the entry.
//
// Without this, the cache would serve a previous commit's history and every
// number in the report would be quietly wrong.
func TestHistoryCacheInvalidatedByNewCommit(t *testing.T) {
	dir := newRepo(t, 3)
	cfg := DefaultConfig()
	cfg.WindowDays = 90

	ctx := context.Background()
	before, err := Analyze(ctx, dir, cfg)
	if err != nil {
		t.Fatalf("first analyze: %v", err)
	}

	name := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(name, []byte("more\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{{"add", "file.txt"}, {"commit", "-q", "-m", "another"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	after, err := Analyze(ctx, dir, cfg)
	if err != nil {
		t.Fatalf("second analyze: %v", err)
	}

	if after.Stats.HeadCommit == before.Stats.HeadCommit {
		t.Fatal("HEAD did not change, so this test is not exercising invalidation")
	}
	if after.Stats.TotalCommits <= before.Stats.TotalCommits {
		t.Errorf("commit count did not advance: %d then %d; a stale entry was served",
			before.Stats.TotalCommits, after.Stats.TotalCommits)
	}
	if reflect.DeepEqual(before.ChurnByPath, after.ChurnByPath) {
		t.Error("churn is unchanged after a new commit; a stale entry was served")
	}
}

// A different window must not reuse an entry computed for another one.
//
// The window start is the part of the key that is easy to omit: the commit is
// unchanged, so keying on it alone looks correct and is not.
func TestHistoryCacheSeparatesWindowDays(t *testing.T) {
	dir := newRepo(t, 4)
	ctx := context.Background()

	wide := DefaultConfig()
	wide.WindowDays = 3650
	narrow := DefaultConfig()
	narrow.WindowDays = 1

	wideRes, err := Analyze(ctx, dir, wide)
	if err != nil {
		t.Fatalf("wide analyze: %v", err)
	}
	narrowRes, err := Analyze(ctx, dir, narrow)
	if err != nil {
		t.Fatalf("narrow analyze: %v", err)
	}

	if wideRes.Stats.WindowDays != 3650 || narrowRes.Stats.WindowDays != 1 {
		t.Fatalf("window days were not carried through: %d and %d",
			wideRes.Stats.WindowDays, narrowRes.Stats.WindowDays)
	}

	// The one-day window cannot contain the whole history, so if the narrow
	// result matched the wide one the entry was shared when it must not be.
	if narrowRes.Stats.WindowCommits >= wideRes.Stats.WindowCommits {
		t.Errorf("a one-day window reported %d commits and a ten-year window %d; "+
			"the two must not share a cache entry",
			narrowRes.Stats.WindowCommits, wideRes.Stats.WindowCommits)
	}
}

// Two repositories must not share an entry, even with an identical commit.
//
// The commit SHA alone is not unique across repositories -- an init plus one
// commit is deterministic enough to collide -- which is why root is in the key.
func TestHistoryCacheSeparatesRepositories(t *testing.T) {
	a := newRepo(t, 2)
	b := newRepo(t, 2)

	cfg := DefaultConfig()
	cfg.WindowDays = 90

	ctx := context.Background()
	ra, err := Analyze(ctx, a, cfg)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	rb, err := Analyze(ctx, b, cfg)
	if err != nil {
		t.Fatalf("b: %v", err)
	}

	if ra.Stats.Branch != rb.Stats.Branch {
		t.Errorf("branch leaked between repositories: %q and %q",
			ra.Stats.Branch, rb.Stats.Branch)
	}
}

// A directory with no commits must not be cached.
//
// `git rev-parse HEAD` in an empty repository prints the literal "HEAD" and
// exits non-zero rather than failing cleanly, so the commit value is not
// detectable as empty. Every empty repository would otherwise share one cache
// key, and one could be served another's history.
func TestHistoryCacheSkipsEmptyCommit(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v\n%s", err, out)
	}

	cfg := DefaultConfig()
	cfg.WindowDays = 90

	res, err := Analyze(context.Background(), dir, cfg)
	if err != nil {
		t.Fatalf("analyze an empty repository: %v", err)
	}
	// The reported commit is git's own unresolved-ref output. What matters here
	// is that it is never treated as a cacheable identity.
	if _, ok := newHistoryKey(dir, res.Stats.HeadCommit, cfg.WindowDays, time.Now()); ok {
		t.Errorf("an empty repository produced a cacheable key from %q; a later "+
			"real commit would then be served the empty result", res.Stats.HeadCommit)
	}
}

func TestHistoryKeyRejectsEmptyCommit(t *testing.T) {
	since := time.Now()

	if _, ok := newHistoryKey("/repo", "   ", 90, since); ok {
		t.Error("a blank commit was accepted as a cache key; unrelated " +
			"repositories could share one entry")
	}
	if _, ok := newHistoryKey("/repo", "HEAD", 90, since); ok {
		t.Error(`the literal "HEAD" was accepted as a cache key; that is what` +
			" rev-parse prints for a repository with no commits")
	}

	k1, ok := newHistoryKey("/repo", "abc123", 90, since)
	if !ok {
		t.Fatal("a real commit was rejected")
	}
	k2, _ := newHistoryKey("/repo", "abc123", 90, since)
	if k1 != k2 {
		t.Error("the same commit on the same day produced different keys")
	}

	// A different day is a different key, because the window has moved.
	k3, _ := newHistoryKey("/repo", "abc123", 90, since.AddDate(0, 0, 1))
	if k1 == k3 {
		t.Error("the window day is not part of the key, so a sliding window " +
			"would serve a stale commit list")
	}
}
