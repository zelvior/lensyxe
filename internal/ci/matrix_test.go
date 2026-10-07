package ci

import (
	"strings"
	"testing"
)

func TestDetectConstantMatrixAxis(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    strategy:
      matrix:
        os: [ubuntu-latest]
        node: [18, 20]
    steps:
      - uses: actions/checkout@v4
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	f := findingFor(t, report, "constant_matrix_axis")
	if !strings.Contains(f.Detail, "matrix.os") {
		t.Errorf("detail should name the axis, got %q", f.Detail)
	}
}

func TestDetectExcessiveMatrixFanout(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    strategy:
      matrix:
        os: [a, b, c, d]
        node: [18, 20, 22, 24]
        arch: [amd64, arm64, ppc64, s390x]
    steps:
      - uses: actions/checkout@v4
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	// 4*4*4 = 64, which is under the 256 bar.
	if hasRule(report, "excessive_matrix") {
		t.Error("64 jobs is a reasonable matrix and must not be flagged")
	}
}

// include and exclude cannot be evaluated statically, so they are excluded from
// the fan-out count rather than guessed at.
func TestMatrixIncludeExcludeAreNotCounted(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    strategy:
      matrix:
        os: [a, b, c, d]
        include:
          - os: a
            node: 18
          - os: b
            node: 20
        exclude:
          - os: c
            node: 22
    steps:
      - uses: actions/checkout@v4
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	if hasRule(report, "excessive_matrix") {
		t.Errorf("include/exclude must not inflate the count: %+v", report.Findings)
	}
}
