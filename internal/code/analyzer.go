// Package code implements the source-code analyzer.
//
// It walks the target tree once, classifies every file by language, counts
// physical and code lines, estimates lexical complexity, identifies test
// files, and reports files that combine size, churn, and complexity risk.
//
// The walk is filesystem-local only: no network, no build execution, no code
// generation. Churn data is supplied by the caller because it originates in
// the git analyzer; keeping that dependency one-directional avoids an import
// cycle and keeps this package testable without a repository.
//
// # Layout
//
// The walk, the per-file measurement, and the two things derived from them are
// separate files. The lexical line classifier is the only part that needs to
// know about source languages, and it is the part most likely to be edited when
// a language is added -- so keeping it away from the walk means that edit cannot
// disturb the aggregation.
//
//	analyzer.go   config, the tree walk, and the result type
//	lex.go        per-file measurement: line classification and complexity
//	language.go   the language table lex.go dispatches through
//	hotspot.go    hotspot candidacy and its rationale
//	aggregate.go  the roll-up and the findings
package code

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zelvior/lensyxe/pkg/models"
)

// Config tunes the code analyzer. The zero value is not usable; use
// DefaultConfig and adjust from there.
type Config struct {
	// IgnoreDirs are directory names pruned during the walk (matched at any depth).
	IgnoreDirs []string
	// MaxFileBytes skips files larger than this, guarding against vendored
	// blobs and generated artifacts that would distort LOC counts.
	MaxFileBytes int64
	// HotspotThreshold is the code-line count at which a file becomes a
	// size-based hotspot candidate.
	HotspotThreshold int
	// HotspotLimit caps how many hotspots are retained.
	HotspotLimit int
	// LanguageLimit caps how many language rows are retained.
	LanguageLimit int
	// ComplexityLimit caps how many per-file complexity rows are retained in
	// the summary. Zero uses complexityDefaultLimit.
	ComplexityLimit int
	// EnableComplexity turns the complexity estimator on. It costs one extra
	// pass of bookkeeping over lines already being read.
	EnableComplexity bool
}

// complexityDefaultLimit is the number of worst-complexity files retained.
const complexityDefaultLimit = 10

// DefaultConfig returns the baseline code analyzer configuration.
func DefaultConfig() Config {
	return Config{
		// IgnoreDirs holds directory names pruned from the walk, matched at any
		// depth. Every entry is either a VCS/editor directory or the
		// conventional name for vendored or generated content.
		//
		// "assets" belongs here for the same reason "dist" and "out" do: it is
		// the standard name for static content that a bundler produced, not
		// source anyone writes. Scoring a minified webpack chunk as a
		// 2,686-complexity source file distorts the language breakdown, the
		// average file size, and the score.
		//
		// A project that keeps hand-written source under a directory named
		// "assets" can remove this entry via the ignore_dirs config key.
		IgnoreDirs: []string{
			".git", ".hg", ".svn", ".idea", ".vscode",
			"node_modules", "vendor", "dist", "build", "out", "target",
			"coverage", ".next", ".nuxt", ".cache", ".venv", "venv",
			"__pycache__", ".terraform", ".gradle", "bin", "obj",
			"assets", "public", "static",
		},
		MaxFileBytes:     2 << 20, // 2 MiB
		HotspotThreshold: 400,
		HotspotLimit:     10,
		LanguageLimit:    8,
		ComplexityLimit:  complexityDefaultLimit,
		EnableComplexity: true,
	}
}

// HotspotRules define the three-factor test that promotes a file to a
// confirmed hotspot: it must be large, it must be actively changing, and it
// must be hard to change.
type HotspotRules struct {
	// MinLines is the code-line count at or above which a file counts as large.
	MinLines int
	// MinChurn is the added+deleted line count in the git window at or above
	// which a file counts as actively changing.
	MinChurn int
	// MinComplexityRank is the lowest complexity band that counts as hard to
	// change. Compared with ComplexityLevel.Rank so the rule stays ordered.
	MinComplexityRank int
	// MaxLimit caps how many hotspots are returned.
	MaxLimit int
}

// DefaultHotspotRules returns the documented three-factor confirmation
// thresholds: LOC at or above 500, churn at or above 30 modified lines, and
// complexity at least High.
//
// These gate confirmation only. Candidate selection uses the separately
// configurable HotspotThreshold, so lowering that below 500 still lists
// candidates and simply never confirms them on size alone.
func DefaultHotspotRules() HotspotRules {
	return HotspotRules{
		MinLines:          500,
		MinChurn:          30,
		MinComplexityRank: models.ComplexityHigh.Rank(),
		MaxLimit:          10,
	}
}

// Result pairs the computed stats with any non-fatal observations.
type Result struct {
	Stats    models.CodeStats
	Findings []models.Finding
	// PerFile keeps the full per-file detail needed for hotspot
	// classification. It is not part of CodeStats because the JSON contract
	// only exposes the ranked summaries.
	PerFile []FileRecord
}

// ApplyChurn recomputes hotspot classification against churn data and returns
// an updated Result.
//
// This exists because the tree walk and the git analysis run concurrently, so
// churn is not available when the (expensive) walk happens. Reclassifying from
// the retained per-file records costs one linear pass over the file list,
// which is far cheaper than walking the filesystem twice.
func (r Result) ApplyChurn(churnByPath map[string]int, cfg Config) Result {
	r.Stats.Hotspots = classifyHotspots(r.PerFile, churnByPath,
		cfg.HotspotThreshold, cfg.HotspotLimit, DefaultHotspotRules())
	r.Findings = buildFindings(r.Stats, 0)
	return r
}

// FileRecord is the per-file measurement produced during the walk.
type FileRecord struct {
	Path      string
	Language  string
	CodeLines int
	// TotalLines is the physical line count, so a subset of records can be
	// re-aggregated into a faithful CodeStats without re-reading the file.
	TotalLines int
	// Bytes is the file size on disk, retained for the same reason.
	Bytes      int64
	IsTest     bool
	Complexity models.ComplexityLevel
	// ComplexityMeasured is false for languages the lexical estimator does
	// not score, such as declarative config. The zero ComplexityLevel on such
	// a file means "not measured", never "simple", so it must never be
	// reported as a measured level.
	ComplexityMeasured bool
	// ComplexityScore is the mean estimated cyclomatic complexity.
	ComplexityScore float64
	// MaxFunctionComplexity is the worst single-function estimate.
	MaxFunctionComplexity float64
	// BranchPoints and MaxNesting back the complexity numbers.
	BranchPoints int
	MaxNesting   int
	Functions    int
}

// Analyze walks root and returns deterministic code statistics.
//
// churnByPath maps repo-relative paths to their added+deleted line count in the
// git window. Pass nil when git data is unavailable; size-only hotspots are
// still reported, but none will be marked confirmed.
//
// The walk order is irrelevant: every aggregate is either a sum or is sorted
// before returning, so identical trees always produce identical output.
func Analyze(root string, cfg Config, churnByPath map[string]int) (Result, error) {
	info, err := os.Stat(root)
	if err != nil {
		return Result{}, fmt.Errorf("code analyzer: stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("code analyzer: %s is not a directory", root)
	}

	ignore := toSet(cfg.IgnoreDirs)

	langLines := map[string]int{}
	langFiles := map[string]int{}
	langTests := map[string]int{}
	records := make([]FileRecord, 0, 128)
	complexities := make([]models.FileComplexity, 0, 128)

	var (
		files, totalLines, codeLines, maxLines int
		testFiles, sourceFiles, testLines      int
		bytesTotal                             int64
		truncated                              bool
	)
	skipped := 0
	sumFuncs, sumBranches, maxNesting := 0, 0, 0
	veryHighFuncs, highFuncs := 0, 0
	var sumComplexity float64
	maxComplexity := 0.0
	maxComplexityPath := ""

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable entries are counted and skipped; a single permission
			// error must not abort the whole scan.
			skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if ignore[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // skip symlinks, devices, sockets
		}

		lang, ok := languageOf(d.Name())
		if !ok {
			return nil
		}

		fi, err := d.Info()
		if err != nil {
			skipped++
			return nil
		}
		if cfg.MaxFileBytes > 0 && fi.Size() > cfg.MaxFileBytes {
			truncated = true
			return nil
		}

		scan, err := scanFile(path, lang, cfg.EnableComplexity)
		if err != nil {
			skipped++
			return nil
		}

		files++
		totalLines += scan.physical
		codeLines += scan.code
		bytesTotal += fi.Size()
		langFiles[lang]++
		langLines[lang] += scan.code
		if scan.code > maxLines {
			maxLines = scan.code
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		isTest := isTestPath(rel)
		if isTest {
			testFiles++
			testLines += scan.code
			// The per-language test tally. This was missing: langTests was
			// declared and handed to buildLanguages but never incremented here,
			// so every row of the language breakdown reported zero test files
			// however many tests the repository had. Aggregate() has always
			// counted them correctly, which is what made the two paths disagree
			// and hid the omission -- only the analyze path showed the zeros.
			langTests[lang]++
		} else {
			sourceFiles++
		}

		rec := FileRecord{
			Path:                  rel,
			Language:              lang,
			CodeLines:             scan.code,
			TotalLines:            scan.physical,
			Bytes:                 fi.Size(),
			IsTest:                isTest,
			Complexity:            models.ComplexityLow,
			ComplexityScore:       0,
			MaxFunctionComplexity: 0,
		}
		// A declarative file is left out of the complexity aggregate
		// entirely rather than scored as trivial: reporting it as low
		// would claim a measurement was made, and scoring it would claim
		// a false one.
		if cfg.EnableComplexity && hasControlFlow(lang) {
			cx := scan.complexity.estimate(scan.code)
			rec.ComplexityMeasured = true
			rec.Complexity = cx.Level
			rec.ComplexityScore = cx.EstimatedComplexity
			rec.MaxFunctionComplexity = cx.MaxFunctionComplex
			rec.BranchPoints = cx.BranchPoints
			rec.MaxNesting = cx.MaxNesting
			rec.Functions = cx.Functions

			complexities = append(complexities, models.FileComplexity{
				Path:                rel,
				Language:            lang,
				Lines:               scan.code,
				Functions:           cx.Functions,
				BranchPoints:        cx.BranchPoints,
				MaxNesting:          cx.MaxNesting,
				EstimatedComplexity: cx.EstimatedComplexity,
				MaxFunctionComplex:  cx.MaxFunctionComplex,
				Level:               cx.Level,
				Density:             cx.Density,
			})
			sumFuncs += cx.Functions
			sumBranches += cx.BranchPoints
			sumComplexity += cx.EstimatedComplexity
			if cx.MaxNesting > maxNesting {
				maxNesting = cx.MaxNesting
			}
			if cx.EstimatedComplexity > maxComplexity {
				maxComplexity = cx.EstimatedComplexity
				maxComplexityPath = rel
			}
			switch cx.Level {
			case models.ComplexityVeryHigh:
				veryHighFuncs++
			case models.ComplexityHigh:
				highFuncs++
			}
		}
		records = append(records, rec)
		return nil
	})
	if err != nil {
		return Result{}, fmt.Errorf("code analyzer: walk %s: %w", root, err)
	}

	stats := models.CodeStats{
		Files:          files,
		TotalLines:     totalLines,
		CodeLines:      codeLines,
		BlankOrComment: totalLines - codeLines,
		MaxFileLines:   maxLines,
		Bytes:          bytesTotal,
		Languages:      buildLanguages(langLines, langFiles, langTests, cfg.LanguageLimit),
		Truncated:      truncated,
		TestFiles:      testFiles,
		SourceFiles:    sourceFiles,
		HasTests:       testFiles > 0,
	}
	if files > 0 {
		stats.AverageLines = round2(float64(codeLines) / float64(files))
	}
	if total := testFiles + sourceFiles; total > 0 {
		stats.TestFileRatio = round2(float64(testFiles) / float64(total))
	}
	if codeLines > 0 {
		stats.TestLineRatio = round2(float64(testLines) / float64(codeLines))
	}
	stats.Complexity = summarizeComplexity(
		complexities, len(complexities), sumFuncs, sumBranches, sumComplexity,
		maxNesting, maxComplexity, maxComplexityPath, veryHighFuncs, highFuncs,
		complexityLimit(cfg),
	)

	// Hotspots are classified last so churn data can be joined in.
	stats.Hotspots = classifyHotspots(records, churnByPath, cfg.HotspotThreshold,
		cfg.HotspotLimit, DefaultHotspotRules())

	if records == nil {
		records = []FileRecord{}
	}
	// PerFile must be returned even when empty so ApplyChurn can reclassify
	// without another filesystem walk.
	return Result{
		Stats:    stats,
		Findings: buildFindings(stats, skipped),
		PerFile:  records,
	}, nil
}

// Aggregate builds CodeStats from a subset of an existing walk's records.
//
// It exists so a monorepo breakdown can score each package without walking the
// filesystem once per package: the expensive pass has already happened, and the
// per-file records it retained are enough to reconstruct every aggregate the
// scorer reads. A second walk per package would multiply scan time by the
// package count for no new information.
//
// churnByPath is passed through to hotspot classification. A package's
// hotspots are confirmed against its own files' churn, so a file cannot be
// confirmed by churn it contributed to a sibling package.
//
// repoRelative is true when paths are already repo-relative. It is false when
// the records came from walking a package directory directly, in which case
// paths are package-relative and are prefixed here.
