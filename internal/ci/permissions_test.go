package ci

import (
	"testing"
)

func TestDetectWriteAllPermissions(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "release.yml", `
name: Release
on: [push]
permissions: write-all
jobs:
  release:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v4
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	f := findingFor(t, report, "write_all_permissions")
	if f.Severity != severityHigh {
		t.Errorf("severity = %v, want high", f.Severity)
	}
	if f.Job != "" {
		t.Errorf("workflow-level finding should have no job, got %q", f.Job)
	}
}

func TestDetectJobWriteScope(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: [push]
jobs:
  deploy:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	f := findingFor(t, report, "write_scope")
	if f.Severity != severityMedium {
		t.Errorf("severity = %v, want medium for contents: write", f.Severity)
	}
	if f.Job != "deploy" {
		t.Errorf("Job = %q, want deploy", f.Job)
	}
}

func TestReadOnlyPermissionsAreClean(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: [push]
permissions:
  contents: read
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@v4
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	if hasRule(report, "write_scope") || hasRule(report, "write_all_permissions") {
		t.Errorf("read-only permissions must be clean: %v", rules(report))
	}
}
