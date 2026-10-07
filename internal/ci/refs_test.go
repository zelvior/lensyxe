package ci

import (
	"testing"
)

func TestDetectUnpinnedAndMutableRefs(t *testing.T) {
	root := t.TempDir()
	writeWorkflow(t, root, "ci.yml", `
name: CI
on: [push]
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@8f4b7f84864484a7bf31766abe9204da3cbe65b3
      - uses: some/action@v1.2.3
      - uses: evil/action@main
      - uses: other/action@latest
      - uses: third/action
      - uses: ./local-action
`)
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}

	// The SHA pin is fine; ./local needs no pin; the rest must be flagged.
	mutable, unpinned := 0, 0
	for _, f := range report.Findings {
		switch f.Rule {
		case "mutable_ref":
			mutable++
		case "unpinned_action":
			unpinned++
		}
	}
	if mutable != 2 {
		t.Errorf("mutable_ref findings = %d, want 2 (@main and @latest)", mutable)
	}
	if unpinned != 1 {
		t.Errorf("unpinned_action findings = %d, want 1 (no @ref)", unpinned)
	}
	if len(report.Findings) != 3 {
		t.Errorf("total findings = %d, want 3:\n%+v", len(report.Findings), report.Findings)
	}
}
