package code

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zelvior/lensyxe/pkg/models"
)

// Hotspot candidacy and the rationale shown for each one.
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
