package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A repository-level .lensyxe.yml must be discovered and enforced, so the
// policy can live in version control rather than in every workflow.
func TestConfigFileThresholdIsEnforced(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "min_health_score: 99\n")

	// Copy the risky example in, so the target has content to analyze.
	copyTree(t, filepath.Join("..", "examples", "risky-go"), dir)

	res := runCLI(t, dir, "analyze", ".", "--no-persist")
	if res.Code != ExitGateFailed {
		t.Fatalf("a config-file threshold must be enforced, got exit %d\n%s",
			res.Code, res.Combined())
	}
	if !strings.Contains(res.Stderr, "min_health_score") {
		t.Errorf("the breach must name the rule from the config file:\n%s", res.Stderr)
	}
}

// A flag must beat the config file, which is the documented precedence.
func TestFlagBeatsConfigFile(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "min_health_score: 99\n")
	copyTree(t, filepath.Join("..", "examples", "risky-go"), dir)

	// --no-gate must win over the config file's threshold.
	res := runCLI(t, dir, "analyze", ".", "--no-persist", "--no-gate")
	if res.Code != ExitOK {
		t.Fatalf("--no-gate must override the config file, got exit %d\n%s",
			res.Code, res.Combined())
	}
}

// A malformed config must never abort a run. The engine degrades to defaults
// with a warning, because a typo in a config file should not be able to block
// a pipeline.
func TestMalformedConfigStillAnalyzes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".lensyxe.yml"),
		[]byte("min_health_score: [this is not: valid yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	copyTree(t, filepath.Join("..", "examples", "healthy-go"), dir)

	res := runCLI(t, dir, "analyze", ".", "--no-persist")
	if res.Code != ExitOK {
		t.Fatalf("a malformed config must not fail the run, got exit %d\n%s",
			res.Code, res.Combined())
	}
	if !strings.Contains(res.Combined(), "lensyxe:") {
		t.Errorf("the problem must be reported:\n%s", res.Combined())
	}
}

// The workspace example has manifests and no lockfile, so the drift path fires.
func TestDependencyDriftIsDetected(t *testing.T) {
	_, doc := analyzeJSON(t, "../examples/npm-workspace")
	if !doc.Dependencies.Detected {
		t.Fatal("the npm workspace example should have a detected manifest")
	}
	if doc.Dependencies.Locked {
		t.Error("the example has no lockfile, so it must not be reported as locked")
	}
	if !doc.Dependencies.Drift {
		t.Error("a manifest with no lockfile is drift and must be reported")
	}
}

// require_tests is a boolean threshold. It lives only in the config file, not
// behind a flag, so the gate is exercised through a config file rather than a
// command-line flag.
func TestRequireTestsGateFires(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "require_tests: true\n")
	copyTree(t, filepath.Join("..", "examples", "risky-go"), dir)

	res := runCLI(t, dir, "analyze", ".", "--no-persist")
	if res.Code != ExitGateFailed {
		t.Fatalf("require_tests must reject a repository with no tests, got %d\n%s",
			res.Code, res.Combined())
	}
	if !strings.Contains(res.Stderr, "require_tests") {
		t.Errorf("the breach must name the rule:\n%s", res.Stderr)
	}
}

// fail_on_drift is the other boolean threshold, and it needs a repository with
// a manifest and no lockfile.
func TestFailOnDriftGateFires(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "fail_on_drift: true\n")
	copyTree(t, filepath.Join("..", "examples", "npm-workspace"), dir)

	res := runCLI(t, dir, "analyze", ".", "--no-persist")
	if res.Code != ExitGateFailed {
		t.Fatalf("fail_on_drift must reject an unlocked dependency, got %d\n%s",
			res.Code, res.Combined())
	}
}

func TestMonorepoBreakdown(t *testing.T) {
	_, doc := analyzeJSON(t, "../examples/npm-workspace", "--monorepo")

	if doc.Workspace == nil {
		t.Fatal("--monorepo must populate the workspace field")
	}
	if doc.Workspace.Kind != "npm" {
		t.Errorf("workspace kind = %q, want npm", doc.Workspace.Kind)
	}
	if len(doc.Workspace.Packages) != 3 {
		t.Fatalf("found %d packages, want 3: %+v",
			len(doc.Workspace.Packages), doc.Workspace.Packages)
	}

	for _, pkg := range doc.Workspace.Packages {
		if pkg.Path == "" {
			t.Error("every package must have a path")
		}
		if pkg.Health.Score <= 0 {
			t.Errorf("package %s scored %v", pkg.Path, pkg.Health.Score)
		}
		// Git signals are not attributable to a package; saying so is the
		// difference between an honest score and a misleading one.
		if pkg.GitApplicable {
			t.Errorf("package %s must not report git as applicable", pkg.Path)
		}
	}

	// Packages are sorted by path so the output is byte-stable.
	paths := make([]string, len(doc.Workspace.Packages))
	for i, p := range doc.Workspace.Packages {
		paths[i] = p.Path
	}
	for i := 1; i < len(paths); i++ {
		if paths[i-1] > paths[i] {
			t.Errorf("packages are not sorted by path: %v", paths)
			break
		}
	}
}

// Without the flag the workspace field must be absent rather than empty, so a
// consumer can test for its presence.
func TestWorkspaceOmittedWithoutFlag(t *testing.T) {
	_, doc := analyzeJSON(t, "../examples/npm-workspace")
	if doc.Workspace != nil {
		t.Error("the workspace field must be omitted when detection is off")
	}
}
