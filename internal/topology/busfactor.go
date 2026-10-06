package topology

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/zelvior/lensyxe/internal/gitlog"
)

// BusFactorConfig tunes the ownership analysis.
type BusFactorConfig struct {
	// HalfLifeDays is the age at which a commit counts half as much as one made
	// today. This is the recency decay: ownership of a file that changes often
	// belongs to whoever changes it *now*, not to whoever wrote it first.
	HalfLifeDays float64
	// RiskShare is the ownership share above which one author counts as
	// controlling a file.
	RiskShare float64
	// StaleDays is how long an author may be absent before a file they control
	// is flagged at risk.
	StaleDays int
	// Limit caps how many files the at-risk list holds.
	Limit int
}

// DefaultBusFactorConfig returns the settings used when a caller supplies none.
//
// The 90-day half-life and the 90-day staleness threshold are deliberately the
// same span: a contributor who has not committed in three months no longer holds
// meaningful ownership of anything, whatever the arithmetic says about it.
func DefaultBusFactorConfig() BusFactorConfig {
	return BusFactorConfig{
		HalfLifeDays: 90,
		RiskShare:    0.7,
		StaleDays:    90,
		Limit:        25,
	}
}

// Ownership is one author's weighted share of a file or directory.
type Ownership struct {
	// Author is the name as recorded by git. Two spellings of one person read as
	// two owners, which understates the risk rather than overstating it.
	Author string `json:"author"`
	// Commits is the raw commit count.
	Commits int `json:"commits"`
	// WeightedShare is the recency-decayed share, 0..1.
	WeightedShare float64 `json:"weighted_share"`
	// RawShare is the unweighted commit share, reported so the effect of decay
	// is visible rather than something the reader has to infer.
	RawShare float64 `json:"raw_share"`
	// LastCommit is when this author last touched the file.
	LastCommit time.Time `json:"last_commit"`
}

// FileOwnership is the ownership breakdown for one file.
type FileOwnership struct {
	Path          string      `json:"path"`
	Boundary      string      `json:"boundary"`
	Commits       int         `json:"commits"`
	Authors       []Ownership `json:"authors"`
	Dominant      Ownership   `json:"dominant"`
	DominantShare float64     `json:"dominant_share"`
	// BusFactor is how many authors are needed to reach RiskShare of the
	// weighted ownership. Zero means nobody has touched the file.
	BusFactor int `json:"bus_factor"`
	// AtRisk is true when one author holds more than RiskShare *and* has not
	// committed in StaleDays. Either condition alone is not a risk: a file can
	// be single-author and actively maintained.
	AtRisk     bool      `json:"at_risk"`
	RiskReason string    `json:"risk_reason,omitempty"`
	LastCommit time.Time `json:"last_commit"`
	// InactiveDays is how long since the file last changed.
	InactiveDays int `json:"inactive_days"`
}

// DirectoryOwnership aggregates the files in one directory.
type DirectoryOwnership struct {
	Path          string      `json:"path"`
	Boundary      string      `json:"boundary"`
	Files         int         `json:"files"`
	Commits       int         `json:"commits"`
	Authors       []Ownership `json:"authors"`
	Dominant      Ownership   `json:"dominant"`
	DominantShare float64     `json:"dominant_share"`
	BusFactor     int         `json:"bus_factor"`
	AtRisk        bool        `json:"at_risk"`
	RiskReason    string      `json:"risk_reason,omitempty"`
}

// BusFactorResult is the whole outcome.
type BusFactorResult struct {
	Root          string               `json:"root"`
	Files         []FileOwnership      `json:"files"`
	Directories   []DirectoryOwnership `json:"directories"`
	Authors       []Ownership          `json:"authors"`
	Commits       int                  `json:"commits"`
	FirstCommitAt time.Time            `json:"first_commit_at"`
	LastCommitAt  time.Time            `json:"last_commit_at"`
	// StaleFiles are the files flagged at risk, strongest first.
	StaleFiles []FileOwnership `json:"stale_files"`
	Note       string          `json:"note,omitempty"`
	// InsufficientHistory explains why the at-risk verdict is withheld, and is
	// empty when it is offered.
	//
	// Ownership weighted by recency needs commits spread over time. With a day
	// of history every commit carries the same weight, the decay does nothing,
	// and "recent owner" means only "whoever committed last" -- an artefact of
	// the clock rather than a fact about the repository. The shares are still
	// reported. What is withheld is the verdict.
	InsufficientHistory string `json:"insufficient_history,omitempty"`
}

// MinHistoryDaysForRisk is the history depth below which the staleness verdict is
// withheld.
//
// It is not about whether the numbers are computable -- they are -- but about
// whether "has not committed in 90 days" means anything when the entire history
// is a few hours long. It does not: everyone satisfies it trivially.
const MinHistoryDaysForRisk = 30

// accumulator tallies weighted ownership per author for one file, directory, or
// the repository as a whole.
type accumulator struct {
	// counts is the raw commit count per author.
	counts map[string]int
	// weighted is the recency-decayed commit weight per author.
	weighted map[string]float64
	// last is each author's most recent commit.
	last map[string]time.Time
	// files counts distinct files, used by the directory rollup.
	files map[string]bool
}

func newAccumulator() *accumulator {
	return &accumulator{
		counts:   map[string]int{},
		weighted: map[string]float64{},
		last:     map[string]time.Time{},
		files:    map[string]bool{},
	}
}

func (a *accumulator) add(author string, weight float64, when time.Time) {
	a.counts[author]++
	a.weighted[author] += weight
	if when.After(a.last[author]) {
		a.last[author] = when
	}
}

func (a *accumulator) addFile(f string) { a.files[f] = true }

// total returns the summed decay weight, which is the denominator for shares.
func (a *accumulator) total() float64 {
	t := 0.0
	for _, w := range a.weighted {
		t += w
	}
	return t
}

// rawCommits returns the summed commit count.
func (a *accumulator) rawCommits() int {
	t := 0
	for _, n := range a.counts {
		t += n
	}
	return t
}

// BusFactor computes recency-weighted ownership per file and directory.
//
// now is injected rather than read from the clock, so the result is reproducible
// and testable. A metric whose value depends on when it was run cannot be
// compared between runs, which is the one thing a comparison tool must do.
func BusFactor(ctx context.Context, root string, cfg BusFactorConfig, now time.Time) (BusFactorResult, error) {
	commits, err := gitlog.Reader{
		Root:    root,
		Timeout: 30 * time.Second,
		Options: gitlog.Options{
			WantSHA:    true,
			WantEmail:  true,
			SkipMerges: true,
		},
	}.Read(ctx)
	if err != nil {
		return BusFactorResult{Root: root, Note: err.Error()}, err
	}
	return AggregateBusFactor(root, cfg, commits, now), nil
}

// AggregateBusFactor is the pure computation over already-read commits.
//
// It is exported so the coupling analysis can reuse the same reading and so the
// arithmetic can be tested against hand-built histories with no repository.
func AggregateBusFactor(root string, cfg BusFactorConfig, commits []gitlog.Commit, now time.Time) BusFactorResult {
	res := BusFactorResult{Root: root, Commits: len(commits)}

	files := map[string]*accumulator{}
	dirs := map[string]*accumulator{}
	repo := newAccumulator()

	for _, c := range commits {
		if c.When.IsZero() {
			continue
		}
		if res.FirstCommitAt.IsZero() || c.When.Before(res.FirstCommitAt) {
			res.FirstCommitAt = c.When
		}
		if res.LastCommitAt.IsZero() || c.When.After(res.LastCommitAt) {
			res.LastCommitAt = c.When
		}

		w := DecayWeight(c.When, now, cfg.HalfLifeDays)

		// A commit is attributed to its author once at the repository level.
		repo.add(c.Author, w, c.When)

		// Per file, a commit counts at most once even if it lists the same path
		// twice; a rename that appears under one path must not double an
		// author's ownership of it.
		seen := map[string]bool{}
		for _, f := range c.Files {
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true

			fa, ok := files[f]
			if !ok {
				fa = newAccumulator()
				files[f] = fa
			}
			fa.add(c.Author, w, c.When)
			fa.addFile(f)

			d := dirOf(f)
			da, ok := dirs[d]
			if !ok {
				da = newAccumulator()
				dirs[d] = da
			}
			da.add(c.Author, w, c.When)
			da.addFile(f)
		}
	}

	historyDays := 0
	if !res.FirstCommitAt.IsZero() {
		historyDays = daysSince(res.FirstCommitAt, now)
	}
	riskKnown := res.Commits == 0 || historyDays >= MinHistoryDaysForRisk

	for p, a := range files {
		res.Files = append(res.Files, finishFile(p, a, cfg, now, riskKnown))
	}
	for d, a := range dirs {
		res.Directories = append(res.Directories, finishDir(d, a, cfg, now, riskKnown))
	}
	res.Authors = rankAuthors(repo)

	sort.SliceStable(res.Files, func(i, j int) bool {
		if res.Files[i].DominantShare != res.Files[j].DominantShare {
			return res.Files[i].DominantShare > res.Files[j].DominantShare
		}
		return res.Files[i].Path < res.Files[j].Path
	})
	sort.SliceStable(res.Directories, func(i, j int) bool {
		if res.Directories[i].DominantShare != res.Directories[j].DominantShare {
			return res.Directories[i].DominantShare > res.Directories[j].DominantShare
		}
		return res.Directories[i].Path < res.Directories[j].Path
	})

	for _, f := range res.Files {
		if f.AtRisk {
			res.StaleFiles = append(res.StaleFiles, f)
		}
	}
	if len(res.StaleFiles) > cfg.Limit {
		res.StaleFiles = res.StaleFiles[:cfg.Limit]
	}

	if res.Commits == 0 {
		res.Note = "no commits found: nothing to attribute"
	} else if !riskKnown {
		res.InsufficientHistory = fmt.Sprintf(
			"history spans %d day(s); a %d-day recency decay and a %d-day "+
				"staleness threshold cannot distinguish an absent maintainer from "+
				"a repository that did not exist yet. Ownership shares are reported; "+
				"the at-risk verdict is withheld. This is a limit of the data, not a "+
				"finding that the repository is unowned.",
			historyDays, int(cfg.HalfLifeDays), cfg.StaleDays)
	}
	return res
}

func finishFile(path string, a *accumulator, cfg BusFactorConfig, now time.Time, riskKnown bool) FileOwnership {
	authors := rankAuthors(a)
	fo := FileOwnership{
		Path: path, Boundary: boundaryOf(path),
		Commits: a.rawCommits(), Authors: authors,
	}
	if len(authors) > 0 {
		fo.Dominant = authors[0]
		fo.DominantShare = authors[0].WeightedShare
		fo.BusFactor = busFactorOf(authors, cfg.RiskShare)
		fo.LastCommit = authors[0].LastCommit
	}
	fo.InactiveDays = daysSince(fo.LastCommit, now)
	if riskKnown {
		fo.AtRisk, fo.RiskReason = riskOf(authors, cfg, now)
	}
	return fo
}

func finishDir(dir string, a *accumulator, cfg BusFactorConfig, now time.Time, riskKnown bool) DirectoryOwnership {
	authors := rankAuthors(a)
	do := DirectoryOwnership{
		Path: dir, Boundary: boundaryOf(dir + "/x"),
		Files: len(a.files), Commits: a.rawCommits(), Authors: authors,
	}
	if len(authors) > 0 {
		do.Dominant = authors[0]
		do.DominantShare = authors[0].WeightedShare
		do.BusFactor = busFactorOf(authors, cfg.RiskShare)
	}
	if riskKnown {
		do.AtRisk, do.RiskReason = riskOf(authors, cfg, now)
	}
	return do
}

// rankAuthors converts an accumulator into a ranked ownership list.
func rankAuthors(a *accumulator) []Ownership {
	total := a.total()
	raw := a.rawCommits()

	out := make([]Ownership, 0, len(a.weighted))
	for author, w := range a.weighted {
		share, rawShare := 0.0, 0.0
		if total > 0 {
			share = w / total
		}
		if raw > 0 {
			rawShare = float64(a.counts[author]) / float64(raw)
		}
		out = append(out, Ownership{
			Author: author, Commits: a.counts[author],
			WeightedShare: round4(share), RawShare: round4(rawShare),
			LastCommit: a.last[author],
		})
	}
	// Strongest first, then commit count, then name: a total order, so the
	// output is byte-identical between runs.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].WeightedShare != out[j].WeightedShare {
			return out[i].WeightedShare > out[j].WeightedShare
		}
		if out[i].Commits != out[j].Commits {
			return out[i].Commits > out[j].Commits
		}
		return out[i].Author < out[j].Author
	})
	return out
}

// busFactorOf counts how many authors are needed to reach the threshold.
//
// The shares sum to one, so the loop always returns; the fallthrough guards
// against float rounding leaving the total a hair under one.
func busFactorOf(authors []Ownership, threshold float64) int {
	if len(authors) == 0 {
		return 0
	}
	if threshold <= 0 {
		return 1
	}
	acc := 0.0
	for i, a := range authors {
		acc += a.WeightedShare
		if acc >= threshold {
			return i + 1
		}
	}
	return len(authors)
}

// riskOf decides whether one author both dominates and has gone quiet.
//
// Both conditions are required. A file can be single-author and actively
// maintained, which is not a risk, and a file can have an author who left while
// others keep working on it, which is a fact about that person rather than about
// the file.
func riskOf(authors []Ownership, cfg BusFactorConfig, now time.Time) (bool, string) {
	if len(authors) == 0 {
		return false, ""
	}
	top := authors[0]
	if top.WeightedShare < cfg.RiskShare {
		return false, ""
	}
	quiet := daysSince(top.LastCommit, now)
	if quiet < cfg.StaleDays {
		return false, ""
	}
	return true, fmt.Sprintf(
		"%s holds %.0f%% of the weighted history and has not committed here in %d days",
		top.Author, top.WeightedShare*100, quiet)
}

// ln2 is the natural logarithm of two, which is what makes the decay an actual
// half-life rather than merely an exponential.
const ln2 = 0.6931471805599453

// DecayWeight is the exponential recency weight of a commit.
//
// exp(-ln2 * age / halfLife): a commit made today weighs 1, one made exactly one
// half-life ago weighs exactly 0.5, and one a year old with a 90-day half-life
// weighs about 0.004. It never reaches zero, so old work stays visible -- just
// discounted.
//
// The ln2 factor is what an earlier version omitted, writing exp(-age/halfLife)
// instead. That decayed by a factor of e per half-life and gave 0.368 where the
// name promised 0.5, so every share computed from it was quietly wrong by a
// constant. A parameter named for what it does has to actually do it.
func DecayWeight(when, now time.Time, halfLifeDays float64) float64 {
	if halfLifeDays <= 0 {
		return 1
	}
	ageDays := now.Sub(when).Hours() / 24
	if ageDays <= 0 {
		return 1
	}
	return math.Exp(-ln2 * ageDays / halfLifeDays)
}

func daysSince(t, now time.Time) int {
	if t.IsZero() {
		return 0
	}
	d := int(now.Sub(t).Hours() / 24)
	if d < 0 {
		return 0
	}
	return d
}

// dirOf returns the directory a file belongs to, repository-relative.
func dirOf(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return "."
	}
	return p[:i]
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }
