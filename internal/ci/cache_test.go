package ci

import (
	"strings"
	"testing"
)

func TestDetectMissingCache(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
      - uses: actions/setup-go@v5
`)

	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	if report.Workflows != 1 || report.Jobs != 1 {
		t.Errorf("workflows=%d jobs=%d, want 1/1", report.Workflows, report.Jobs)
	}
	// Both setup actions are uncached, so two findings under the same rule.
	count := 0
	for _, r := range rules(report) {
		if r == "missing_cache" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("missing_cache findings = %d, want 2:\n%+v", count, report.Findings)
	}
	f := findingFor(t, report, "missing_cache")
	if f.Severity != severityMedium {
		t.Errorf("severity = %v, want medium", f.Severity)
	}
	if !strings.Contains(f.Detail, "setup-node") && !strings.Contains(f.Detail, "setup-go") {
		t.Errorf("detail should name the action, got %q", f.Detail)
	}
}

func TestCachedSetupIsNotFlagged(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: [push]
jobs:
  node:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          cache: npm
  go:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          cache: true
`)

	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	if hasRule(report, "missing_cache") {
		t.Errorf("cached setup actions must not be flagged:\n%+v", report.Findings)
	}
}

// cache: false is an explicit request not to cache, which is still a finding.
func TestCacheFalseIsStillUncached(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 5
    steps:
      - uses: actions/setup-go@v5
        with:
          cache: false
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	if !hasRule(report, "missing_cache") {
		t.Error("cache: false is still uncached and must be reported")
	}
}
