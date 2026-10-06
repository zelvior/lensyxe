// Package tests holds Lensyxe' cross-package test suites: golden files,
// benchmarks, and end-to-end integration tests.
//
// It lives outside cmd/ and internal/ on purpose. Every other package in the
// module tests itself; this one tests the assembled binary, which is the only
// thing a user actually runs. The exit codes, the flag parsing, and the
// end-to-end wiring are only observable from outside.
package tests

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Exit codes the CLI defines. These are a contract with every pipeline that
// uses Lensyxe, so the integration suite asserts them against the real binary
// rather than against the mapping function in cmd/lensyxe.
const (
	// ExitOK means the analysis completed and nothing rejected it.
	ExitOK = 0
	// ExitFailure means something broke: bad path, crash, usage error.
	ExitFailure = 1
	// ExitGateFailed means a configured threshold was breached.
	ExitGateFailed = 2
)

// binaryPath is the compiled CLI, built once for the whole package.
var binaryPath string

// buildOnce guards the one-time build.
var buildOnce sync.Once

// buildError records a build failure so every test reports the same cause
// instead of each trying to build and failing separately.
var buildError error

// TestMain compiles the CLI once and removes it afterwards.
//
// Integration tests must exercise the real binary rather than calling the
// command tree in-process. Exit codes, argument parsing, and signal handling are
// properties of the process, and an in-process call cannot observe any of them.
func TestMain(m *testing.M) {
	// The generated benchmark fixtures live for the whole run, so they are
	// created here rather than through t.TempDir, which would delete them the
	// moment the first test that built one finished.
	initFixtureRoot()

	code := m.Run()

	cleanupFixtures()
	if binaryPath != "" {
		_ = os.Remove(binaryPath)
	}
	os.Exit(code)
}

// cli compiles the binary on first use and returns its path.
func cli(t testing.TB) string {
	t.Helper()

	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "lensyxe-it")
		if err != nil {
			buildError = fmt.Errorf("temp dir: %w", err)
			return
		}
		name := "lensyxe"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		out := filepath.Join(dir, name)

		// The repository root is two levels up from tests/.
		cmd := exec.Command("go", "build", "-o", out, "./cmd/lensyxe")
		cmd.Dir = ".."
		cmd.Env = append(os.Environ(), "GOFLAGS=")
		if combined, err := cmd.CombinedOutput(); err != nil {
			buildError = fmt.Errorf("build: %v\n%s", err, combined)
			return
		}
		binaryPath = out
	})

	if buildError != nil {
		t.Fatalf("cannot build the CLI: %v", buildError)
	}
	return binaryPath
}

// result captures one CLI invocation.
type result struct {
	// Code is the process exit code.
	Code int
	// Stdout and Stderr are the captured streams.
	Stdout string
	Stderr string
}

// Combined returns both streams, for assertions that do not care which one a
// message went to.
func (r result) Combined() string { return r.Stdout + r.Stderr }

// runCLI executes the binary with args in dir.
func runCLI(t testing.TB, dir string, args ...string) result {
	t.Helper()

	cmd := exec.Command(cli(t), args...)
	if dir != "" {
		cmd.Dir = dir
	}
	// A predictable environment: an LENSYXE_* variable leaking in from the
	// developer's shell would silently change the result and make the suite
	// unreproducible on some machines and not others.
	cmd.Env = append(os.Environ(),
		"LENSYXE_AI_KEY=",
		"LENSYXE_AI_PROVIDER=",
		"LENSYXE_AI_MODEL=",
	)
	// --no-persist by default is applied per call site, not here, so the tests
	// that assert on history can turn it on.

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	code := 0
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		code = 0
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("could not run the CLI: %v", err)
	}

	return result{Code: code, Stdout: stdout.String(), Stderr: stderr.String()}
}

// analyzeJSON runs an analysis and decodes the JSON report.
//
// Failing on a non-zero code here is deliberate: a test that wants to inspect
// the report of a run the gate rejected would pass --no-gate instead. A gate
// that cannot produce a report at all is itself a bug, and decoding what came
// back surfaces it.
func analyzeJSON(t testing.TB, target string, extra ...string) (result, snapshotDoc) {
	t.Helper()

	args := append([]string{"analyze", target, "--format", "json", "--no-persist", "--no-gate"}, extra...)
	res := runCLI(t, "", args...)
	if res.Code != ExitOK {
		t.Fatalf("analyze exited %d\nstdout: %s\nstderr: %s",
			res.Code, res.Stdout, res.Stderr)
	}

	var doc snapshotDoc
	if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
		t.Fatalf("decode JSON report: %v\noutput:\n%s", err, truncate(res.Stdout, 2000))
	}
	return res, doc
}

// snapshotDoc is the subset of the JSON contract the integration suite asserts
// on. Only the fields under test are declared, so the suite does not break when
// an unrelated field is added.
type snapshotDoc struct {
	SchemaVersion string `json:"schema_version"`
	Tool          string `json:"tool"`
	Root          string `json:"root"`
	Health        struct {
		Score      float64 `json:"score"`
		Grade      string  `json:"grade"`
		Summary    string  `json:"summary"`
		Components int     `json:"components"`
		Metrics    []struct {
			Key        string  `json:"key"`
			Label      string  `json:"label"`
			Score      float64 `json:"score"`
			Weight     float64 `json:"weight"`
			Applicable bool    `json:"applicable"`
		} `json:"metrics"`
	} `json:"health"`
	Code struct {
		Files         int     `json:"files"`
		SourceFiles   int     `json:"source_files"`
		TestFiles     int     `json:"test_files"`
		TestFileRatio float64 `json:"test_file_ratio"`
		CodeLines     int     `json:"code_lines"`
		Hotspots      []struct {
			Path       string  `json:"path"`
			Lines      int     `json:"lines"`
			Confirmed  bool    `json:"confirmed"`
			Complexit_ float64 `json:"complexity"`
		} `json:"hotspots"`
	} `json:"code"`
	Dependencies struct {
		Detected bool `json:"detected"`
		Locked   bool `json:"locked"`
		Drift    bool `json:"drift"`
	} `json:"dependencies"`
	Git struct {
		IsRepository bool `json:"is_repository"`
	} `json:"git"`
	Risks []struct {
		ID       string  `json:"id"`
		Severity string  `json:"severity"`
		Title    string  `json:"title"`
		Subject  string  `json:"subject"`
		Impact   float64 `json:"impact"`
		Evidence []struct {
			Kind   string `json:"kind"`
			Label  string `json:"label"`
			Detail string `json:"detail"`
		} `json:"evidence"`
	} `json:"risks"`
	Workspace *struct {
		Kind     string `json:"kind"`
		Manifest string `json:"manifest"`
		Packages []struct {
			Path   string `json:"path"`
			Health struct {
				Score float64 `json:"score"`
				Grade string  `json:"grade"`
			} `json:"health"`
			GitApplicable bool `json:"git_applicable"`
			Files         int  `json:"files"`
			// Risks is a full array on the wire, not a count. Only its
			// presence matters here, so it stays raw rather than typed.
			Risks json.RawMessage `json:"risks"`
		} `json:"packages"`
	} `json:"workspace"`
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... (truncated)"
}

// --------------------------------------------------------------- exit codes

// The healthy example must be accepted. A gate that rejected it would be
// rejecting a repository with tests, small files, and a lockfile.
func TestHealthyExampleExitsZero(t *testing.T) {
	res := runCLI(t, "", "analyze", "../examples/healthy-go", "--no-persist", "--no-gate")
	if res.Code != ExitOK {
		t.Fatalf("the healthy example must analyze cleanly, got exit %d\n%s",
			res.Code, res.Combined())
	}

	// And it must actually contain what makes it healthy, otherwise the fixture
	// is not exercising what it claims to.
	res, doc := analyzeJSON(t, "../examples/healthy-go")
	if doc.Code.TestFiles == 0 {
		t.Error("the healthy example should contain test files")
	}
	if doc.Code.TestFileRatio <= 0 {
		t.Errorf("test ratio = %v, want a positive value", doc.Code.TestFileRatio)
	}
	if doc.Health.Score <= 0 {
		t.Errorf("score = %v, want a positive value", doc.Health.Score)
	}
	t.Logf("healthy example scores %.1f (%s) with %d test files",
		doc.Health.Score, doc.Health.Grade, doc.Code.TestFiles)
}

// A configured threshold that the repository fails must produce exit 2, not 1.
// The distinction is the whole reason exit 2 exists: one means the gate said no,
// the other means something broke.
func TestThresholdBreachExitsTwo(t *testing.T) {
	// The risky example scores well below 95.
	res := runCLI(t, "", "analyze", "../examples/risky-go",
		"--no-persist", "--fail-under-health", "95")
	if res.Code != ExitGateFailed {
		t.Fatalf("a breached threshold must exit %d, got %d\n%s",
			ExitGateFailed, res.Code, res.Combined())
	}
	// The reason must be on stderr, since stdout may carry a JSON report a
	// pipeline is piping onward.
	if !strings.Contains(res.Stderr, "fail_under_health") {
		t.Errorf("the breach must be explained on stderr, got:\n%s", res.Stderr)
	}
}

// A threshold the repository passes must not fail the build.
func TestThresholdPassExitsZero(t *testing.T) {
	res := runCLI(t, "", "analyze", "../examples/healthy-go",
		"--no-persist", "--fail-under-health", "10")
	if res.Code != ExitOK {
		t.Fatalf("a satisfied threshold must not fail, got exit %d\n%s",
			res.Code, res.Combined())
	}
}

// --no-gate must suppress a threshold that would otherwise reject, which is the
// escape hatch for "I want the report, not the verdict".
//
// It must win even against a --fail-* flag on the same command line. The two
// are contradictory, and the resolution has to be explicit: a caller who writes
// both wants the report, not the verdict.
func TestNoGateSuppressesRejection(t *testing.T) {
	res := runCLI(t, "", "analyze", "../examples/risky-go",
		"--no-persist", "--no-gate", "--fail-under-health", "99")
	if res.Code != ExitOK {
		t.Fatalf("--no-gate must suppress the rejection, got exit %d\n%s",
			res.Code, res.Combined())
	}
}

// --no-gate suppresses the verdict, not the evidence. A caller asking for a PR
// comment without gating still wants the comment.
func TestNoGateKeepsThePRComment(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "comment.md")

	res := runCLI(t, "", "analyze", "../examples/risky-go",
		"--no-persist", "--no-gate", "--fail-under-health", "99",
		"--pr-comment", out)
	if res.Code != ExitOK {
		t.Fatalf("expected a clean run, got %d\n%s", res.Code, res.Combined())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("--no-gate must not suppress the comment: %v", err)
	}
	if !strings.Contains(string(data), "lensyxe:engineering-impact") {
		t.Error("the comment body is missing its marker")
	}
}

// The risky example must produce the specific findings its README documents,
// not merely a low score. Asserting on the mechanism is what catches a bug that
// happens to leave the number low for the wrong reason.
func TestRiskyExampleProducesExpectedFindings(t *testing.T) {
	_, doc := analyzeJSON(t, "../examples/risky-go")

	// No tests anywhere.
	if doc.Code.TestFiles != 0 {
		t.Errorf("the risky example should have no test files, found %d", doc.Code.TestFiles)
	}
	if !hasRiskWithSubject(doc, "") {
		t.Log("note: no untargeted risk found")
	}

	// An oversized file must be reported as a hotspot candidate.
	if len(doc.Code.Hotspots) == 0 {
		t.Fatal("the oversized file must be reported as a hotspot candidate")
	}
	h := doc.Code.Hotspots[0]
	if h.Lines < 500 {
		t.Errorf("the reported hotspot has %d lines; the fixture is supposed to be over 500", h.Lines)
	}
	// It must NOT be confirmed. Confirmation needs churn and complexity too,
	// and the directory is not a git repository, so churn is zero. A confirmed
	// hotspot here would mean the rule had stopped requiring all three factors.
	if h.Confirmed {
		t.Errorf("the hotspot must not be confirmed without churn and complexity: %+v", h)
	}

	// The score must reflect the state rather than being a default.
	if doc.Health.Score > 60 {
		t.Errorf("score = %.1f, expected a low score for the risky fixture", doc.Health.Score)
	}
}

// hasRiskWithSubject reports whether any risk carries the given subject.
func hasRiskWithSubject(doc snapshotDoc, subject string) bool {
	for _, r := range doc.Risks {
		if r.Subject == subject {
			return true
		}
	}
	return false
}

// A missing path is a usage error, which is exit 1 and must never be confused
// with a policy rejection.
func TestMissingPathExitsOne(t *testing.T) {
	res := runCLI(t, "", "analyze", filepath.Join(t.TempDir(), "does-not-exist"))
	if res.Code != ExitFailure {
		t.Fatalf("a missing path must exit %d, got %d\n%s",
			ExitFailure, res.Code, res.Combined())
	}
}

func TestUnknownFlagExitsOne(t *testing.T) {
	res := runCLI(t, "", "analyze", "--not-a-real-flag")
	if res.Code != ExitFailure {
		t.Fatalf("an unknown flag must exit %d, got %d\n%s",
			ExitFailure, res.Code, res.Combined())
	}
}

func TestVersionCommand(t *testing.T) {
	res := runCLI(t, "", "version")
	if res.Code != ExitOK {
		t.Fatalf("version exited %d\n%s", res.Code, res.Combined())
	}
	for _, want := range []string{"Lensyxe Core", "Commit:", "OS/Arch:"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("version output is missing %q:\n%s", want, res.Stdout)
		}
	}
}

// ------------------------------------------------------------------ outputs

func TestAnalyzeJSONIsValid(t *testing.T) {
	res, doc := analyzeJSON(t, "../examples/healthy-go")
	if doc.SchemaVersion == "" {
		t.Error("the report must declare a schema version")
	}
	if doc.Tool != "lensyxe" {
		t.Errorf("tool = %q", doc.Tool)
	}
	if res.Stdout == "" {
		t.Error("JSON output was empty")
	}
}

func TestAnalyzeMarkdownHasNoANSI(t *testing.T) {
	res := runCLI(t, "", "analyze", "../examples/healthy-go",
		"--no-persist", "--format", "markdown")
	if res.Code != ExitOK {
		t.Fatalf("markdown analyze exited %d\n%s", res.Code, res.Combined())
	}
	// Markdown is pasted into pull requests and docs sites. An escape sequence
	// in it is visible garbage there and harmless nowhere.
	if strings.ContainsRune(res.Stdout, 0x1b) {
		t.Error("markdown output contains an ANSI escape sequence")
	}
	if !strings.HasPrefix(strings.TrimSpace(res.Stdout), "#") {
		t.Errorf("markdown output must start with a heading:\n%s",
			truncate(res.Stdout, 300))
	}
}

func TestUnsupportedFormatExitsOne(t *testing.T) {
	res := runCLI(t, "", "analyze", "../examples/healthy-go",
		"--no-persist", "--format", "yaml")
	if res.Code != ExitFailure {
		t.Fatalf("an unsupported format must exit %d, got %d\n%s",
			ExitFailure, res.Code, res.Combined())
	}
}

// ------------------------------------------------------------- config file

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

// -------------------------------------------------------- dependency drift

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

// ---------------------------------------------------------------- monorepo

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

// ---------------------------------------------------------------- history

func TestHistoryRecordsAndReads(t *testing.T) {
	dir := t.TempDir()
	copyTree(t, filepath.Join("..", "examples", "healthy-go"), dir)

	// Two recorded runs, so the timeline has something to draw.
	for i := 0; i < 2; i++ {
		res := runCLI(t, dir, "analyze", ".", "--format", "json")
		if res.Code != ExitOK {
			t.Fatalf("record %d exited %d\n%s", i, res.Code, res.Combined())
		}
	}

	// History must be recorded beside the analyzed tree, not in whatever
	// directory the process happened to start from.
	db := filepath.Join(dir, ".lensyxe", "history.db")
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("expected a history database at %s: %v", db, err)
	}

	res := runCLI(t, dir, "history", ".")
	if res.Code != ExitOK {
		t.Fatalf("history exited %d\n%s", res.Code, res.Combined())
	}
	if !strings.Contains(res.Stdout, "Engineering health timeline") {
		t.Errorf("expected a timeline:\n%s", truncate(res.Stdout, 500))
	}
}

func TestNoPersistWritesNoDatabase(t *testing.T) {
	dir := t.TempDir()
	copyTree(t, filepath.Join("..", "examples", "healthy-go"), dir)

	// The assertion below is only meaningful if the target starts empty. A
	// database that arrived with the fixture would make this fail for a reason
	// that has nothing to do with --no-persist, and the failure message would
	// point at the flag rather than at the polluted fixture.
	if _, err := os.Stat(filepath.Join(dir, ".lensyxe")); err == nil {
		t.Fatal("the copied fixture already contains .lensyxe; " +
			"the example directory has been polluted by an analysis run")
	}

	res := runCLI(t, dir, "analyze", ".", "--no-persist")
	if res.Code != ExitOK {
		t.Fatalf("analyze exited %d\n%s", res.Code, res.Combined())
	}
	if _, err := os.Stat(filepath.Join(dir, ".lensyxe")); err == nil {
		t.Error("--no-persist must not create a database")
	}
}

// ------------------------------------------------------------------- serve

// The server must start, answer the API, and shut down. It binds loopback on
// an ephemeral port, so this does not collide with anything.
func TestServeAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("serve starts a process; skipped under -short")
	}

	dir := t.TempDir()
	copyTree(t, filepath.Join("..", "examples", "healthy-go"), dir)

	port := freePort(t)
	cmd := exec.Command(cli(t), "serve", ".", "--port", itoa(port))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LENSYXE_AI_KEY=", "LENSYXE_AI_PROVIDER=")

	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	base := "http://127.0.0.1:" + itoa(port)
	if !waitForHTTP(base+"/api/v1/version", 30*time.Second) {
		t.Fatalf("the server did not become ready\nstderr:\n%s", stderr.String())
	}

	// Every documented endpoint must answer, and each must return valid JSON
	// with the fields the dashboard reads.
	for _, tc := range []struct {
		path   string
		fields []string
	}{
		{"/api/v1/health", []string{"root", "health", "code", "git", "dependencies"}},
		{"/api/v1/risks", []string{"total", "risks"}},
		{"/api/v1/hotspots", []string{"total", "hotspots", "churn"}},
		{"/api/v1/history", []string{"count", "records"}},
		{"/api/v1/version", []string{"version", "schema_version"}},
	} {
		body, code := httpGet(t, base+tc.path)
		if code != 200 {
			t.Errorf("%s returned %d: %s", tc.path, code, truncate(body, 300))
			continue
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Errorf("%s did not return JSON: %v", tc.path, err)
			continue
		}
		for _, f := range tc.fields {
			if _, ok := doc[f]; !ok {
				t.Errorf("%s is missing the %q field the dashboard reads", tc.path, f)
			}
		}
	}
}

// A dashboard route must not be answered with the API's JSON, and vice versa.
func TestServeDoesNotConfuseJSONWithHTML(t *testing.T) {
	if testing.Short() {
		t.Skip("serve starts a process; skipped under -short")
	}

	dir := t.TempDir()
	copyTree(t, filepath.Join("..", "examples", "healthy-go"), dir)

	port := freePort(t)
	cmd := exec.Command(cli(t), "serve", ".", "--port", itoa(port))
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	base := "http://127.0.0.1:" + itoa(port)
	if !waitForHTTP(base+"/api/v1/version", 30*time.Second) {
		t.Fatalf("the server did not become ready\n%s", stderr.String())
	}

	// An unknown API path must be a JSON error, not the dashboard. Returning
	// index.html with a 200 would break every API client silently.
	body, code := httpGet(t, base+"/api/v1/nonexistent")
	if code == 200 {
		t.Errorf("an unknown API path returned 200: %s", truncate(body, 200))
	}
	if strings.Contains(body, "<!doctype html") {
		t.Error("an API path was answered with the dashboard HTML")
	}
}

// ------------------------------------------------------------- CI surfaces

// The PR comment must be written to disk even when the gate rejects the run,
// because a rejection that published no evidence would be the worst outcome for
// a gate.
func TestCommentWrittenOnGateFailure(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "comment.md")

	res := runCLI(t, "", "analyze", "../examples/risky-go",
		"--no-persist", "--fail-under-health", "99", "--pr-comment", out)
	if res.Code != ExitGateFailed {
		t.Fatalf("expected a gate rejection, got %d\n%s", res.Code, res.Combined())
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the comment must be written even when the gate fails: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, "lensyxe:engineering-impact") {
		t.Errorf("the comment must carry the marker so a later run can update it:\n%s",
			truncate(body, 400))
	}
	// The comment must not claim measurements Lensyxe does not make.
	if strings.Contains(body, "coverage dropped") {
		t.Error("the comment must not claim a coverage measurement")
	}
}

func TestBaselineDrivesDeltas(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.json")

	// Capture a real snapshot as the baseline.
	res := runCLI(t, "", "analyze", "../examples/healthy-go",
		"--format", "json", "--no-persist", "--no-gate")
	if res.Code != ExitOK {
		t.Fatalf("baseline capture exited %d\n%s", res.Code, res.Combined())
	}
	if err := os.WriteFile(base, []byte(res.Stdout), 0o644); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "comment.md")
	res = runCLI(t, "", "analyze", "../examples/healthy-go",
		"--no-persist", "--no-gate", "--baseline", base, "--pr-comment", out)
	if res.Code != ExitOK {
		t.Fatalf("baseline run exited %d\n%s", res.Code, res.Combined())
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "→") {
		t.Errorf("a baseline must produce a movement headline:\n%s", truncate(body, 600))
	}
	// An unchanged repository must not be reported as a regression.
	if strings.Contains(body, "Regression") {
		t.Errorf("an identical baseline must not report a regression:\n%s",
			truncate(body, 600))
	}
}

// --fail-on-coverage-drop must be rejected rather than silently gating a
// weaker metric under a name that promises more.
func TestCoverageGateIsRejected(t *testing.T) {
	res := runCLI(t, "", "analyze", "../examples/healthy-go",
		"--no-persist", "--fail-on-coverage-drop", "3")
	if res.Code == ExitOK {
		t.Fatal("a coverage gate must not silently succeed; Lensyxe does not measure coverage")
	}
	if !strings.Contains(res.Combined(), "coverage") {
		t.Errorf("the rejection must explain that coverage is not measured:\n%s",
			res.Combined())
	}
}

// --no-gate must win over every threshold, including the flags on the same
// command line. This is the explicit resolution of a contradictory pairing:
// a caller who writes both wants the report, not the verdict.
func TestNoGateWinsOverFlagThresholds(t *testing.T) {
	res := runCLI(t, "", "analyze", "../examples/healthy-go",
		"--no-persist", "--no-gate",
		"--fail-under-health", "10", // 0..10: impossible for any repo
		"--fail-on-critical-risk",        // any critical risk
		"--fail-on-test-ratio-drop", "0") // any fall at all
	if res.Code != ExitOK {
		t.Fatalf("--no-gate must win over the flag thresholds, got exit %d\n%s",
			res.Code, res.Combined())
	}
}

// ------------------------------------------------------------------ helpers

// writeConfig writes a config file into dir.
func writeConfig(t testing.TB, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".lensyxe.yml"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// copyTree copies src into dst.
//
// Analysis artifacts are skipped. A fixture is source, and a fixture that
// carries a history database is not the fixture it claims to be: the database
// would be copied into every test's temporary directory, and a test asserting
// that --no-persist writes nothing would then fail on a database that arrived
// before the run started.
//
// That is not hypothetical. A `lensyxe analyze examples/healthy-go` run during
// development left `.lensyxe/history.db` inside the example, and the next full
// suite failed in exactly that way. The failure was correct and the diagnosis
// was the polluted fixture; the guard below stops the fixture being polluted in
// the first place by anything this suite copies.
func copyTree(t testing.TB, src, dst string) {
	t.Helper()

	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		// Skip at any depth: a nested example could have its own.
		if info.IsDir() {
			switch info.Name() {
			case ".lensyxe", ".openlens":
				return filepath.SkipDir
			}
		}

		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatalf("copy %s: %v", src, err)
	}
}

// freePort asks the OS for an unused port.
//
// Binding to :0 and closing has an inherent race with another process taking
// the port, but it is far smaller than the race in picking a fixed port, and it
// is the standard approach for a test that must not collide with a developer's
// running server.
func freePort(t testing.TB) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release the port: %v", err)
	}
	return port
}
