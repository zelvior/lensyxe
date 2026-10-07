package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The cadence denominator must be the history that exists, not the configured
// window.
//
// This is the bug that started it. Lensyxe's own repository had 38 commits
// spanning one day and reported 2.96 commits/week, because 38*7/90 averages over
// 89 days on which nothing happened. The number was not slow -- it was wrong,
// and it made a young project look dormant.
//
// A test that constructs a GitStats by hand cannot catch this, because the field
// it would assert is the one the test itself populated. So this builds a real
// repository and reads what the analyzer actually computed.

// youngRepo builds a repository whose entire history fits inside `hours`.
func youngRepo(t *testing.T, commits, hours int) string {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	base := []string{
		"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	}
	run := func(when string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		env := append(append([]string{}, base...),
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.invalid",
		)
		if when != "" {
			env = append(env, "GIT_AUTHOR_DATE="+when, "GIT_COMMITTER_DATE="+when)
		}
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("", "init", "-q", "-b", "main")
	for i := range commits {
		name := filepath.Join(dir, "file.txt")
		if err := os.WriteFile(name, []byte("line\n"+string(rune('a'+i))), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		run("", "add", "file.txt")
		// Spread the commits across the requested span so the age of the
		// repository is unambiguous rather than "all in one instant".
		offset := time.Duration(i) * time.Duration(hours) * time.Hour /
			time.Duration(commits)
		when := time.Now().Add(-offset).Format(time.RFC3339)
		run(when, "commit", "-q", "-m", "commit")
	}
	return dir
}

func TestCadenceSpanIsTheHistoryNotTheWindow(t *testing.T) {
	const (
		commits = 12
		hours   = 36
	)
	dir := youngRepo(t, commits, hours)

	res, err := Analyze(context.Background(), dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	cfg := DefaultConfig()
	if res.Stats.CadenceSpanDays > cfg.WindowDays {
		t.Fatalf("CadenceSpanDays = %d exceeds the configured window %d; the "+
			"window is the cap, never the floor",
			res.Stats.CadenceSpanDays, cfg.WindowDays)
	}
	// A repository two days old must not be measured over a 90-day window.
	if res.Stats.CadenceSpanDays >= 7 {
		t.Errorf("CadenceSpanDays = %d for a %d-hour-old repository; the "+
			"denominator is still averaging over days with no commits",
			res.Stats.CadenceSpanDays, hours)
	}
	// And the rate must reflect the short span, so it must be far above the
	// window-based figure it would have produced.
	windowBased := float64(res.Stats.WindowCommits) * 7 / float64(cfg.WindowDays)
	if res.Stats.CommitsPerWeek <= windowBased {
		t.Errorf("CommitsPerWeek = %v, not above the window-based %v; the fix "+
			"did not take effect",
			res.Stats.CommitsPerWeek, windowBased)
	}
}

// An old repository still measures over the window, not over its whole age. A
// two-year project with a 90-day window must not be averaged over 730 days.
func TestCadenceSpanIsCappedAtTheWindowForAnOldRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := youngRepo(t, 4, 2)
	// Backdate the root commit by two years so the repository is unambiguously
	// older than any plausible window.
	run := func(when string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid",
			"GIT_AUTHOR_DATE="+when, "GIT_COMMITTER_DATE="+when,
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(time.Now().Add(-730*24*time.Hour).Format(time.RFC3339),
		"commit", "-q", "--amend", "--no-edit", "--allow-empty")

	res, err := Analyze(context.Background(), dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	cfg := DefaultConfig()
	if res.Stats.CadenceSpanDays > cfg.WindowDays {
		t.Errorf("CadenceSpanDays = %d; the %d-day window must cap a "+
			"730-day-old repository", res.Stats.CadenceSpanDays, cfg.WindowDays)
	}
}
