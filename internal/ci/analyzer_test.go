package ci

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeWorkflow writes a workflow file and returns its repo-relative path.
func writeWorkflow(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, WorkflowsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return filepath.ToSlash(filepath.Join(WorkflowsDir, name))
}

// rules returns the sorted rule names present in a report.
func rules(r Report) []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.Rule)
	}
	sort.Strings(out)
	return out
}

// hasRule reports whether a report contains the given rule.
func hasRule(r Report, rule string) bool {
	for _, f := range r.Findings {
		if f.Rule == rule {
			return true
		}
	}
	return false
}

// findingFor returns the first finding with the given rule.
func findingFor(t *testing.T, r Report, rule string) WorkflowFinding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Rule == rule {
			return f
		}
	}
	t.Fatalf("rule %q not found in %v", rule, rules(r))
	return WorkflowFinding{}
}

// A repository with no workflows is normal, not an error.
func TestAnalyzeWorkflowsNoDirectory(t *testing.T) {
	report, err := AnalyzeWorkflows(t.TempDir())
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	if report.HasFindings() || report.Workflows != 0 {
		t.Errorf("expected an empty report, got %+v", report)
	}
}

func TestAnalyzeWorkflowsEmptyDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, WorkflowsDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	report, err := AnalyzeWorkflows(root)
	if err != nil {
		t.Fatalf("AnalyzeWorkflows: %v", err)
	}
	if report.HasFindings() {
		t.Errorf("expected no findings, got %v", rules(report))
	}
}
