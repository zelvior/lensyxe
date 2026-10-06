// Package compare analyzes two git revisions and reports the metric deltas
// between them.
//
// The approach is deliberately read-only. Rather than checking out either
// revision in the user's working tree, which would mutate their index and
// leave it dirty on failure, the comparator exports each revision into a
// temporary directory using `git archive`, analyzes those trees, and removes
// them. Nothing outside the temporary directory is touched, and no branch or
// ref is moved.
package compare

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zelvior/lensyxe/pkg/models"
)

// ErrNotRepository indicates the target is not inside a git work tree.
var ErrNotRepository = errors.New("not a git repository")

// Config tunes the comparison.
type Config struct {
	// Root is the repository to compare within.
	Root string
	// A and B are the revisions to compare, given as anything git accepts:
	// a commit SHA, a tag, a branch, or a relative ref like HEAD~3.
	A string
	B string
	// Timeout bounds each git subprocess and each analysis pass.
	Timeout int // seconds
	// Analyzer is invoked once per exported revision tree. It is injected so
	// this package does not import internal/analyzer, which would create an
	// import cycle, and so tests can supply a stub.
	Analyzer func(ctx context.Context, treePath string) (*models.Snapshot, error)
	// KeepTemp retains the exported trees for inspection. Used by tests.
	KeepTemp bool
}

// Result is the complete comparison between two revisions.
type Result struct {
	SchemaVersion string        `json:"schema_version"`
	Tool          string        `json:"tool"`
	Version       string        `json:"version"`
	Root          string        `json:"root"`
	A             RevisionInfo  `json:"a"`
	B             RevisionInfo  `json:"b"`
	ScoreA        float64       `json:"score_a"`
	ScoreB        float64       `json:"score_b"`
	GradeA        string        `json:"grade_a"`
	GradeB        string        `json:"grade_b"`
	ScoreDelta    float64       `json:"score_delta"`
	Metrics       []MetricDelta `json:"metrics"`
	RisksAdded    []models.Risk `json:"risks_added"`
	RisksResolved []models.Risk `json:"risks_resolved"`
	RisksChanged  []RiskChange  `json:"risks_changed"`
	GradeChange   string        `json:"grade_change"`
	Verdict       string        `json:"verdict"`
}

// RevisionInfo identifies one side of the comparison.
type RevisionInfo struct {
	Ref      string `json:"ref"`
	Commit   string `json:"commit"`
	ShortSHA string `json:"short_sha"`
	Date     string `json:"date"`
	Subject  string `json:"subject"`
}

// MetricDelta is a single side-by-side metric comparison.
type MetricDelta struct {
	Label string  `json:"label"`
	A     float64 `json:"a"`
	B     float64 `json:"b"`
	Delta float64 `json:"delta"`
	Unit  string  `json:"unit"`
	// Better is +1 when the change is an improvement, -1 a regression, and
	// 0 when the change is neutral or not directionally meaningful.
	Better int `json:"better"`
	// Display is a preformatted "a -> b (delta)" string for humans.
	Display string `json:"display"`
}

// RiskChange is a risk present in both revisions whose severity or impact moved.
type RiskChange struct {
	ID       string          `json:"id"`
	Title    string          `json:"title"`
	Category models.Category `json:"category"`
	Subject  string          `json:"subject,omitempty"`
	Severity models.Severity `json:"a_severity"`
	After    models.Severity `json:"b_severity"`
	ImpactA  float64         `json:"a_impact"`
	ImpactB  float64         `json:"b_impact"`
	Delta    float64         `json:"delta"`
	Better   int             `json:"better"`
}

// Run performs the comparison.
//
// Analysis of each revision is sequential by design: exporting two trees and
// scanning both is I/O-bound, and running them concurrently would double peak
// memory for a comparison that takes well under a second on typical repos.
func Run(ctx context.Context, cfg Config, toolVersion string) (*Result, error) {
	if cfg.A == "" || cfg.B == "" {
		return nil, fmt.Errorf("compare: both revisions are required")
	}
	if cfg.Analyzer == nil {
		return nil, fmt.Errorf("compare: no analyzer configured")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120
	}

	repoRoot, err := repoRoot(cfg.Root)
	if err != nil {
		return nil, err
	}

	infoA, err := describeRevision(ctx, repoRoot, cfg.A)
	if err != nil {
		return nil, err
	}
	infoB, err := describeRevision(ctx, repoRoot, cfg.B)
	if err != nil {
		return nil, err
	}

	if infoA.Commit == infoB.Commit {
		return nil, fmt.Errorf(
			"compare: %q and %q resolve to the same commit (%s); nothing to compare",
			cfg.A, cfg.B, infoA.ShortSHA)
	}

	// Export both revisions into temp trees. Each export gets its own
	// directory so a failure in one cannot corrupt the other.
	treeA, cleanupA, err := exportRevision(ctx, repoRoot, infoA.Commit)
	if err != nil {
		return nil, err
	}
	treeB, cleanupB, err := exportRevision(ctx, repoRoot, infoB.Commit)
	if err != nil {
		cleanupA()
		return nil, err
	}
	defer func() {
		cleanupA()
		cleanupB()
	}()

	snapA, err := cfg.Analyzer(ctx, treeA)
	if err != nil {
		return nil, fmt.Errorf("compare: analyze %s: %w", infoA.Ref, err)
	}
	snapB, err := cfg.Analyzer(ctx, treeB)
	if err != nil {
		return nil, fmt.Errorf("compare: analyze %s: %w", infoB.Ref, err)
	}

	return build(cfg, toolVersion, repoRoot, infoA, infoB, snapA, snapB), nil
}

// build assembles the comparison from two analyzed snapshots.
func build(
	cfg Config, toolVersion, repoRoot string,
	infoA, infoB RevisionInfo,
	snapA, snapB *models.Snapshot,
) *Result {
	res := &Result{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       toolVersion,
		Root:          repoRoot,
		A:             infoA,
		B:             infoB,
		ScoreA:        snapA.Health.Score,
		ScoreB:        snapB.Health.Score,
		GradeA:        snapA.Health.Grade,
		GradeB:        snapB.Health.Grade,
	}

	res.ScoreDelta = round2(snapB.Health.Score - snapA.Health.Score)
	res.Metrics = metricDeltas(snapA, snapB)
	res.RisksAdded, res.RisksResolved, res.RisksChanged = diffRisks(snapA, snapB)

	switch {
	case res.ScoreDelta < 0:
		res.GradeChange = fmt.Sprintf("%s -> %s (%+.1f)",
			snapA.Health.Grade, snapB.Health.Grade, res.ScoreDelta)
	case res.ScoreDelta > 0:
		res.GradeChange = fmt.Sprintf("%s -> %s (%+.1f)",
			snapA.Health.Grade, snapB.Health.Grade, res.ScoreDelta)
	default:
		res.GradeChange = fmt.Sprintf("%s -> %s (+0.0)",
			snapA.Health.Grade, snapB.Health.Grade)
	}
	res.Verdict = verdict(res, snapA, snapB)
	return res
}

// metricDeltas builds the side-by-side metric table.
//
// Only metrics that moved are reported; a comparison should surface change,
// not restate everything that stayed the same. Comparison is on the raw
// measurements, not the sub-scores, because sub-score movement is already
// summarized by ScoreDelta.
func metricDeltas(a, b *models.Snapshot) []MetricDelta {
	out := make([]MetricDelta, 0, 12)
	// direction values: better=1, worse=-1, neutral=0.
	//
	// Size metrics are deliberately NEUTRAL. Labelling "code lines 7 -> 889"
	// as "improved" is actively misleading: that is usually the bloat that
	// tanks the score, as the surrounding rows make obvious. Only metrics with
	// an unambiguous direction get a verdict.
	const (
		lowerIsBetter = iota
		higherIsBetter
		neutral
	)

	add := func(label string, av, bv float64, unit string, direction, digits int) {
		delta := round2(bv - av)
		if delta == 0 {
			return
		}
		better := 0
		switch direction {
		case lowerIsBetter:
			if delta < 0 {
				better = 1
			} else {
				better = -1
			}
		case higherIsBetter:
			if delta > 0 {
				better = 1
			} else {
				better = -1
			}
		}

		out = append(out, MetricDelta{
			Label:   label,
			A:       av,
			B:       bv,
			Delta:   delta,
			Unit:    unit,
			Better:  better,
			Display: formatDelta(label, av, bv, delta, unit, digits),
		})
	}

	add("Health score", a.Health.Score, b.Health.Score, "", higherIsBetter, 1)
	// Volumes: reported, never judged.
	add("Code lines", float64(a.Code.CodeLines), float64(b.Code.CodeLines), "LOC", neutral, 0)
	add("Total lines", float64(a.Code.TotalLines), float64(b.Code.TotalLines), "LOC", neutral, 0)
	add("Source files", float64(a.Code.SourceFiles), float64(b.Code.SourceFiles), "", neutral, 0)
	add("Test files", float64(a.Code.TestFiles), float64(b.Code.TestFiles), "", higherIsBetter, 0)
	add("Test file ratio", a.Code.TestFileRatio, b.Code.TestFileRatio, "", higherIsBetter, 2)
	add("Avg LOC per file", a.Code.AverageLines, b.Code.AverageLines, "LOC", lowerIsBetter, 1)
	add("Hotspots", float64(len(a.Code.Hotspots)), float64(len(b.Code.Hotspots)), "", lowerIsBetter, 0)
	add("Confirmed hotspots", float64(countConfirmed(a)), float64(countConfirmed(b)), "", lowerIsBetter, 0)
	add("Max complexity", a.Code.Complexity.MaxComplexity, b.Code.Complexity.MaxComplexity, "", lowerIsBetter, 1)
	add("Avg complexity", a.Code.Complexity.AverageComplexity, b.Code.Complexity.AverageComplexity, "", lowerIsBetter, 1)
	add("Max nesting", float64(a.Code.Complexity.MaxNesting), float64(b.Code.Complexity.MaxNesting), "levels", lowerIsBetter, 0)
	add("Direct deps", float64(a.Dependencies.Direct), float64(b.Dependencies.Direct), "", lowerIsBetter, 0)
	add("Total deps", float64(a.Dependencies.Total), float64(b.Dependencies.Total), "", lowerIsBetter, 0)
	add("Transitive deps", float64(a.Dependencies.Transitive), float64(b.Dependencies.Transitive), "", lowerIsBetter, 0)
	// History metrics are unavailable in a revision export, so they only
	// appear when a caller supplies analyzed snapshots with git data.
	add("Commits (window)", float64(a.Git.WindowCommits), float64(b.Git.WindowCommits), "", neutral, 0)
	add("Authors", float64(a.Git.Authors), float64(b.Git.Authors), "", higherIsBetter, 0)
	add("Bus factor", float64(a.Git.BusFactor), float64(b.Git.BusFactor), "", higherIsBetter, 0)
	add("Churn concentration", a.Git.ChurnConcentration, b.Git.ChurnConcentration, "", lowerIsBetter, 2)

	sort.SliceStable(out, func(i, j int) bool {
		// Bigger absolute movement first, then label for a stable tiebreak.
		ai, aj := abs(out[i].Delta), abs(out[j].Delta)
		if ai != aj {
			return ai > aj
		}
		return out[i].Label < out[j].Label
	})
	return out
}

// countConfirmed counts confirmed hotspots in a snapshot.
func countConfirmed(s *models.Snapshot) int {
	n := 0
	for _, h := range s.Code.Hotspots {
		if h.Confirmed {
			n++
		}
	}
	return n
}

// formatDelta renders the canonical "a -> b (delta)" form.
func formatDelta(label string, a, b, delta float64, unit string, digits int) string {
	suffix := ""
	if unit != "" {
		suffix = " " + unit
	}
	return fmt.Sprintf("%s: %.*f -> %.*f (%+.1f)%s",
		label, digits, a, digits, b, delta, suffix)
}

// diffRisks splits risk changes into added, resolved, and modified.
//
// Matching is by Risk.ID, which is derived from the rule and its subject, so a
// risk that merely changed severity is correctly reported as modified rather
// than as one removal plus one addition.
func diffRisks(a, b *models.Snapshot) (added, resolved []models.Risk, changed []RiskChange) {
	byIDA := indexRisks(a.Risks)
	byIDB := indexRisks(b.Risks)

	ids := make([]string, 0, len(byIDA)+len(byIDB))
	for id := range byIDA {
		ids = append(ids, id)
	}
	for id := range byIDB {
		if _, ok := byIDA[id]; !ok {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids) // deterministic iteration order

	for _, id := range ids {
		ra, inA := byIDA[id]
		rb, inB := byIDB[id]
		switch {
		case inB && !inA:
			added = append(added, rb)
		case inA && !inB:
			resolved = append(resolved, ra)
		default:
			if ra.Severity == rb.Severity && ra.Impact == rb.Impact {
				continue // unchanged
			}
			delta := round2(rb.Impact - ra.Impact)
			better := 0
			if delta < 0 {
				better = 1 // lower impact is better
			} else {
				better = -1
			}
			changed = append(changed, RiskChange{
				ID:       id,
				Title:    rb.Title,
				Category: rb.Category,
				Subject:  rb.Subject,
				Severity: ra.Severity,
				After:    rb.Severity,
				ImpactA:  ra.Impact,
				ImpactB:  rb.Impact,
				Delta:    delta,
				Better:   better,
			})
		}
	}

	if added == nil {
		added = []models.Risk{}
	}
	if resolved == nil {
		resolved = []models.Risk{}
	}
	if changed == nil {
		changed = []RiskChange{}
	}
	return added, resolved, changed
}

func indexRisks(risks []models.Risk) map[string]models.Risk {
	m := make(map[string]models.Risk, len(risks))
	for _, r := range risks {
		m[r.ID] = r
	}
	return m
}

// verdict renders a one-line summary of the direction of travel.
func verdict(res *Result, a, b *models.Snapshot) string {
	switch {
	case res.ScoreDelta <= -5:
		return fmt.Sprintf("Significant regression: health fell %.1f points (%s -> %s).",
			abs(res.ScoreDelta), a.Health.Grade, b.Health.Grade)
	case res.ScoreDelta < 0:
		return fmt.Sprintf("Slight regression: health fell %.1f point(s).", abs(res.ScoreDelta))
	case res.ScoreDelta > 5:
		return fmt.Sprintf("Significant improvement: health rose %.1f points (%s -> %s).",
			res.ScoreDelta, a.Health.Grade, b.Health.Grade)
	case res.ScoreDelta > 0:
		return fmt.Sprintf("Slight improvement: health rose %.1f point(s).", res.ScoreDelta)
	default:
		return "No net change in health score."
	}
}

// repoRoot verifies that dir is inside a work tree and returns its top level,
// so `git archive` runs against the repository rather than a subdirectory.
func repoRoot(dir string) (string, error) {
	out, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotRepository, dir)
	}
	root := strings.TrimSpace(out)
	if root == "" {
		return "", fmt.Errorf("%w: %s", ErrNotRepository, dir)
	}
	return root, nil
}

// describeRevision resolves a ref to its commit and metadata.
func describeRevision(ctx context.Context, root, ref string) (RevisionInfo, error) {
	// rev-parse normalizes any accepted ref form into a full SHA.
	commit, err := git(root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return RevisionInfo{}, fmt.Errorf("compare: cannot resolve revision %q: %w", ref, err)
	}
	commit = strings.TrimSpace(commit)

	info := RevisionInfo{Ref: ref, Commit: commit, ShortSHA: shorten(commit)}

	// git show -s gives date and subject in one call.
	if out, err := git(root, "show", "-s", "--format=%cI%n%s", commit); err == nil {
		lines := strings.SplitN(strings.TrimSpace(out), "\n", 2)
		if len(lines) > 0 {
			info.Date = strings.TrimSpace(lines[0])
		}
		if len(lines) > 1 {
			info.Subject = strings.TrimSpace(lines[1])
		}
	}
	return info, nil
}

// exportRevision materializes a commit into a temp directory via git archive.
//
// `git archive` is used instead of `git checkout` precisely so the user's
// working tree, index, and HEAD are never modified. The caller must invoke the
// returned cleanup.
func exportRevision(ctx context.Context, root, commit string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "lensyxe-compare-")
	if err != nil {
		return "", nil, fmt.Errorf("compare: create temp dir: %w", err)
	}
	cleanup := func() {
		// Best effort: a leftover temp dir is not worth failing the command.
		_ = os.RemoveAll(dir)
	}

	// git archive writes a tar to stdout; -o writes it to a file instead,
	// which avoids a shell pipe and works identically on Windows.
	archivePath := filepath.Join(dir, "tree.tar")
	if err := gitArchive(root, archivePath, commit); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("compare: export %s: %w", commit[:min(12, len(commit))], err)
	}
	if err := extractTar(archivePath, dir); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("compare: extract %s: %w", commit[:min(12, len(commit))], err)
	}
	// The tar itself is not part of the tree; leaving it would double the
	// analyzed byte count.
	_ = os.Remove(archivePath)
	return dir, cleanup, nil
}

// git runs a git command in dir and returns its stdout.
func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Same environment hygiene as the git analyzer: no pager, no prompting,
	// deterministic path quoting.
	cmd.Env = append(cmd.Environ(),
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(firstLine(stderr.String()))
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return stdout.String(), nil
}

// gitArchive writes a commit's tree as a tar file at outPath.
func gitArchive(dir, outPath, commit string) error {
	cmd := exec.Command("git", "archive", "--format=tar", "-o", outPath, commit)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create archive: %w", err)
	}
	cmd.Stdout = f

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	closeErr := f.Close()
	if runErr != nil {
		msg := strings.TrimSpace(firstLine(stderr.String()))
		if msg == "" {
			msg = runErr.Error()
		}
		return errors.New(msg)
	}
	return closeErr
}

// shorten truncates a SHA for display.
func shorten(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
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

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
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
