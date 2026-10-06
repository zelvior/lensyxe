package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/internal/config"
	"github.com/zelvior/lensyxe/pkg/models"
)

// refNow is a fixed instant so every proposal in this file is reproducible.
var refNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// baseSnapshot is a small repository: a git history, some tests, a manifest
// with a lockfile. Individual tests adjust one field at a time.
func baseSnapshot() *models.Snapshot {
	return &models.Snapshot{
		SchemaVersion: "0.2",
		Tool:          "lensyxe",
		Version:       "v1.0.0-rc1",
		Root:          "/repo",
		DurationMS:    1500,
		Code: models.CodeStats{
			Files: 40, TestFiles: 12, SourceFiles: 28, HasTests: true,
			TestFileRatio: 0.43,
			Hotspots: []models.Hotspot{
				{Path: "big.go", Lines: 900, Churn: 400, Complexity: 55},
			},
		},
		Git: models.GitStats{
			IsRepository:  true,
			Branch:        "main",
			FirstCommitAt: refNow.AddDate(0, -6, 0), // six months
			WindowDays:    90,
		},
		Dependencies: models.DependencyStats{
			Detected: true, Locked: true, Direct: 6, Indirect: 24,
		},
		Health: models.Health{
			Score: 72.5, Grade: "C", Components: 3,
			Metrics: []models.Metric{
				{Key: "code", Label: "Code health", Score: 46, Weight: 0.4, Applicable: true},
				{Key: "dependency", Label: "Dependency health", Score: 91, Weight: 0.3, Applicable: true},
				{Key: "git", Label: "Maintainability", Score: 78, Weight: 0.3, Applicable: true},
			},
		},
	}
}

// entry returns the proposed entry for key, failing the test if absent.
func entry(t *testing.T, p Proposal, key string) Entry {
	t.Helper()
	for _, e := range p.Entries {
		if e.Key == key {
			return e
		}
	}
	t.Fatalf("no %s entry was proposed; entries were %v", key, keysOf(p))
	return Entry{}
}

// value returns the scalar value for key, failing if it is absent or a block.
func value(t *testing.T, p Proposal, key string) string {
	t.Helper()
	e := entry(t, p, key)
	if e.Block != nil {
		t.Fatalf("%s is a block entry; want a scalar", key)
	}
	return e.Value
}

func has(p Proposal, key string) bool {
	for _, e := range p.Entries {
		if e.Key == key {
			return true
		}
	}
	return false
}

func keysOf(p Proposal) []string {
	out := make([]string, len(p.Entries))
	for i, e := range p.Entries {
		out[i] = e.Key
	}
	return out
}

func allNotes(p Proposal) string {
	var b strings.Builder
	for _, e := range p.Entries {
		b.WriteString(e.Note)
		b.WriteByte('\n')
	}
	return b.String()
}

func allWarnings(p Proposal) string { return strings.Join(p.Warnings, "\n") }

// A key the loader does not read produces a file that looks authoritative and
// does nothing at all, which is worse than not proposing it.
func TestProposeOnlyWritesRealConfigKeys(t *testing.T) {
	p := Propose(baseSnapshot(), map[string]bool{"node_modules": true}, refNow)

	known := map[string]bool{
		"ignore_dirs": true, "git_window_days": true, "hotspot_threshold": true,
		"enable_complexity": true, "max_file_bytes": true, "timeout_seconds": true,
		"detect_workspace": true, "explain": true, "ai_provider": true,
		"ai_model": true, "ai_key_env": true, "history_limit": true,
		"database_path": true, "compare_root": true, "watch_debounce_ms": true,
		"watch_interval_seconds": true,
	}
	for _, e := range p.Entries {
		if !known[e.Key] {
			t.Errorf("proposed key %q is not a key the config loader reads", e.Key)
		}
	}
}

// The CI thresholds are flags on analyze, not config keys. Proposing them in the
// file would be the single most misleading thing this wizard could do.
func TestProposeKeepsCIThresholdsOutOfTheConfigFile(t *testing.T) {
	p := Propose(baseSnapshot(), nil, refNow)
	body := p.Render()

	for _, notAKey := range []string{
		"min_health_score", "fail_on_drift", "require_tests", "thresholds:",
	} {
		if strings.Contains(body, notAKey) {
			t.Errorf("proposal contains %q, which the loader does not read:\n%s",
				notAKey, body)
		}
	}
	if !strings.Contains(p.CISuggestion, "--fail-under-health") {
		t.Errorf("no CI command line was suggested; got %q", p.CISuggestion)
	}
}

// Rendering must be byte-identical across runs, or the wizard becomes a source
// of diff noise on every invocation.
func TestProposeRendersIdenticallyOnRepeatedRuns(t *testing.T) {
	present := map[string]bool{"node_modules": true, "target": true, "dist": true}
	first := Propose(baseSnapshot(), present, refNow).Render()
	for i := 0; i < 5; i++ {
		if again := Propose(baseSnapshot(), present, refNow).Render(); again != first {
			t.Fatalf("render %d differs:\n--- first ---\n%s\n--- again ---\n%s",
				i, first, again)
		}
	}
}

// Each key must appear exactly once. An earlier draft built the ignore_dirs
// block by embedding the key inside the value, which emitted "ignore_dirs:" twice
// and produced a file with a null value followed by a duplicate key.
func TestNoKeyIsWrittenTwice(t *testing.T) {
	p := Propose(baseSnapshot(), map[string]bool{
		"node_modules": true, "target": true, "coverage": true,
	}, refNow)

	counts := map[string]int{}
	for _, line := range strings.Split(p.Render(), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") ||
			strings.HasPrefix(trimmed, "- ") {
			continue
		}
		key, _, found := strings.Cut(trimmed, ":")
		if !found {
			t.Errorf("line %q is neither a comment nor a key", line)
			continue
		}
		counts[strings.TrimSpace(key)]++
	}
	for key, n := range counts {
		if n != 1 {
			t.Errorf("key %q written %d times:\n%s", key, n, p.Render())
		}
	}
}

// The rendered body must load through the real loader, and every key must
// arrive with the value that was written. This is the assertion that would have
// caught the duplicate-key draft, and it is the only one that proves the file
// this command writes is a file the tool can actually read.
func TestRenderedConfigLoadsBackWithTheValuesWritten(t *testing.T) {
	present := map[string]bool{
		"node_modules": true, "target": true, "dist": true, "coverage": true,
	}
	p := Propose(baseSnapshot(), present, refNow)

	dir := t.TempDir()
	path := filepath.Join(dir, ".lensyxe.yml")
	if err := os.WriteFile(path, []byte(p.Render()), 0o644); err != nil {
		t.Fatal(err)
	}

	got, warnings, err := config.Load(path, dir)
	if err != nil {
		t.Fatalf("the loader rejected the generated config: %v\n%s", err, p.Render())
	}
	if len(warnings) > 0 {
		t.Errorf("the loader warned about the generated config: %v\n%s",
			warnings, p.Render())
	}

	if got.GitWindowDays != 90 {
		t.Errorf("git_window_days came back as %d, want 90\n%s",
			got.GitWindowDays, p.Render())
	}
	if got.TimeoutSeconds != 60 {
		t.Errorf("timeout_seconds came back as %d, want 60\n%s",
			got.TimeoutSeconds, p.Render())
	}
	if len(got.IgnoreDirs) != 6 {
		t.Errorf("ignore_dirs came back as %v, want 6 entries\n%s",
			got.IgnoreDirs, p.Render())
	}
	for _, want := range []string{"node_modules", "target", "dist", "coverage", ".git", "vendor"} {
		if !contains(got.IgnoreDirs, want) {
			t.Errorf("ignore_dirs lost %q: %v", want, got.IgnoreDirs)
		}
	}
}

// The defaults must be intact for keys the proposal deliberately omits.
func TestOmittedKeysKeepTheirDefaults(t *testing.T) {
	p := Propose(baseSnapshot(), nil, refNow)
	dir := t.TempDir()
	path := filepath.Join(dir, ".lensyxe.yml")
	if err := os.WriteFile(path, []byte(p.Render()), 0o644); err != nil {
		t.Fatal(err)
	}

	got, _, err := config.Load(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	def := config.Default("/repo")
	if got.HotspotThreshold != def.HotspotThreshold {
		t.Errorf("hotspot_threshold became %d, want the default %d",
			got.HotspotThreshold, def.HotspotThreshold)
	}
	if got.Explain {
		t.Error("the explanation layer was switched on by the generated file")
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ignore_dirs REPLACES the built-in list, so the proposal must be the whole
// story: nothing absent, nothing missing that is there.
func TestIgnoreDirsProposesOnlyDirectoriesThatExist(t *testing.T) {
	p := Propose(baseSnapshot(), map[string]bool{
		"node_modules": true,
		"target":       true,
	}, refNow)

	block := strings.Join(entry(t, p, "ignore_dirs").Block, "\n")
	for _, absent := range []string{"- dist", "- .next", "- coverage", "- __pycache__"} {
		if strings.Contains(block, absent) {
			t.Errorf("proposed absent directory %q:\n%s", absent, block)
		}
	}
	for _, there := range []string{"- node_modules", "- target", "- .git", "- vendor"} {
		if !strings.Contains(block, there) {
			t.Errorf("did not propose present directory %q:\n%s", there, block)
		}
	}
}

// The always-pruned set holds even for a directory with nothing in it.
func TestIgnoreDirsAlwaysPrunesGitAndVendor(t *testing.T) {
	p := Propose(baseSnapshot(), map[string]bool{}, refNow)
	block := strings.Join(entry(t, p, "ignore_dirs").Block, "\n")
	for _, always := range []string{"- .git", "- vendor", "- node_modules"} {
		if !strings.Contains(block, always) {
			t.Errorf("%q missing from an empty repository:\n%s", always, block)
		}
	}
}

// A young repository needs a short window, or bus factor and cadence get
// reported as findings that are really just lack of elapsed time.
func TestWindowShrinksForAYoungRepository(t *testing.T) {
	cases := []struct {
		name string
		age  time.Duration
		want string
	}{
		{"two weeks", 14 * 24 * time.Hour, "7"},
		{"two months", 60 * 24 * time.Hour, "30"},
		{"six months", 180 * 24 * time.Hour, "90"},
		{"three years", 3 * 365 * 24 * time.Hour, "180"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap := baseSnapshot()
			snap.Git.FirstCommitAt = refNow.Add(-c.age)
			got := value(t, Propose(snap, nil, refNow), "git_window_days")
			if got != c.want {
				t.Errorf("history %s proposed window %s, want %s", c.name, got, c.want)
			}
		})
	}
}

// Clock skew or a bad commit date must not produce a negative age.
func TestWindowHandlesFutureFirstCommit(t *testing.T) {
	snap := baseSnapshot()
	snap.Git.FirstCommitAt = refNow.AddDate(0, 3, 0) // dated in the future
	if got := value(t, Propose(snap, nil, refNow), "git_window_days"); got != "7" {
		t.Errorf("a future first commit produced window %s, want 7", got)
	}
}

// A directory that is not a repository still gets a usable value.
func TestWindowWithoutGit(t *testing.T) {
	snap := baseSnapshot()
	snap.Git = models.GitStats{IsRepository: false}
	if got := value(t, Propose(snap, nil, refNow), "git_window_days"); got != "90" {
		t.Errorf("got %s, want the 90-day default", got)
	}
}

// The timeout must never be tightened below the shipped default, and must scale
// with an actually-measured duration.
func TestTimeoutDerivesFromMeasuredDuration(t *testing.T) {
	snap := baseSnapshot()
	snap.DurationMS = 1500 // 1.5s observed
	if got := value(t, Propose(snap, nil, refNow), "timeout_seconds"); got != "60" {
		t.Errorf("a 1.5s scan proposed %s, want the 60s floor", got)
	}

	snap.DurationMS = 40000 // 40s observed
	if got := value(t, Propose(snap, nil, refNow), "timeout_seconds"); got != "160" {
		t.Errorf("a 40s scan proposed %s, want 160", got)
	}
}

// A zero duration means the scan never completed; a timeout derived from it
// would be derived from nothing.
func TestTimeoutSkippedWithoutAMeasurement(t *testing.T) {
	snap := baseSnapshot()
	snap.DurationMS = 0
	if has(Propose(snap, nil, refNow), "timeout_seconds") {
		t.Error("proposed a timeout from an unmeasured scan duration")
	}
}

// The wizard must not move someone's score, so hotspot_threshold is reported
// rather than written.
func TestHotspotThresholdIsWarnedNotProposed(t *testing.T) {
	p := Propose(baseSnapshot(), nil, refNow)
	if has(p, "hotspot_threshold") {
		t.Error("hotspot_threshold was written, which would change the code health score")
	}
	if !strings.Contains(allWarnings(p), "hotspot_threshold") {
		t.Errorf("hotspots exist but were not mentioned:\n%s", allWarnings(p))
	}
}

// Every proposed key carries an explanation, or the file is just numbers.
func TestEveryProposedEntryIsExplained(t *testing.T) {
	p := Propose(baseSnapshot(), map[string]bool{"node_modules": true}, refNow)
	for _, e := range p.Entries {
		if strings.TrimSpace(e.Note) == "" {
			t.Errorf("key %q was written with no explanation", e.Key)
		}
	}
	for _, phrase := range []string{"REPLACES", "history is", "four times"} {
		if !strings.Contains(allNotes(p), phrase) {
			t.Errorf("no note explains %q:\n%s", phrase, allNotes(p))
		}
	}
}

// A comment must never end up inside a value, which is how a note becomes part
// of the parsed configuration instead of documentation.
func TestNoCommentLeaksIntoAValue(t *testing.T) {
	p := Propose(baseSnapshot(), map[string]bool{"node_modules": true}, refNow)
	for _, e := range p.Entries {
		if e.Block != nil {
			for _, item := range e.Block {
				if strings.Contains(item, "#") {
					t.Errorf("block item %q for %q carries a comment", item, e.Key)
				}
			}
			continue
		}
		if strings.Contains(e.Value, "#") {
			t.Errorf("value %q for %q carries an inline comment", e.Value, e.Key)
		}
	}
}

// A workspace is only detected if the layout was found.
func TestDetectWorkspaceOnlyWhenFound(t *testing.T) {
	if has(Propose(baseSnapshot(), nil, refNow), "detect_workspace") {
		t.Error("detect_workspace proposed for a repository with no workspace layout")
	}

	snap := baseSnapshot()
	snap.Workspace = &models.Workspace{
		Kind:     models.WorkspacePNPM,
		Manifest: "pnpm-workspace.yaml",
		Packages: []models.PackageHealth{{Path: "a"}, {Path: "b"}, {Path: "c"}},
	}
	p := Propose(snap, nil, refNow)
	if got := value(t, p, "detect_workspace"); got != "true" {
		t.Errorf("detect_workspace: %s, want true", got)
	}
	if !strings.Contains(allNotes(p), "3 workspace package") {
		t.Errorf("the number of packages was not reported:\n%s", allNotes(p))
	}
}

// An unscored repository gets no threshold, and says why.
func TestNoScoreThresholdWhenNothingCouldBeScored(t *testing.T) {
	snap := baseSnapshot()
	snap.Health = models.Health{Score: 0, Components: 0}
	p := Propose(snap, nil, refNow)

	if p.CISuggestion != "" {
		t.Errorf("suggested a CI threshold against an unmeasured score: %q", p.CISuggestion)
	}
	if !strings.Contains(allWarnings(p), "no dimension could be scored") {
		t.Errorf("the unscored case was not explained:\n%s", allWarnings(p))
	}
}

// The gate sits below the current score, so it catches regressions rather than
// failing on the next unrelated commit.
func TestCISuggestionSitsBelowTheCurrentScore(t *testing.T) {
	p := Propose(baseSnapshot(), nil, refNow) // score 72.5
	if !strings.Contains(p.CISuggestion, "--fail-under-health 67") {
		t.Errorf("suggested %q, want a threshold below 72.5", p.CISuggestion)
	}

	// A score of 2 must floor at zero rather than go negative.
	snap := baseSnapshot()
	snap.Health.Score = 2
	p = Propose(snap, nil, refNow)
	if !strings.Contains(p.CISuggestion, "--fail-under-health 0 ") {
		t.Errorf("a score of 2 produced %q, want a floored threshold of 0", p.CISuggestion)
	}
}

// The explanation layer stays off and the proposal says so.
func TestWarnsThatTheAILayerStaysOff(t *testing.T) {
	p := Propose(baseSnapshot(), nil, refNow)
	if !strings.Contains(allWarnings(p), "explanation layer is off") {
		t.Errorf("the AI layer was not addressed:\n%s", allWarnings(p))
	}
	if strings.Contains(p.Render(), "explain:") {
		t.Error("the proposal enabled the explanation layer")
	}
}

// A missing lockfile is a finding, and the wizard says so.
func TestWarnsAboutAMissingLockfile(t *testing.T) {
	snap := baseSnapshot()
	snap.Dependencies.Locked = false
	if !strings.Contains(allWarnings(Propose(snap, nil, refNow)), "no lockfile") {
		t.Error("a missing lockfile was not reported")
	}
}

func TestProposeHandlesNilSnapshot(t *testing.T) {
	p := Propose(nil, nil, refNow)
	if len(p.Entries) != 0 {
		t.Errorf("a nil snapshot produced %d entries", len(p.Entries))
	}
	if got := p.Render(); got != "" {
		t.Errorf("a nil snapshot rendered %q", got)
	}
}
