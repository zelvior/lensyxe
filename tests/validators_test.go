package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The repository validators are `//go:build ignore` programs, so they cannot be
// imported and a unit test cannot call into them. They are exercised the only
// way that works: run them, against the real repository, and assert on what they
// report.
//
// They are worth covering because each one is a guard against a failure mode
// that is silent by construction. A malformed workflow simply does not run. An
// issue form with a bad schema renders blank. A site link to a deleted anchor
// looks fine in the source. Nothing else in this repository reads those files,
// so if a validator stops working there is no signal at all -- which is exactly
// how the test-install.sh template extraction went stale for several commits
// while reporting a false failure, and how the archive name_template check
// reported success while every published archive carried a space in its name.
//
// The negative cases here matter more than the positive ones. A validator that
// cannot fail is worse than no validator, because it reads as coverage.

// validator is one `go run` invokable checker.
type validator struct {
	name string
	// script is the path passed to `go run`, relative to the repository root.
	script string
	// wantOutput is a substring the success run must print. It confirms the
	// validator actually did its work rather than exiting zero without looking.
	wantOutput string
}

func validators() []validator {
	return []validator{
		{"check-workflows", "scripts/check-workflows.go", "workflow(s) parsed"},
		{"check-issue-templates", "scripts/check-issue-templates.go", "file(s) checked, 0 problem(s)"},
		{"check-repo-yaml", "scripts/check-repo-yaml.go", "file(s) checked"},
		{"check-web", "scripts/check-web.go", "security headers present"},
		{"check-manifest-docs", "ide/vscode/scripts/check-manifest-docs.go", "setting(s)"},
	}
}

// runValidator executes a checker with the repository root as its working
// directory, which is what every one of them assumes.
func runValidator(t *testing.T, script string) (stdout, stderr string, err error) {
	t.Helper()

	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}

	cmd := exec.Command("go", "run", script)
	// The repository root is one level above this package.
	cmd.Dir = ".."

	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	err = cmd.Run()
	return out.String(), errOut.String(), err
}

// Every validator must pass against the repository as committed.
func TestRepositoryValidatorsPass(t *testing.T) {
	for _, v := range validators() {
		t.Run(v.name, func(t *testing.T) {
			stdout, stderr, err := runValidator(t, v.script)
			if err != nil {
				t.Fatalf("%s failed on a clean tree: %v\nstdout:\n%s\nstderr:\n%s",
					v.name, err, stdout, stderr)
			}
			if !strings.Contains(stdout, v.wantOutput) {
				t.Errorf("%s exited zero but did not print %q; it may not be checking anything.\nstdout:\n%s",
					v.name, v.wantOutput, stdout)
			}
		})
	}
}

// check-web is the validator whose failure mode is easiest to get wrong, because
// every assertion it makes is about a file that simply being present or absent
// would satisfy. These cases confirm it still rejects each fault.
//
// A temporary copy of the whole site is used rather than the real one, so a
// deliberately broken file can never reach the repository.
func TestCheckWebRejectsFaults(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	cases := []struct {
		name    string
		file    string
		mutate  func(string) string
		wantSub string
	}{
		{
			name:    "a dangling asset reference",
			file:    "index.html",
			mutate:  func(s string) string { return strings.Replace(s, `href="/styles.css"`, `href="/missing.css"`, 1) },
			wantSub: "does not exist",
		},
		{
			name:    "a link to a heading that does not exist",
			file:    "index.html",
			mutate:  func(s string) string { return strings.Replace(s, `href="#scores"`, `href="#nonexistent"`, 1) },
			wantSub: "not a heading",
		},
		{
			name: "a weakened content security policy",
			file: "vercel.json",
			mutate: func(s string) string {
				return strings.Replace(s, "script-src 'self'", "script-src 'self' 'unsafe-inline'", 1)
			},
			wantSub: "unsafe-inline",
		},
		{
			name:    "a removed security header",
			file:    "vercel.json",
			mutate:  func(s string) string { return strings.Replace(s, `"Strict-Transport-Security"`, `"X-Removed"`, 1) },
			wantSub: "does not set",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := copyWebTree(t)

			target := filepath.Join(dir, "web", tc.file)
			original, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			mutated := tc.mutate(string(original))
			if mutated == string(original) {
				t.Fatalf("the mutation for %q did not change %s; the test is not "+
					"exercising anything", tc.name, tc.file)
			}
			if err := os.WriteFile(target, []byte(mutated), 0o644); err != nil {
				t.Fatalf("write %s: %v", tc.file, err)
			}

			cmd := exec.Command("go", "run", "scripts/check-web.go")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()

			if err == nil {
				t.Fatalf("check-web accepted %s, which it should reject.\noutput:\n%s",
					tc.name, out)
			}
			if !strings.Contains(string(out), tc.wantSub) {
				t.Errorf("check-web failed on %s but did not say %q.\noutput:\n%s",
					tc.name, tc.wantSub, out)
			}
		})
	}
}

// check-web passes on the copy it was not broken, so a failure above is the
// mutation and not the harness.
func TestCheckWebAcceptsTheRealSite(t *testing.T) {
	dir := copyWebTree(t)

	cmd := exec.Command("go", "run", "scripts/check-web.go")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("check-web rejected an unmodified copy of the site: %v\noutput:\n%s", err, out)
	}
}

// copyWebTree builds a self-contained copy of the repository that the validators
// can run against: the Go module and the site, and nothing else.
//
// The module is required because the validator is run with `go run`, and the
// copy is made rather than used in place because two of these cases write a
// deliberately broken file.
func copyWebTree(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}

	dir := t.TempDir()

	// go.mod and go.sum so `go run` has a module. The checkers import nothing, so
	// no dependency needs to be present and no download happens.
	for _, f := range []string{"go.mod", "go.sum"} {
		copyFile(t, filepath.Join(root, f), filepath.Join(dir, f))
	}
	copyDir(t, filepath.Join(root, "web"), filepath.Join(dir, "web"))
	copyDir(t, filepath.Join(root, "scripts"), filepath.Join(dir, "scripts"))

	return dir
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", dst, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

// copyDir copies a tree, skipping build output and dependencies.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()

	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dst, err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			switch name {
			case "node_modules", "out", ".next":
				continue
			}
			copyDir(t, filepath.Join(src, name), filepath.Join(dst, name))
			continue
		}
		copyFile(t, filepath.Join(src, name), filepath.Join(dst, name))
	}
}
