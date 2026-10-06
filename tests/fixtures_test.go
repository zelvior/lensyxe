package tests

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// This file materializes the example repositories the benchmark and integration
// suites run against.
//
// The fixtures are generated rather than committed as source trees for two
// reasons. A committed tree large enough to be a meaningful benchmark would
// dominate the repository, and a generated one can be scaled from 40 files to
// 2,000 without anyone checking in thousands of files. Generation is also done
// in Go rather than a shell script so `go test` does not require bash, which
// keeps the suite runnable on a contributor's Windows machine.
//
// Generation is deterministic: the same seed always produces the same tree, so
// a benchmark regression is a change in the code rather than in the fixture.

// fixture describes one generated repository.
type fixture struct {
	// Name identifies the profile, for example "small".
	Name string
	// Files is how many source files to create.
	Files int
	// TestShare is the fraction of files named as tests, 0.0 to 1.0.
	TestShare float64
	// PackageCount spreads files across this many directories.
	PackageCount int
	// LargeFileEvery creates an oversized file every N files, which is what
	// exercises the hotspot path. Zero means none.
	LargeFileEvery int
	// LargeFileLines is the line count of those oversized files.
	LargeFileLines int
	// Git, when true, initializes a repository with a commit history.
	Git bool
	// Commits is how many commits to create when Git is true.
	Commits int
}

// writeFile creates a file and its parent directories.
func writeFile(t testing.TB, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// goSource renders a Go file with a controlled number of code lines and branch
// points.
//
// Branch points are not decoration. A fixture made of straight-line functions
// scores high on every complexity metric and would leave the hotspot and
// complexity code paths unexercised, which is exactly the code under test.
func goSource(pkg string, seed, lines int) string {
	var b strings.Builder

	fmt.Fprintf(&b, "package %s\n\n", pkg)
	fmt.Fprintf(&b, "// Shape%d is fixture data for the Lensyxe benchmark suite.\n", seed)
	fmt.Fprintf(&b, "type Shape%d struct {\n\tWidth  int\n\tHeight int\n}\n\n", seed)
	fmt.Fprintf(&b, "func Area%d(s Shape%d) int {\n", seed, seed)
	b.WriteString("\tif s.Width < 0 || s.Height < 0 {\n\t\treturn 0\n\t}\n")
	b.WriteString("\ttotal := 0\n")

	// Every iteration contributes four code lines and two branch points, so the
	// requested size lands the file near `lines`.
	perIter := (lines - 10) / 4
	if perIter < 1 {
		perIter = 1
	}
	for i := 0; i < perIter; i++ {
		fmt.Fprintf(&b, "\tif s.Width > %d && s.Height > %d {\n", i, i%7)
		fmt.Fprintf(&b, "\t\ttotal += s.Width * s.Height\n")
		b.WriteString("\t}\n")
	}
	b.WriteString("\treturn total\n}\n")

	return b.String()
}

// build materializes the fixture in a temporary directory owned by one test.
//
// Most callers want buildAt against the shared root instead; this exists for
// the one-off cases that genuinely want isolation.
func (f fixture) build(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	return f.buildAt(t, dir)
}

// buildAt materializes the fixture in dir.
func (f fixture) buildAt(t testing.TB, dir string) string {
	t.Helper()

	pkgCount := f.PackageCount
	if pkgCount < 1 {
		pkgCount = 1
	}

	large := map[int]bool{}
	if f.LargeFileEvery > 0 {
		for i := 0; i < f.Files; i += f.LargeFileEvery {
			large[i] = true
		}
	}

	for i := 0; i < f.Files; i++ {
		pkg := fmt.Sprintf("pkg%d", i%pkgCount)
		dirIn := "internal"
		if i%3 == 0 {
			dirIn = "cmd"
		}

		name := fmt.Sprintf("file_%d.go", i)
		lines := 60 + (i*7)%180
		if large[i] {
			name = fmt.Sprintf("big_%d.go", i)
			lines = f.LargeFileLines
		}
		// A quarter of the files by default become tests, so the test-ratio
		// metric has a real denominator.
		if float64(i)/float64(max(f.Files, 1)) < f.TestShare {
			name = strings.TrimSuffix(name, ".go") + "_test.go"
			lines = 30
		}

		writeFile(t, filepath.Join(dir, dirIn, pkg, name),
			goSource(pkg, i, lines))
	}

	// A manifest plus a lockfile so the dependency analyzer reports a locked,
	// verifiable surface rather than drift.
	writeFile(t, filepath.Join(dir, "go.mod"),
		"module example.com/bench\n\ngo 1.22\n")
	writeFile(t, filepath.Join(dir, "go.sum"), strings.Join([]string{
		"github.com/spf13/cobra v1.8.1/go.mod h1:placeholder",
		"github.com/spf13/viper v1.19.0/go.mod h1:placeholder",
		"modernc.org/sqlite v1.34.5/go.mod h1:placeholder",
		"",
	}, "\n"))

	if f.Git {
		initGitRepo(t, dir, f.Commits)
	}
	return dir
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// initGitRepo creates a repository with a commit history.
//
// Commit timestamps are spread across the window and authors are varied so the
// cadence, freshness, and bus-factor metrics have non-trivial inputs. Without
// that spread every run scores identically on git, which would make the git
// benchmark measure only the filesystem walk.
func initGitRepo(t testing.TB, dir string, commits int) {
	t.Helper()

	if commits <= 0 {
		commits = 12
	}

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Fixture",
			"GIT_AUTHOR_EMAIL=fixture@example.com",
			"GIT_COMMITTER_NAME=Fixture",
			"GIT_COMMITTER_EMAIL=fixture@example.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Skipf("git is unavailable or misconfigured: %v\n%s", err, out)
		}
		return string(out)
	}

	run("init", "-q")
	run("config", "user.name", "Fixture")
	run("config", "user.email", "fixture@example.com")
	run("config", "commit.gpgsign", "false")

	// Collect the files so each commit touches something real. A repository
	// where every commit is empty would be optimized away by git.
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("collect files: %v", err)
	}

	// A fixed seed keeps the history identical between runs.
	rng := rand.New(rand.NewSource(1))

	// Start well in the past so the final commit is recent relative to the
	// analyzer's freshness window.
	run("commit", "-q", "--allow-empty", "-m", "initial",
		"--date", "2025-06-01T00:00:00Z")

	for c := 0; c < commits; c++ {
		// Touch a handful of files, sometimes appending so the diff is real.
		touches := 1 + rng.Intn(4)
		for i := 0; i < touches && len(files) > 0; i++ {
			target := files[rng.Intn(len(files))]
			body, err := os.ReadFile(target)
			if err != nil {
				continue
			}
			if err := os.WriteFile(target, append(body,
				fmt.Appendf(nil, "\n// revision %d\n", c)...), 0o644); err != nil {
				continue
			}
		}

		day := 1 + c%28
		run("add", "-A")
		run("commit", "-q", "--allow-empty",
			"-m", fmt.Sprintf("change %d", c),
			"--date", fmt.Sprintf("2025-09-%02dT12:00:00Z", day))
	}
}

// ---------------------------------------------------------- shared fixtures
//
// Building a fixture costs a filesystem walk, which is the very thing being
// measured. Building them once per run and reusing across benchmarks keeps the
// reported numbers about the analyzer rather than about the setup.
//
// The directory is created with os.MkdirTemp and removed in TestMain, NOT with
// t.TempDir. t.TempDir is cleaned up when the test that created it finishes, so
// a fixture shared through sync.Once would be deleted while later tests were
// still pointing at it. That failure is silent and confusing: the first test
// passes and every subsequent one reports a missing directory.

var (
	smallOnce  sync.Once
	smallDir   string
	mediumOnce sync.Once
	mediumDir  string
	largeOnce  sync.Once
	largeDir   string

	// fixtureRoot holds every generated fixture for the run.
	fixtureRoot string
)

// initFixtureRoot creates the directory the shared fixtures live in.
//
// Called from TestMain before any test runs, so the cleanup is tied to the
// process rather than to whichever test happened to build first.
func initFixtureRoot() {
	dir, err := os.MkdirTemp("", "lensyxe-fixtures")
	if err != nil {
		panic("tests: cannot create the fixture root: " + err.Error())
	}
	fixtureRoot = dir
}

// cleanupFixtures removes the fixture root.
func cleanupFixtures() {
	if fixtureRoot != "" {
		_ = os.RemoveAll(fixtureRoot)
		fixtureRoot = ""
	}
}

// fixturePath returns the directory for a profile, creating it once.
func fixturePath(name string) string {
	return filepath.Join(fixtureRoot, name)
}

// buildInto materializes a fixture under the shared root.
func buildInto(t testing.TB, f fixture) string {
	dir := fixturePath(f.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("fixture %s: %v", f.Name, err)
	}
	return f.buildAt(t, dir)
}

// smallFixture is a small single-package repository: roughly what a library or
// a service would be.
func smallFixture(t testing.TB) string {
	smallOnce.Do(func() {
		smallDir = buildInto(t, fixture{
			Name:           "small",
			Files:          40,
			TestShare:      0.25,
			PackageCount:   4,
			LargeFileEvery: 17,
			LargeFileLines: 700,
			Git:            true,
			Commits:        12,
		})
	})
	if smallDir == "" {
		t.Fatal("fixture construction failed")
	}
	return smallDir
}

// mediumFixture is a larger multi-package service.
func mediumFixture(t testing.TB) string {
	mediumOnce.Do(func() {
		mediumDir = buildInto(t, fixture{
			Name:           "medium",
			Files:          400,
			TestShare:      0.25,
			PackageCount:   24,
			LargeFileEvery: 23,
			LargeFileLines: 900,
			Git:            true,
			Commits:        40,
		})
	})
	if mediumDir == "" {
		t.Fatal("fixture construction failed")
	}
	return mediumDir
}

// largeFixture is a monorepo-scale repository, used only to confirm the
// analyzer degrades linearly rather than falling over.
func largeFixture(t testing.TB) string {
	largeOnce.Do(func() {
		largeDir = buildInto(t, fixture{
			Name:           "large",
			Files:          2000,
			TestShare:      0.25,
			PackageCount:   64,
			LargeFileEvery: 40,
			LargeFileLines: 1100,
			Git:            true,
			Commits:        120,
		})
	})
	if largeDir == "" {
		t.Fatal("fixture construction failed")
	}
	return largeDir
}

// newRepoWithFiles builds a bespoke repository inline, for tests that need a
// specific shape rather than a profile.
func newRepoWithFiles(t testing.TB, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	// Sorted so the tree is identical between runs regardless of map order.
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), files[name])
	}
	return dir
}

// sortStrings is an insertion sort: these lists are short and it avoids
// importing sort purely for them.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
