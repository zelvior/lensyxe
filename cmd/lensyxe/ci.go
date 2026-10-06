package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/ci"
	"github.com/zelvior/lensyxe/internal/gates"
	"github.com/zelvior/lensyxe/internal/report"
	"github.com/zelvior/lensyxe/pkg/models"
)

// ciFlags holds the command-line CI configuration.
//
// Numeric fields use a sentinel default of -1 rather than pointers. "Not
// passed" and "passed as zero" must both be expressible: `--fail-under-health 0`
// is a real gate (accept any score), while omitting the flag means no gate.
const ciUnset = -1.0

type ciFlags struct {
	failUnderHealth     float64
	failOnTestRatioDrop float64
	failOnCoverageDrop  float64
	failOnCriticalRisk  bool
	workflowFail        string
	prCommentOut        string
	baselinePath        string
}

// errCoverageUnmeasured is returned when --fail-on-coverage-drop is used.
//
// Lensyxe does not instrument tests, so it has no coverage figure. Silently
// substituting the test-to-code file ratio under a "coverage" name would let a
// team believe they had a coverage gate when they had a much weaker one, so the
// flag is rejected outright and points at the real one.
var errCoverageUnmeasured = errors.New(
	"--fail-on-coverage-drop requires code coverage, which Lensyxe does not measure; " +
		"use --fail-on-test-ratio-drop to gate the test-to-code file ratio instead")

// addCIFlags registers the CI flags on a command.
func addCIFlags(cmd *cobra.Command, f *ciFlags) {
	cmd.Flags().Float64Var(&f.failUnderHealth, "fail-under-health", ciUnset,
		"exit 2 when the health score is below this value")
	cmd.Flags().Float64Var(&f.failOnTestRatioDrop, "fail-on-test-ratio-drop", ciUnset,
		"exit 2 when the test-to-code file ratio drops by more than this many percentage points")
	cmd.Flags().Float64Var(&f.failOnCoverageDrop, "fail-on-coverage-drop", ciUnset,
		"unsupported: Lensyxe does not measure code coverage")
	cmd.Flags().BoolVar(&f.failOnCriticalRisk, "fail-on-critical-risk", false,
		"exit 2 when any critical risk is detected")
	cmd.Flags().StringVar(&f.workflowFail, "workflow-fail-severity", "none",
		"also exit 2 when a workflow finding reaches this severity or worse: critical, high, medium, low, or none")
	cmd.Flags().StringVar(&f.prCommentOut, "pr-comment", "",
		"write a pull request comment body to this file (local CI or a workflow step)")
	cmd.Flags().StringVar(&f.baselinePath, "baseline", "",
		"path to a base-revision snapshot JSON to compare against")
}

// any returns whether the caller set any CI gate at all.
func (f *ciFlags) any() bool {
	return f.failUnderHealth >= 0 ||
		f.failOnTestRatioDrop >= 0 ||
		f.failOnCoverageDrop >= 0 ||
		f.failOnCriticalRisk ||
		strings.EqualFold(f.workflowFail, "critical") ||
		strings.EqualFold(f.workflowFail, "high") ||
		strings.EqualFold(f.workflowFail, "medium") ||
		strings.EqualFold(f.workflowFail, "low") ||
		f.prCommentOut != "" ||
		f.baselinePath != ""
}

// validate rejects unusable flag combinations before any work is done.
func (f *ciFlags) validate() error {
	if f.failOnCoverageDrop >= 0 {
		return errCoverageUnmeasured
	}
	// Values below the unset sentinel are typos, not "no gate": silently
	// treating them as unset would disable a gate the user asked for.
	if f.failUnderHealth < ciUnset || f.failUnderHealth > 100 {
		return fmt.Errorf("--fail-under-health must be between 0 and 100, got %v", f.failUnderHealth)
	}
	if f.failOnTestRatioDrop < ciUnset || f.failOnTestRatioDrop > 100 {
		return fmt.Errorf("--fail-on-test-ratio-drop must be between 0 and 100, got %v", f.failOnTestRatioDrop)
	}
	if _, ok := severityRankOf(f.workflowFail); !ok {
		return fmt.Errorf("unknown --workflow-fail-severity %q (want critical, high, medium, low, or none)", f.workflowFail)
	}
	return nil
}

// evaluate applies the metric gates to a snapshot.
func (f *ciFlags) evaluate(snap *models.Snapshot, baseline *models.Snapshot) []gates.Breach {
	var out []gates.Breach

	if f.failUnderHealth >= 0 && snap.Health.Score < f.failUnderHealth {
		out = append(out, gates.Breach{
			Rule: "fail_under_health",
			Message: fmt.Sprintf("health score %.1f is below the required minimum %.1f",
				snap.Health.Score, f.failUnderHealth),
			Actual: snap.Health.Score,
			Limit:  f.failUnderHealth,
		})
	}

	// A ratio gate needs a baseline. Without one there is nothing to regress
	// from, so it is skipped: a first run must never fail the build.
	if f.failOnTestRatioDrop >= 0 && baseline != nil {
		before := baseline.Code.TestFileRatio * 100
		after := snap.Code.TestFileRatio * 100
		if drop := before - after; drop > f.failOnTestRatioDrop {
			out = append(out, gates.Breach{
				Rule: "fail_on_test_ratio_drop",
				Message: fmt.Sprintf(
					"test-to-code file ratio fell %.1f percentage points (%.1f%% -> %.1f%%), exceeding the allowed %.1f",
					drop, before, after, f.failOnTestRatioDrop),
				Actual: drop,
				Limit:  f.failOnTestRatioDrop,
			})
		}
	}

	if f.failOnCriticalRisk {
		for _, r := range snap.Risks {
			if r.Severity != models.SeverityCritical {
				continue
			}
			out = append(out, gates.Breach{
				Rule:    "fail_on_critical_risk",
				Message: fmt.Sprintf("critical risk %q: %s", r.Title, r.Detail),
				Actual:  r.Impact,
			})
		}
	}

	return out
}

// severityRankOf maps a severity name to a numeric rank, higher being worse.
// "none" maps to -1, which disables a gate. An unrecognized name is an error
// rather than a silent default that would quietly pass everything.
func severityRankOf(name string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "critical":
		return 4, true
	case "high":
		return 3, true
	case "medium":
		return 2, true
	case "low":
		return 1, true
	case "none", "off":
		return -1, true
	default:
		return 0, false
	}
}

// workflowBreaches converts workflow findings into breaches at or above minSev.
func workflowBreaches(report ci.Report, minSev string) []gates.Breach {
	rank, ok := severityRankOf(minSev)
	if !ok || rank < 0 {
		return nil
	}

	var out []gates.Breach
	for _, f := range report.Findings {
		sevRank, ok := severityRankOf(string(f.Severity))
		if !ok || sevRank < rank {
			continue
		}
		location := f.File
		if f.Job != "" {
			location += " job " + f.Job
		}
		out = append(out, gates.Breach{
			Rule:    "workflow_" + f.Rule,
			Message: fmt.Sprintf("%s (%s): %s", f.Title, location, f.Detail),
		})
	}
	return out
}

// ciOptions are the resolved inputs to runCIFlags.
type ciOptions struct {
	Flags  *ciFlags
	Target string
	// NoGate suppresses threshold evaluation while leaving the side outputs
	// (the PR comment, the workflow report) intact.
	//
	// It exists because `--no-gate --fail-under-health 70` is otherwise
	// self-contradictory with undefined precedence. The resolution is that
	// --no-gate always wins: its whole purpose is "give me the report without
	// the verdict", and a caller who writes both clearly wants that.
	NoGate  bool
	Version string
}

// runCIFlags executes the CI pipeline for a completed analysis.
//
// Ordering is deliberate: the comment is rendered and the workflow report is
// printed BEFORE the gates are evaluated, so a rejected build still publishes
// the evidence a maintainer needs to act on it.
func runCIFlags(cmd *cobra.Command, snap *models.Snapshot, opts ciOptions) error {
	f := opts.Flags
	if err := f.validate(); err != nil {
		return err
	}

	var baseline *models.Snapshot
	if f.baselinePath != "" {
		loaded, err := ci.LoadBaseline(f.baselinePath)
		if err != nil {
			return err
		}
		baseline = loaded
	}

	// The comment is written before gating so a rejected run still publishes.
	if f.prCommentOut != "" {
		if err := writePRComment(snap, baseline, f); err != nil {
			return err
		}
	}

	var breaches []gates.Breach

	// The side outputs happen regardless of --no-gate. Suppressing the verdict
	// must not also suppress the evidence: a caller who asks for a PR comment
	// and a report without gating wants the comment.
	if f.workflowFail != "" && !strings.EqualFold(f.workflowFail, "none") {
		report, err := ci.AnalyzeWorkflows(opts.Target)
		if err != nil {
			return err
		}
		printWorkflowReport(cmd, report)
		if !opts.NoGate {
			breaches = append(breaches, workflowBreaches(report, f.workflowFail)...)
		}
	}

	// Threshold evaluation is what --no-gate suppresses.
	if opts.NoGate {
		return nil
	}

	breaches = append(breaches, f.evaluate(snap, baseline)...)

	if len(breaches) == 0 {
		return nil
	}
	if err := report.RenderGateFailures(cmd.ErrOrStderr(), breaches); err != nil {
		return err
	}
	return fmt.Errorf("%w: %d threshold(s) breached", errGateFailed, len(breaches))
}

// writePRComment renders the comment body and writes it to the requested file.
//
// The body is Markdown. A `.json` destination additionally wraps it with
// metadata so a workflow step can post it without parsing.
func writePRComment(snap, baseline *models.Snapshot, f *ciFlags) error {
	env := ci.LoadEnvironment()

	commentOpts := ci.CommentOptions{
		Baseline: baseline,
		MaxRisks: 10,
	}
	// The event file is read whenever it exists, not only inside Actions: a
	// local dry run with GITHUB_EVENT_PATH set should still produce the same
	// provenance footer.
	event, err := ci.ReadEvent(env.EventPath)
	if err != nil {
		return err
	}
	commentOpts.Event = event
	if event.BaseRef != "" {
		commentOpts.BaselineLabel = event.BaseRef
	}

	var body strings.Builder
	if err := ci.RenderComment(&body, snap, commentOpts); err != nil {
		return err
	}

	out := f.prCommentOut
	if strings.HasSuffix(out, ".json") {
		payload := struct {
			Marker string          `json:"marker"`
			Title  string          `json:"title"`
			Body   string          `json:"body"`
			Event  ci.EventContext `json:"event"`
			PR     int             `json:"pr_number,omitempty"`
		}{
			Marker: ci.CommentMarker,
			Title:  ci.CommentTitle,
			Body:   body.String(),
			Event:  event,
			PR:     event.Number,
		}
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return fmt.Errorf("encode PR comment: %w", err)
		}
		data = append(data, '\n')
		return os.WriteFile(out, data, 0o644)
	}

	if err := os.WriteFile(out, []byte(body.String()), 0o644); err != nil {
		return fmt.Errorf("write PR comment: %w", err)
	}
	return nil
}

// printWorkflowReport writes the workflow analysis to stderr.
//
// stderr is used deliberately: stdout may carry a JSON report that a pipeline
// pipes into another step.
func printWorkflowReport(cmd *cobra.Command, report ci.Report) {
	w := cmd.ErrOrStderr()
	if !report.HasFindings() {
		fmt.Fprintf(w, "\n  workflows: %d inspected, no risks detected\n", report.Workflows)
		return
	}
	fmt.Fprintf(w, "\nWORKFLOW RISKS (%d across %d workflow(s), %d job(s))\n",
		len(report.Findings), report.Workflows, report.Jobs)
	for _, f := range report.Findings {
		location := f.File
		if f.Job != "" {
			location += " job " + f.Job
		}
		fmt.Fprintf(w, "  %-8s %-26s %s\n", f.Severity, f.Rule, location)
		fmt.Fprintf(w, "           %s\n", f.Detail)
		if f.Recommendation != "" {
			fmt.Fprintf(w, "           fix: %s\n", f.Recommendation)
		}
	}
}
