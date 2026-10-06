package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/internal/compare"
	"github.com/zelvior/lensyxe/pkg/models"
)

// runCLI executes the command tree with args, capturing stdout and stderr.
//
// Every invocation gets a fresh app, so configuration loaded by one command can
// never leak into the next. Several tests assert exactly that.
func runCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := newRootCmd(&app{})
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errBuf.String(), err
}

// runCLIIn runs the command tree with cwd temporarily set to dir.
func runCLIIn(t *testing.T, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	wd, gerr := os.Getwd()
	if gerr != nil {
		t.Fatalf("getwd: %v", gerr)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	return runCLI(t, args...)
}

func TestAnalyzeDefaultsToCurrentDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLIIn(t, dir, "analyze")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if !strings.Contains(stdout, "ENGINEERING HEALTH") {
		t.Errorf("expected a health card, got:\n%s", stdout)
	}
}

func TestAnalyzeJSONOutputIsValid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLI(t, "analyze", dir, "--format", "json")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	var snap models.Snapshot
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if snap.SchemaVersion != models.SchemaVersion {
		t.Errorf("schema_version = %q, want %q", snap.SchemaVersion, models.SchemaVersion)
	}
	if snap.Root == "" {
		t.Error("root should be populated")
	}
	if snap.Code.Complexity.WorstFiles == nil {
		t.Error("complexity worst_files should be [] not null")
	}
}

// The v0.1 --format json contract must keep working unchanged.
func TestAnalyzeJSONRetainsPhaseOneContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	stdout, _, err := runCLI(t, "analyze", dir, "--format", "json")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal([]byte(stdout), &generic); err != nil {
		t.Fatalf("json: %v", err)
	}
	for _, key := range []string{"schema_version", "tool", "root", "code", "git", "dependencies", "health", "findings", "risks"} {
		if _, ok := generic[key]; !ok {
			t.Errorf("top-level key %q missing from JSON output", key)
		}
	}
	code, ok := generic["code"].(map[string]any)
	if !ok {
		t.Fatal("code is not an object")
	}
	// Phase 1 code fields must still be present.
	for _, key := range []string{"files", "total_lines", "code_lines", "max_file_lines", "average_lines", "languages", "hotspots", "bytes", "truncated"} {
		if _, ok := code[key]; !ok {
			t.Errorf("code.%q missing", key)
		}
	}
	deps, ok := generic["dependencies"].(map[string]any)
	if !ok {
		t.Fatal("dependencies is not an object")
	}
	for _, key := range []string{"detected", "ecosystems", "total", "direct", "dev", "locked"} {
		if _, ok := deps[key]; !ok {
			t.Errorf("dependencies.%q missing", key)
		}
	}
}

func TestAnalyzeMarkdownOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLI(t, "analyze", dir, "--format", "markdown")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	for _, want := range []string{"# ", "## Health score", "## Code", "## Risks", "**Recommendation**"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	if strings.Contains(stdout, "\x1b[") {
		t.Error("markdown output must not contain ANSI escapes")
	}
}

func TestAnalyzeMarkdownFindingsFlag(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	without, _, err := runCLI(t, "analyze", dir, "--format", "markdown")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	with, _, err := runCLI(t, "analyze", dir, "--format", "markdown", "--findings")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if strings.Contains(without, "## Findings") {
		t.Error("findings should be omitted without --findings")
	}
	if !strings.Contains(with, "## Findings") {
		t.Error("expected a findings section with --findings")
	}
}

// "md" is accepted as an alias so scripts can use the short form.
func TestAnalyzeMarkdownAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := runCLI(t, "analyze", dir, "--format", "md"); err != nil {
		t.Fatalf("md alias should be accepted: %v", err)
	}
}

func TestAnalyzeRejectsUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := runCLI(t, "analyze", dir, "--format", "yaml"); err == nil {
		t.Fatal("expected an error for an unsupported format")
	}
	if _, _, err := runCLI(t, "analyze", dir, "--format", "YAML"); err == nil {
		t.Fatal("format matching must be case-insensitive in both directions")
	}
}

func TestAnalyzeRejectsTooManyArgs(t *testing.T) {
	if _, _, err := runCLI(t, "analyze", "a", "b"); err == nil {
		t.Fatal("expected an error for more than one positional argument")
	}
}

func TestAnalyzeMissingPathErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if _, _, err := runCLI(t, "analyze", missing); err == nil {
		t.Fatal("expected an error for a nonexistent path")
	}
}

func TestAnalyzeFlagsOverrideConfig(t *testing.T) {
	dir := t.TempDir()
	body := ""
	for i := 0; i < 30; i++ {
		body += "line\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLI(t, "analyze", dir, "--format", "json", "--hotspot-threshold", "10")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	var snap models.Snapshot
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(snap.Code.Hotspots) != 1 {
		t.Errorf("expected one hotspot with --hotspot-threshold 10, got %d", len(snap.Code.Hotspots))
	}

	stdout, _, err = runCLI(t, "analyze", dir, "--format", "json", "--hotspot-threshold", "1000")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	snap = models.Snapshot{}
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(snap.Code.Hotspots) != 0 {
		t.Errorf("expected no hotspots with a high threshold, got %d", len(snap.Code.Hotspots))
	}
}

func TestConfigFileIsHonored(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "custom.yml")
	if err := os.WriteFile(cfg, []byte("hotspot_threshold: 5\ngit_window_days: 30\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	body := ""
	for i := 0; i < 10; i++ {
		body += "line\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLI(t, "analyze", dir, "--format", "json", "--config", cfg)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	var snap models.Snapshot
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("json: %v", err)
	}
	if snap.Git.WindowDays != 30 {
		t.Errorf("WindowDays = %d, want 30 from the config file", snap.Git.WindowDays)
	}
	if len(snap.Code.Hotspots) != 1 {
		t.Errorf("expected the config hotspot_threshold to apply, got %d hotspots", len(snap.Code.Hotspots))
	}
}

func TestFlagBeatsConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "custom.yml")
	if err := os.WriteFile(cfg, []byte("hotspot_threshold: 1000\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("line\nline\nline\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLI(t, "analyze", dir, "--format", "json",
		"--config", cfg, "--hotspot-threshold", "2")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	var snap models.Snapshot
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(snap.Code.Hotspots) != 1 {
		t.Error("the flag should override the config file value")
	}
}

func TestMissingConfigFileIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.yml")
	if _, _, err := runCLI(t, "analyze", dir, "--config", missing); err != nil {
		t.Fatalf("a missing config file must not fail the run: %v", err)
	}
}

func TestMalformedConfigFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "broken.yml")
	if err := os.WriteFile(cfg, []byte("hotspot_threshold: [unclosed\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, stderr, err := runCLI(t, "analyze", dir, "--config", cfg, "--format", "json")
	if err != nil {
		t.Fatalf("a malformed config must not be fatal: %v", err)
	}
	if !strings.Contains(stderr, "ignoring config") {
		t.Errorf("expected a warning on stderr, got %q", stderr)
	}
	var snap models.Snapshot
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("json: %v", err)
	}
	if snap.Health.Score <= 0 {
		t.Error("expected a scored snapshot despite the broken config")
	}
}

func TestVersionCommand(t *testing.T) {
	stdout, _, err := runCLI(t, "version")
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.Contains(stdout, Version) {
		t.Errorf("version output %q should contain %q", stdout, Version)
	}
	// Every injected field must appear, since the whole point of the command
	// is to tell a user exactly which binary they are running.
	for _, want := range []string{"Commit:", "Build Date:", "Built By:", "Go Version:", "OS/Arch:"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("version output is missing the %q field:\n%s", want, stdout)
		}
	}
}

// A binary built without injected metadata must say so. Presenting a local
// build identically to a release is how a bug gets filed against a tag that
// never shipped.
func TestVersionFlagsDevelopmentBuild(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	Version = "dev"
	stdout, _, err := runCLI(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "Development build") {
		t.Errorf("a dev build must be labelled:\n%s", stdout)
	}

	Version = "1.2.3"
	stdout, _, err = runCLI(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "Development build") {
		t.Errorf("a release build must not be labelled as development:\n%s", stdout)
	}
	if !strings.Contains(stdout, "v1.2.3") {
		t.Errorf("the version must carry a leading v:\n%s", stdout)
	}
}

// A tag whose version already begins with "v" must not render "vv1.0.0".
func TestVersionStringDoesNotDoubleThePrefix(t *testing.T) {
	cases := map[string]string{
		"1.2.3":  "v1.2.3",
		"v1.2.3": "v1.2.3",
		"":       "vdev",
		"  ":     "vdev",
		"dev":    "vdev",
	}
	for in, want := range cases {
		orig := Version
		Version = in
		if got := versionString(); got != want {
			t.Errorf("versionString(%q) = %q, want %q", in, got, want)
		}
		Version = orig
	}
}

func TestIsDevelopmentBuild(t *testing.T) {
	for _, in := range []string{"", "dev", "DEV", "devel", "unknown"} {
		orig := Version
		Version = in
		if !isDevelopmentBuild() {
			t.Errorf("version %q should be treated as a development build", in)
		}
		Version = orig
	}
	for _, in := range []string{"1.2.3", "v1.2.3", "0.0.1-rc1"} {
		orig := Version
		Version = in
		if isDevelopmentBuild() {
			t.Errorf("version %q should be treated as a release", in)
		}
		Version = orig
	}
}

// The rendered block must stay aligned as fields are added.
func TestBuildInfoStringIsAligned(t *testing.T) {
	info := BuildInfo{
		Version: "v1.0.0", Commit: "8f3a9d2", Date: "2026-10-05T06:00:00Z",
		BuiltBy: "goreleaser", GoVersion: "go1.22.0", OS: "windows", Arch: "amd64",
	}
	out := info.String()
	if !strings.Contains(out, "Lensyxe Core v1.0.0") {
		t.Errorf("missing version line:\n%s", out)
	}
	for _, want := range []string{
		"Commit:     8f3a9d2",
		"Build Date: 2026-10-05T06:00:00Z",
		"Built By:   goreleaser",
		"OS/Arch:    windows/amd64",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected the line %q in:\n%s", want, out)
		}
	}
}

func TestCurrentBuildInfo(t *testing.T) {
	info := CurrentBuildInfo()
	if info.GoVersion == "" || info.OS == "" || info.Arch == "" {
		t.Errorf("runtime facts must always be present: %+v", info)
	}
	if info.Version == "" {
		t.Error("version must never be empty")
	}
}

func TestVersionRejectsArguments(t *testing.T) {
	if _, _, err := runCLI(t, "version", "extra"); err == nil {
		t.Error("version takes no arguments and must reject them")
	}
}

func TestRootHasHelpAndVersion(t *testing.T) {
	cmd := newRootCmd(&app{})
	if cmd.Version == "" {
		t.Error("root command should expose a version")
	}
	if cmd.Use != "lensyxe" {
		t.Errorf("Use = %q", cmd.Use)
	}
}

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"ordinary failure", errors.New("boom"), 1},
		{"gate failure", fmt.Errorf("%w: 1 threshold breached", errGateFailed), exitGateFailure},
		{"wrapped gate failure", fmt.Errorf("context: %w", errGateFailed), exitGateFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestGateFailureExitCodeIsDistinct(t *testing.T) {
	// CI must be able to tell a policy rejection from a crash.
	if exitGateFailure == 1 {
		t.Error("the gate exit code must differ from the generic failure code")
	}
}

// History must be recorded beside the ANALYZED tree, not beside the working
// directory. Recording against the cwd meant `lensyxe analyze /other/repo`
// wrote its history into whatever directory the user happened to be in.
func TestAnalyzePersistsHistoryBesideTarget(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	elsewhere := t.TempDir()

	stdout, stderr, err := runCLIIn(t, elsewhere, "analyze", target)
	if err != nil {
		t.Fatalf("analyze: %v\n%s", stderr, stdout)
	}

	wantDB := filepath.Join(target, ".lensyxe", "history.db")
	if _, err := os.Stat(wantDB); err != nil {
		t.Fatalf("expected history at %s: %v", wantDB, err)
	}
	// Nothing must be written into the invocation directory.
	if _, err := os.Stat(filepath.Join(elsewhere, ".lensyxe")); err == nil {
		t.Error("history was written into the working directory instead of the target")
	}

	// And `history` must find it, even from an unrelated directory. The
	// "N snapshot(s)" note goes to stderr, so the timeline itself is the
	// signal that a run was recorded.
	out, stderr, err := runCLIIn(t, elsewhere, "history", target)
	if err != nil {
		t.Fatalf("history: %v\n%s", err, stderr)
	}
	if !strings.Contains(out, "Engineering health timeline") {
		t.Errorf("expected a timeline on stdout, got:\n%s", out)
	}
	if !strings.Contains(out, "first run") {
		t.Errorf("expected the recorded run to appear, got:\n%s", out)
	}
	if !strings.Contains(stderr, "1 snapshot") {
		t.Errorf("expected the snapshot count on stderr, got:\n%s", stderr)
	}
}

func TestAnalyzeNoPersistSkipsDatabase(t *testing.T) {
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := runCLI(t, "analyze", target, "--no-persist"); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".lensyxe")); err == nil {
		t.Error("--no-persist must not create a database")
	}
}

func TestCompareRequiresTwoArgs(t *testing.T) {
	if _, _, err := runCLI(t, "compare", "only-one"); err == nil {
		t.Error("expected an error with one argument")
	}
	if _, _, err := runCLI(t, "compare", "a", "b", "c"); err == nil {
		t.Error("expected an error with three arguments")
	}
}

func TestCompareOnNonRepositoryErrors(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runCLI(t, "compare", "HEAD", "HEAD~1", "--compare-root", dir)
	if err == nil {
		t.Fatal("expected an error outside a git repository")
	}
	if !strings.Contains(err.Error(), "git repository") {
		t.Errorf("error = %v, want it to mention the missing repository", err)
	}
}

func TestCompareRejectsUnknownFormat(t *testing.T) {
	dir := t.TempDir()
	_, _, err := runCLI(t, "compare", "HEAD", "HEAD~1", "--compare-root", dir, "--format", "xml")
	if err == nil {
		t.Error("expected an error for an unsupported format")
	}
}

// End-to-end compare against a real repository, exercising the archive-export
// path and both renderers.
func TestCompareEndToEnd(t *testing.T) {
	hasGit(t)
	repo := initRepo(t)

	writeIn(t, repo, "main.go", "package main\n\nfunc main() {}\n")
	writeIn(t, repo, "main_test.go", "package main\n\nfunc TestMain(t *testing.T) {}\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "first")

	var big strings.Builder
	big.WriteString("package main\n")
	big.WriteString(branchyBody(60))
	for i := 0; i < 500; i++ {
		big.WriteString("var pad")
		big.WriteString(strings.Repeat("X", i%5))
		big.WriteString(" = 1\n")
	}
	writeIn(t, repo, "big.go", big.String())
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "second")

	headBefore, err := gitOut2(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}

	t.Run("terminal", func(t *testing.T) {
		stdout, _, err := runCLI(t, "compare", "HEAD~1", "HEAD", "--compare-root", repo)
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		for _, want := range []string{
			"lensyxe compare", "HEALTH SCORE", "metric deltas",
			"NEW RISKS", "delta", "Significant regression",
		} {
			if !strings.Contains(stdout, want) {
				t.Errorf("compare terminal output missing %q\n%s", want, stdout)
			}
		}
	})

	t.Run("json", func(t *testing.T) {
		stdout, _, err := runCLI(t, "compare", "HEAD~1", "HEAD",
			"--compare-root", repo, "--format", "json")
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		var res compare.Result
		if err := json.Unmarshal([]byte(stdout), &res); err != nil {
			t.Fatalf("json: %v\n%s", err, stdout)
		}
		if res.ScoreDelta >= 0 {
			t.Errorf("ScoreDelta = %v, want a negative delta for added complexity", res.ScoreDelta)
		}
		if len(res.Metrics) == 0 {
			t.Error("expected metric deltas")
		}
		if res.SchemaVersion != models.SchemaVersion {
			t.Errorf("schema = %q", res.SchemaVersion)
		}
		// The exported tree for the later revision contains big.go, so it must
		// register as a new risk.
		if len(res.RisksAdded) == 0 {
			t.Errorf("expected new risks, got none (verdict: %s)", res.Verdict)
		}
	})

	t.Run("markdown", func(t *testing.T) {
		stdout, _, err := runCLI(t, "compare", "HEAD~1", "HEAD",
			"--compare-root", repo, "--format", "markdown")
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		if strings.Contains(stdout, "\x1b[") {
			t.Error("compare markdown must not contain ANSI escapes")
		}
		for _, want := range []string{"# Engineering health", "**Delta:", "## Metric deltas"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("compare markdown missing %q", want)
			}
		}
	})

	t.Run("leaves the working tree untouched", func(t *testing.T) {
		headAfter, err := gitOut2(repo, "rev-parse", "HEAD")
		if err != nil {
			t.Fatalf("rev-parse: %v", err)
		}
		if strings.TrimSpace(headAfter) != strings.TrimSpace(headBefore) {
			t.Error("compare moved HEAD")
		}
		status, err := gitOut2(repo, "status", "--porcelain")
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if strings.TrimSpace(status) != "" {
			t.Errorf("compare dirtied the working tree:\n%s", status)
		}
		// big.go must still be present, not checked out away.
		if _, err := os.Stat(filepath.Join(repo, "big.go")); err != nil {
			t.Errorf("compare removed a working-tree file: %v", err)
		}
	})
}

// hasGit reports whether git is available, skipping the test if not.
func hasGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// initRepo creates a git repository with deterministic identity.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main", ".")
	gitIn(t, dir, "config", "user.name", "Tester")
	gitIn(t, dir, "config", "user.email", "tester@example.com")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// gitIn runs a git command in dir, skipping the test on failure.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git %v failed (%v): %s", args, err, out)
	}
}

// gitOut2 runs git and returns stdout.
func gitOut2(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// writeIn writes a file inside dir.
func writeIn(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// branchyBody returns a function body with n branch points.
func branchyBody(n int) string {
	var b strings.Builder
	b.WriteString("func branchy() {\n")
	for i := 0; i < n; i++ {
		b.WriteString("\tif c")
		b.WriteString(strings.Repeat("x", i%3))
		b.WriteString(" && d")
		b.WriteString(strings.Repeat("y", i%3))
		b.WriteString(" {\n\t\tswitch v {\n\t\tcase 1:\n\t\tdefault:\n\t\t}\n\t}\n")
	}
	b.WriteString("}\n")
	return b.String()
}
