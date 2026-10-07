package ci

import (
	"reflect"
	"testing"
)

func TestFindingsAreSortedBySeverity(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: [push]
permissions: write-all
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@main
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	if len(report.Findings) < 2 {
		t.Fatalf("expected several findings, got %v", rules(report))
	}
	for i := 1; i < len(report.Findings); i++ {
		if report.Findings[i].Severity.rank() > report.Findings[i-1].Severity.rank() {
			t.Fatalf("findings not ordered by severity: %+v", report.Findings)
		}
	}
}

func TestAnalyzeWorkflowsIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "a.yml", "name: A\non: [push]\njobs:\n  j1:\n    steps:\n      - uses: x/y@main\n")
	writeWorkflow(t, root, "b.yml", "name: B\non: [push]\njobs:\n  j2:\n    steps:\n      - uses: p/q@master\n")

	first, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	for i := 0; i < 10; i++ {
		got, err := AnalyzeWorkflows(root)
		if err != nil {
			t.Fatalf("AnalyzeWorkflows: %v", err)
		}
		if !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs", i)
		}
	}
}

func TestIsCommitSHA(t *testing.T) {
	if !isCommitSHA("8f4b7f84864484a7bf31766abe9204da3cbe65b3") {
		t.Error("a full SHA must be accepted")
	}
	for _, bad := range []string{"", "abc", "v1.2.3", "8f4b7f84864484a7bf31766abe9204da3cbe65b", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		if isCommitSHA(bad) {
			t.Errorf("isCommitSHA(%q) = true, want false", bad)
		}
	}
}

func TestSeverityRanking(t *testing.T) {
	order := []severity{severityCritical, severityHigh, severityMedium, severityLow}
	for i := 1; i < len(order); i++ {
		if order[i].rank() >= order[i-1].rank() {
			t.Errorf("%v rank %d should be below %v rank %d",
				order[i], order[i].rank(), order[i-1], order[i-1].rank())
		}
	}
	if severity("bogus").rank() != 0 {
		t.Error("an unknown severity must rank lowest")
	}
}
