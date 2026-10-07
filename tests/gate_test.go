package tests

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
