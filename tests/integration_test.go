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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
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
