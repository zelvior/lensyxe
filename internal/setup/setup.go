// Package setup proposes a .lensyxe.yml for one specific repository.
//
// It exists because the documented configuration file is a template: it lists
// every key with its default and explains what each one does, which is the
// right thing to read and the wrong thing to start from. A template cannot know
// that this repository has a Rust target directory, that its history is only six
// weeks deep, or how long its own scan actually takes.
//
// Every value here is derived from a snapshot the deterministic engine already
// produced. The wizard never invents a measurement, and never proposes a value
// that would change the current score without saying so.
//
// Only keys the loader actually reads are written. The CI thresholds are flags
// on `analyze`, not configuration keys, so proposing them in a config file
// would produce a file that looks authoritative and is silently ignored on
// every key in it.
package setup

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

// Proposal is a recommended configuration for one repository.
type Proposal struct {
	// Entries are the configuration keys to write, in the order written.
	Entries []Entry
	// Warnings are things a human has to decide, and none of them are applied.
	Warnings []string
	// CISuggestion is a ready command line, not a config key.
	CISuggestion string
}

// Entry is one configuration key and its value.
//
// Exactly one of Value and Block is used. Carrying a block list inside Value as
// text is what produced a file with the key written twice: once bare and once
// ahead of the list. Keeping them apart is what makes that impossible.
type Entry struct {
	Key string
	// Value is a scalar, rendered as "key: value".
	Value string
	// Block is a list, rendered as "key:" followed by one indented item per
	// line. Its items are already indented.
	Block []string
	// Note is emitted as comment lines above the key, never inline, so no
	// comment can end up inside a value.
	Note string
}

// Render produces the commented YAML body for the proposal.
//
// Keys are emitted in slice order rather than map order, so running the wizard
// twice on an unchanged repository produces an identical file. A config
// generator that reshuffles its output is a config generator that shows up as a
// diff every time anyone runs it.
func (p Proposal) Render() string {
	var b strings.Builder
	for _, e := range p.Entries {
		if e.Note != "" {
			for _, line := range strings.Split(strings.ReplaceAll(e.Note, "\n", " "), "; ") {
				fmt.Fprintf(&b, "# %s\n", line)
			}
		}
		if e.Block != nil {
			fmt.Fprintf(&b, "%s:\n", e.Key)
			for _, item := range e.Block {
				fmt.Fprintf(&b, "%s\n", item)
			}
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", e.Key, e.Value)
	}
	return b.String()
}

// presentDirs are directories that indicate a specific toolchain.
//
// Only a directory that is actually present is proposed. Writing out the whole
// catalogue would be noise, and a name in it that a future toolchain starts
// using would silently begin pruning first-party source.
var presentDirs = []struct {
	dir string
	why string
}{
	{"node_modules", "npm, pnpm, or yarn dependencies are present"},
	{"target", "a Rust build directory is present"},
	{"dist", "bundler output is present"},
	{"build", "build output is present"},
	{".next", "a Next.js build cache is present"},
	{".nuxt", "a Nuxt build cache is present"},
	{"__pycache__", "compiled Python bytecode is present"},
	{".venv", "a Python virtual environment is present"},
	{"venv", "a Python virtual environment is present"},
	{".terraform", "Terraform providers are present"},
	{".gradle", "a Gradle cache is present"},
	{"coverage", "a coverage report is present"},
}

// alwaysIgnored are pruned for every repository regardless of what is present:
// version control metadata and vendored dependency trees carry no first-party
// source in any language.
var alwaysIgnored = []string{".git", "node_modules", "vendor"}

// Propose derives a configuration from a snapshot.
//
// present is the set of directory names present at the repository root, read by
// the caller from disk and passed in so this stays a pure function.
//
// now is injected rather than read from the clock for the same reason: the
// output must be reproducible, and a wizard whose proposal changes with the
// wall clock cannot be tested.
func Propose(snap *models.Snapshot, present map[string]bool, now time.Time) Proposal {
	p := Proposal{}
	if snap == nil {
		return p
	}

	p.Entries = append(p.Entries, ignoreDirsEntry(present))
	p.Entries = append(p.Entries, windowEntry(snap, now))
	if e, ok := timeoutEntry(snap); ok {
		p.Entries = append(p.Entries, e)
	}
	if snap.Workspace.Detected() {
		p.Entries = append(p.Entries, Entry{
			Key:   "detect_workspace",
			Value: "true",
			Note: fmt.Sprintf("%d workspace package(s) are present (%s), "+
				"so each is scored separately",
				len(snap.Workspace.Packages), snap.Workspace.Manifest),
		})
	}

	p.Warnings = append(p.Warnings, scoreWarnings(snap)...)
	p.Warnings = append(p.Warnings, dependencyWarnings(snap)...)
	p.CISuggestion = ciSuggestion(snap)
	return p
}

// ignoreDirsEntry proposes the prune list.
//
// Only directories that exist are included, because ignore_dirs REPLACES the
// built-in list rather than extending it: the proposed list is the whole story,
// and padding it with absent names would misrepresent what is being pruned.
func ignoreDirsEntry(present map[string]bool) Entry {
	// Deduplicated because a directory can legitimately appear twice in the
	// catalogue: node_modules is both always-pruned and a detected toolchain
	// directory. A duplicate in the list is not an error the loader reports, so
	// without this the generated file would quietly carry a repeated entry.
	seen := map[string]bool{}
	dirs := make([]string, 0, len(alwaysIgnored)+len(presentDirs))
	for _, d := range alwaysIgnored {
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}

	why := map[string]string{}
	for _, d := range presentDirs {
		if !present[d.dir] {
			continue
		}
		if !seen[d.dir] {
			seen[d.dir] = true
			dirs = append(dirs, d.dir)
		}
		// The reason is recorded whether or not the name was a duplicate, so
		// the note explains every detected directory.
		why[d.dir] = d.why
	}
	sort.Strings(dirs)

	note := "Pruned from the code walk, matched at any depth. This REPLACES " +
		"the built-in list rather than extending it."
	for _, d := range dirs {
		if reason, ok := why[d]; ok {
			note += "; " + d + ": " + reason
		}
	}

	block := make([]string, len(dirs))
	for i, d := range dirs {
		block[i] = "  - " + d
	}
	return Entry{Key: "ignore_dirs", Block: block, Note: note}
}

// windowEntry matches the churn window to the history that actually exists.
//
// A ninety-day window on a repository six weeks old reports a bus factor of one
// and near-zero cadence because nobody has had the time to do anything, and
// both of those are then reported as findings.
func windowEntry(snap *models.Snapshot, now time.Time) Entry {
	if !snap.Git.IsRepository || snap.Git.FirstCommitAt.IsZero() {
		return Entry{
			Key:   "git_window_days",
			Value: "90",
			Note:  "the default ninety days, which suits a repository with a few months of history",
		}
	}
	age := int(now.Sub(snap.Git.FirstCommitAt).Hours() / 24)
	if age < 0 {
		age = 0
	}

	var days int
	var why string
	switch {
	case age < 30:
		days, why = 7, fmt.Sprintf("history is only %d days old, so a long window "+
			"would report every author and file as a single-author risk", age)
	case age < 90:
		days, why = 30, fmt.Sprintf("history is %d days old; a thirty-day window "+
			"covers it without padding the sample with silence", age)
	case age < 365:
		days, why = 90, "history is under a year, so the ninety-day default "+
			"covers a representative stretch of it"
	default:
		days, why = 180, fmt.Sprintf("history spans %.0f years, so the window is "+
			"widened to six months to keep the sample meaningful", float64(age)/365)
	}
	return Entry{
		Key:   "git_window_days",
		Value: fmt.Sprintf("%d", days),
		Note:  why,
	}
}

// timeoutEntry proposes a scan timeout derived from how long this repository
// actually took to scan.
//
// The shipped default is a fixed 60s, which is a reasonable guess and a poor
// measurement. A repository whose scan takes four seconds does not need sixty,
// and one that takes ninety fails on a cold cache regardless of how healthy it
// is.
func timeoutEntry(snap *models.Snapshot) (Entry, bool) {
	if snap.DurationMS <= 0 {
		return Entry{}, false
	}
	// Four times the observed time, rounded up to the next 10s, floored at the
	// shipped default so the proposal never tightens below a known-good value.
	seconds := (snap.DurationMS*4 + 9999) / 10000 * 10
	if seconds < 60 {
		seconds = 60
	}
	return Entry{
		Key:   "timeout_seconds",
		Value: fmt.Sprintf("%d", seconds),
		Note: fmt.Sprintf("four times the %.1fs this repository took to scan, "+
			"rounded up, and never below the 60s default",
			float64(snap.DurationMS)/1000),
	}, true
}

// scoreWarnings reports findings that a config value could paper over, and is
// explicit that the wizard does not.
func scoreWarnings(snap *models.Snapshot) []string {
	var out []string

	if n := len(snap.Code.Hotspots); n > 0 {
		out = append(out, fmt.Sprintf(
			"%d file(s) already exceed hotspot_threshold (400 code lines). That "+
				"threshold is left alone: raising or lowering it moves the code "+
				"health score, and a wizard that quietly changes your score is "+
				"worse than one that leaves it alone. A file is only a confirmed "+
				"hotspot when churn and complexity agree as well.", n))
	}
	if snap.Code.HasTests && snap.Code.TestFileRatio < 0.2 {
		out = append(out, fmt.Sprintf(
			"test files are %.0f%% of source files. No config value fixes that; "+
				"it is a finding, not a setting.",
			snap.Code.TestFileRatio*100))
	}
	if snap.Code.TestFiles == 0 {
		out = append(out, "no test files were found, so --fail-on-test-ratio-drop "+
			"is not suggested: it would have no baseline and could never fail usefully")
	}
	if snap.Health.Components == 0 {
		out = append(out, "no dimension could be scored, so no score threshold is "+
			"suggested; a threshold against an unmeasured score is meaningless")
	}
	out = append(out, "the AI explanation layer is off and stays off. It needs a "+
		"key you supply, it is the only outbound request this tool can make, and "+
		"no number above depends on it")
	return out
}

// dependencyWarnings reports what was found about dependency handling.
func dependencyWarnings(snap *models.Snapshot) []string {
	if !snap.Dependencies.Detected {
		return nil
	}
	if snap.Dependencies.Locked {
		return []string{fmt.Sprintf(
			"every detected ecosystem has a lockfile (%d direct, %d indirect "+
				"dependencies), so installs are reproducible",
			snap.Dependencies.Direct, snap.Dependencies.Indirect)}
	}
	return []string{
		"a manifest was found with no lockfile, so installs from it are not " +
			"reproducible. Nothing in the config fixes that; committing the " +
			"lockfile does",
	}
}

// ciSuggestion builds a command line rather than a config block, because the CI
// thresholds are flags on `analyze` and not configuration keys.
func ciSuggestion(snap *models.Snapshot) string {
	if snap.Health.Components == 0 {
		return ""
	}
	// Floored, not rounded. Rounding 67.5 to 68 would leave four and a half
	// points of margin while the note promises five, and the note is the part
	// a reader trusts.
	floor := math.Floor(snap.Health.Score) - 5
	if floor < 0 {
		floor = 0
	}
	return fmt.Sprintf("lensyxe analyze --fail-under-health %.0f --fail-on-critical-risk", floor)
}
