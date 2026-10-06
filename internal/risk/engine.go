// Package risk implements evidence-first risk assessment.
//
// A risk is not a finding. A finding states what was measured; a risk states
// what that measurement threatens, how much of the health score it explains,
// and exactly which numbers support the claim. Every Risk this package emits
// carries at least one Evidence entry, and the severity is a pure function of
// those numbers, so two runs on the same tree produce identical output.
//
// Classification bands, by Impact (the share of the 0-100 health score a risk
// is responsible for):
//
//	🔴 Critical   impact >= 12
//	🟠 High       impact >= 6
//	🟡 Medium     impact >= 2
//	🔵 Low        impact > 0
package risk

import (
	"fmt"
	"sort"

	"github.com/zelvior/lensyxe/pkg/models"
)

// Classification bands. Exported so tests and docs can pin them.
const (
	CriticalImpact = 12.0
	HighImpact     = 6.0
	MediumImpact   = 2.0
)

// Weight of each category in the impact calculation. Weights sum to 1.0.
//
// The values are deliberately modest: risks explain the score, they do not
// double-count it. The metrics scorer remains the single source of the
// score; this package only decomposes the deficit.
const (
	weightCode       = 0.40
	weightDependency = 0.30
	weightGit        = 0.30
)

// Thresholds for individual heuristics. Each is tied to the point at which the
// corresponding scorer component begins losing points, so a risk appears
// exactly when the score starts to move.
const (
	// Complexity: a file is worth flagging above the "high" band.
	complexityFlagRank = 2 // models.ComplexityHigh.Rank()

	// Testing: test-to-code ratio below this is a risk. 0.15 means fewer
	// than one test file per six source files.
	lowTestRatio = 0.15
	// noTestsImpact is the impact when a repository has no tests at all.
	noTestsImpact = 4.0

	// Dependency drift: share of declared dependencies that are unverified.
	unlockedImpact = 5.0

	// Large direct dependency surface, as a fraction of the full-penalty
	// point, contributing to impact.
	dependencyPenaltyRate = 50.0

	// Churn: a single file absorbing this share of window churn is a risk.
	churnConcentrationFlag = 0.60

	// Bus factor: a single author owning at least this share of window commits.
	busFactorShare = 0.70

	// Staleness: days since the last commit at which inactivity is a risk.
	staleDays = 180
)

// Assess derives the full risk list for a snapshot.
//
// Input is the raw analyzer output plus the scored health, so the engine can
// explain the score without recomputing it. Output is sorted by severity, then
// impact descending, then ID, which makes the ordering total and stable.
func Assess(
	code models.CodeStats,
	git models.GitStats,
	deps models.DependencyStats,
	health models.Health,
) []models.Risk {
	risks := make([]models.Risk, 0, 16)
	risks = append(risks, hotspotRisks(code)...)
	risks = append(risks, complexityRisks(code)...)
	risks = append(risks, testingRisks(code)...)
	risks = append(risks, dependencyRisks(deps)...)
	risks = append(risks, gitRisks(git)...)
	risks = append(risks, dimensionRisks(health)...)

	for i := range risks {
		risks[i].Severity = classify(risks[i].Impact)
	}
	sortRisks(risks)
	return risks
}

// classify maps an impact to a severity band.
func classify(impact float64) models.Severity {
	switch {
	case impact >= CriticalImpact:
		return models.SeverityCritical
	case impact >= HighImpact:
		return models.SeverityHigh
	case impact >= MediumImpact:
		return models.SeverityMedium
	default:
		return models.SeverityLow
	}
}

// hotspotRisks flags files that combine size, churn, and complexity.
func hotspotRisks(code models.CodeStats) []models.Risk {
	var out []models.Risk
	for _, h := range code.Hotspots {
		// A size-only hotspot is a maintenance note, not a risk: long files
		// that never change do not threaten the codebase.
		if !h.Confirmed {
			continue
		}
		// Impact scales with how far past the thresholds the file sits,
		// capped so one enormous file cannot dominate the whole report.
		sizeExcess := float64(h.Lines) / 500.0
		churnExcess := float64(h.Churn) / 30.0
		cxExcess := 1.0
		if h.Complexity > 0 {
			cxExcess = h.Complexity / 20.0
		}
		severityFactor := 3.0 + 2.0*sizeExcess + 1.5*churnExcess + 2.0*cxExcess
		impact := capImpact(severityFactor * weightCode)

		out = append(out, models.Risk{
			ID:       "code.hotspot." + h.Path,
			Category: models.CategoryCode,
			Title:    "Confirmed hotspot",
			Detail: fmt.Sprintf(
				"Hotspot detected in %s: %s It is simultaneously large, actively changing, and hard to change, which is where defects concentrate.",
				h.Path, h.Rationale),
			Subject: h.Path,
			Impact:  impact,
			Evidence: []models.Evidence{
				models.NewEvidence("file", "LOC", fmt.Sprintf("%d code lines", h.Lines),
					float64(h.Lines), h.Path),
				models.NewEvidence("metric", "Churn", fmt.Sprintf("%d lines modified in the git window", h.Churn),
					float64(h.Churn), h.Path),
				models.NewEvidence("metric", "Complexity",
					fmt.Sprintf("%s estimated cyclomatic complexity (%s)", formatComplexity(h.Complexity), h.Level),
					h.Complexity, h.Path),
			},
			Recommendation: fmt.Sprintf(
				"Split %s until no unit exceeds the size threshold, and add tests around the decision-heavy code before changing it further.",
				h.Path),
		})
	}
	return out
}

// complexityRisks flags files whose estimated complexity is extreme.
func complexityRisks(code models.CodeStats) []models.Risk {
	if !code.Complexity.Measured {
		return nil
	}
	var out []models.Risk

	// Repository-level risk when a meaningful number of files are very high.
	if code.Complexity.VeryHighFunctions > 0 {
		share := float64(code.Complexity.VeryHighFunctions) / float64(maxInt(code.Complexity.Files, 1))
		impact := capImpact(share * 30.0 * weightCode)
		out = append(out, models.Risk{
			ID:       "code.complexity.distribution",
			Category: models.CategoryComplexity,
			Title:    "Very high complexity concentration",
			Detail: fmt.Sprintf(
				"%d of %d analyzed file(s) exceed the very-high complexity threshold (max %s in %s).",
				code.Complexity.VeryHighFunctions, code.Complexity.Files,
				formatComplexity(code.Complexity.MaxComplexity), code.Complexity.MaxComplexityFile),
			Subject: code.Complexity.MaxComplexityFile,
			Impact:  impact,
			Evidence: []models.Evidence{
				models.NewEvidence("metric", "Files very high",
					fmt.Sprintf("%d of %d", code.Complexity.VeryHighFunctions, code.Complexity.Files),
					float64(code.Complexity.VeryHighFunctions), ""),
				models.NewEvidence("metric", "Max complexity",
					fmt.Sprintf("%s in %s", formatComplexity(code.Complexity.MaxComplexity), code.Complexity.MaxComplexityFile),
					code.Complexity.MaxComplexity, code.Complexity.MaxComplexityFile),
				models.NewEvidence("metric", "Max nesting",
					fmt.Sprintf("%d levels", code.Complexity.MaxNesting),
					float64(code.Complexity.MaxNesting), ""),
			},
			Recommendation: "Flatten nesting in the worst file and break long conditional chains into named predicates.",
		})
	}

	// Per-file risk for the single worst offender, which is more actionable
	// than a distribution statistic.
	if worst := worstComplexityFile(code); worst != nil {
		if worst.Level.Rank() < complexityFlagRank {
			return out
		}
		impact := capImpact(worst.EstimatedComplexity / 20.0 * 8.0 * weightCode)
		out = append(out, models.Risk{
			ID:       "code.complexity." + worst.Path,
			Category: models.CategoryComplexity,
			Title:    "File exceeds complexity threshold",
			Detail: fmt.Sprintf(
				"%s averages %s estimated cyclomatic complexity with %d branch points across %d definition(s), nesting %d deep.",
				worst.Path, formatComplexity(worst.EstimatedComplexity),
				worst.BranchPoints, worst.Functions, worst.MaxNesting),
			Subject: worst.Path,
			Impact:  impact,
			Evidence: []models.Evidence{
				models.NewEvidence("metric", "Complexity",
					fmt.Sprintf("%s (%s band)", formatComplexity(worst.EstimatedComplexity), worst.Level),
					worst.EstimatedComplexity, worst.Path),
				models.NewEvidence("metric", "Branch points",
					fmt.Sprintf("%d decision tokens", worst.BranchPoints),
					float64(worst.BranchPoints), worst.Path),
				models.NewEvidence("metric", "Max nesting",
					fmt.Sprintf("%d levels", worst.MaxNesting),
					float64(worst.MaxNesting), worst.Path),
			},
			Recommendation: "Extract the longest conditional chain in this file into a small number of named functions.",
		})
	}
	return out
}

// testingRisks flags a weak or absent test suite.
func testingRisks(code models.CodeStats) []models.Risk {
	if code.SourceFiles == 0 {
		return nil // no production code, no test-coverage opinion
	}
	var out []models.Risk

	if !code.HasTests {
		out = append(out, models.Risk{
			ID:       "testing.none",
			Category: models.CategoryTesting,
			Title:    "No tests detected",
			Detail: fmt.Sprintf(
				"None of the %d source files matched a test naming convention, so test-to-code ratio is 0.00.",
				code.SourceFiles),
			Impact: noTestsImpact,
			Evidence: []models.Evidence{
				models.NewEvidence("metric", "Test files", "0 identified", 0, ""),
				models.NewEvidence("metric", "Source files",
					fmt.Sprintf("%d analyzed", code.SourceFiles), float64(code.SourceFiles), ""),
			},
			Recommendation: "Add tests for the highest-churn modules first; they are where regressions are most likely.",
		})
		return out
	}

	if code.TestFileRatio < lowTestRatio {
		// Impact scales with the gap, so a repo at 0.14 is flagged mildly and
		// one at 0.01 is flagged strongly.
		gap := (lowTestRatio - code.TestFileRatio) / lowTestRatio
		impact := capImpact(gap * 6.0 * weightCode)
		out = append(out, models.Risk{
			ID:       "testing.ratio",
			Category: models.CategoryTesting,
			Title:    "Low test-to-code ratio",
			Detail: fmt.Sprintf(
				"Test-to-code file ratio is %.2f (%d test files vs %d source files), below the %.2f threshold.",
				code.TestFileRatio, code.TestFiles, code.SourceFiles, lowTestRatio),
			Impact: impact,
			Evidence: []models.Evidence{
				models.NewEvidence("metric", "Test file ratio",
					fmt.Sprintf("%.2f (%d test / %d source)", code.TestFileRatio, code.TestFiles, code.SourceFiles),
					code.TestFileRatio, ""),
				models.NewEvidence("metric", "Test line share",
					fmt.Sprintf("%.1f%% of code lines live in test files", code.TestLineRatio*100),
					code.TestLineRatio, ""),
			},
			Recommendation: "Target the ratio above 0.15 by covering the modules with the highest churn.",
		})
	}
	return out
}

// dependencyRisks flags drift and an oversized dependency surface.
func dependencyRisks(deps models.DependencyStats) []models.Risk {
	var out []models.Risk

	for _, eco := range deps.Ecosystems {
		if eco.Drift {
			out = append(out, models.Risk{
				ID:       "dependency.drift." + eco.Name,
				Category: models.CategoryDependency,
				Title:    "Missing lockfile",
				Detail: fmt.Sprintf("Missing lockfile for %s: %s",
					eco.Manifest, eco.DriftReason),
				Subject: eco.Manifest,
				Impact:  capImpact(unlockedImpact * weightDependency),
				Evidence: []models.Evidence{
					models.NewEvidence("lockfile", "Lockfile",
						"no lockfile found alongside the manifest", 0, eco.Manifest),
					models.NewEvidence("metric", "Direct dependencies",
						fmt.Sprintf("%d declared without a pinned resolution", eco.Direct),
						float64(eco.Direct), eco.Manifest),
				},
				Recommendation: fmt.Sprintf(
					"Commit the lockfile for %s so installs resolve to identical versions.", eco.Name),
			})
		} else if eco.LockfileStale {
			out = append(out, models.Risk{
				ID:       "dependency.lockfile_stale." + eco.Name,
				Category: models.CategoryDependency,
				Title:    "Lockfile older than manifest",
				Detail: fmt.Sprintf(
					"%s was modified after %s, so the lockfile may not match the declared dependency set.",
					eco.Manifest, eco.Lockfile),
				Subject: eco.Lockfile,
				Impact:  1.0,
				Evidence: []models.Evidence{
					models.NewEvidence("lockfile", "Lockfile format", eco.LockfileFormat, 0, eco.Lockfile),
					models.NewEvidence("metric", "Manifest mtime",
						"newer than lockfile mtime", 0, eco.Manifest),
				},
				Recommendation: "Regenerate and commit the lockfile to confirm it still resolves the declared set.",
			})
		}

		if eco.Direct > 0 && eco.Transitive > 0 {
			// A large multiplier means a small manifest drags in a deep tree.
			multiplier := float64(eco.Transitive) / float64(eco.Direct)
			if multiplier >= 10 && eco.Direct > 5 {
				excess := (multiplier - 10) / 10
				impact := capImpact(excess * 4.0 * weightDependency)
				out = append(out, models.Risk{
					ID:       "dependency.transitive." + eco.Name,
					Category: models.CategoryDependency,
					Title:    "Deep transitive tree",
					Detail: fmt.Sprintf(
						"%s declares %d direct dependencies that resolve to %d packages (%.1fx multiplier).",
						eco.Manifest, eco.Direct, eco.Transitive, multiplier),
					Subject: eco.Manifest,
					Impact:  impact,
					Evidence: []models.Evidence{
						models.NewEvidence("lockfile", "Resolved packages",
							fmt.Sprintf("%d in %s", eco.Transitive, eco.Lockfile),
							float64(eco.Transitive), eco.Lockfile),
						models.NewEvidence("metric", "Direct dependencies",
							fmt.Sprintf("%d declared", eco.Direct), float64(eco.Direct), eco.Manifest),
					},
					Recommendation: "Review whether all direct dependencies are still needed; each one extends the update surface.",
				})
			}
		}
	}
	return out
}

// gitRisks flags maintainability risks from local history.
func gitRisks(git models.GitStats) []models.Risk {
	if !git.IsRepository || git.TotalCommits == 0 {
		return nil
	}
	var out []models.Risk

	if git.BusFactor <= 1 && git.TopAuthorShare >= busFactorShare {
		out = append(out, models.Risk{
			ID:       "git.bus_factor",
			Category: models.CategoryGit,
			Title:    "Bus factor of one",
			Detail: fmt.Sprintf(
				"A single author owns %.0f%% of recent commits across %d author(s), so knowledge of the codebase is concentrated in one person.",
				git.TopAuthorShare*100, git.Authors),
			Impact: capImpact(git.TopAuthorShare * 6.0 * weightGit),
			Evidence: []models.Evidence{
				models.NewEvidence("metric", "Top author share",
					fmt.Sprintf("%.0f%% of window commits", git.TopAuthorShare*100),
					git.TopAuthorShare, ""),
				models.NewEvidence("metric", "Authors",
					fmt.Sprintf("%d in the last %d days", git.Authors, git.WindowDays),
					float64(git.Authors), ""),
			},
			Recommendation: "Pair on the highest-churn modules so at least two people can review changes to them.",
		})
	}

	if git.ChurnConcentration >= churnConcentrationFlag && len(git.Churn) > 0 {
		out = append(out, models.Risk{
			ID:       "git.churn_concentration",
			Category: models.CategoryGit,
			Title:    "Churn concentrated in few files",
			Detail: fmt.Sprintf(
				"Change is concentrated: the top files hold %.1f%% of window churn across %d changed files (concentration %.2f).",
				git.ChurnHotspotRate*100, git.ChurnFiles, git.ChurnConcentration),
			Subject: git.Churn[0].Path,
			Impact:  capImpact(git.ChurnConcentration * 5.0 * weightGit),
			Evidence: []models.Evidence{
				models.NewEvidence("file", "Top churn file",
					fmt.Sprintf("%s: %d lines modified in %d commits", git.Churn[0].Path,
						git.Churn[0].Added+git.Churn[0].Deleted, git.Churn[0].Commits),
					float64(git.Churn[0].Score), git.Churn[0].Path),
				models.NewEvidence("metric", "Concentration",
					fmt.Sprintf("%.2f (1.00 means one file took everything)", git.ChurnConcentration),
					git.ChurnConcentration, ""),
			},
			Recommendation: "Reduce churn in the top file by splitting responsibilities, or accept it and raise its test coverage.",
		})
	}

	if git.DaysSinceCommit >= staleDays {
		out = append(out, models.Risk{
			ID:       "git.stale",
			Category: models.CategoryGit,
			Title:    "Repository is inactive",
			Detail: fmt.Sprintf(
				"The last commit was %d days ago (%d threshold). Stale repositories carry unpatched dependencies and rot in review.",
				git.DaysSinceCommit, staleDays),
			Impact: capImpact(float64(git.DaysSinceCommit) / float64(staleDays) * 4.0 * weightGit),
			Evidence: []models.Evidence{
				models.NewEvidence("metric", "Days since commit",
					fmt.Sprintf("%d days", git.DaysSinceCommit), float64(git.DaysSinceCommit), ""),
				models.NewEvidence("metric", "Last commit",
					git.LastCommitAt.UTC().Format("2006-01-02"), 0, ""),
			},
			Recommendation: "Confirm the project is still maintained before depending on it.",
		})
	}
	return out
}

// dimensionRisks reports the aggregate score deficits computed by the metrics
// scorer. They are included so every point of missing health has a name.
func dimensionRisks(health models.Health) []models.Risk {
	var out []models.Risk
	for _, m := range health.Metrics {
		if !m.Applicable {
			continue
		}
		deficit := 100 - m.Score
		if deficit < 10 {
			continue
		}
		out = append(out, models.Risk{
			ID:       "score." + m.Key,
			Category: categoryFor(m.Key),
			Title:    m.Label + " below expectations",
			Detail:   fmt.Sprintf("%s scored %.1f/100 (weight %.0f%%): %s.", m.Label, m.Score, m.Weight*100, m.Detail),
			// Capped like every other risk. One dimension can dominate the raw
			// deficit; letting a single line item explain 40 points would
			// misrepresent every other rule as noise.
			Impact: capImpact(deficit * m.Weight),
			Evidence: []models.Evidence{
				models.NewEvidence("metric", m.Label,
					fmt.Sprintf("%.1f/100 at %.0f%% weight", m.Score, m.Weight*100),
					m.Score, ""),
			},
		})
	}
	return out
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

// worstComplexityFile returns the highest-complexity file in the summary.
func worstComplexityFile(code models.CodeStats) *models.FileComplexity {
	if len(code.Complexity.WorstFiles) == 0 {
		return nil
	}
	worst := code.Complexity.WorstFiles[0]
	return &worst
}

// sortRisks orders by severity, then impact descending, then ID. The ID
// tiebreak makes the order total, which keeps diffs and tests stable.
func sortRisks(risks []models.Risk) {
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

// capImpact keeps a single risk from claiming more than the code dimension's
// entire weight, which would distort the explanation of the score.
func capImpact(impact float64) float64 {
	if impact < 0 {
		return 0
	}
	if impact > 15 {
		return 15
	}
	return round2(impact)
}

// formatComplexity renders a complexity value without trailing zeros.
func formatComplexity(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
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

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
