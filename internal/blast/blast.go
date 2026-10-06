// Package blast estimates which files a change is likely to affect, from the
// repository's own history.
//
// It answers one question: given the files you are about to touch, which other
// files have historically been changed in the same commit? That is co-change
// coupling, and it is measured here rather than guessed at.
//
// What this is not. It is not a call graph, not a type dependency graph, and not
// a prediction. Coupling is an association observed in commits, so it captures
// what changes together, which is not the same as what depends on what. A commit
// that touches two files to bump a version produces the same coupling as a commit
// that refactors one into the other, and this analysis cannot tell them apart.
// Every coupling it reports is a real historical co-change; none of it is a
// guarantee that the files must change together next time.
//
// The method is the one from the software-engineering literature on
// organizational coupling: for a pair of files, the commits touching both
// divided by the commits touching either. A value of 1 means the two files have
// only ever changed together, which is the strongest signal available and is
// still not a rule.
package blast

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
		// 50% of the commits touching either file is the threshold above which
		// two files are treated as coupled. It is high on purpose: a warning
		// that fires on weak evidence trains people to ignore warnings.
		CouplingThreshold: 0.5,
		// A pair seen in fewer than three commits is not evidence of anything.
		// This floor is what keeps a repository with two commits from
		// reporting every pair as perfectly coupled.
		MinSharedCommits: 3,
		// Ten commits is the point past which a co-change ratio stops being
		// dominated by individual coincidences. It is deliberately separate from
		// MinSharedCommits: one governs a single pair's evidence, the other
		// governs whether the history can support any ratio at all.
		MinCommits: 10,
		// Co-change coupling saturates with distance; requiring a 95% shared
		// fraction across every pair would be satisfied by any file touched in
		// every single commit, which is not a meaningful statement.
		MaxDistance: 3,
		Limit:       50,
		Timeout:     30 * time.Second,
	}
}

// Config tunes the analysis.
type Config struct {
	// CouplingThreshold is the minimum shared-commit fraction for a pair to be
	// reported. 0.5 means "half the time you touched one, you touched both".
	CouplingThreshold float64
	// MinSharedCommits is the smallest co-change count that counts as evidence
	// for one pair.
	MinSharedCommits int
	// MinCommits is the repository history depth below which no coupling is
	// reported at all, however many commits any individual pair shares.
	MinCommits int
	// MaxDistance is how far from a changed file to search, in co-change hops.
	MaxDistance int
	// Limit caps how many predicted files are returned.
	Limit int
	// Timeout bounds the git invocation.
	Timeout time.Duration
}

// Pair is one co-change relationship between two files.
type Pair struct {
	// A and B are repository-relative paths. A sorts before B.
	A string `json:"a"`
	B string `json:"b"`
	// SharedCommits counts commits touching both files.
	SharedCommits int `json:"shared_commits"`
	// CommitsA and CommitsB count commits touching each file alone.
	CommitsA int `json:"commits_a"`
	CommitsB int `json:"commits_b"`
	// Coupling is SharedCommits / min(CommitsA, CommitsB), clamped to 1.
	Coupling float64 `json:"coupling"`
}

// Prediction is a file that historically changes alongside the changeset.
type Prediction struct {
	// Path is the predicted file.
	Path string `json:"path"`
	// Coupling is the strongest single-hop coupling that reached it.
	Coupling float64 `json:"coupling"`
	// Via is the changed file that leads to it.
	Via string `json:"via"`
	// Hops from the changeset. 1 means directly co-changed with a file in the
	// changeset; 2 means reached through one intermediate file.
	Hops int `json:"hops"`
	// SharedCommits is the co-change count behind the strongest link.
	SharedCommits int `json:"shared_commits"`
	// InChangeset marks a file that is already part of the changeset, so the
	// caller can tell which edges are internal.
	InChangeset bool `json:"in_changeset"`
}

// Result is the full outcome.
type Result struct {
	// Root is the analyzed directory.
	Root string `json:"root"`
	// Commits is how many commits the coupling was measured over.
	Commits int `json:"commits"`
	// FirstCommitAt and LastCommitAt bound the history. Both zero when the
	// directory is not a repository.
	FirstCommitAt time.Time `json:"first_commit_at"`
	LastCommitAt  time.Time `json:"last_commit_at"`
	// Changeset is the files analysed, in the order given.
	Changeset []string `json:"changeset"`
	// Missing lists changeset files that appeared in no commit at all. A file
	// that has never been committed cannot have a coupling, and saying so is
	// more useful than omitting it.
	Missing []string `json:"missing"`
	// Predictions are the files that historically co-change with the changeset,
	// strongest first.
	Predictions []Prediction `json:"predictions"`
	// Pairs is the coupling table, for callers that want the raw relationships.
	Pairs []Pair `json:"pairs"`
	// InsufficientHistory explains why no coupling was computed, and is empty
	// when the analysis ran.
	//
	// A repository with two commits has no co-change evidence. Every pair in it
	// shares every commit it has, so every pair would look perfectly coupled and
	// the warnings would be pure noise. Rather than print confident numbers
	// derived from nothing, the analysis declines and says what it needs.
	InsufficientHistory string `json:"insufficient_history,omitempty"`
	// Note carries a degradation reason, such as the directory not being a
	// repository.
	Note string `json:"note,omitempty"`
}

// analyze is the whole computation, separated from the git call so it can be
// tested against hand-built commit sets with no repository at all.
func analyze(root string, cfg Config, commits []commitFiles, changeset []string) Result {
	res := Result{Root: root, Commits: len(commits)}
	res.Changeset = dedupeSorted(changeset)

	// Per-file commit counts.
	perFile := map[string]int{}
	for _, c := range commits {
		for _, f := range c.files {
			perFile[f]++
		}
	}

	// Pair counts.
	shared := map[Pair]int{}
	for _, c := range commits {
		// A commit touching the same file twice is one file as far as co-change
		// is concerned; sorting alone would not dedupe a rename that appears
		// under one path twice.
		files := dedupeSorted(c.files)
		for i := 0; i < len(files); i++ {
			for j := i + 1; j < len(files); j++ {
				p := Pair{A: files[i], B: files[j]}
				shared[p]++
			}
		}
	}

	// Keep only pairs with enough evidence and enough coupling.
	for p, n := range shared {
		if n < cfg.MinSharedCommits {
			continue
		}
		ca, cb := perFile[p.A], perFile[p.B]
		if ca == 0 || cb == 0 {
			continue
		}
		// Normalising by min(ca, cb) is what makes the measure meaningful: if
		// one file changes every commit and the other changes once, they share
		// one commit out of one, which is 1.0, and that is honest -- every time
		// the rare file changed, the common one did too.
		p.SharedCommits = n
		p.CommitsA = ca
		p.CommitsB = cb
		p.Coupling = clamp01(float64(n) / float64(minInt(ca, cb)))
		if p.Coupling < cfg.CouplingThreshold {
			continue
		}
		res.Pairs = append(res.Pairs, p)
	}
	sort.Slice(res.Pairs, func(i, j int) bool {
		if res.Pairs[i].Coupling != res.Pairs[j].Coupling {
			return res.Pairs[i].Coupling > res.Pairs[j].Coupling
		}
		if res.Pairs[i].SharedCommits != res.Pairs[j].SharedCommits {
			return res.Pairs[i].SharedCommits > res.Pairs[j].SharedCommits
		}
		if res.Pairs[i].A != res.Pairs[j].A {
			return res.Pairs[i].A < res.Pairs[j].A
		}
		return res.Pairs[i].B < res.Pairs[j].B
	})

	res.Predictions = expand(cfg, res.Changeset, perFile, res.Pairs)
	return res
}

// expand walks the coupling graph outward from the changeset.
//
// Breadth-first, best-first within each hop: a strong direct co-change always
// outranks a weak indirect one, so the list reads as "the most likely files
// first" rather than "whatever the traversal reached first".
// expand walks the coupling graph outward from the changeset.
//
// Seeds are the changeset's own files, never the endpoints of the coupling
// table. Seeding from the table would make every coupled file a zero-hop seed,
// so every file would be excluded as "part of the changeset" and the prediction
// list would always be empty.
func expand(cfg Config, changeset []string, perFile map[string]int, pairs []Pair) []Prediction {
	if len(pairs) == 0 || len(changeset) == 0 {
		return nil
	}
	changeSet := map[string]bool{}
	for _, f := range changeset {
		changeSet[f] = true
	}

	// Adjacency, built once.
	adj := map[string][]Pair{}
	for _, p := range pairs {
		adj[p.A] = append(adj[p.A], p)
		adj[p.B] = append(adj[p.B], p)
	}

	type node struct {
		path  string
		hops  int
		coup  float64
		via   string
		shard int
	}
	best := map[string]node{}
	queue := []node{}

	// Seed with the changeset files that actually appear in the coupling table.
	seeds := make([]string, 0, len(changeSet))
	for f := range changeSet {
		seeds = append(seeds, f)
	}
	sort.Strings(seeds)
	for _, f := range seeds {
		queue = append(queue, node{path: f, hops: 0, coup: 1, via: "", shard: perFile[f]})
	}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		// A path already reached at a better rank is not re-expanded. Ranked by
		// hops first, then coupling: one hop with coupling 0.5 is more useful
		// than two hops with coupling 1.0.
		if prev, seen := best[cur.path]; seen {
			if prev.hops < cur.hops ||
				(prev.hops == cur.hops && prev.coup >= cur.coup) {
				continue
			}
		}
		best[cur.path] = cur

		if cur.hops >= cfg.MaxDistance {
			continue
		}
		for _, p := range adj[cur.path] {
			next := p.A
			if next == cur.path {
				next = p.B
			}
			if next == "" {
				continue
			}
			queue = append(queue, node{
				path: next, hops: cur.hops + 1, coup: p.Coupling,
				via: cur.path, shard: p.SharedCommits,
			})
		}
	}

	out := make([]Prediction, 0, len(best))
	for path, n := range best {
		if n.hops == 0 {
			continue // a changeset file, not a prediction
		}
		out = append(out, Prediction{
			Path: path, Coupling: n.coup, Via: n.via, Hops: n.hops,
			SharedCommits: n.shard, InChangeset: false,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hops != out[j].Hops {
			return out[i].Hops < out[j].Hops
		}
		if out[i].Coupling != out[j].Coupling {
			return out[i].Coupling > out[j].Coupling
		}
		if out[i].SharedCommits != out[j].SharedCommits {
			return out[i].SharedCommits > out[j].SharedCommits
		}
		return out[i].Path < out[j].Path
	})

	if len(out) > cfg.Limit {
		out = out[:cfg.Limit]
	}
	return out
}

// commitFiles is one commit and the files it touched.
type commitFiles struct {
	sha   string
	files []string
}

// Analyze measures co-change coupling for a changeset.
//
// changeset is the files the change touches. They may or may not exist on disk:
// a proposed file that was never committed simply has no coupling, and is
// reported in Result.Missing rather than dropped.
func Analyze(ctx context.Context, root string, changeset []string, cfg Config) (Result, error) {
	if cfg.MaxDistance < 1 {
		cfg.MaxDistance = 1
	}
	if cfg.MinSharedCommits < 1 {
		cfg.MinSharedCommits = 1
	}

	commits, first, last, err := readCommits(ctx, root, cfg.Timeout)
	if err != nil {
		return Result{Root: root, Note: err.Error()}, err
	}

	res := analyze(root, cfg, commits, changeset)
	res.FirstCommitAt = first
	res.LastCommitAt = last

	// Files with no history at all cannot have a coupling.
	touched := map[string]bool{}
	for _, c := range commits {
		for _, f := range c.files {
			touched[f] = true
		}
	}
	for _, f := range res.Changeset {
		if !touched[f] {
			res.Missing = append(res.Missing, f)
		}
	}
	sort.Strings(res.Missing)

	res.InsufficientHistory = historyVerdict(res.Commits, cfg)
	return res, nil
}

// PendingChanges lists the files changed or added but not yet committed.
//
// It is the default changeset, because "what am I about to commit" is what the
// question is actually about. Two commands are needed and neither covers the
// other half:
//
//   - `git diff --name-only HEAD` for tracked files, staged or unstaged. The
//     `HEAD` form is what makes it one call instead of two; comparing against
//     the index alone would miss everything unstaged, which is most of what a
//     developer is working on.
//   - `git ls-files --others --exclude-standard` for new files. A brand new file
//     is exactly the case with no history, and omitting it would quietly drop
//     the files most likely to need attention.
//
// It deliberately does not fall back to anything when git fails. A caller that
// cannot determine the changeset should say so rather than analyze the whole
// repository and present it as a review of the current change.
func PendingChanges(ctx context.Context, root string, timeout time.Duration) ([]string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not found: a pending changeset is read from git")
	}

	runOne := func(args ...string) ([]string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
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
			return nil, fmt.Errorf("git %s timed out after %s", args[0], timeout)
		}
		if err != nil {
			return nil, err
		}
		var files []string
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				files = append(files, line)
			}
		}
		return files, nil
	}

	tracked, err := runOne("diff", "--name-only", "HEAD")
	if err != nil {
		if isUnavailableRepo(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("git diff: %s", gitFailureText(err))
	}

	// A repository with no commits has no HEAD to diff against, which is the
	// one-commit case `ls-files` still answers correctly.
	untracked, err := runOne("ls-files", "--others", "--exclude-standard")
	if err != nil && !isUnavailableRepo(err) {
		return nil, fmt.Errorf("git ls-files: %s", gitFailureText(err))
	}

	return dedupeSorted(append(tracked, untracked...)), nil
}

// historyVerdict decides whether the coupling is worth reporting.
//
// A coupling ratio is shared/min(a,b). With very few commits that ratio is
// dominated by coincidence: over four commits a pair either shares most of them
// or none, and the result looks categorical whether or not any real relationship
// exists. Below the configured depth the analysis declines and says what it
// needs, because a confident number derived from four commits is worse than no
// number.
func historyVerdict(commits int, cfg Config) string {
	if commits == 0 {
		return "no commits found: nothing to measure co-change over"
	}
	if commits < cfg.MinCommits {
		return fmt.Sprintf(
			"only %d commit(s) of history, and co-change coupling needs at least %d "+
				"before the ratio distinguishes a real relationship from a "+
				"coincidence. No coupling is reported. This is a limit of the data, "+
				"not a finding about the code.", commits, cfg.MinCommits)
	}
	return ""
}

// readCommits pulls every commit and its touched files in one pass.
//
// One `git log --name-only` rather than a `git log -- <file>` per file: the
// per-file form is O(files) subprocesses, which on a repository of any size is
// the difference between a second and a minute.
func readCommits(ctx context.Context, root string, timeout time.Duration) (
	[]commitFiles, time.Time, time.Time, error,
) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, time.Time{}, time.Time{},
			fmt.Errorf("git not found: co-change coupling is read from git history")
	}

	// A NUL-separated record format makes parsing exact. A newline-separated one
	// cannot express a path containing a space without ambiguity, and
	// repositories do contain files with spaces in their names.
	args := []string{
		"log", "--no-merges", "--name-only", "--pretty=format:%x00%H%x00%aI",
	}
	cmd := exec.CommandContext(ctx, "git", args...)
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
		return nil, time.Time{}, time.Time{}, fmt.Errorf("git log timed out after %s", timeout)
	}
	if err != nil {
		// A directory with no history exits non-zero. That is a valid state to
		// analyze, and the caller reports it rather than treating it as failure.
		if isUnavailableRepo(err) {
			return nil, time.Time{}, time.Time{}, nil
		}
		return nil, time.Time{}, time.Time{}, fmt.Errorf("git log: %w", err)
	}

	commits, first, last := parseLogNUL(out)
	return commits, first, last, nil
}

// isUnavailableRepo reports whether git's exit status means "there is no
// history to read" rather than "the analysis failed".
//
// Both cases are legitimate states for a caller to handle: a directory that is
// not a repository, and a repository with no commits yet. Neither is an error,
// and treating either as one would make `blast` fail on a plain source
// directory when the honest answer is simply "no coupling available".
func isUnavailableRepo(err error) bool {
	// Lower-cased because git is not consistent about it: `git log` says
	// "not a git repository" while `git diff` says "Not a git repository. Use
	// --no-index". Matching case-sensitively caught one and missed the other.
	msg := strings.ToLower(gitFailureText(err))
	return strings.Contains(msg, "does not have any commits yet") ||
		strings.Contains(msg, "unknown revision") ||
		strings.Contains(msg, "bad revision") ||
		strings.Contains(msg, "not a git repository")
}

// gitFailureText returns everything known about a failed git invocation.
//
// err.Error() alone is only "exit status 128": the message git actually printed
// goes to stderr, and *exec.ExitError carries it in its own Stderr field. An
// earlier version matched on err.Error(), which never contained any of the
// phrases, so every "not a git repository" was misreported as a hard failure.
func gitFailureText(err error) string {
	if err == nil {
		return ""
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return string(ee.Stderr) + " " + err.Error()
	}
	return err.Error()
}

// parseLogNUL splits the NUL-delimited log into commits.
//
// Record shape: \x00<sha>\x00<author-date>\n<path>\n<path>\n...
// The leading NUL separates one record's paths from the next record's header,
// which is what makes this unambiguous.
func parseLogNUL(raw []byte) ([]commitFiles, time.Time, time.Time) {
	var commits []commitFiles
	var first, last time.Time

	// Indexed rather than strings.Split. Splitting on NUL puts the sha and the
	// date in *separate* pieces, because the NUL between them is itself a
	// separator: the stream is "", sha, "date\npaths", sha, "date\npaths". A
	// split-based parser looks for the header separator inside a piece that no
	// longer has one and silently yields zero commits.
	//
	// A path can never contain NUL, and a date never does either, so walking the
	// three delimiters in order is unambiguous.
	s := string(raw)
	for {
		i := strings.IndexByte(s, 0)
		if i < 0 {
			return commits, first, last
		}
		s = s[i+1:] // past the record's leading NUL

		j := strings.IndexByte(s, 0)
		if j < 0 {
			return commits, first, last
		}
		sha := strings.TrimSpace(s[:j])
		s = s[j+1:]

		k := strings.IndexByte(s, '\n')
		if k < 0 {
			return commits, first, last
		}
		date := strings.TrimSpace(s[:k])
		s = s[k+1:]

		// Paths run until the next NUL, which is the next record's marker.
		body := s
		if n := strings.IndexByte(s, 0); n >= 0 {
			body = s[:n]
			s = s[n:]
		} else {
			s = ""
		}

		var files []string
		for _, line := range strings.Split(body, "\n") {
			line = strings.TrimSpace(line)
			// Git prints a lone blank line between records, and an empty line is
			// never a path.
			if line != "" {
				files = append(files, line)
			}
		}
		// A commit with no file is a real thing (an empty commit) but carries no
		// co-change evidence, so it is skipped rather than stored as an empty
		// record.
		if sha == "" || len(files) == 0 {
			if len(s) == 0 {
				return commits, first, last
			}
			continue
		}

		commits = append(commits, commitFiles{sha: sha, files: dedupeSorted(files)})
		if ts, err := time.Parse(time.RFC3339, date); err == nil {
			if first.IsZero() || ts.Before(first) {
				first = ts
			}
			if last.IsZero() || ts.After(last) {
				last = ts
			}
		}
		if len(s) == 0 {
			return commits, first, last
		}
	}
}

// dedupeSorted returns a sorted, duplicate-free copy.
//
// Sorting is what makes the pair keys stable: a pair is stored under the
// (min, max) ordering, and unsorted input would produce both (A,B) and (B,A) as
// separate keys, halving every shared-commit count.
func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
