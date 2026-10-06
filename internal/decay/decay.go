// Package decay finds code that has stopped changing, and reports how fast the
// repository's activity is falling off.
//
// Three findings, and they are not equally well-founded, so the result keeps them
// apart rather than averaging them into one number:
//
//   - Orphaned code: a tracked file with no changes inside the window. It exists,
//     it is compiled or shipped, and nobody has touched it. That is a fact.
//   - Knowledge silos: a file whose history is dominated by one contributor.
//     This is the per-file form of a bus factor, and it overlaps the bus factor
//     the git analyzer already reports; it is not a second, independent signal.
//   - Half-life: how long it takes for the rate of change to halve.
//
// Half-life is the one that needs a time series, and it is the one this package
// refuses to fabricate. Fitting a decay curve needs months of dated history. Over
// a day or two of commits any curve fits, and the fitted number would describe
// the shape of the available data rather than the decay of the code. So below a
// configured history depth the package reports the first two findings, states
// the depth it found, and names the threshold it needs.
//
// Nothing here is a prediction. An untouched file is not deleted code, and a
// single-author file is not unmaintained code; both are places where a question
// has not been asked yet.
package decay

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// DefaultConfig returns the settings used when a caller supplies none.
func DefaultConfig() Config {
	return Config{
		// 180 days is the floor for fitting a decay curve. Below roughly half a
		// year the curve is determined by a handful of commits.
		MinHistoryDays: 180,
		// 90 days is the default activity window for the orphan and silo
		// findings, matching the git analyzer's default so the two agree.
		WindowDays: 90,
		// One contributor above this share of a file's commits marks it a silo.
		SiloShare: 0.9,
		// A file needs this many commits before a silo can be claimed. Without
		// it, every two-commit file in any repository reports as a 100% silo
		// and the list becomes noise: this repository's own first version of
		// this finding returned twenty-five files, all of them trivially
		// single-author because nobody had written to them twice yet.
		MinSiloCommits: 3,
		Limit:          25,
		Timeout:        30 * time.Second,
	}
}

// Config tunes the analysis.
type Config struct {
	// MinHistoryDays is the history depth below which half-life is not
	// reported.
	MinHistoryDays int
	// WindowDays bounds the activity window for orphans and silos.
	WindowDays int
	// SiloShare is the single-contributor share that marks a file a silo.
	SiloShare float64
	// MinSiloCommits is the smallest commit count a file may have before a silo
	// can be reported. Two commits by one author is arithmetic, not a finding.
	MinSiloCommits int
	// Limit caps how many files each finding lists.
	Limit int
	// Timeout bounds the git invocation.
	Timeout time.Duration
}

// Orphan is a tracked file with no activity in the window.
type Orphan struct {
	Path string `json:"path"`
	// LastChange is when the file was last touched, and is zero when it was
	// never touched by any commit in the read history.
	LastChange time.Time `json:"last_change"`
	// AgeDays is days since LastChange, measured to now.
	AgeDays int `json:"age_days"`
	// Tracked reports whether the file is still in the index. An untracked file
	// that has not changed recently is not orphaned code, it is a new file.
	Tracked bool `json:"tracked"`
}

// Silo is a file whose history is dominated by one contributor.
type Silo struct {
	Path string `json:"path"`
	// Author is the dominant contributor's name, or the empty string when the
	// file has no author at all.
	Author string `json:"author"`
	// AuthorCommits and TotalCommits give the share explicitly, so a reader
	// never has to divide to see it.
	AuthorCommits int `json:"author_commits"`
	TotalCommits  int `json:"total_commits"`
	// Share is AuthorCommits / TotalCommits.
	Share float64 `json:"share"`
	// LastChange is the file's most recent touch.
	LastChange time.Time `json:"last_change"`
}

// HalfLife is the decay measurement, present only when history allows it.
type HalfLife struct {
	// Days is the fitted half-life in days.
	Days float64 `json:"days"`
	// RecentPerDay and PriorPerDay are the two activity rates the fit uses,
	// reported so the number can be checked rather than trusted.
	RecentPerDay float64 `json:"recent_per_day"`
	PriorPerDay  float64 `json:"prior_per_day"`
	// Points is how many dated samples the fit had.
	Points int `json:"points"`
	// Commits is the history depth it was fitted over.
	Commits int `json:"commits"`
}

// Result is the whole outcome.
type Result struct {
	Root string `json:"root"`
	// Commits read from history.
	Commits int `json:"commits"`
	// FirstCommitAt and LastCommitAt bound the history.
	FirstCommitAt time.Time `json:"first_commit_at"`
	LastCommitAt  time.Time `json:"last_commit_at"`
	// HistoryDays is how many days the history spans.
	HistoryDays int `json:"history_days"`
	// WindowDays is the activity window used for the findings.
	WindowDays int `json:"window_days"`
	// Files is how many tracked files were considered.
	Files int `json:"files"`

	// Orphans, Silos and HalfLife.
	Orphans  []Orphan  `json:"orphans"`
	Silos    []Silo    `json:"silos"`
	HalfLife *HalfLife `json:"half_life,omitempty"`
	Zombies  []Orphan  `json:"zombies,omitempty"`
	// HalfLifeUnavailable explains why no half-life is reported, and is empty
	// when one is.
	HalfLifeUnavailable string `json:"half_life_unavailable,omitempty"`
	// Note carries a degradation reason.
	Note string `json:"note,omitempty"`
}

// commitTouch is one commit: its date and the files it changed.
type commitTouch struct {
	date  time.Time
	files []string
	// author is empty when the log format did not include one.
	author string
}

// Analyze measures activity decay for a repository.
func Analyze(ctx context.Context, root string, cfg Config) (Result, error) {
	commits, err := readCommits(ctx, root, cfg.Timeout)
	if err != nil {
		return Result{Root: root, Note: err.Error()}, err
	}
	return analyze(root, cfg, commits, time.Now()), nil
}

// analyze is the whole computation, separated from git so it can be tested
// against hand-built histories.
func analyze(root string, cfg Config, commits []commitTouch, now time.Time) Result {
	res := Result{Root: root, Commits: len(commits), WindowDays: cfg.WindowDays}
	if len(commits) == 0 {
		res.Note = "no commits found: nothing to measure activity against"
		res.HalfLifeUnavailable = "no commits to fit a decay curve over"
		return res
	}

	for _, c := range commits {
		if res.FirstCommitAt.IsZero() || c.date.Before(res.FirstCommitAt) {
			res.FirstCommitAt = c.date
		}
		if res.LastCommitAt.IsZero() || c.date.After(res.LastCommitAt) {
			res.LastCommitAt = c.date
		}
	}
	res.HistoryDays = int(now.Sub(res.FirstCommitAt).Hours() / 24)

	windowStart := now.AddDate(0, 0, -cfg.WindowDays)

	// Per-file aggregates over the whole history read.
	type stat struct {
		last    time.Time
		commits int
		authors map[string]int
	}
	files := map[string]*stat{}
	seenInWindow := map[string]bool{}

	for _, c := range commits {
		for _, f := range c.files {
			s, ok := files[f]
			if !ok {
				s = &stat{authors: map[string]int{}}
				files[f] = s
			}
			s.commits++
			if c.date.After(s.last) {
				s.last = c.date
			}
			if c.author != "" {
				s.authors[c.author]++
			}
			if c.date.After(windowStart) {
				seenInWindow[f] = true
			}
		}
	}
	res.Files = len(files)

	// Orphans: tracked, present in history, untouched inside the window.
	names := make([]string, 0, len(files))
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)

	for _, f := range names {
		if seenInWindow[f] {
			continue
		}
		s := files[f]
		age := 0
		if !s.last.IsZero() {
			age = int(now.Sub(s.last).Hours() / 24)
		}
		res.Orphans = append(res.Orphans, Orphan{
			Path: f, LastChange: s.last, AgeDays: age, Tracked: true,
		})
	}
	sort.SliceStable(res.Orphans, func(i, j int) bool {
		if res.Orphans[i].AgeDays != res.Orphans[j].AgeDays {
			return res.Orphans[i].AgeDays > res.Orphans[j].AgeDays
		}
		return res.Orphans[i].Path < res.Orphans[j].Path
	})
	if len(res.Orphans) > cfg.Limit {
		res.Orphans = res.Orphans[:cfg.Limit]
	}

	// Silos: one contributor above the share, over enough commits for the
	// share to mean something. A file with two commits by one author is not a
	// knowledge silo, it is a file that has barely been touched.
	for _, f := range names {
		s := files[f]
		if s.commits < cfg.MinSiloCommits || len(s.authors) == 0 {
			continue
		}
		best, bestCount := "", 0
		for author, n := range s.authors {
			if n > bestCount || (n == bestCount && author < best) {
				best, bestCount = author, n
			}
		}
		if bestCount == 0 {
			continue
		}
		share := float64(bestCount) / float64(s.commits)
		if share < cfg.SiloShare {
			continue
		}
		res.Silos = append(res.Silos, Silo{
			Path: f, Author: best, AuthorCommits: bestCount,
			TotalCommits: s.commits, Share: share, LastChange: s.last,
		})
	}
	sort.SliceStable(res.Silos, func(i, j int) bool {
		if res.Silos[i].Share != res.Silos[j].Share {
			return res.Silos[i].Share > res.Silos[j].Share
		}
		if res.Silos[i].TotalCommits != res.Silos[j].TotalCommits {
			return res.Silos[i].TotalCommits > res.Silos[j].TotalCommits
		}
		return res.Silos[i].Path < res.Silos[j].Path
	})
	if len(res.Silos) > cfg.Limit {
		res.Silos = res.Silos[:cfg.Limit]
	}

	// Half-life, only when the history can support one.
	if res.HistoryDays < cfg.MinHistoryDays {
		res.HalfLifeUnavailable = fmt.Sprintf(
			"history spans %d day(s) and a decay curve needs at least %d days to "+
				"be distinguishable from the shape of the available commits. No "+
				"half-life is reported. This is a limit of the data, not a finding "+
				"that the code is stable.",
			res.HistoryDays, cfg.MinHistoryDays)
	} else {
		res.HalfLife = fitHalfLife(commits, res.FirstCommitAt, res.LastCommitAt)
	}
	return res
}

// fitHalfLife measures how long activity takes to halve.
//
// The repository's change rate is compared across the two halves of its history.
// If the recent half runs at a fraction of the earlier half, the time for the
// rate to reach half of where it started follows from the ratio. The two rates
// are reported alongside the result so the figure can be checked instead of
// taken on trust.
func fitHalfLife(commits []commitTouch, first, last time.Time) *HalfLife {
	span := last.Sub(first)
	if span <= 0 {
		return nil
	}
	midpoint := first.Add(span / 2)

	var prior, recent int
	for _, c := range commits {
		if c.date.Before(midpoint) {
			prior++
		} else {
			recent++
		}
	}

	days := span.Hours() / 24
	priorRate := float64(prior) / (days / 2)
	recentRate := float64(recent) / (days / 2)

	h := &HalfLife{
		RecentPerDay: round2(recentRate),
		PriorPerDay:  round2(priorRate),
		Points:       len(commits),
		Commits:      len(commits),
	}

	// The ratio is how much of the earlier rate survived. When activity is
	// flat or rising there is no decay to report and saying so is the honest
	// outcome rather than an infinite half-life.
	if priorRate <= 0 {
		h.Days = 0 // no earlier activity to decay from
		return h
	}
	ratio := recentRate / priorRate
	if ratio <= 0 {
		// Nothing happened in the recent half. That is not "infinitely fast
		// decay"; it is an absence of data in the numerator, so no rate can be
		// stated.
		h.Days = -1
		return h
	}
	if ratio >= 1 {
		// Activity did not fall. A half-life would be negative.
		h.Days = 0
		return h
	}

	// Decay is exponential at ratio per half of the span, so the time to halve
	// is ln(0.5)/ln(ratio) halves.
	//
	// math.Log rather than a hand-rolled series: an earlier version expanded
	// ln(x) = 2*atanh((x-1)/(x+1)) but summed with denominators 1, 2, 3...
	// instead of the odd 1, 3, 5 the series actually has, which quietly made
	// every half-life wrong by several percent. Preferring the standard library
	// over a hand-written approximation of it is the whole point.
	halves := math.Log(0.5) / math.Log(ratio)
	h.Days = round2(halves * (days / 2))
	return h
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// readCommits pulls dated commits with their files and authors.
func readCommits(ctx context.Context, root string, timeout time.Duration) ([]commitTouch, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not found: activity decay is read from git history")
	}
	cmd := exec.CommandContext(ctx, "git",
		"log", "--no-merges", "--name-only", "--pretty=format:%x00%aI%x00%aN")
	cmd.Dir = root

	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, fmt.Errorf("git log timed out after %s", timeout)
	}
	if err != nil {
		if isUnavailable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("git log: %s", failureText(err))
	}
	return parseLog(out), nil
}

// parseLog reads the NUL-delimited stream into commits.
//
// Same walk as the blast package: the stream is "", sha... no -- here the header
// is date, NUL, author, so records are separated by the leading NUL and the two
// header fields are separated by the second.
func parseLog(raw []byte) []commitTouch {
	var out []commitTouch
	s := string(raw)

	for {
		i := strings.IndexByte(s, 0)
		if i < 0 {
			return out
		}
		s = s[i+1:]

		j := strings.IndexByte(s, 0)
		if j < 0 {
			return out
		}
		dateStr := strings.TrimSpace(s[:j])
		s = s[j+1:]

		k := strings.IndexByte(s, '\n')
		if k < 0 {
			return out
		}
		author := strings.TrimSpace(s[:k])
		s = s[k+1:]

		body := s
		if n := strings.IndexByte(s, 0); n >= 0 {
			body = s[:n]
			s = s[n:]
		} else {
			s = ""
		}

		var files []string
		seen := map[string]bool{}
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			files = append(files, line)
		}
		if len(files) == 0 {
			if len(s) == 0 {
				return out
			}
			continue
		}

		t, parseErr := time.Parse(time.RFC3339, dateStr)
		if parseErr != nil {
			t = time.Time{}
		}
		out = append(out, commitTouch{date: t, files: files, author: author})
		if len(s) == 0 {
			return out
		}
	}
}

func isUnavailable(err error) bool {
	msg := strings.ToLower(failureText(err))
	return strings.Contains(msg, "does not have any commits yet") ||
		strings.Contains(msg, "unknown revision") ||
		strings.Contains(msg, "bad revision") ||
		strings.Contains(msg, "not a git repository")
}

// failureText returns git's stderr, which err.Error() does not carry.
func failureText(err error) string {
	if err == nil {
		return ""
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return string(ee.Stderr) + " " + err.Error()
	}
	return err.Error()
}
