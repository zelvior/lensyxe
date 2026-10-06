package ci

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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

// The Action definition must stay loadable by GitHub; a syntax error there
// breaks the whole integration, so it is validated here.
func TestActionDefinitionIsValid(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "action", "action.yml"))
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}

	var action struct {
		Name        string                              `yaml:"name"`
		Description string                              `yaml:"description"`
		Inputs      map[string]struct{ Default string } `yaml:"inputs"`
		Outputs     map[string]struct {
			Value string `yaml:"value"`
		} `yaml:"outputs"`
		Runs struct {
			Using string `yaml:"using"`
			Steps []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
				If   string `yaml:"if"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatalf("action.yml does not parse: %v", err)
	}

	if action.Runs.Using != "composite" {
		t.Errorf("runs.using = %q, want composite", action.Runs.Using)
	}
	if len(action.Runs.Steps) == 0 {
		t.Fatal("a composite action must define steps")
	}

	// Every input named in the specification must exist with the documented
	// default, so a consumer relying on it does not get a silent no-op.
	want := map[string]string{
		"version":                "latest",
		"fail-on-threshold":      "true",
		"comment-on-pr":          "true",
		"format":                 "markdown",
		"path":                   ".",
		"workflow-fail-severity": "none",
	}
	for name, def := range want {
		in, ok := action.Inputs[name]
		if !ok {
			t.Errorf("action.yml is missing the %q input", name)
			continue
		}
		if in.Default != def {
			t.Errorf("input %q default = %q, want %q", name, in.Default, def)
		}
	}

	// The required steps must all be present.
	joined := ""
	runs := ""
	for _, s := range action.Runs.Steps {
		joined += s.Name + "|" + s.Uses + "|" + s.Run + "\n"
		runs += s.Run + "\n"
	}
	for _, needle := range []string{
		"actions/checkout",      // checkout
		"actions/cache",         // binary caching
		"actions/github-script", // comment posting
	} {
		if !strings.Contains(joined, needle) {
			t.Errorf("action.yml has no step referencing %q", needle)
		}
	}

	// The analysis step invokes the binary through a variable, so the command
	// is matched by its parts rather than as one literal string.
	if !strings.Contains(runs, "lensyxe") || !strings.Contains(runs, "analyze") {
		t.Error("action.yml has no step running `lensyxe analyze`")
	}

	// The install step must fail loudly rather than producing a broken binary.
	if !strings.Contains(runs, "curl -fsSL") {
		t.Error("action.yml does not download the release binary")
	}
}

// A declared input that the action body never reads is worse than no input: a
// user sets it, sees no error, and reasonably concludes the setting applied.
//
// This caught two real cases: `format` was declared but hardcoded, and
// `min-health-score` duplicated `fail-under-health` without being wired to
// anything.
func TestEveryActionInputIsReferenced(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "action", "action.yml"))
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}

	var action struct {
		Inputs map[string]struct {
			Default     string `yaml:"default"`
			Description string `yaml:"description"`
		} `yaml:"inputs"`
		Runs struct {
			Steps []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
				// With is typed as raw nodes because its values are not all
				// strings: `fetch-depth: 0` and `persist-credentials: false`
				// are an int and a bool, and a string field would fail to
				// unmarshal the whole document.
				With map[string]yaml.Node `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatalf("action.yml does not parse: %v", err)
	}

	// Collect every expression that could reference an input: step bodies and
	// step `with:` blocks. `with:` values are rendered as raw YAML so a numeric
	// or boolean value still appears as text.
	var body strings.Builder
	for _, s := range action.Runs.Steps {
		body.WriteString(s.Run)
		body.WriteString(s.Uses)
		for _, v := range s.With {
			body.WriteString(v.Value)
		}
	}
	// Outputs are declared outside `runs` and referenced from other workflows,
	// so they are read from the raw document rather than the step bodies.
	raw := string(data)
	inputsSection, outputsSection := raw, ""
	if idx := strings.Index(raw, "\noutputs:"); idx >= 0 {
		inputsSection = raw[:idx]
		outputsSection = raw[idx:]
	}

	for name, spec := range action.Inputs {
		ref := "inputs." + name
		if !strings.Contains(body.String(), ref) &&
			!strings.Contains(outputsSection, ref) &&
			!strings.Contains(inputsSection, ref) {
			t.Errorf("input %q is declared but never referenced; setting it silently does nothing", name)
		}
		if strings.TrimSpace(spec.Description) == "" {
			t.Errorf("input %q has no description", name)
		}
	}
}

// A gate rejection must be distinguishable from a crash, so the action has to
// propagate exit 2 rather than flattening it.
func TestActionPreservesGateExitCode(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "action", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)

	if !strings.Contains(script, `[ "$status" -eq 2 ]`) {
		t.Error("the analyze step does not test for exit code 2 specifically")
	}
	if !strings.Contains(script, `exit 2`) {
		t.Error("the analyze step does not propagate exit code 2")
	}
}

// The comment is generated before the gate is evaluated, so a rejected pull
// request still shows what regressed.
func TestActionPublishesCommentOnFailure(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "action", "action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)

	// `always()` on the posting step is what makes this true.
	if !strings.Contains(script, "always() && inputs.comment-on-pr") {
		t.Error("the comment-posting step is not conditioned on always(); " +
			"a failed gate would leave no comment")
	}
}
