package code

import (
	"fmt"
	"path"
	"sort"

	"github.com/zelvior/lensyxe/pkg/models"
)

// The roll-up: records in, CodeStats and findings out.

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

// ---------------------------------------------------------------------------
// findings
// ---------------------------------------------------------------------------
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
