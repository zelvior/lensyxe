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
package code

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

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
func Aggregate(records []FileRecord, churnByPath map[string]int, cfg Config, prefix string) Result {
	if len(records) == 0 {
		return Result{
			Stats: models.CodeStats{
				Languages:  []models.LanguageStat{},
				Hotspots:   []models.Hotspot{},
				Complexity: models.ComplexitySummary{WorstFiles: []models.FileComplexity{}},
			},
			PerFile:  []FileRecord{},
			Findings: []models.Finding{},
		}
	}

	var (
		langLines = map[string]int{}
		langFiles = map[string]int{}
		langTests = map[string]int{}

		totalLines, codeLines, maxLines   int
		testFiles, sourceFiles, testLines int
		bytesTotal                        int64

		complexities []models.FileComplexity
		sumFuncs     int
		sumBranches  int
		sumComplex   float64
		maxNesting   int
		maxComplex   float64
		maxPath      string
		veryHigh     int
		high         int
	)

	// Hoist the prefix out of the loop: the records themselves are reused, but
	// a re-aggregated record needs a path the hotspot classifier can match
	// against the churn map, which is keyed by repo-relative path.
	normalized := records
	if prefix != "" {
		normalized = make([]FileRecord, len(records))
		for i, r := range records {
			r.Path = path.Join(prefix, r.Path)
			normalized[i] = r
		}
	}

	for _, rec := range normalized {
		totalLines += rec.TotalLines
		codeLines += rec.CodeLines
		bytesTotal += rec.Bytes
		langFiles[rec.Language]++
		langLines[rec.Language] += rec.CodeLines

		if rec.CodeLines > maxLines {
			maxLines = rec.CodeLines
		}
		if rec.IsTest {
			testFiles++
			langTests[rec.Language]++
			testLines += rec.CodeLines
		} else {
			sourceFiles++
		}

		if !rec.ComplexityMeasured {
			// Same rule as the walk: an unscored language is absent from the
			// aggregate rather than counted as trivial.
			continue
		}
		complexities = append(complexities, models.FileComplexity{
			Path:                rec.Path,
			Language:            rec.Language,
			Lines:               rec.CodeLines,
			Functions:           rec.Functions,
			BranchPoints:        rec.BranchPoints,
			MaxNesting:          rec.MaxNesting,
			EstimatedComplexity: rec.ComplexityScore,
			MaxFunctionComplex:  rec.MaxFunctionComplexity,
			Level:               rec.Complexity,
			Density:             0, // recomputed below from the subset total
		})
		sumFuncs += rec.Functions
		sumBranches += rec.BranchPoints
		sumComplex += rec.ComplexityScore
		if rec.MaxNesting > maxNesting {
			maxNesting = rec.MaxNesting
		}
		if rec.ComplexityScore > maxComplex {
			maxComplex = rec.ComplexityScore
			maxPath = rec.Path
		}
		switch rec.Complexity {
		case models.ComplexityVeryHigh:
			veryHigh++
		case models.ComplexityHigh:
			high++
		}
	}

	// Density is per 100 code lines of the subset, so a package's figure is
	// comparable with the repository's.
	for i := range complexities {
		if codeLines > 0 {
			complexities[i].Density = round2(complexities[i].EstimatedComplexity * 100 / float64(codeLines))
		}
	}

	stats := models.CodeStats{
		Files:          len(normalized),
		TotalLines:     totalLines,
		CodeLines:      codeLines,
		BlankOrComment: totalLines - codeLines,
		MaxFileLines:   maxLines,
		Bytes:          bytesTotal,
		Languages:      buildLanguages(langLines, langFiles, langTests, cfg.LanguageLimit),
		TestFiles:      testFiles,
		SourceFiles:    sourceFiles,
		HasTests:       testFiles > 0,
	}
	if len(normalized) > 0 {
		stats.AverageLines = round2(float64(codeLines) / float64(len(normalized)))
	}
	if total := testFiles + sourceFiles; total > 0 {
		stats.TestFileRatio = round2(float64(testFiles) / float64(total))
	}
	if codeLines > 0 {
		stats.TestLineRatio = round2(float64(testLines) / float64(codeLines))
	}
	stats.Complexity = summarizeComplexity(
		complexities, len(complexities), sumFuncs, sumBranches, sumComplex,
		maxNesting, maxComplex, maxPath, veryHigh, high,
		complexityLimit(cfg),
	)
	stats.Hotspots = classifyHotspots(normalized, churnByPath, cfg.HotspotThreshold,
		cfg.HotspotLimit, DefaultHotspotRules())

	return Result{
		Stats:    stats,
		Findings: buildFindings(stats, 0),
		PerFile:  normalized,
	}
}

// complexityLimit returns the effective per-file complexity cap.
func complexityLimit(cfg Config) int {
	if cfg.ComplexityLimit > 0 {
		return cfg.ComplexityLimit
	}
	return complexityDefaultLimit
}

// summarizeComplexity aggregates per-file complexity into a repository summary.
func summarizeComplexity(
	all []models.FileComplexity,
	count, sumFuncs, sumBranches int,
	sumComplexity float64,
	maxNesting int,
	maxComplexity float64,
	maxComplexityPath string,
	veryHighFuncs, highFuncs, limit int,
) models.ComplexitySummary {
	s := models.ComplexitySummary{
		Measured:          count > 0,
		Files:             count,
		Functions:         sumFuncs,
		BranchPoints:      sumBranches,
		MaxNesting:        maxNesting,
		VeryHighFunctions: veryHighFuncs,
		HighFunctions:     highFuncs,
		MaxComplexity:     round2(maxComplexity),
		MaxComplexityFile: maxComplexityPath,
		WorstFiles:        []models.FileComplexity{},
	}
	if count > 0 {
		s.AverageComplexity = round2(sumComplexity / float64(count))
	}
	if !s.Measured {
		return s
	}

	// Sort a copy: the caller's slice order is walk order and must not change.
	worst := append([]models.FileComplexity(nil), all...)
	sort.SliceStable(worst, func(i, j int) bool {
		if worst[i].EstimatedComplexity != worst[j].EstimatedComplexity {
			return worst[i].EstimatedComplexity > worst[j].EstimatedComplexity
		}
		return worst[i].Path < worst[j].Path
	})
	if limit > 0 && len(worst) > limit {
		worst = worst[:limit]
	}
	s.WorstFiles = worst
	return s
}

// classifyHotspots selects and ranks hotspot files.
//
// Two independent decisions are made here, and conflating them would make the
// configurable threshold useless:
//
//  1. Candidacy. Any file at or above sizeThreshold is a hotspot candidate.
//     This is the user's knob, and it stays exactly as configured.
//
//  2. Confirmation. A candidate is Confirmed only when size, churn, and
//     complexity all cross the documented rule thresholds (LOC >= MinLines,
//     churn >= MinChurn, complexity rank >= MinComplexityRank). Size alone is
//     weak evidence: a long file of declarative data changes rarely and
//     breaks little, so it must not be reported as a confirmed hotspot.
//
// A caller who lowers sizeThreshold below the rule's MinLines therefore still
// sees candidates, and simply never sees them confirmed on the size factor
// alone.
func classifyHotspots(
	records []FileRecord,
	churnByPath map[string]int,
	sizeThreshold, limit int,
	rules HotspotRules,
) []models.Hotspot {
	if sizeThreshold <= 0 {
		sizeThreshold = rules.MinLines
	}

	out := make([]models.Hotspot, 0, limit)
	for _, rec := range records {
		if rec.CodeLines < sizeThreshold {
			continue
		}
		churn := 0
		if churnByPath != nil {
			churn = churnByPath[rec.Path]
		}
		h := models.Hotspot{
			Path:       rec.Path,
			Lines:      rec.CodeLines,
			Language:   rec.Language,
			Churn:      churn,
			Complexity: rec.ComplexityScore,
			Level:      rec.Complexity,
		}

		// Met factors are collected in a fixed order, so the token list is
		// deterministic without sorting.
		var factors []string
		if rec.CodeLines >= rules.MinLines {
			factors = append(factors, "size")
		}
		if churn >= rules.MinChurn {
			factors = append(factors, "churn")
		}
		// An unmeasured language can never satisfy the complexity factor: a zero
		// level would read as "simple" and let a declarative file be confirmed
		// as a hotspot on two factors it never earned.
		if rec.ComplexityMeasured && rec.Complexity.Rank() >= rules.MinComplexityRank {
			factors = append(factors, "complexity")
		}

		h.Confirmed = len(factors) == 3
		if h.Confirmed {
			factors = append(factors, "confirmed")
		}
		h.Classification = strings.Join(factors, ",")
		h.Rationale = hotspotRationale(rec, churn, rules, h.Confirmed)
		out = append(out, h)
	}

	// Confirmed hotspots first, then by descending size, then by path for a
	// stable tie-break.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Confirmed != out[j].Confirmed {
			return out[i].Confirmed
		}
		if out[i].Lines != out[j].Lines {
			return out[i].Lines > out[j].Lines
		}
		return out[i].Path < out[j].Path
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// hotspotRationale renders the one-sentence explanation with real numbers.
func hotspotRationale(rec FileRecord, churn int, rules HotspotRules, confirmed bool) string {
	if confirmed {
		return fmt.Sprintf(
			"%d code lines (>%d), %d lines churned in window (>%d), %s complexity (>%s): large, actively changing, and hard to change.",
			rec.CodeLines, rules.MinLines, churn, rules.MinChurn,
			rec.Complexity, complexityRankName(rules.MinComplexityRank))
	}
	var missing []string
	if rec.CodeLines < rules.MinLines {
		missing = append(missing, fmt.Sprintf("size %d<=%d", rec.CodeLines, rules.MinLines))
	}
	if churn < rules.MinChurn {
		missing = append(missing, fmt.Sprintf("churn %d<=%d", churn, rules.MinChurn))
	}
	if !rec.ComplexityMeasured {
		// The estimator does not score this language, so it cannot be the
		// reason the file fell short. Saying "complexity low" here would be
		// a measurement claim that was never made.
		missing = append(missing, "complexity not measured for "+rec.Language)
	} else if rec.Complexity.Rank() < rules.MinComplexityRank {
		missing = append(missing, fmt.Sprintf("complexity %s<=%s",
			rec.Complexity, complexityRankName(rules.MinComplexityRank)))
	}
	return fmt.Sprintf("%d code lines; below the confirmed-hotspot bar on %s.",
		rec.CodeLines, strings.Join(missing, ", "))
}

// complexityRankName maps a complexity rank back to a level name for prose.
func complexityRankName(rank int) models.ComplexityLevel {
	levels := []models.ComplexityLevel{
		models.ComplexityLow, models.ComplexityModerate,
		models.ComplexityHigh, models.ComplexityVeryHigh,
	}
	if rank < 0 || rank >= len(levels) {
		return models.ComplexityVeryHigh
	}
	return levels[rank]
}

// languageRule describes how to strip comments for one language family.
type languageRule struct {
	language string
	// lineComments are prefixes that mark a full-line comment.
	lineComments []string
	// blockStart/blockEnd delimit block comments; empty disables block handling.
	blockStart string
	blockEnd   string
	// stringDelims are the quote characters that open a string or character
	// literal. Literal contents are blanked before token and brace counting,
	// so a line like ContainsRune(s, '{') cannot fake a nesting level.
	stringDelims []byte
}

// controlFlowLanguages is the set of languages the lexical complexity estimator
// is allowed to score.
//
// Membership is opt-in rather than opt-out so a newly added language is treated
// as declarative until someone shows it has control flow. The failure mode of
// guessing wrong is what this guards against: in a GitHub Actions workflow,
// `if:`, `for:`, and `when:` are mapping keys and any shell embedded in a
// `run: |` block would be counted as YAML branching, producing a confident,
// meaningless number. "Not measured" is the honest answer for those files;
// a fabricated score is worse than no score, because it drives the risk engine.
var controlFlowLanguages = map[string]bool{
	"Go": true, "C": true, "C++": true, "C#": true, "Java": true,
	"JavaScript": true, "TypeScript": true, "Rust": true, "Zig": true,
	"Swift": true, "Kotlin": true, "Scala": true, "PHP": true, "Ruby": true,
	"Python": true, "Shell": true, "SQL": true, "Lua": true,
}

// hasControlFlow reports whether the lexical estimator applies to a language.
func hasControlFlow(lang string) bool { return controlFlowLanguages[lang] }

var (
	dq   = []byte{'"'}
	dqS  = []byte{'"', '\''}
	dqB  = []byte{'"', '\'', '`'}
	dqCh = []byte{'"', '\''}
)

var rules = map[string]languageRule{
	"Go":         {language: "Go", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqB},
	"C":          {language: "C", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"C++":        {language: "C++", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"C#":         {language: "C#", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dq},
	"Java":       {language: "Java", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dq},
	"JavaScript": {language: "JavaScript", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqB},
	"TypeScript": {language: "TypeScript", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqB},
	"Rust":       {language: "Rust", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"Zig":        {language: "Zig", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"Swift":      {language: "Swift", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dq},
	"Kotlin":     {language: "Kotlin", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dq},
	"Scala":      {language: "Scala", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"PHP":        {language: "PHP", lineComments: []string{"//", "#"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"Ruby":       {language: "Ruby", lineComments: []string{"#"}, blockStart: "=begin", blockEnd: "=end", stringDelims: dqCh},
	"Python":     {language: "Python", lineComments: []string{"#"}, stringDelims: dqS},
	"Shell":      {language: "Shell", lineComments: []string{"#"}, stringDelims: dqB},
	"YAML":       {language: "YAML", lineComments: []string{"#"}, stringDelims: dqS},
	"SQL":        {language: "SQL", lineComments: []string{"--"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"Lua":        {language: "Lua", lineComments: []string{"--"}, blockStart: "--[[", blockEnd: "]]", stringDelims: dqCh},
}

// extensions maps lowercase file extensions to a language name. Keys are the
// full extension including the dot.
var extensions = map[string]string{
	".go": "Go", ".c": "C", ".h": "C", ".cc": "C++", ".cpp": "C++", ".cxx": "C++",
	".hpp": "C++", ".hh": "C++", ".cs": "C#", ".java": "Java",
	".js": "JavaScript", ".jsx": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript",
	".ts": "TypeScript", ".tsx": "TypeScript", ".mts": "TypeScript", ".cts": "TypeScript",
	".rs": "Rust", ".zig": "Zig", ".swift": "Swift", ".kt": "Kotlin", ".kts": "Kotlin",
	".scala": "Scala", ".sc": "Scala", ".php": "PHP",
	".rb": "Ruby", ".py": "Python", ".pyi": "Python", ".sh": "Shell", ".bash": "Shell", ".zsh": "Shell",
	".yml": "YAML", ".yaml": "YAML", ".sql": "SQL", ".lua": "Lua",
	".vue": "TypeScript", ".svelte": "TypeScript",
}

// languageOf resolves a filename to a language, or reports false for files
// Lensyxe does not count (docs, lockfiles, assets, images).
func languageOf(name string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	lang, ok := extensions[ext]
	return lang, ok
}

// fileScan is the per-file result of one streaming pass.
type fileScan struct {
	physical   int
	code       int
	complexity complexityScanner
}

// ChurnMap turns churn entries into the path -> added+deleted lookup that
// hotspot classification needs.
func ChurnMap(entries []models.ChurnEntry) map[string]int {
	if len(entries) == 0 {
		return nil
	}
	m := make(map[string]int, len(entries))
	for _, e := range entries {
		m[e.Path] = e.Added + e.Deleted
	}
	return m
}

// scanFile reads path once and returns physical lines, code lines, and the
// accumulated complexity estimate.
//
// Counting is intentionally simple and allocation-light: bufio.Scanner with a
// large buffer, no regex, no AST. This keeps the scan fast and the result
// reproducible across platforms (CRLF is normalized by the scanner).
func scanFile(path, language string, withComplexity bool) (fileScan, error) {
	var out fileScan

	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()

	rule, hasRule := rules[language]
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	inBlock := false

	for sc.Scan() {
		line := sc.Text()
		out.physical++

		stripped, isComment, nextInBlock := classifyLine(line, rule, hasRule, inBlock)
		inBlock = nextInBlock
		if isComment || strings.TrimSpace(stripped) == "" {
			continue
		}
		out.code++
		if withComplexity {
			out.complexity.feed(strings.TrimSpace(stripped))
		}
	}
	if err := sc.Err(); err != nil {
		// Treat scanner failures (usually "token too long") as a read error.
		return out, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// classifyLine decides whether a raw line is a comment and returns the line
// with comment markers and string-literal contents removed, suitable for token
// and brace counting.
//
// The returned boolean reports "this whole line is comment or blank".
//
// String literals MUST be blanked. Brace counting runs on this output, and a
// single line like `strings.ContainsRune(s, '{')` would otherwise add a level
// of nesting with no matching close, inflating MaxNesting without bound across
// a file that mentions brace characters in string form. Character literals are
// blanked for the same reason.
func classifyLine(raw string, rule languageRule, hasRule bool, inBlock bool) (stripped string, skip bool, stillInBlock bool) {
	if !hasRule {
		return raw, false, inBlock
	}

	trimmed := strings.TrimSpace(raw)
	if inBlock {
		if rule.blockEnd == "" || !strings.Contains(trimmed, rule.blockEnd) {
			return "", true, true
		}
		// The block comment ends mid-line; anything after it is real code.
		idx := strings.Index(trimmed, rule.blockEnd)
		rest := strings.TrimSpace(trimmed[idx+len(rule.blockEnd):])
		return blankStringLiterals(rest, rule.stringDelims), rest == "", false
	}

	if trimmed == "" {
		return "", true, false
	}

	// String literals MUST be blanked before comment markers are searched for.
	// Doing it the other way round truncates a line like
	//     []string{"//"}, blockStart: "/*"},
	// at the "//" inside the literal, leaving the brace unbalanced and
	// inflating MaxNesting for the rest of the file.
	trimmed = blankStringLiterals(trimmed, rule.stringDelims)

	// A trailing line comment does not make the whole line a comment, but the
	// marker and everything after it must not be counted as code.
	if cut := strings.Index(trimmed, rule.lineComments[0]); cut >= 0 {
		trimmed = strings.TrimSpace(trimmed[:cut])
	}
	// Strip additional line-comment prefixes (PHP accepts both // and #).
	for _, prefix := range rule.lineComments[1:] {
		if cut := strings.Index(trimmed, prefix); cut >= 0 {
			trimmed = strings.TrimSpace(trimmed[:cut])
		}
	}

	if rule.blockStart != "" && strings.HasPrefix(trimmed, rule.blockStart) {
		rest := trimmed[len(rule.blockStart):]
		if rule.blockEnd != "" {
			if idx := strings.Index(rest, rule.blockEnd); idx >= 0 {
				// Single-line block comment: /** ... */
				return strings.TrimSpace(rest[idx+len(rule.blockEnd):]), true, false
			}
		}
		return "", true, true
	}
	if trimmed == "" {
		return "", true, false
	}
	return trimmed, false, false
}

// blankStringLiterals replaces the contents of string literals with spaces.
//
// Blanking rather than deleting preserves byte offsets and, more importantly,
// keeps tokens on either side of a literal separated, so `a"x"b` does not
// collapse into the single identifier `axb`.
//
// Escape handling is deliberately minimal but correct for the cases that
// matter here: a backslash escapes the next byte, so `\"` does not terminate a
// literal. Unterminated literals blank to end of line, which is the safe
// direction for a lexical heuristic: it can only under-count, never fabricate a
// brace.
func blankStringLiterals(line string, delims []byte) string {
	if len(delims) == 0 || !containsAnyByte(line, delims) {
		return line
	}
	out := []byte(line)
	esc := byte(0)
	for i := 0; i < len(out); i++ {
		c := out[i]
		if esc != 0 {
			out[i] = ' '
			esc = 0
			continue
		}
		if c == '\\' {
			esc = c
			continue
		}
		if !isDelim(c, delims) {
			continue
		}
		// Find the closing delimiter.
		j := i + 1
		for j < len(out) {
			if out[j] == '\\' {
				j += 2
				continue
			}
			if out[j] == c {
				break
			}
			j++
		}
		// Blank the interior, leaving both delimiters visible so the line still
		// reads as containing a literal.
		for k := i + 1; k < j && k < len(out); k++ {
			out[k] = ' '
		}
		if j >= len(out) {
			break // unterminated: nothing more to do
		}
		i = j
	}
	return string(out)
}

func isDelim(c byte, delims []byte) bool {
	for _, d := range delims {
		if c == d {
			return true
		}
	}
	return false
}

func containsAnyByte(s string, set []byte) bool {
	for i := 0; i < len(s); i++ {
		if isDelim(s[i], set) {
			return true
		}
	}
	return false
}

// countLines is the compatibility wrapper used by existing callers and tests.
// It counts physical and code lines without the complexity estimate.
func countLines(path, language string) (int, int, error) {
	s, err := scanFile(path, language, false)
	return s.physical, s.code, err
}

// buildLanguages flattens the per-language maps into a sorted slice.
func buildLanguages(lines, files, tests map[string]int, limit int) []models.LanguageStat {
	out := make([]models.LanguageStat, 0, len(lines))
	for lang, n := range lines {
		out = append(out, models.LanguageStat{
			Name: lang, Files: files[lang], Lines: n, TestFiles: tests[lang],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Lines != out[j].Lines {
			return out[i].Lines > out[j].Lines
		}
		return out[i].Name < out[j].Name
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// buildFindings derives factual observations from the stats. Findings never
// alter the score directly; they explain it.
func buildFindings(stats models.CodeStats, skipped int) []models.Finding {
	f := make([]models.Finding, 0, 4)
	for _, h := range stats.Hotspots {
		if h.Confirmed {
			f = append(f, models.Finding{
				Severity: models.SeverityHigh,
				Category: models.CategoryCode,
				Title:    "Confirmed hotspot",
				Detail:   h.Rationale,
				Subject:  h.Path,
			})
			continue
		}
		// An unconfirmed candidate is size alone, which is weak evidence.
		// Reporting a dozen of these at medium severity drowns out the real
		// findings, so they stay at low.
		f = append(f, models.Finding{
			Severity: models.SeverityLow,
			Category: models.CategoryCode,
			Title:    "Large file / hotspot candidate",
			Detail:   h.Rationale,
			Subject:  h.Path,
		})
	}
	if stats.Complexity.Measured && stats.Complexity.VeryHighFunctions > 0 {
		f = append(f, models.Finding{
			Severity: models.SeverityMedium,
			Category: models.CategoryComplexity,
			Title:    "Very high estimated complexity",
			Detail: fmt.Sprintf("%d file(s) contain definitions above the very-high complexity threshold (max %.1f at %s).",
				stats.Complexity.VeryHighFunctions,
				stats.Complexity.MaxComplexity,
				stats.Complexity.MaxComplexityFile),
		})
	}
	if !stats.HasTests && stats.Files > 0 {
		f = append(f, models.Finding{
			Severity: models.SeverityMedium,
			Category: models.CategoryTesting,
			Title:    "No test files detected",
			Detail:   fmt.Sprintf("None of the %d source files matched a test naming convention.", stats.Files),
		})
	}
	if stats.Truncated {
		f = append(f, models.Finding{
			Severity: models.SeverityLow,
			Category: models.CategoryCode,
			Title:    "Oversized files skipped",
			Detail:   "Some files exceeded the size cap and were excluded from LOC counts.",
		})
	}
	if skipped > 0 {
		f = append(f, models.Finding{
			Severity: models.SeverityInfo,
			Category: models.CategoryCode,
			Title:    "Unreadable entries skipped",
			Detail:   fmt.Sprintf("%d entries could not be read and were skipped.", skipped),
		})
	}
	if stats.Files == 0 {
		f = append(f, models.Finding{
			Severity: models.SeverityInfo,
			Category: models.CategoryCode,
			Title:    "No source files detected",
			Detail:   "No recognized source file extensions were found under the target path.",
		})
	}
	return f
}

func toSet(values []string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		m[v] = true
	}
	return m
}

func round2(v float64) float64 {
	// Deterministic half-away-from-zero rounding to two decimals, avoiding the
	// banker's rounding of math.Round on exact .5 boundaries.
	scaled := v * 100
	if scaled >= 0 {
		scaled = float64(int64(scaled + 0.5))
	} else {
		scaled = float64(int64(scaled - 0.5))
	}
	return scaled / 100
}
