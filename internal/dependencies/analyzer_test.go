package dependencies

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestAnalyzeNPM(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{
  "name": "demo",
  "dependencies": {"alpha": "^1.0.0", "beta": "^2.0.0"},
  "devDependencies": {"gamma": "^3.0.0"}
}`)
	writeFile(t, dir, "package-lock.json", `{"lockfileVersion": 3}`)

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !res.Stats.Detected {
		t.Fatal("Detected = false")
	}
	if res.Stats.Direct != 2 || res.Stats.Dev != 1 || res.Stats.Total != 3 {
		t.Errorf("direct=%d dev=%d total=%d, want 2/1/3", res.Stats.Direct, res.Stats.Dev, res.Stats.Total)
	}
	if !res.Stats.Locked {
		t.Error("Locked = false despite a package-lock.json")
	}
	if res.Stats.Drift {
		t.Error("Drift = true despite a lockfile")
	}
	if len(res.Stats.Ecosystems) != 1 || res.Stats.Ecosystems[0].Name != "npm" {
		t.Fatalf("ecosystems = %+v", res.Stats.Ecosystems)
	}
	e := res.Stats.Ecosystems[0]
	if e.LockfileFormat != "npm-lock-v2" {
		t.Errorf("LockfileFormat = %q, want npm-lock-v2", e.LockfileFormat)
	}
	pkgs := e.Packages
	if len(pkgs) != 2 || pkgs[0] != "alpha" || pkgs[1] != "beta" {
		t.Errorf("packages = %v, want [alpha beta]", pkgs)
	}
}

func TestAnalyzeNPMMissingLockfileDetectsDrift(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"alpha":"1"}}`)

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Stats.Locked {
		t.Error("Locked = true without a lockfile")
	}
	if !res.Stats.Drift {
		t.Error("Drift = true expected without a lockfile")
	}
	if len(res.Stats.DriftReason) != 1 {
		t.Errorf("DriftReason = %v, want one entry", res.Stats.DriftReason)
	}
	found := false
	for _, f := range res.Findings {
		if f.Title == "Missing lockfile for package.json" && f.Severity == "high" {
			found = true
		}
	}
	if !found {
		t.Error("expected a high-severity drift finding")
	}
}

// A package in both dependencies and devDependencies is counted once.
func TestAnalyzeNPMDoubleCountedDep(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json",
		`{"dependencies":{"shared":"1","runtime":"1"},"devDependencies":{"shared":"1","tooling":"1"}}`)

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Stats.Direct != 2 {
		t.Errorf("Direct = %d, want 2 (shared counted once, as runtime)", res.Stats.Direct)
	}
	if res.Stats.Dev != 1 {
		t.Errorf("Dev = %d, want 1 (shared not double counted)", res.Stats.Dev)
	}
	if res.Stats.Total != 3 {
		t.Errorf("Total = %d, want 3", res.Stats.Total)
	}
}

func TestAnalyzeGoMod(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", `module example.com/demo

go 1.22

require (
	github.com/spf13/cobra v1.8.1
	github.com/charmbracelet/lipgloss v1.0.0 // indirect
)

require golang.org/x/sys v0.19.0
`)
	writeFile(t, dir, "go.sum", `github.com/spf13/cobra v1.8.1 h1:aaa=
github.com/spf13/cobra v1.8.1/go.mod h1:bbb=
github.com/charmbracelet/lipgloss v1.0.0 h1:ccc=
golang.org/x/sys v0.19.0 h1:ddd=
`)

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	e := res.Stats.Ecosystems[0]
	if e.Name != "gomod" {
		t.Fatalf("ecosystem = %q, want gomod", e.Name)
	}
	if e.Total != 3 {
		t.Errorf("total = %d, want 3", e.Total)
	}
	if e.Direct != 2 {
		t.Errorf("direct = %d, want 2 (cobra + x/sys)", e.Direct)
	}
	if e.Indirect != 1 {
		t.Errorf("indirect = %d, want 1 (lipgloss)", e.Indirect)
	}
	if e.Lockfile != "go.sum" {
		t.Errorf("lockfile = %q, want go.sum", e.Lockfile)
	}
	// go.sum lists cobra twice (module + /go.mod hashes); distinct modules = 3.
	if e.Transitive != 3 {
		t.Errorf("transitive = %d, want 3 distinct modules", e.Transitive)
	}
	if res.Stats.Indirect != 1 {
		t.Errorf("aggregate Indirect = %d, want 1", res.Stats.Indirect)
	}
}

func TestAnalyzePyproject(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", `[project]
name = "demo"
version = "0.1.0"
dependencies = [
    "requests>=2.31",
    "flask[async]==2.3",
    "pydantic @ https://example.com/pydantic.whl",
]

[project.optional-dependencies]
dev = [
    "pytest",
    "ruff",
]

[tool.poetry.group.dev.dependencies]
mypy = "^1.8"
`)
	writeFile(t, dir, "poetry.lock", `[[package]]
name = "requests"
version = "2.31.0"

[[package]]
name = "urllib3"
version = "2.0.0"

[[package]]
name = "idna"
version = "3.6"
`)

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Stats.Ecosystems) != 1 {
		t.Fatalf("ecosystems = %+v", res.Stats.Ecosystems)
	}
	e := res.Stats.Ecosystems[0]
	if e.Name != "python" {
		t.Fatalf("name = %q, want python", e.Name)
	}
	if e.Manifest != "pyproject.toml" {
		t.Errorf("manifest = %q", e.Manifest)
	}
	// 3 direct from [project].dependencies, 2 dev from optional-dependencies,
	// 1 dev from the poetry group.
	if e.Direct != 3 {
		t.Errorf("Direct = %d, want 3", e.Direct)
	}
	if e.Dev != 3 {
		t.Errorf("Dev = %d, want 3", e.Dev)
	}
	if e.Lockfile != "poetry.lock" {
		t.Errorf("lockfile = %q, want poetry.lock", e.Lockfile)
	}
	if e.Transitive != 3 {
		t.Errorf("Transitive = %d, want 3 [[package]] blocks", e.Transitive)
	}
}

func TestAnalyzeRequirementsTxt(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "requirements.txt", `# comment line
flask==2.3.0
requests>=2.31
Django_SQLAlchemy==1.4  # inline comment
-r other.txt
-e git+https://github.com/x/y.git#egg=y
https://example.com/bare.whl
uvicorn[standard]>=0.27
`)

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	e := res.Stats.Ecosystems[0]
	if e.Name != "python" || e.Manifest != "requirements.txt" {
		t.Fatalf("ecosystem = %q manifest = %q", e.Name, e.Manifest)
	}
	// flask, requests, django-sqlalchemy, y (editable), uvicorn = 5.
	if e.Direct != 5 {
		t.Errorf("Direct = %d, want 5: %v", e.Direct, e.Packages)
	}
	// No recognized lockfile for a flat requirements.txt.
	if e.Lockfile != "" {
		t.Errorf("Lockfile = %q, want empty", e.Lockfile)
	}
	if !e.Drift {
		t.Error("Drift = true expected with no lockfile")
	}
}

func TestAnalyzeBothEcosystems(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module x\n\ngo 1.22\n")
	writeFile(t, dir, "go.sum", "")
	writeFile(t, dir, "package.json", `{"dependencies":{"a":"1"}}`)
	writeFile(t, dir, "package-lock.json", "{}")

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Stats.Ecosystems) != 2 {
		t.Fatalf("ecosystems = %d, want 2", len(res.Stats.Ecosystems))
	}
	// Sorted by name: gomod before npm.
	if res.Stats.Ecosystems[0].Name != "gomod" || res.Stats.Ecosystems[1].Name != "npm" {
		t.Errorf("ecosystems not sorted by name: %+v", res.Stats.Ecosystems)
	}
	if !res.Stats.Locked {
		t.Error("Locked = false even though both ecosystems have lockfiles")
	}
}

func TestAnalyzeNoManifest(t *testing.T) {
	res, err := Analyze(t.TempDir(), DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Stats.Detected {
		t.Error("Detected = true in an empty directory")
	}
	if res.Stats.Note == "" {
		t.Error("expected a note when no manifest is found")
	}
	if res.Stats.Ecosystems == nil {
		t.Error("Ecosystems must be non-nil so JSON emits []")
	}
}

func TestAnalyzeMalformedManifestDegradesToFinding(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", "{not valid json")

	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("a malformed manifest must not fail the scan: %v", err)
	}
	found := false
	for _, f := range res.Findings {
		if f.Title == "Unreadable package.json" {
			found = true
		}
	}
	if !found {
		t.Error("expected an unreadable-manifest finding")
	}
}

func TestAnalyzeIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"z":"1","a":"2","m":"3"}}`)
	writeFile(t, dir, "package-lock.json", "{}")

	first, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	for i := 0; i < 10; i++ {
		got, err := Analyze(dir, DefaultConfig())
		if err != nil {
			t.Fatalf("Analyze: %v", err)
		}
		if !reflect.DeepEqual(got.Stats, first.Stats) {
			t.Fatalf("run %d differs:\n%+v\n%+v", i, got.Stats, first.Stats)
		}
	}
}

func TestPackageLimit(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"a":"1","b":"2","c":"3","d":"4"}}`)

	cfg := DefaultConfig()
	cfg.PackageLimit = 2
	res, err := Analyze(dir, cfg)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if got := len(res.Stats.Ecosystems[0].Packages); got != 2 {
		t.Errorf("packages = %d, want 2", got)
	}
	// Counts must reflect the full manifest even when the list is truncated.
	if res.Stats.Direct != 4 {
		t.Errorf("Direct = %d, want 4", res.Stats.Direct)
	}
}

// A manifest newer than its lockfile is reported as a low-severity staleness
// hint. This is mtime-based, so the test sets mtimes explicitly.
func TestLockfileStalenessUsesManifestMtime(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"dependencies":{"a":"1"}}`)
	writeFile(t, dir, "package-lock.json", `{}`)
	manifest := filepath.Join(dir, "package.json")
	lock := filepath.Join(dir, "package-lock.json")

	base := time.Now()
	// Manifest older than lockfile: fresh.
	if err := os.Chtimes(manifest, base.Add(-2*time.Hour), base.Add(-2*time.Hour)); err != nil {
		t.Fatalf("chtimes manifest: %v", err)
	}
	if err := os.Chtimes(lock, base, base); err != nil {
		t.Fatalf("chtimes lock: %v", err)
	}
	res, err := Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Stats.Ecosystems[0].LockfileStale {
		t.Error("LockfileStale = true when the lockfile is newer")
	}

	// Manifest newer than lockfile: stale.
	if err := os.Chtimes(manifest, base, base); err != nil {
		t.Fatalf("chtimes manifest: %v", err)
	}
	if err := os.Chtimes(lock, base.Add(-2*time.Hour), base.Add(-2*time.Hour)); err != nil {
		t.Fatalf("chtimes lock: %v", err)
	}
	res, err = Analyze(dir, DefaultConfig())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !res.Stats.Ecosystems[0].LockfileStale {
		t.Error("LockfileStale = false when the manifest is newer")
	}
}

func TestOverlap(t *testing.T) {
	a := map[string]string{"x": "1", "y": "2"}
	b := map[string]string{"y": "3", "z": "4"}
	if got := overlap(a, b); got != 1 {
		t.Errorf("overlap = %d, want 1", got)
	}
}

func TestNormalizePythonDep(t *testing.T) {
	yes := map[string]string{
		"flask":                        "flask",
		"flask==2.3.0":                 "flask",
		"flask[async]==2.3":            "flask",
		"Django_SQLAlchemy==1.4":       "django-sqlalchemy",
		"requests @ https://x/y.whl":   "requests",
		"pkg>=1.0; python_version>'3'": "pkg",
		"  spaced  ":                   "spaced",
		"\"quoted\"":                   "quoted",
	}
	for in, want := range yes {
		got, ok := normalizePythonDep(in)
		if !ok || got != want {
			t.Errorf("normalizePythonDep(%q) = (%q, %v), want %q", in, got, ok, want)
		}
	}
	no := []string{"", "   ", "# comment", "-r other.txt", "-e ./local", "https://x.whl"}
	for _, in := range no {
		if got, ok := normalizePythonDep(in); ok {
			t.Errorf("normalizePythonDep(%q) = %q, want rejected", in, got)
		}
	}
}

func TestExtractArrayItems(t *testing.T) {
	got := extractArrayItems(`["a", "b>=1.0", 'c[extra]', "d # not comment"]`)
	if len(got) != 4 {
		t.Fatalf("items = %v, want 4", got)
	}
	if got[0] != "a" || got[1] != "b>=1.0" || got[2] != "c[extra]" {
		t.Errorf("items = %v", got)
	}
	// A '#' inside a quoted string is part of the value, not a comment.
	if got[3] != "d # not comment" {
		t.Errorf("item 3 = %q, want the hash preserved", got[3])
	}
}

func TestCountGoSumDeduplicatesVersions(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.sum", `mod/a v1.0.0 h1:x=
mod/a v1.0.0/go.mod h1:y=
mod/b v1.0.0 h1:z=
`)
	n, err := countGoSum(filepath.Join(dir, "go.sum"), DefaultConfig())
	if err != nil {
		t.Fatalf("countGoSum: %v", err)
	}
	if n != 2 {
		t.Errorf("countGoSum = %d, want 2 distinct modules", n)
	}
}

func TestCountYarnLock(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "yarn.lock", `# yarn lockfile v1

"@babel/core@^7.0.0":
  version "7.20.0"
  resolved "https://registry.yarnpkg.com/x"

lodash@^4.17.0:
  version "4.17.0"
`)
	n, err := countYarnLock(filepath.Join(dir, "yarn.lock"), DefaultConfig())
	if err != nil {
		t.Fatalf("countYarnLock: %v", err)
	}
	if n != 2 {
		t.Errorf("countYarnLock = %d, want 2", n)
	}
}

func TestCountNPMLockV2ExcludesRoot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package-lock.json", `{
  "lockfileVersion": 3,
  "packages": {
    "": {"name": "root"},
    "node_modules/a": {},
    "node_modules/b": {}
  }
}`)
	n, err := countNPMLock(filepath.Join(dir, "package-lock.json"), DefaultConfig())
	if err != nil {
		t.Fatalf("countNPMLock: %v", err)
	}
	if n != 2 {
		t.Errorf("countNPMLock = %d, want 2 (root entry excluded)", n)
	}
}

func TestCountNPMLockV1FallsBackToDependencies(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package-lock.json", `{
  "lockfileVersion": 1,
  "dependencies": {"a": {}, "b": {}, "c": {}}
}`)
	n, err := countNPMLock(filepath.Join(dir, "package-lock.json"), DefaultConfig())
	if err != nil {
		t.Fatalf("countNPMLock: %v", err)
	}
	if n != 3 {
		t.Errorf("countNPMLock = %d, want 3 from the v1 dependencies map", n)
	}
}

func TestOpenCappedRejectsOversizedFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "big.json", "0123456789")
	path := filepath.Join(dir, "big.json")
	if _, err := openCapped(path, 5); err == nil {
		t.Error("expected an error for a file over the cap")
	}
	f, err := openCapped(path, 100)
	if err != nil {
		t.Fatalf("openCapped: %v", err)
	}
	f.Close()
}
