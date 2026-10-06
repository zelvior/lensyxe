package git

import (
	"strings"
	"sync"
	"time"
)

// History reads are expensive: on Windows a git subprocess costs far more than
// the whole code walk, and a single analysis spawns several. But they are also
// highly repetitive. `lensyxe watch` re-analyzes on every save and
// `lensyxe serve` has a reload button, and neither typically creates a commit in
// between, so the same history is read over and over.
//
// This cache exists for that case. It stores the raw stdout of the history reads,
// not the parsed results, so a cache hit is re-parsed by exactly the same code
// as a miss. That keeps the hit path behaviourally identical to the cold path
// rather than merely equivalent, which matters because the parsed values are
// what the score and every golden file are built from.

// historyKey identifies a set of history reads that would produce identical
// output.
//
// The commit is the obvious component and is necessary but not sufficient. The
// window is a sliding one: `since` is computed as now minus WindowDays, so the
// same commit yields different churn, a different window commit count, and a
// different bus factor as time passes. Keying on the commit alone would serve a
// commit list that has since aged out of the window, which is a wrong answer
// rather than a stale one.
//
// sinceDay is that window start truncated to a date. Two analyses on the same
// day share it and therefore share a cache entry, which is the case that matters
// for watch and serve; an analysis on a different day misses and recomputes,
// which is what correctness requires.
type historyKey struct {
	root       string
	commit     string
	windowDays int
	sinceDay   string
}

// historyOutputs is the raw stdout of the cached git reads.
//
// Only the four window-dependent reads are cached. The metadata pass also reads
// the full author list, which is the single most expensive call in the whole
// analysis on a large repository, but capturing it means moving those reads out
// of the parallel metadata group. That is the obvious next step and is
// deliberately not done here: it restructures the phase the previous change
// parallelised, and the win below does not depend on it.
type historyOutputs struct {
	numstat     string // log --numstat within the window
	windowCount string // rev-list --count within the window
	windowNames string // log --format=%aN within the window
	lastCommit  string // log -1 --format=%aI
}

var (
	cacheMu sync.Mutex
	// cache holds at most a handful of entries. The realistic working set is one
	// per repository a long-running process watches, so a small bound is enough
	// and avoids turning a long-lived `serve` into a memory leak.
	cache = map[historyKey]historyOutputs{}
)

// maxCacheEntries bounds the cache. Beyond this the oldest entry is evicted.
const maxCacheEntries = 8

// lookup returns cached history outputs for the key.
func (k historyKey) lookup() (historyOutputs, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	out, ok := cache[k]
	return out, ok
}

// store records history outputs for the key.
//
// Eviction is not LRU: it drops an arbitrary entry once the bound is exceeded.
// A precise policy would be more code for a cache whose working set is normally
// a single entry, and correctness does not depend on which one goes.
func (k historyKey) store(out historyOutputs) {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	if _, exists := cache[k]; !exists && len(cache) >= maxCacheEntries {
		for oldest := range cache {
			delete(cache, oldest)
			break
		}
	}
	cache[k] = out
}

// newHistoryKey builds the cache key for a commit and window.
//
// Caching is disabled for a directory that is not a repository or has no
// commits. `git rev-parse HEAD` in an empty repository does not fail cleanly:
// it prints the literal string "HEAD" and exits non-zero, so an empty commit is
// not detectable by emptiness alone. Every such directory would otherwise share
// one key and could be served another repository's history.
func newHistoryKey(root, commit string, windowDays int, since time.Time) (historyKey, bool) {
	commit = strings.TrimSpace(commit)
	if commit == "" || commit == "HEAD" {
		return historyKey{}, false
	}
	return historyKey{
		root:       root,
		commit:     commit,
		windowDays: windowDays,
		sinceDay:   since.UTC().Format("2006-01-02"),
	}, true
}
