package main

import (
	"strings"
	"testing"
)

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
