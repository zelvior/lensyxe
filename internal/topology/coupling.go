package topology

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/zelvior/lensyxe/internal/gitlog"
)

// CouplingConfig tunes the co-change analysis.
type CouplingConfig struct {
	// Threshold is the minimum shared-commit fraction for a pair to be reported.
	Threshold float64
	// MinSharedCommits is the smallest co-change count that counts as evidence.
	// One commit is not a pattern; it is a coincidence with a timestamp.
	MinSharedCommits int
	// MinCommits is the history depth below which nothing is reported at all.
	MinCommits int
	// Limit caps how many pairs are returned.
	Limit int
}

// DefaultCouplingConfig returns the settings used when a caller supplies none.
//
// The 0.6 threshold is the one the feature was specified with and it is high on
// purpose: a warning that fires on weak evidence trains people to ignore
// warnings, which is the opposite of what this is for.
func DefaultCouplingConfig() CouplingConfig {
	return CouplingConfig{
		Threshold:        0.6,
		MinSharedCommits: 3,
		MinCommits:       10,
		Limit:            40,
	}
}

// CouplingPair is one hidden co-change relationship.
//
// "Hidden" means the two files do not import each other. Co-change without an
// import is the interesting case: there is no compiler-enforced reason for the
// two files to move together, so the coupling lives in a convention or a review
// habit that nothing enforces.
type CouplingPair struct {
	A string `json:"a"`
	B string `json:"b"`
	// SharedCommits is how many commits touched both.
	SharedCommits int `json:"shared_commits"`
	// CommitsA and CommitsB are the individual commit counts.
	CommitsA int `json:"commits_a"`
	CommitsB int `json:"commits_b"`
	// Coupling is SharedCommits / min(CommitsA, CommitsB), in 0..1.
	Coupling float64 `json:"coupling"`
	// SamePackage reports that the two files are in one package, which is a
	// different situation from coupling across packages.
	SamePackage bool `json:"same_package"`
	// SameBoundary reports that they are in one architectural region.
	SameBoundary bool `json:"same_boundary"`
	// Imports is true when one file's package imports the other's. Such a pair
	// is excluded from HiddenPairs, because a compiler already enforces it.
	Imports bool `json:"imports"`
	// FirstShared and LastShared bound the commits the pair co-occurred in.
	FirstShared time.Time `json:"first_shared"`
	LastShared  time.Time `json:"last_shared"`
}

// CouplingResult is the whole outcome.
type CouplingResult struct {
	Root string `json:"root"`
	// Pairs are every pair above the coupling threshold, strongest first.
	Pairs []CouplingPair `json:"pairs"`
	// HiddenPairs are the subset with no import relationship. This is the list
	// the command reports as a warning; the rest is available for context.
	HiddenPairs []CouplingPair `json:"hidden_pairs"`
	// HiddenFiles lists the files appearing in a hidden pair, so a reviewer can
	// see the affected surface rather than only the edges.
	HiddenFiles   []string  `json:"hidden_files"`
	Commits       int       `json:"commits"`
	FirstCommitAt time.Time `json:"first_commit_at"`
	LastCommitAt  time.Time `json:"last_commit_at"`
	// FilesWithImportGraph counts files whose import relationship is known. It is
	// the denominator for the "no import" claim: a pair can only be called
	// unimported if both files were parsed.
	FilesWithImportGraph int    `json:"files_with_import_graph"`
	Note                 string `json:"note,omitempty"`
	// InsufficientHistory explains why nothing is reported, and is empty when it is.
	InsufficientHistory string `json:"insufficient_history,omitempty"`
}

// Coupling finds pairs of files that co-occur in commits without importing each
// other.
//
// graph supplies the import relationships. It may be nil, in which case no pair
// can be labelled hidden and HiddenPairs is empty while Pairs is still populated:
// reporting "no import relationship" without having read the imports would be a
// claim about files nobody parsed.
func Coupling(
	ctx context.Context,
	root string,
	cfg CouplingConfig,
	graph *Graph,
) (CouplingResult, error) {
	commits, err := gitlog.Reader{
		Root:    root,
		Timeout: 30 * time.Second,
		Options: gitlog.Options{SkipMerges: true},
	}.Read(ctx)
	if err != nil {
		return CouplingResult{Root: root, Note: err.Error()}, err
	}
	return AggregateCoupling(root, cfg, commits, graph), nil
}

// AggregateCoupling is the pure computation over already-read commits.
func AggregateCoupling(
	root string,
	cfg CouplingConfig,
	commits []gitlog.Commit,
	graph *Graph,
) CouplingResult {
	res := CouplingResult{Root: root, Commits: len(commits)}
	if len(commits) == 0 {
		res.Note = "no commits found: nothing to measure co-change over"
		res.InsufficientHistory = "no commits to measure co-change over"
		return res
	}

	for _, c := range commits {
		if res.FirstCommitAt.IsZero() || c.When.Before(res.FirstCommitAt) {
			res.FirstCommitAt = c.When
		}
		if res.LastCommitAt.IsZero() || c.When.After(res.LastCommitAt) {
			res.LastCommitAt = c.When
		}
	}

	// Per-file commit counts.
	perFile := map[string]int{}
	pkgOf := map[string]string{}
	type pairKey struct{ a, b string }
	shared := map[pairKey]*CouplingPair{}

	for _, c := range commits {
		seen := map[string]bool{}
		var files []string
		for _, f := range c.Files {
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			files = append(files, f)
			perFile[f]++
		}
		sort.Strings(files)

		for i := 0; i < len(files); i++ {
			for j := i + 1; j < len(files); j++ {
				k := pairKey{files[i], files[j]}
				p, ok := shared[k]
				if !ok {
					p = &CouplingPair{A: k.a, B: k.b}
					shared[k] = p
				}
				p.SharedCommits++
				if p.FirstShared.IsZero() || c.When.Before(p.FirstShared) {
					p.FirstShared = c.When
				}
				if c.When.After(p.LastShared) {
					p.LastShared = c.When
				}
			}
		}
	}

	for k, p := range shared {
		if p.SharedCommits < cfg.MinSharedCommits {
			continue
		}
		ca, cb := perFile[k.a], perFile[k.b]
		if ca == 0 || cb == 0 {
			continue
		}
		den := ca
		if cb < den {
			den = cb
		}
		p.CommitsA, p.CommitsB = ca, cb
		p.Coupling = clamp01(float64(p.SharedCommits) / float64(den))
		if p.Coupling < cfg.Threshold {
			continue
		}
		p.SameBoundary = boundaryOf(k.a) == boundaryOf(k.b)

		if graph != nil {
			pkgOf[k.a] = pkgOfPath(graph, k.a)
			pkgOf[k.b] = pkgOfPath(graph, k.b)
			p.Imports = importsEitherWay(graph, pkgOf[k.a], pkgOf[k.b])
			p.SamePackage = pkgOf[k.a] != "" && pkgOf[k.a] == pkgOf[k.b]
			if p.Imports {
				res.FilesWithImportGraph++
			}
		}
		res.Pairs = append(res.Pairs, *p)
	}

	sortPairs(res.Pairs)

	// Hidden means: co-changed strongly, and no import relationship was found.
	// Without a parsed graph this set is empty rather than "everything", because
	// the claim is only meaningful once both files have been read.
	if graph != nil {
		hidden := map[string]bool{}
		for _, p := range res.Pairs {
			if p.Imports {
				continue
			}
			res.HiddenPairs = append(res.HiddenPairs, p)
			hidden[p.A] = true
			hidden[p.B] = true
		}
		res.HiddenFiles = sortedKeys(hidden)
	}

	if res.Commits < cfg.MinCommits {
		res.Pairs = nil
		res.HiddenPairs = nil
		res.InsufficientHistory = fmt.Sprintf(
			"only %d commit(s) of history, and co-change coupling needs at "+
				"least %d before the ratio distinguishes a real relationship from "+
				"a coincidence. No coupling is reported. This is a limit of the "+
				"data, not a finding about the code.", res.Commits, cfg.MinCommits)
	}
	if len(res.Pairs) > cfg.Limit {
		res.Pairs = res.Pairs[:cfg.Limit]
	}
	if len(res.HiddenPairs) > cfg.Limit {
		res.HiddenPairs = res.HiddenPairs[:cfg.Limit]
	}
	return res
}

// pkgOfPath finds the import path of the package containing a file.
//
// The graph is keyed by package, not by file, so this resolves through the
// boundary map by matching the file's directory against the known packages.
func pkgOfPath(g *Graph, file string) string {
	if g == nil || g.Module == "" {
		return ""
	}
	dir := dirOf(file)
	if dir == "." {
		return g.Module
	}
	candidate := g.Module + "/" + dir
	if _, ok := g.Packages[candidate]; ok {
		return candidate
	}
	// A test file or a nested package: walk up until a known package matches.
	for {
		i := lastSlash(dir)
		if i < 0 {
			return ""
		}
		dir = dir[:i]
		candidate = g.Module + "/" + dir
		if _, ok := g.Packages[candidate]; ok {
			return candidate
		}
	}
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}

// importsEitherWay reports whether either package imports the other.
//
// Only a direct import counts. A transitive dependency through an intermediate
// package is a real relationship but not a direct one, and claiming otherwise
// would mislabel a pair as unimported when a compiler does in fact tie them.
func importsEitherWay(g *Graph, a, b string) bool {
	if g == nil || a == "" || b == "" {
		return false
	}
	if a == b {
		// Same package: no import statement is involved at all.
		return true
	}
	for _, imp := range g.Imports[a] {
		if imp == b {
			return true
		}
	}
	for _, imp := range g.Imports[b] {
		if imp == a {
			return true
		}
	}
	return false
}

func sortPairs(pairs []CouplingPair) {
	sort.SliceStable(pairs, func(i, j int) bool {
		if pairs[i].Coupling != pairs[j].Coupling {
			return pairs[i].Coupling > pairs[j].Coupling
		}
		if pairs[i].SharedCommits != pairs[j].SharedCommits {
			return pairs[i].SharedCommits > pairs[j].SharedCommits
		}
		if pairs[i].A != pairs[j].A {
			return pairs[i].A < pairs[j].A
		}
		return pairs[i].B < pairs[j].B
	})
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}
