package ci

import (
	"testing"
)

func TestDetectMissingTimeout(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        timeout-minutes: 5
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	// A step-level timeout also bounds the job, so this must be clean.
	if hasRule(report, "missing_timeout") {
		t.Error("a step-level timeout already bounds the job")
	}
}

func TestDetectPrivilegedTriggerCheckout(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "label.yml", `
name: Label
on: pull_request_target
jobs:
  label:
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ github.event.pull_request.head.sha }}
      - run: echo "trusting PR code"
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	f := findingFor(t, report, "privileged_trigger_checkout")
	if f.Severity != severityCritical {
		t.Errorf("severity = %v, want critical", f.Severity)
	}
}

// A workflow that does not parse is itself a finding: GitHub ignores it
// silently.
func TestUnparseableWorkflowIsReported(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "broken.yml", "name: Broken\non: [push\njobs:\n  - bad")
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	f := findingFor(t, report, "unparseable_workflow")
	if f.Severity != severityHigh {
		t.Errorf("severity = %v, want high", f.Severity)
	}
}

// `on:` is resolved as a boolean by YAML 1.1 resolvers, so both forms must be
// recognized as the pull_request_target trigger.
func TestTriggerDetectionHandlesBooleanOn(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "label.yml", `
name: Label
on: [pull_request_target]
jobs:
  label:
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - uses: actions/checkout@v4
        with:
          ref: ${{ github.event.pull_request.head.sha }}
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	if !hasRule(report, "privileged_trigger_checkout") {
		t.Error("the sequence form of the trigger must be recognized")
	}
}
