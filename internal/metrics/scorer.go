// Package metrics implements the deterministic Engineering Health Score.
//
// The score is a weighted average of three normalized sub-scores:
//
//	Code health            weight 0.40
//	Dependency health      weight 0.30
//	Maintainability (Git)  weight 0.30
//
// Every sub-score is a pure function of the analyzer output using fixed
// thresholds, so the same repository always yields the same score. There is no
// randomness, no time-of-day input, and no model inference anywhere in this
// package.
package metrics

import (
	"fmt"
	"sort"

	"github.com/zelvior/lensyxe/pkg/models"
)

// Weights for the three sub-scores. They sum to 1.0.
const (
	WeightCode = 0.40
	WeightDeps = 0.30
	WeightGit  = 0.30
)

// Scoring thresholds. Exported so the CLI can document them and tests can pin
// them; changing a value changes scores for everyone, so treat them as API.
const (
	// Code health
	MaxFilesPerLanguage = 40    // ratio at which language fragmentation bottoms the score
	IdealAvgFileLines   = 150.0 // average code lines per file considered ideal
	AvgLinesPenaltyRate = 150.0 // extra lines above ideal before full penalty
	HotspotSharePenalty = 0.30  // hotspot share of code lines for full penalty

	// Fragmentation. The size component is two-sided on purpose: a healthy
	// file sits in a band, and both a 2000-line file and a 3-line file are
	// maintenance costs.
	//
	// Without the lower bound the score is trivially gameable: splitting
	// code into many tiny files raises the average-per-file measure toward
	// the ideal and pins language cohesion at 100, so fragmentation
	// *increases* the score. Two repositories with identical logic then
	// score 100 and 2 purely on how they were cut up.
	MinAvgFileLines     = 25.0 // average at or above which a file is not "too small"
	FragmentPenaltyRate = 15.0 // lines below the floor before full fragmentation penalty

	// Dependency health
	MaxDirectDeps     = 50   // direct deps at which the dependency score bottoms
	UnlockedPenalty   = 0.20 // absolute score penalty when no lockfile is present
	NoManifestPenalty = 0.10

	// Git / maintainability
	IdealCommitsPerWeek = 8.0
	LowCadenceFloor     = 0.5 // commits/week below which cadence scores zero
	StaleDaysPenalty    = 180 // days since last commit at which freshness hits zero
	// ChurnConcentrationThreshold is the normalized churn concentration at
	// which the churn component of the git score hits zero.
	ChurnConcentrationThreshold = 0.6
	BusFactorPenalty            = 0.15
)

// Score combines analyzer output into a Health plus the derived Risks that
// explain the score.
type Score struct {
	Health  models.Health
	Risks   []models.Risk
	Summary string
}

// Compute derives the Engineering Health Score from the raw stats.
//
// Metrics whose input data is unavailable are marked not applicable and their
// weight is redistributed across the remaining metrics, so a non-git directory
// is judged on code and dependencies instead of being punished for having no
// git history.
func Compute(code models.CodeStats, git models.GitStats, deps models.DependencyStats) Score {
	metrics := []models.Metric{
		scoreCode(code),
		scoreDependencies(deps),
		scoreGit(git),
	}

	totalWeight, weighted := 0.0, 0.0
	for _, m := range metrics {
		if !m.Applicable {
			continue
		}
		totalWeight += m.Weight
		weighted += m.Score * m.Weight
	}

	score := 0.0
	if totalWeight > 0 {
		// Renormalize so the applicable weights sum to 1 before averaging.
		score = weighted / totalWeight
	}
	score = clamp(score, 0, 100)

	// Normalize weights so applicable metrics sum to exactly 1 and
	// non-applicable ones report 0, keeping the JSON weight column honest.
	for i := range metrics {
		switch {
		case !metrics[i].Applicable:
			metrics[i].Weight = 0
		case totalWeight > 0:
			metrics[i].Weight = round2(metrics[i].Weight / totalWeight)
		}
	}
	sort.SliceStable(metrics, func(i, j int) bool {
		if metrics[i].Weight != metrics[j].Weight {
			return metrics[i].Weight > metrics[j].Weight
		}
		return metrics[i].Key < metrics[j].Key
	})

	components := 0
	for _, m := range metrics {
		if m.Applicable {
			components++
		}
	}

	health := models.Health{
		Score:      round2(score),
		Grade:      models.Grade(score),
		Summary:    summarize(score, metrics),
		Metrics:    metrics,
		Components: components,
	}
	return Score{Health: health, Risks: buildRisks(metrics), Summary: health.Summary}
}

// scoreCode scores the codebase on size discipline and language cohesion.
//
// Larger files are harder to change safely and are the primary refactoring
// signal; heavy language fragmentation raises integration cost.
func scoreCode(s models.CodeStats) models.Metric {
	m := models.Metric{
		Key:    "code",
		Label:  "Code health",
		Weight: WeightCode,
	}

	if s.Files == 0 {
		m.Detail = "no source files detected"
		return m // not applicable
	}
	m.Applicable = true
	m.Value = s.AverageLines
	m.Display = fmt.Sprintf("%.1f LOC/file across %d files", s.AverageLines, s.Files)

	// Component 1: average file size, penalized at BOTH extremes.
	//
	// Above the ideal band, files are too big to work in safely. Below the
	// floor, they are fragments: overhead without cohesion, and a codebase
	// that is one refactor away from scattering a single unit across dozens of
	// files. The band is what makes the measure resist gaming.
	sizeScore := 100.0
	switch {
	case s.AverageLines > IdealAvgFileLines:
		excess := (s.AverageLines - IdealAvgFileLines) / AvgLinesPenaltyRate
		sizeScore = 100 * (1 - clamp(excess, 0, 1))
	case s.AverageLines < MinAvgFileLines:
		shortfall := (MinAvgFileLines - s.AverageLines) / FragmentPenaltyRate
		sizeScore = 100 * (1 - clamp(shortfall, 0, 1))
	}

	// Component 2: hotspot concentration. Share of code lines sitting in
	// oversized files, capped by the configured threshold.
	var hotspotLines int
	for _, h := range s.Hotspots {
		hotspotLines += h.Lines
	}
	hotspotShare := 0.0
	if s.CodeLines > 0 {
		hotspotShare = float64(hotspotLines) / float64(s.CodeLines)
	}
	hotspotScore := 100 * (1 - clamp(hotspotShare/HotspotSharePenalty, 0, 1))

	// Component 3: language cohesion. Many single-file LANGUAGES suggest a
	// scattered codebase.
	//
	// This deliberately measures language spread only. It must not be read as
	// a general "more files is better" signal: file granularity is handled by
	// the two-sided size component above, and rewarding it here as well is
	// what made the score gameable.
	fragmentScore := 100.0
	if len(s.Languages) > 0 {
		avgFilesPerLang := float64(s.Files) / float64(len(s.Languages))
		if avgFilesPerLang < 1 {
			fragmentScore = 0
		} else if avgFilesPerLang < MaxFilesPerLanguage {
			fragmentScore = 100 * (avgFilesPerLang / MaxFilesPerLanguage)
		}
	}

	// Weighted blend. Weights sum to 1.
	m.Score = round2(0.50*sizeScore + 0.30*hotspotScore + 0.20*fragmentScore)
	m.Detail = fmt.Sprintf("size %.0f (%s), hotspots %.0f%% of lines, cohesion %.0f",
		sizeScore, sizeVerdict(s.AverageLines), hotspotShare*100, fragmentScore)
	return m
}

// sizeVerdict names which side of the healthy band the average file size falls
// on, so a low score is actionable without reading the source.
func sizeVerdict(avg float64) string {
	switch {
	case avg > IdealAvgFileLines:
		return "files too large"
	case avg < MinAvgFileLines:
		return "files too fragmented"
	default:
		return "in band"
	}
}

// scoreDependencies scores the declared dependency surface.
func scoreDependencies(d models.DependencyStats) models.Metric {
	m := models.Metric{
		Key:    "dependency",
		Label:  "Dependency health",
		Weight: WeightDeps,
	}

	if !d.Detected {
		// A project with no manifest has no dependency risk to speak of, but
		// the metric stays applicable at a neutral baseline minus a small
		// penalty for being unverifiable.
		m.Applicable = true
		m.Value = 0
		m.Display = "no manifest detected"
		m.Score = 100 - NoManifestPenalty*100
		m.Detail = "no supported manifest found; dependency risk unverifiable"
		return m
	}

	m.Applicable = true
	m.Value = float64(d.Direct)
	m.Display = fmt.Sprintf("%d direct / %d total dependencies", d.Direct, d.Total)

	// Component 1: dependency count, linear from full credit at zero to zero
	// at MaxDirectDeps.
	countScore := 100 * (1 - clamp(float64(d.Direct)/MaxDirectDeps, 0, 1))

	// Component 2: reproducibility. A missing lockfile is an absolute penalty
	// because builds stop being deterministic.
	reproScore := 100.0
	if !d.Locked {
		reproScore = 100 * (1 - UnlockedPenalty)
	}

	// Component 3: ecosystem spread. Multiple ecosystems multiply the
	// toolchain surface area.
	spreadScore := 100.0
	if len(d.Ecosystems) > 1 {
		spreadScore = 100 / float64(len(d.Ecosystems))
	}

	m.Score = round2(clamp(0.50*countScore+0.30*reproScore+0.20*spreadScore, 0, 100))
	m.Detail = fmt.Sprintf("count %.0f, reproducibility %.0f, spread %.0f",
		countScore, reproScore, spreadScore)
	return m
}

// scoreGit scores maintainability signals from local history.
func scoreGit(g models.GitStats) models.Metric {
	m := models.Metric{
		Key:    "git",
		Label:  "Maintainability (Git)",
		Weight: WeightGit,
	}

	if !g.IsRepository {
		m.Detail = "target is not a git repository"
		return m // not applicable: nothing to judge
	}
	m.Applicable = true
	m.Value = g.CommitsPerWeek
	m.Display = fmt.Sprintf("%.1f commits/week, %d authors", g.CommitsPerWeek, g.Authors)

	if g.TotalCommits == 0 {
		m.Score = 0
		m.Detail = "repository has no commits"
		return m
	}

	// Component 1: commit cadence. Full credit from LowCadenceFloor up to
	// IdealCommitsPerWeek; zero below the floor.
	cadenceScore := 100.0
	if g.CommitsPerWeek < IdealCommitsPerWeek {
		if g.CommitsPerWeek <= LowCadenceFloor {
			cadenceScore = 0
		} else {
			span := IdealCommitsPerWeek - LowCadenceFloor
			cadenceScore = 100 * ((g.CommitsPerWeek - LowCadenceFloor) / span)
		}
	}

	// Component 2: freshness. Full credit for a commit inside the window,
	// decaying linearly to zero at StaleDaysPenalty days.
	freshnessScore := 0.0
	if g.LastCommitAt.IsZero() {
		freshnessScore = 50 // unknown recency: partial credit, not full
	} else {
		ratio := float64(g.DaysSinceCommit) / StaleDaysPenalty
		freshnessScore = 100 * (1 - clamp(ratio, 0, 1))
	}

	// Component 3: churn concentration. One file soaking up most changes is a
	// merge-conflict and ownership-risk signal. ChurnConcentration is already
	// normalized against an even distribution, so it does not penalize small
	// repositories for having few files.
	churnScore := 100 * (1 - clamp(g.ChurnConcentration/ChurnConcentrationThreshold, 0, 1))

	// Component 4: knowledge distribution. A bus factor of one is a risk.
	busScore := 100.0
	if g.BusFactor <= 1 {
		busScore = 100 * (1 - BusFactorPenalty)
	}

	m.Score = round2(clamp(0.30*cadenceScore+0.25*freshnessScore+
		0.25*churnScore+0.20*busScore, 0, 100))
	m.Detail = fmt.Sprintf("cadence %.0f, freshness %.0f, churn %.0f, bus factor %d",
		cadenceScore, freshnessScore, churnScore, g.BusFactor)
	return m
}

// buildRisks turns the weakest scoring components into concrete, ranked risks.
func buildRisks(metrics []models.Metric) []models.Risk {
	risks := make([]models.Risk, 0, 4)
	for _, m := range metrics {
		if !m.Applicable {
			continue
		}
		deficit := 100 - m.Score
		if deficit < 10 {
			continue
		}
		sev := models.SeverityMedium
		switch {
		case deficit >= 60:
			sev = models.SeverityCritical
		case deficit >= 40:
			sev = models.SeverityHigh
		}
		risks = append(risks, models.Risk{
			Severity: sev,
			Category: categoryFor(m.Key),
			Title:    m.Label + " below expectations",
			Detail:   m.Detail,
			Impact:   round2(deficit * m.Weight),
		})
	}
	sort.SliceStable(risks, func(i, j int) bool {
		if risks[i].Impact != risks[j].Impact {
			return risks[i].Impact > risks[j].Impact
		}
		return risks[i].Title < risks[j].Title
	})
	return risks
}

func categoryFor(key string) models.Category {
	switch key {
	case "code":
		return models.CategoryCode
	case "dependency":
		return models.CategoryDependency
	default:
		return models.CategoryGit
	}
}

// summarize renders a one-line verdict from the score and the weakest metric.
func summarize(score float64, metrics []models.Metric) string {
	var weakest *models.Metric
	for i := range metrics {
		m := metrics[i]
		if !m.Applicable {
			continue
		}
		if weakest == nil || m.Score < weakest.Score {
			weakest = &metrics[i]
		}
	}
	// label is declared rather than initialised because the switch below is
	// exhaustive: its default branch covers everything below 40, so no
	// initial value is ever read. Giving it one suggested a reachable fallback
	// band that does not exist, and a reader could reasonably expect a score
	// between 55 and 70 to fall through to it.
	var label string
	switch {
	case score >= 85:
		label = "healthy"
	case score >= 70:
		label = "adequate"
	case score >= 55:
		label = "needs work"
	case score >= 40:
		label = "at risk"
	default:
		label = "critical"
	}
	if weakest == nil {
		return fmt.Sprintf("No scorable dimensions were available (score %.1f).", score)
	}
	return fmt.Sprintf("Overall %s (%.1f/100). Weakest dimension: %s at %.1f.",
		label, score, weakest.Label, weakest.Score)
}

// clamp bounds v to [lo, hi]. NaN collapses to lo, which keeps a corrupt
// measurement from propagating into the final score.
func clamp(v, lo, hi float64) float64 {
	switch {
	case v != v: // NaN
		return lo
	case v < lo:
		return lo
	case v > hi:
		return hi
	default:
		return v
	}
}

// round2 rounds to two decimals deterministically (half away from zero).
func round2(v float64) float64 {
	scaled := v * 100
	if scaled >= 0 {
		scaled = float64(int64(scaled + 0.5))
	} else {
		scaled = float64(int64(scaled - 0.5))
	}
	return scaled / 100
}
