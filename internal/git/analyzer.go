// Package git implements local git-history analysis.
//
// Every metric is derived by shelling out to the native `git` binary via
// os/exec with machine-readable output formats. Lensyxe never links a git
// library and never touches the network: no fetching, no submodule updates.
// If `git` is unavailable or the path is not a repository, the analyzer
// degrades to a documented zero value instead of failing the run.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

// ErrNoRepository indicates the target path is not inside a git work tree.
var ErrNoRepository = errors.New("not a git repository")

// Config tunes the git analyzer.
type Config struct {
	// WindowDays bounds the churn analysis window. Commits older than this do
	// not contribute to churn or commit-frequency metrics.
	WindowDays int
	// ChurnLimit caps how many churn rows are retained.
	ChurnLimit int
	// Timeout bounds each git subprocess. Cancelling the parent context
	// propagates here and kills the child process.
	Timeout time.Duration
	// BusyAuthorShare is the per-author commit share above which an author
	// counts toward the bus factor.
	BusyAuthorShare float64
	// MaxParallelDiff bounds concurrent diff parsing inside a single git
	// process. Reserved for future streaming work; kept explicit for clarity.
	MaxParallelDiff int
}

// DefaultConfig returns the baseline git analyzer configuration.
func DefaultConfig() Config {
	return Config{
		WindowDays:      90,
		ChurnLimit:      10,
		Timeout:         30 * time.Second,
		BusyAuthorShare: 0.20,
		MaxParallelDiff: 4,
	}
}

// Result pairs the computed stats with any non-fatal observations.
type Result struct {
	Stats    models.GitStats
	Findings []models.Finding
	// ChurnByPath maps every file changed in the window to its
	// added+deleted line count.
	//
	// Stats.Churn is truncated to the top entries for display, but hotspot
	// classification needs churn for every file above the size threshold, so
	// the full mapping is retained here rather than in the JSON contract.
	ChurnByPath map[string]int
}

// Analyze inspects the git repository containing root.
//
// ErrNoRepository is returned (alongside a zero-value Result) when the path is
// not tracked by git; callers are expected to treat that as a normal outcome.
func Analyze(ctx context.Context, root string, cfg Config) (Result, error) {
	zero := models.GitStats{WindowDays: cfg.WindowDays}

	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout+5*time.Second)
	defer cancel()

	// Step 1: confirm we are in a work tree. `--is-inside-work-tree` exits 0
	// for real work trees and non-zero inside a bare repo or a plain folder.
	if _, err := run(ctx, root, 10*time.Second, "rev-parse", "--is-inside-work-tree"); err != nil {
		zero.Note = "target is not a git repository"
		return Result{Stats: zero}, fmt.Errorf("%w: %s", ErrNoRepository, root)
	}

	stats := models.GitStats{IsRepository: true, WindowDays: cfg.WindowDays}

	// Step 2: scalar metadata. Each call is an independent, cheap process, so
	// they run concurrently. Every goroutine writes to its own field, and
	// wait() provides the happens-before edge before the values are read.
	var (
		branchOut, headOut, totalOut, authorOut, firstOut string
		branchErr, headErr, totalErr, authorErr, firstErr error
	)
	wg := newGroup()
	wg.run(func() error {
		branchOut, branchErr = run(ctx, root, 10*time.Second, "rev-parse", "--abbrev-ref", "HEAD")
		return branchErr
	})
	wg.run(func() error {
		headOut, headErr = run(ctx, root, 10*time.Second, "rev-parse", "HEAD")
		return headErr
	})
	wg.run(func() error {
		totalOut, totalErr = run(ctx, root, 10*time.Second, "rev-list", "--count", "HEAD")
		return totalErr
	})
	wg.run(func() error {
		authorOut, authorErr = run(ctx, root, 20*time.Second, "log", "--format=%aN")
		return authorErr
	})
	wg.run(func() error {
		// The root commit carries the earliest timestamp in the history.
		firstOut, firstErr = run(ctx, root, 20*time.Second,
			"log", "--reverse", "--format=%aI", "--max-parents=0", "HEAD")
		return firstErr
	})
	if err := wg.wait(); err != nil && !errors.Is(err, context.Canceled) {
		// A partial failure still yields a usable snapshot, so record why the
		// data may be incomplete instead of aborting the analysis.
		stats.Note = "some git metadata could not be read"
	}

	stats.Branch = strings.TrimSpace(branchOut)
	stats.HeadCommit = shortenSHA(strings.TrimSpace(headOut))
	stats.TotalCommits = atoi(strings.TrimSpace(totalOut))

	authorCounts := countAuthors(authorOut)
	stats.Authors = len(authorCounts)
	stats.TopAuthorShare = topShare(authorCounts)

	if branchErr != nil {
		stats.Branch = "unknown"
	}
	if firstErr == nil {
		if t, err := time.Parse(time.RFC3339, firstNonEmptyLine(firstOut)); err == nil {
			stats.FirstCommitAt = t.UTC()
		}
	}

	// Step 3: history-derived statistics.
	//
	// These four reads are independent of each other and each costs a process
	// spawn, which on Windows dominates the whole analysis: the code walk over
	// this repository is 18ms while these calls are the bulk of a ~375ms run.
	// Running them concurrently replaces the sum of the spawns with the slowest
	// one, and changes no output.
	//
	// Nothing is assigned to stats until after wait(), which supplies the
	// happens-before edge, so no field is written concurrently. The names are
	// prefixed to avoid colliding with the metadata pass above, which has its
	// own authorErr.
	since := time.Now().AddDate(0, 0, -cfg.WindowDays)
	sinceArg := "--since=" + since.Format(time.RFC3339)

	var (
		histNumstat, histWindow, histAuthors, histLast string
		histNumstatErr, histWindowErr                  error
		histAuthorsErr, histLastErr                    error
	)

	wg = newGroup()
	wg.run(func() error {
		// `git log --numstat` gives add/delete counts per file in one pass; no
		// per-commit subprocesses.
		histNumstat, histNumstatErr = run(ctx, root, cfg.Timeout, "log",
			sinceArg,
			"--no-merges",
			"--pretty=format:"+commitSeparator,
			"--numstat",
			"HEAD")
		return histNumstatErr
	})
	wg.run(func() error {
		histWindow, histWindowErr = run(ctx, root, cfg.Timeout, "rev-list",
			"--count", sinceArg, "HEAD")
		return histWindowErr
	})
	wg.run(func() error {
		histAuthors, histAuthorsErr = run(ctx, root, cfg.Timeout, "log",
			sinceArg, "--no-merges", "--format=%aN", "HEAD")
		return histAuthorsErr
	})
	wg.run(func() error {
		histLast, histLastErr = run(ctx, root, 10*time.Second, "log", "-1", "--format=%aI", "HEAD")
		return histLastErr
	})
	// A failure in any of these leaves that field at its zero value rather than
	// aborting, which is the tolerance the parallel metadata pass above already
	// applies: a partial history still yields a usable snapshot.
	_ = wg.wait()

	if histNumstatErr != nil && strings.TrimSpace(histNumstat) == "" && stats.Note == "" {
		stats.Note = "git history unavailable"
	}

	entries, added, deleted := parseNumstat(histNumstat, commitSeparator)
	stats.LinesAdded = added
	stats.LinesDeleted = deleted
	stats.ChurnFiles = len(entries)
	stats.ChurnConcentration = churnConcentration(entries)
	stats.Churn = topChurn(entries, cfg.ChurnLimit)
	stats.ChurnHotspotRate = churnHotspotRate(entries, added+deleted)
	churnByPath := churnLookup(entries)

	if histWindowErr == nil {
		stats.WindowCommits = atoi(strings.TrimSpace(histWindow))
	}

	// Bus factor from per-author commit counts inside the window.
	if histAuthorsErr == nil {
		stats.BusFactor = busFactor(countAuthors(histAuthors), cfg.BusyAuthorShare)
	}

	if histLastErr == nil {
		if t, perr := time.Parse(time.RFC3339, strings.TrimSpace(histLast)); perr == nil {
			stats.LastCommitAt = t.UTC()
			stats.DaysSinceCommit = int(time.Since(t).Hours() / 24)
			if stats.DaysSinceCommit < 0 {
				stats.DaysSinceCommit = 0
			}
		}
	}
	if cfg.WindowDays > 0 {
		stats.CommitsPerWeek = round2(float64(stats.WindowCommits) * 7 / float64(cfg.WindowDays))
	}

	return Result{Stats: stats, Findings: buildFindings(stats, cfg), ChurnByPath: churnByPath}, nil
}

// churnLookup builds the path -> added+deleted map used for hotspot
// classification.
func churnLookup(entries []models.ChurnEntry) map[string]int {
	if len(entries) == 0 {
		return nil
	}
	m := make(map[string]int, len(entries))
	for _, e := range entries {
		m[e.Path] = e.Added + e.Deleted
	}
	return m
}

// commitSeparator is a record delimiter that cannot appear in git output,
// used to split `--numstat` streams into per-commit sections.
const commitSeparator = "\x1eOL\x1f"

// run executes a git command inside dir and returns trimmed stdout.
//
// Stderr is captured only to build a useful error message; stdout parsing never
// depends on it. `--no-pager` and `-c core.quotepath=false` keep output stable
// across environments and prevent interactive pager hangs.
func run(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	full := make([]string, 0, len(args)+3)
	full = append(full, "--no-pager")
	// Deterministic, locale-independent dates and paths.
	full = append(full, "-c", "core.quotepath=false")
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	// Scrub interactive prompting (for example on credential-protected
	// submodules) without discarding the rest of the environment.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), firstLine(msg))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// parseNumstat walks a `--numstat` stream and aggregates per-file churn.
//
// Format: a commit separator line, then one `<added>\t<deleted>\t<path>`
// line per changed file. Binary files report `-` and are skipped.
func parseNumstat(output, separator string) ([]models.ChurnEntry, int, int) {
	type acc struct{ commits, added, deleted int }
	agg := map[string]*acc{}
	var totalAdded, totalDeleted int

	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, separator) || strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		added, aerr := strconv.Atoi(parts[0])
		deleted, derr := strconv.Atoi(parts[1])
		if aerr != nil || derr != nil {
			continue // binary file or rename-only entry
		}
		path := normalizePath(parts[2])
		if path == "" {
			continue
		}
		totalAdded += added
		totalDeleted += deleted
		e := agg[path]
		if e == nil {
			e = &acc{}
			agg[path] = e
		}
		e.commits++
		e.added += added
		e.deleted += deleted
	}

	out := make([]models.ChurnEntry, 0, len(agg))
	for path, e := range agg {
		out = append(out, models.ChurnEntry{
			Path:    path,
			Commits: e.commits,
			Added:   e.added,
			Deleted: e.deleted,
			Score:   e.commits + e.added + e.deleted,
		})
	}
	return out, totalAdded, totalDeleted
}

// topChurn sorts by Score desc, then Added desc, then path asc, and truncates.
func topChurn(entries []models.ChurnEntry, limit int) []models.ChurnEntry {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Score != entries[j].Score {
			return entries[i].Score > entries[j].Score
		}
		if entries[i].Added != entries[j].Added {
			return entries[i].Added > entries[j].Added
		}
		return entries[i].Path < entries[j].Path
	})
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	if entries == nil {
		return []models.ChurnEntry{}
	}
	return entries
}

// churnHotspotRate is the share of total churn concentrated in the three
// busiest files. entries may be in any order.
func churnHotspotRate(entries []models.ChurnEntry, total int) float64 {
	if total <= 0 || len(entries) == 0 {
		return 0
	}
	ranked := append([]models.ChurnEntry(nil), entries...)
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	sum := 0
	for _, e := range ranked[:min(3, len(ranked))] {
		sum += e.Score
	}
	return round2(float64(sum) / float64(total))
}

// churnConcentration is the normalized Herfindahl index of per-file churn
// shares, rescaled so 0 means "evenly spread" and 1 means "one file took
// everything". For a single-file repository the measure is undefined, so it
// returns 1 (maximum concentration) rather than dividing by zero.
//
// The plain top-3 share is misleading on small repositories: with six files,
// three of them always hold at least half the churn regardless of behavior.
// This measure stays calibrated as the file count grows.
func churnConcentration(entries []models.ChurnEntry) float64 {
	n := len(entries)
	if n == 0 {
		return 0
	}
	total := 0
	for _, e := range entries {
		total += e.Score
	}
	if total == 0 {
		return 0
	}
	hhi := 0.0
	for _, e := range entries {
		p := float64(e.Score) / float64(total)
		hhi += p * p
	}
	uniform := 1 / float64(n)
	if n == 1 {
		return 1
	}
	return round2(clamp01((hhi - uniform) / (1 - uniform)))
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// countAuthors returns per-author commit counts from `git log --format=%aN`.
func countAuthors(output string) map[string]int {
	counts := map[string]int{}
	for _, name := range strings.Split(output, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		counts[name]++
	}
	return counts
}

// topShare is the fraction of commits owned by the busiest author.
func topShare(counts map[string]int) float64 {
	total := 0
	best := 0
	for _, n := range counts {
		total += n
		if n > best {
			best = n
		}
	}
	if total == 0 {
		return 0
	}
	return round2(float64(best) / float64(total))
}

// busFactor counts authors whose commit share meets or exceeds threshold.
func busFactor(counts map[string]int, threshold float64) int {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return 0
	}
	factor := 0
	for _, n := range counts {
		if float64(n)/float64(total) >= threshold {
			factor++
		}
	}
	return factor
}

func buildFindings(stats models.GitStats, cfg Config) []models.Finding {
	f := make([]models.Finding, 0, 4)
	if !stats.IsRepository {
		return f
	}
	if stats.TotalCommits == 0 {
		f = append(f, models.Finding{
			Severity: models.SeverityMedium,
			Category: models.CategoryGit,
			Title:    "No commits on HEAD",
			Detail:   "The repository has no reachable commit history.",
		})
		return f
	}
	if stats.DaysSinceCommit > cfg.WindowDays {
		f = append(f, models.Finding{
			Severity: models.SeverityMedium,
			Category: models.CategoryGit,
			Title:    "Stale repository",
			Detail: fmt.Sprintf("Last commit was %d days ago (window is %d days).",
				stats.DaysSinceCommit, cfg.WindowDays),
		})
	}
	if stats.BusFactor <= 1 && stats.WindowCommits >= 10 {
		f = append(f, models.Finding{
			Severity: models.SeverityHigh,
			Category: models.CategoryGit,
			Title:    "Bus factor of 1",
			Detail: fmt.Sprintf("A single author owns >=%d%% of the last %d commits in the window.",
				int(cfg.BusyAuthorShare*100), stats.WindowCommits),
		})
	}
	if stats.ChurnConcentration >= 0.5 && len(stats.Churn) > 0 {
		f = append(f, models.Finding{
			Severity: models.SeverityHigh,
			Category: models.CategoryGit,
			Title:    "Churn concentrated in few files",
			Detail: fmt.Sprintf("Churn is heavily concentrated: the top 3 files hold %.1f%% of window churn across %d changed files.",
				stats.ChurnHotspotRate*100, stats.ChurnFiles),
			Subject: stats.Churn[0].Path,
		})
	}
	if stats.Note != "" {
		f = append(f, models.Finding{
			Severity: models.SeverityInfo,
			Category: models.CategoryGit,
			Title:    "Partial git data",
			Detail:   stats.Note,
		})
	}
	return f
}

// group is a minimal WaitGroup wrapper that records the first non-nil error.
// errgroup would pull in an extra dependency for no functional gain here.
type group struct {
	wg   sync.WaitGroup
	once sync.Once
	err  error
}

func newGroup() *group { return &group{} }

// run executes fn concurrently, recording the first error it returns.
func (g *group) run(fn func() error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if err := fn(); err != nil {
			g.once.Do(func() { g.err = err })
		}
	}()
}

func (g *group) wait() error {
	g.wg.Wait()
	return g.err
}

// normalizePath converts a git path to forward slashes for stable output.
func normalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	return filepath.ToSlash(p)
}

func shortenSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func round2(v float64) float64 {
	scaled := v * 100
	if scaled >= 0 {
		scaled = float64(int64(scaled + 0.5))
	} else {
		scaled = float64(int64(scaled - 0.5))
	}
	return scaled / 100
}
