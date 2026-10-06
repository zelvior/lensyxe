package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/internal/ci"
	"github.com/zelvior/lensyxe/internal/gates"
	"github.com/zelvior/lensyxe/pkg/models"
)

// snapFixture builds a snapshot for exercising the gate logic directly.
func snapFixture(score float64, ratio float64, risks ...models.Risk) *models.Snapshot {
	return &models.Snapshot{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "0.3.0",
		Health:        models.Health{Score: score, Grade: models.Grade(score)},
		Code:          models.CodeStats{TestFileRatio: ratio, SourceFiles: 10},
		Risks:         risks,
	}
}

// criticalFixture is a critical risk for gate tests.
func criticalFixture() models.Risk {
	return models.Risk{
		ID:       "code.hotspot.a",
		Severity: models.SeverityCritical,
		Title:    "Confirmed hotspot",
		Detail:   "size, churn and complexity all exceeded their thresholds",
		Subject:  "a.go",
		Impact:   9,
	}
}

// rulesOf returns the rule names of a breach list.
func rulesOf(breaches []gates.Breach) []string {
	out := make([]string, 0, len(breaches))
	for _, b := range breaches {
		out = append(out, b.Rule)
	}
	return out
}

func TestCIFlagsAny(t *testing.T) {
	unset := ciFlags{failUnderHealth: ciUnset, failOnTestRatioDrop: ciUnset,
		failOnCoverageDrop: ciUnset, workflowFail: "none"}
	if unset.any() {
		t.Error("the zero-value flag set must not enable the CI pipeline")
	}

	cases := map[string]ciFlags{
		"fail under health": {failUnderHealth: 70, failOnTestRatioDrop: ciUnset, failOnCoverageDrop: ciUnset, workflowFail: "none"},
		"critical risk":     {failUnderHealth: ciUnset, failOnTestRatioDrop: ciUnset, failOnCoverageDrop: ciUnset, failOnCriticalRisk: true, workflowFail: "none"},
		"workflow severity": {failUnderHealth: ciUnset, failOnTestRatioDrop: ciUnset, failOnCoverageDrop: ciUnset, workflowFail: "high"},
		"pr comment":        {failUnderHealth: ciUnset, failOnTestRatioDrop: ciUnset, failOnCoverageDrop: ciUnset, workflowFail: "none", prCommentOut: "c.md"},
		"baseline":          {failUnderHealth: ciUnset, failOnTestRatioDrop: ciUnset, failOnCoverageDrop: ciUnset, workflowFail: "none", baselinePath: "b.json"},
	}
	for name, f := range cases {
		if !f.any() {
			t.Errorf("%s: any() = false, want true", name)
		}
	}
}

// `--fail-under-health 0` is a real gate (accept anything), so "unset" must be
// distinguishable from zero.
func TestFailUnderHealthZeroIsAGate(t *testing.T) {
	f := ciFlags{failUnderHealth: 0, failOnTestRatioDrop: ciUnset,
		failOnCoverageDrop: ciUnset, workflowFail: "none"}
	if !f.any() {
		t.Fatal("an explicit 0 threshold must count as set")
	}
	if err := f.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got := f.evaluate(snapFixture(42, 0.2), nil); len(got) != 0 {
		t.Errorf("a score of 0 threshold must pass a score of 42, got %v", rulesOf(got))
	}
}

func TestFailUnderHealthBreach(t *testing.T) {
	f := ciFlags{failUnderHealth: 90, failOnTestRatioDrop: ciUnset,
		failOnCoverageDrop: ciUnset, workflowFail: "none"}

	if got := f.evaluate(snapFixture(89, 0.2), nil); len(got) != 1 {
		t.Fatalf("expected one breach, got %v", rulesOf(got))
	} else if got[0].Rule != "fail_under_health" {
		t.Errorf("Rule = %q", got[0].Rule)
	}
	if got := f.evaluate(snapFixture(90, 0.2), nil); len(got) != 0 {
		t.Errorf("a score equal to the threshold must pass, got %v", rulesOf(got))
	}
}

// Lensyxe does not measure line coverage. A flag named "coverage" must fail
// loudly rather than silently gating a weaker metric under a stronger name.
func TestFailOnCoverageDropIsRejected(t *testing.T) {
	f := ciFlags{failUnderHealth: ciUnset, failOnTestRatioDrop: ciUnset,
		failOnCoverageDrop: 3, workflowFail: "none"}
	err := f.validate()
	if !errors.Is(err, errCoverageUnmeasured) {
		t.Fatalf("validate error = %v, want errCoverageUnmeasured", err)
	}
	if !strings.Contains(err.Error(), "--fail-on-test-ratio-drop") {
		t.Errorf("the error must point at the real flag, got %q", err)
	}
}

func TestFailOnCoverageDropViaCLI(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "analyze", target, "--fail-on-coverage-drop", "3")
	if !errors.Is(err, errCoverageUnmeasured) {
		t.Fatalf("err = %v, want errCoverageUnmeasured", err)
	}
}

func TestFailOnTestRatioDrop(t *testing.T) {
	f := ciFlags{failUnderHealth: ciUnset, failOnTestRatioDrop: 2,
		failOnCoverageDrop: ciUnset, workflowFail: "none"}

	// 40% -> 30% is a 10 point drop, over the 2 point allowance.
	base := snapFixture(80, 0.40)
	cur := snapFixture(80, 0.30)
	got := f.evaluate(cur, base)
	if len(got) != 1 || got[0].Rule != "fail_on_test_ratio_drop" {
		t.Fatalf("expected a ratio breach, got %v", rulesOf(got))
	}
	if !strings.Contains(got[0].Message, "percentage points") {
		t.Errorf("message must be in percentage points, got %q", got[0].Message)
	}

	// Exactly at the allowance passes.
	cur = snapFixture(80, 0.38)
	if got := f.evaluate(cur, base); len(got) != 0 {
		t.Errorf("a drop equal to the allowance must pass, got %v", rulesOf(got))
	}

	// A rise is never a breach.
	cur = snapFixture(80, 0.55)
	if got := f.evaluate(cur, base); len(got) != 0 {
		t.Errorf("a rising ratio must pass, got %v", rulesOf(got))
	}
}

// A first CI run has no baseline. Failing it would break every new repository.
func TestRatioGateSkippedWithoutBaseline(t *testing.T) {
	f := ciFlags{failUnderHealth: ciUnset, failOnTestRatioDrop: 0.1,
		failOnCoverageDrop: ciUnset, workflowFail: "none"}
	if got := f.evaluate(snapFixture(50, 0.0), nil); len(got) != 0 {
		t.Errorf("a ratio gate with no baseline must be skipped, got %v", rulesOf(got))
	}
}

func TestFailOnCriticalRisk(t *testing.T) {
	f := ciFlags{failUnderHealth: ciUnset, failOnTestRatioDrop: ciUnset,
		failOnCoverageDrop: ciUnset, failOnCriticalRisk: true, workflowFail: "none"}

	got := f.evaluate(snapFixture(70, 0.2, criticalFixture()), nil)
	if len(got) != 1 || got[0].Rule != "fail_on_critical_risk" {
		t.Fatalf("expected a critical-risk breach, got %v", rulesOf(got))
	}
	if !strings.Contains(got[0].Message, "Confirmed hotspot") {
		t.Errorf("the message must name the risk, got %q", got[0].Message)
	}

	// A high-severity risk alone must not trigger the critical gate.
	high := criticalFixture()
	high.Severity = models.SeverityHigh
	if got := f.evaluate(snapFixture(70, 0.2, high), nil); len(got) != 0 {
		t.Errorf("a non-critical risk must not breach, got %v", rulesOf(got))
	}

	// And with the flag off, nothing is reported.
	f.failOnCriticalRisk = false
	if got := f.evaluate(snapFixture(70, 0.2, criticalFixture()), nil); len(got) != 0 {
		t.Errorf("with the flag off nothing must breach, got %v", rulesOf(got))
	}
}

func TestSeverityRankOf(t *testing.T) {
	cases := map[string]int{
		"critical": 4,
		"High":     3,
		" MEDIUM ": 2,
		"low":      1,
		"none":     -1,
		"off":      -1,
	}
	for in, want := range cases {
		got, ok := severityRankOf(in)
		if !ok {
			t.Errorf("severityRankOf(%q) reported unknown", in)
			continue
		}
		if got != want {
			t.Errorf("severityRankOf(%q) = %d, want %d", in, got, want)
		}
	}
	// An unrecognized name must be an error, never a silent pass-everything.
	if _, ok := severityRankOf("nonsense"); ok {
		t.Error("an unknown severity must not be accepted")
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	base := func() ciFlags {
		return ciFlags{failUnderHealth: ciUnset, failOnTestRatioDrop: ciUnset,
			failOnCoverageDrop: ciUnset, workflowFail: "none"}
	}
	f := base()
	f.failUnderHealth = 101
	if err := f.validate(); err == nil {
		t.Error("--fail-under-health above 100 must be rejected")
	}

	f = base()
	// -5 is neither a valid ratio nor the -1 unset sentinel, so a typo must be
	// caught rather than silently disabling the gate.
	f.failOnTestRatioDrop = -5
	if err := f.validate(); err == nil {
		t.Error("--fail-on-test-ratio-drop below the unset sentinel must be rejected")
	}

	f = base()
	f.workflowFail = "bogus"
	if err := f.validate(); err == nil {
		t.Error("an unknown --workflow-fail-severity must be rejected")
	}
}

func TestWorkflowGateEndToEnd(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(target, ci.WorkflowsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	wf := "name: CI\non: [push]\npermissions: write-all\njobs:\n  build:\n    runs-on: ubuntu-latest\n    timeout-minutes: 5\n    steps:\n      - uses: actions/checkout@v4\n"
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte(wf), 0o644); err != nil {
		t.Fatal(err)
	}

	// With the gate set to none, the workflow analysis is skipped entirely.
	if _, stderr, err := runCLI(t, "analyze", target, "--no-persist"); err != nil {
		t.Fatalf("analyze: %v\n%s", err, stderr)
	}

	// At high severity, the write-all finding breaches.
	_, stderr, err := runCLI(t, "analyze", target, "--no-persist",
		"--workflow-fail-severity", "high")
	if !errors.Is(err, errGateFailed) {
		t.Fatalf("err = %v, want a gate failure\n%s", err, stderr)
	}
	if !strings.Contains(stderr, "write-all") {
		t.Errorf("the breach must name the rule:\n%s", stderr)
	}

	// At low severity it also breaches, but at none it does not run.
	_, _, err = runCLI(t, "analyze", target, "--no-persist", "--workflow-fail-severity", "low")
	if !errors.Is(err, errGateFailed) {
		t.Errorf("low severity must include the high finding, got %v", err)
	}
}

func TestPRCommentIsWritten(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "comment.md")

	if _, stderr, err := runCLI(t, "analyze", target, "--no-persist", "--pr-comment", out); err != nil {
		t.Fatalf("analyze: %v\n%s", err, stderr)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read comment: %v", err)
	}
	body := string(data)
	if !strings.HasPrefix(body, ci.CommentMarker) {
		t.Errorf("the comment must start with the marker:\n%s", body)
	}
	if !strings.Contains(body, ci.CommentTitle) {
		t.Errorf("the comment must carry the title:\n%s", body)
	}
	// Writing the comment must not change the exit status.
	if !strings.Contains(body, "Not measured") {
		t.Errorf("unmeasured metrics must be disclosed:\n%s", body)
	}
}

func TestPRCommentJSONDestination(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "comment.json")

	if _, stderr, err := runCLI(t, "analyze", target, "--no-persist", "--pr-comment", out); err != nil {
		t.Fatalf("analyze: %v\n%s", err, stderr)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read comment: %v", err)
	}
	var payload struct {
		Marker string `json:"marker"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("the .json destination must produce valid JSON: %v", err)
	}
	if payload.Marker != ci.CommentMarker || payload.Body == "" {
		t.Errorf("unexpected payload: %+v", payload)
	}
}

// A comment generated with a baseline must show deltas; without one it must not
// invent them.
func TestPRCommentBaselineProducesDeltas(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Capture a real snapshot as the baseline.
	baseline := filepath.Join(t.TempDir(), "baseline.json")
	if _, stderr, err := runCLI(t, "analyze", target, "--no-persist",
		"--format", "json", "--pr-comment", filepath.Join(t.TempDir(), "ignored.md")); err != nil {
		t.Fatalf("baseline run: %v\n%s", err, stderr)
	}
	// The JSON goes to stdout, so re-run capturing it.
	stdout, stderr, err := runCLI(t, "analyze", target, "--no-persist", "--format", "json")
	if err != nil {
		t.Fatalf("baseline run: %v\n%s", err, stderr)
	}
	if err := os.WriteFile(baseline, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "comment.md")
	if _, stderr, err := runCLI(t, "analyze", target, "--no-persist",
		"--baseline", baseline, "--pr-comment", out); err != nil {
		t.Fatalf("analyze with baseline: %v\n%s", err, stderr)
	}

	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "→") {
		t.Errorf("a baseline must produce a movement headline:\n%s", body)
	}
	// An unchanged repository must not be reported as a regression.
	if strings.Contains(string(body), "Regression") {
		t.Errorf("an identical baseline must not report a regression:\n%s", body)
	}
}

func TestBaselineMissingFileFails(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "analyze", target, "--no-persist",
		"--baseline", filepath.Join(t.TempDir(), "absent.json"))
	if err == nil {
		t.Fatal("a missing baseline must be an error, not a silent skip")
	}
}

// A gate rejection must map to exit code 2, not 1.
func TestCIGateRejectionUsesExitCodeTwo(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runCLI(t, "analyze", target, "--no-persist", "--fail-under-health", "100")
	if !errors.Is(err, errGateFailed) {
		t.Fatalf("err = %v, want errGateFailed", err)
	}
	if got := exitCodeFor(err); got != exitGateFailure {
		t.Errorf("exit code = %d, want %d", got, exitGateFailure)
	}
}

// The existing analyze flags must keep working when no CI flag is passed.
func TestExistingAnalyzeFlagsUnaffected(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"analyze", target, "--no-persist", "--format", "json"},
		{"analyze", target, "--no-persist", "--format", "markdown"},
		{"analyze", target, "--no-persist", "--format", "terminal"},
		{"analyze", target, "--no-persist", "--no-gate"},
		{"analyze", target, "--no-persist", "--findings"},
	} {
		if _, stderr, err := runCLI(t, args...); err != nil {
			t.Errorf("%v: %v\n%s", args, err, stderr)
		}
	}
}

// A rejected run must still leave evidence, so the comment is written before
// the gates are evaluated.
func TestCommentWrittenEvenWhenGateFails(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "comment.md")

	_, _, err := runCLI(t, "analyze", target, "--no-persist",
		"--fail-under-health", "100", "--pr-comment", out)
	if !errors.Is(err, errGateFailed) {
		t.Fatalf("err = %v, want a gate failure", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("the comment must be written even when the gate rejects: %v", err)
	}
}
