// Package gates evaluates the CI-facing thresholds configured for a run.
//
// The package is pure: it takes stats and thresholds and returns the list of
// breaches. The caller decides how to report and what exit code to use, which
// keeps the policy separate from the transport.
package gates

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zelvior/lensyxe/internal/config"
	"github.com/zelvior/lensyxe/pkg/models"
)

// Breach is one failed threshold.
type Breach struct {
	// Rule identifies the threshold that failed, for example "min_health_score".
	Rule string
	// Message explains the breach with the actual numbers.
	Message string
	// Actual is the measured value.
	Actual float64
	// Limit is the configured bound.
	Limit float64
}

// Baseline is the previously recorded state a snapshot is compared against.
//
// HasPrevious is false on a first run. Rules that need a baseline are skipped
// rather than failing: a repository with no history cannot have regressed
// against one, and failing would make the first CI run fail for no reason.
type Baseline struct {
	HasPrevious       bool
	Score             float64
	AverageComplexity float64
}

// Evaluate checks a snapshot against the configured thresholds.
//
// The returned slice is sorted by rule name so output is deterministic.
func Evaluate(snap *models.Snapshot, th config.Thresholds, base Baseline) []Breach {
	if snap == nil {
		return nil
	}
	var out []Breach

	if th.IsSet(th.MinHealthScore) && snap.Health.Score < th.MinHealthScore {
		out = append(out, Breach{
			Rule: "min_health_score",
			Message: fmt.Sprintf("health score %.1f is below the minimum %.1f",
				snap.Health.Score, th.MinHealthScore),
			Actual: snap.Health.Score,
			Limit:  th.MinHealthScore,
		})
	}

	if th.IsSet(th.MaxHealthDrop) && base.HasPrevious {
		drop := base.Score - snap.Health.Score
		if drop > th.MaxHealthDrop {
			out = append(out, Breach{
				Rule: "max_health_drop",
				Message: fmt.Sprintf("health dropped %.1f points (%.1f -> %.1f), exceeding the allowed %.1f",
					drop, base.Score, snap.Health.Score, th.MaxHealthDrop),
				Actual: drop,
				Limit:  th.MaxHealthDrop,
			})
		}
	}

	if th.IsSet(th.MaxComplexityIncrease) {
		// With a baseline the rule measures the increase; without one it
		// measures the absolute value, which is still meaningful.
		measured := snap.Code.Complexity.AverageComplexity
		if base.HasPrevious {
			measured = snap.Code.Complexity.AverageComplexity - base.AverageComplexity
		}
		if measured > th.MaxComplexityIncrease {
			msg := fmt.Sprintf("average complexity %.2f exceeds the allowed increase of %.2f",
				snap.Code.Complexity.AverageComplexity, th.MaxComplexityIncrease)
			if base.HasPrevious {
				msg = fmt.Sprintf("average complexity rose %.2f (%.2f -> %.2f), exceeding the allowed %.2f",
					measured, base.AverageComplexity,
					snap.Code.Complexity.AverageComplexity, th.MaxComplexityIncrease)
			}
			out = append(out, Breach{
				Rule:    "max_complexity_increase",
				Message: msg,
				Actual:  measured,
				Limit:   th.MaxComplexityIncrease,
			})
		}
	}

	if th.IsSet(float64(th.MaxRiskCount)) && len(snap.Risks) > th.MaxRiskCount {
		out = append(out, Breach{
			Rule: "max_risk_count",
			Message: fmt.Sprintf("%d risks detected, exceeding the allowed %d",
				len(snap.Risks), th.MaxRiskCount),
			Actual: float64(len(snap.Risks)),
			Limit:  float64(th.MaxRiskCount),
		})
	}

	if th.IsSet(float64(th.MaxHotspots)) {
		confirmed := confirmedHotspots(snap)
		if confirmed > th.MaxHotspots {
			out = append(out, Breach{
				Rule: "max_hotspots",
				Message: fmt.Sprintf("%d confirmed hotspot(s), exceeding the allowed %d",
					confirmed, th.MaxHotspots),
				Actual: float64(confirmed),
				Limit:  float64(th.MaxHotspots),
			})
		}
	}

	if th.FailOnDrift && snap.Dependencies.Drift {
		out = append(out, Breach{
			Rule:    "fail_on_drift",
			Message: fmt.Sprintf("dependency drift detected in %s", driftTargets(snap)),
			Actual:  1,
			Limit:   1,
		})
	}

	if th.RequireTests && snap.Code.SourceFiles > 0 && !snap.Code.HasTests {
		out = append(out, Breach{
			Rule: "require_tests",
			Message: fmt.Sprintf("no test files detected across %d source files",
				snap.Code.SourceFiles),
			Actual: 0,
			Limit:  1,
		})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].Rule < out[j].Rule })
	return out
}

// Any reports whether the config defines at least one threshold, so a command
// can skip the gate stage entirely when nothing is configured.
func Any(th config.Thresholds) bool {
	return th.IsSet(th.MinHealthScore) ||
		th.IsSet(th.MaxHealthDrop) ||
		th.IsSet(th.MaxComplexityIncrease) ||
		th.IsSet(float64(th.MaxRiskCount)) ||
		th.IsSet(float64(th.MaxHotspots)) ||
		th.FailOnDrift ||
		th.RequireTests
}

// confirmedHotspots counts only hotspots that met all three factors.
func confirmedHotspots(snap *models.Snapshot) int {
	n := 0
	for _, h := range snap.Code.Hotspots {
		if h.Confirmed {
			n++
		}
	}
	return n
}

// driftTargets lists the ecosystems with drift, for the breach message.
func driftTargets(snap *models.Snapshot) string {
	var names []string
	for _, eco := range snap.Dependencies.Ecosystems {
		if eco.Drift {
			names = append(names, eco.Name)
		}
	}
	if len(names) == 0 {
		return "an unknown manifest"
	}
	return strings.Join(names, " and ")
}
