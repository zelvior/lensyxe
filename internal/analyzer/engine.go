// Package analyzer wires the individual analyzers together and orchestrates
// the concurrent scan.
package analyzer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"

	"github.com/zelvior/lensyxe/internal/code"
	"github.com/zelvior/lensyxe/internal/dependencies"
	gitanalyzer "github.com/zelvior/lensyxe/internal/git"
	"github.com/zelvior/lensyxe/internal/metrics"
	"github.com/zelvior/lensyxe/internal/monorepo"
	"github.com/zelvior/lensyxe/internal/risk"
)

// Config holds the resolved settings for a single scan.
type Config struct {
	// Target is the directory to analyze.
	Target string
	// GitWindowDays bounds the git churn window.
	GitWindowDays int
	// HotspotThreshold is the code-line count above which a file becomes a
	// size-based hotspot candidate.
	HotspotThreshold int
	// IgnoreDirs are directory names pruned from the code walk.
	IgnoreDirs []string
	// MaxFileBytes skips files larger than this during the code walk.
	MaxFileBytes int64
	// EnableComplexity turns on the lexical complexity estimator.
	EnableComplexity bool
	// DetectWorkspace turns on monorepo detection and the per-package health
	// breakdown.
	//
	// It is off by default because detection reads several root manifests and
	// resolves their globs, which is wasted work on the far more common
	// single-repository target. It costs one linear pass over the records the
	// walk already produced, so enabling it on a workspace does not re-scan
	// anything.
	DetectWorkspace bool
	// Timeout bounds the whole scan; zero means no deadline.
	Timeout time.Duration
}

// DefaultConfig returns the baseline configuration for target.
func DefaultConfig(target string) Config {
	cc := code.DefaultConfig()
	return Config{
		Target:           target,
		GitWindowDays:    90,
		HotspotThreshold: cc.HotspotThreshold,
		IgnoreDirs:       cc.IgnoreDirs,
		MaxFileBytes:     cc.MaxFileBytes,
		EnableComplexity: true,
		Timeout:          60 * time.Second,
	}
}

// Validate checks the configuration and normalizes the target path.
func (c Config) Validate() (Config, error) {
	abs, err := filepath.Abs(c.Target)
	if err != nil {
		return c, fmt.Errorf("resolve target %q: %w", c.Target, err)
	}
	c.Target = filepath.Clean(abs)
	if c.GitWindowDays <= 0 {
		return c, fmt.Errorf("git_window_days must be positive, got %d", c.GitWindowDays)
	}
	if c.HotspotThreshold <= 0 {
		return c, fmt.Errorf("hotspot_threshold must be positive, got %d", c.HotspotThreshold)
	}
	return c, nil
}

// codeConfig derives the code analyzer config from the scan config.
func (c Config) codeConfig() code.Config {
	cc := code.DefaultConfig()
	cc.HotspotThreshold = c.HotspotThreshold
	if len(c.IgnoreDirs) > 0 {
		cc.IgnoreDirs = c.IgnoreDirs
	}
	if c.MaxFileBytes > 0 {
		cc.MaxFileBytes = c.MaxFileBytes
	}
	cc.EnableComplexity = c.EnableComplexity
	return cc
}

// Scan runs every analyzer concurrently and assembles a scored Snapshot.
//
// The git, code, and dependency analyzers are independent, so they run on
// their own goroutines and report through a buffered channel. Analyzer failures
// degrade to findings rather than aborting the run, except for a fatal
// configuration or filesystem error.
//
// One step is deliberately sequential: hotspot classification joins the code
// walk's per-file records with the git churn data. Both inputs are already in
// memory by then, and the join is a single linear pass, so serializing it costs
// nothing while keeping the expensive filesystem walk concurrent with the git
// subprocesses.
func Scan(ctx context.Context, cfg Config, toolVersion string) (*models.Snapshot, error) {
	cfg, err := cfg.Validate()
	if err != nil {
		return nil, err
	}
	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}
	// Fail fast on an already-cancelled context rather than doing the work and
	// discarding the result.
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	started := time.Now()

	codeCfg := cfg.codeConfig()
	gitCfg := gitanalyzer.DefaultConfig()
	gitCfg.WindowDays = cfg.GitWindowDays

	type result struct {
		name     string
		code     *code.Result
		git      *models.GitStats
		churn    map[string]int
		deps     *models.DependencyStats
		findings []models.Finding
		err      error
	}

	// Buffered so no analyzer can block on send even if the collector is
	// momentarily busy; capacity matches the fan-out exactly.
	ch := make(chan result, 3)
	var wg sync.WaitGroup

	run := func(name string, fn func() result) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := fn()
			r.name = name
			ch <- r
		}()
	}

	// The code walk runs without churn data; hotspots are reclassified after
	// the git analyzer reports in.
	run("code", func() result {
		res, err := code.Analyze(cfg.Target, codeCfg, nil)
		if err != nil {
			return result{err: err}
		}
		return result{code: &res}
	})

	run("git", func() result {
		res, err := gitanalyzer.Analyze(ctx, cfg.Target, gitCfg)
		if errors.Is(err, gitanalyzer.ErrNoRepository) {
			// Not an error condition: a plain directory is a valid target.
			return result{git: &res.Stats}
		}
		if err != nil {
			return result{
				git: &models.GitStats{WindowDays: cfg.GitWindowDays},
				findings: []models.Finding{{
					Severity: models.SeverityInfo,
					Category: models.CategoryGit,
					Title:    "Git analysis unavailable",
					Detail:   err.Error(),
				}},
			}
		}
		return result{git: &res.Stats, churn: res.ChurnByPath, findings: res.Findings}
	})

	run("dependencies", func() result {
		res, err := dependencies.Analyze(cfg.Target, dependencies.DefaultConfig())
		if err != nil {
			return result{
				deps: &models.DependencyStats{},
				findings: []models.Finding{{
					Severity: models.SeverityInfo,
					Category: models.CategoryDependency,
					Title:    "Dependency analysis unavailable",
					Detail:   err.Error(),
				}},
			}
		}
		return result{deps: &res.Stats, findings: res.Findings}
	})

	wg.Wait()
	close(ch)

	snap := &models.Snapshot{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       toolVersion,
		Root:          cfg.Target,
		GeneratedAt:   time.Now().UTC(),
		Code: models.CodeStats{
			Languages:  []models.LanguageStat{},
			Hotspots:   []models.Hotspot{},
			Complexity: models.ComplexitySummary{WorstFiles: []models.FileComplexity{}},
		},
		Git:          models.GitStats{WindowDays: cfg.GitWindowDays, Churn: []models.ChurnEntry{}},
		Dependencies: models.DependencyStats{Ecosystems: []models.EcosystemStats{}},
		Findings:     []models.Finding{},
		Risks:        []models.Risk{},
	}

	var (
		fatal    error
		churn    map[string]int
		codeRes  *code.Result
		hadFatal bool
	)
	for r := range ch {
		if r.err != nil {
			// A hard failure means the target itself is unreadable.
			hadFatal = true
			fatal = errors.Join(fatal, fmt.Errorf("%s analyzer: %w", r.name, r.err))
			continue
		}
		if r.code != nil {
			codeRes = r.code
		}
		if r.git != nil {
			snap.Git = *r.git
		}
		if r.churn != nil {
			churn = r.churn
		}
		if r.deps != nil {
			snap.Dependencies = *r.deps
		}
		snap.Findings = append(snap.Findings, r.findings...)
	}
	if hadFatal {
		return nil, fatal
	}

	// Join step: reclassify hotspots now that churn is known. Cheap because it
	// operates on the retained per-file records, not the filesystem.
	if codeRes != nil {
		updated := codeRes.ApplyChurn(churn, codeCfg)
		snap.Code = updated.Stats
		// Replace the size-only findings with the churn-aware versions. The
		// walk's own findings are dropped because ApplyChurn rebuilds them
		// with full context, including skipped-entry counts.
		snap.Findings = replaceCodeFindings(snap.Findings, updated.Findings)
	}

	snap.Health = metrics.Compute(snap.Code, snap.Git, snap.Dependencies).Health
	snap.Risks = risk.Assess(snap.Code, snap.Git, snap.Dependencies, snap.Health)
	SortFindings(snap.Findings)
	SortRisks(snap.Risks)

	// The workspace breakdown runs last so it can read the final risk list for
	// attribution and reuse the already-computed churn map. It reuses the walk's
	// per-file records, so no second filesystem pass happens.
	if cfg.DetectWorkspace {
		if err := addWorkspace(ctx, cfg, snap, codeRes, churn); err != nil {
			// A workspace breakdown is an addition to the analysis, not a
			// precondition for it. Failing here would mean a monorepo whose
			// workspace file is unreadable could not be analyzed at all, which
			// is a worse outcome than reporting the repository without a
			// breakdown.
			return snap, fmt.Errorf("workspace breakdown: %w", err)
		}
	}

	snap.DurationMS = time.Since(started).Milliseconds()

	return snap, nil
}

// addWorkspace detects a workspace layout and fills in the per-package scores.
func addWorkspace(
	ctx context.Context,
	cfg Config,
	snap *models.Snapshot,
	codeRes *code.Result,
	churn map[string]int,
) error {
	ws, err := monorepo.Detect(cfg.Target)
	if err != nil {
		return err
	}
	if ws == nil {
		// Not a monorepo. Leaving Workspace nil means the field is omitted
		// from the JSON entirely rather than emitted as an empty object.
		return nil
	}

	var perFile []code.FileRecord
	if codeRes != nil {
		perFile = codeRes.PerFile
	}
	if err := monorepo.Attribute(ctx, cfg.Target, ws, perFile, churn, cfg.codeConfig()); err != nil {
		return err
	}
	monorepo.AttributeRisks(ws, snap.Risks)

	snap.Workspace = ws
	return nil
}

// replaceCodeFindings swaps the code-category findings produced before the
// churn join for the post-join versions, leaving dependency and git findings
// untouched.
func replaceCodeFindings(existing, refreshed []models.Finding) []models.Finding {
	kept := make([]models.Finding, 0, len(existing)+len(refreshed))
	for _, f := range existing {
		if f.Category != models.CategoryCode &&
			f.Category != models.CategoryComplexity &&
			f.Category != models.CategoryTesting {
			kept = append(kept, f)
		}
	}
	return append(kept, refreshed...)
}

// SortFindings orders findings by severity desc, then title, then subject.
func SortFindings(findings []models.Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Severity.Rank() != findings[j].Severity.Rank() {
			return findings[i].Severity.Rank() > findings[j].Severity.Rank()
		}
		if findings[i].Title != findings[j].Title {
			return findings[i].Title < findings[j].Title
		}
		return findings[i].Subject < findings[j].Subject
	})
}

// SortRisks orders risks by severity desc, then impact desc, then ID.
//
// The ID tiebreak makes the order total, which keeps output byte-stable across
// runs regardless of how the risk engine iterated its internal maps.
func SortRisks(risks []models.Risk) {
	sort.SliceStable(risks, func(i, j int) bool {
		if risks[i].Severity != risks[j].Severity {
			return risks[i].Severity.Rank() > risks[j].Severity.Rank()
		}
		if risks[i].Impact != risks[j].Impact {
			return risks[i].Impact > risks[j].Impact
		}
		return risks[i].ID < risks[j].ID
	})
}
