package monorepo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

// write writes a file inside dir, creating parent directories.
func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// paths returns the detected package paths.
func paths(ws *models.Workspace) []string {
	if ws == nil {
		return nil
	}
	out := make([]string, 0, len(ws.Packages))
	for _, p := range ws.Packages {
		out = append(out, p.Path)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A plain repository is not a workspace. This is the common case and must not
// produce a report.
func TestDetectPlainRepository(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main\n")
	write(t, dir, "package.json", `{"name":"app","dependencies":{}}`)

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ws != nil {
		t.Errorf("a package.json without workspaces is not a workspace, got %+v", ws)
	}
}

func TestDetectNPMWorkspaces(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name":"root","workspaces":["apps/*","packages/*"]}`)
	write(t, dir, "apps/web/package.json", `{"name":"web"}`)
	write(t, dir, "apps/docs/package.json", `{"name":"docs"}`)
	write(t, dir, "packages/ui/package.json", `{"name":"ui"}`)

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ws == nil {
		t.Fatal("expected a workspace")
	}
	if ws.Kind != models.WorkspaceNPM {
		t.Errorf("Kind = %q, want npm", ws.Kind)
	}
	if ws.Manifest != "package.json" {
		t.Errorf("Manifest = %q", ws.Manifest)
	}
	want := []string{"apps/docs", "apps/web", "packages/ui"}
	if got := paths(ws); !equalStrings(got, want) {
		t.Errorf("packages = %v, want %v", got, want)
	}
	if ws.Packages[0].Name != "docs" {
		t.Errorf("declared name not captured: %+v", ws.Packages[0])
	}
	if ws.Packages[0].Ecosystem != "npm" {
		t.Errorf("Ecosystem = %q", ws.Packages[0].Ecosystem)
	}
}

// npm also accepts the object form of `workspaces`.
func TestDetectNPMWorkspacesObjectForm(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"workspaces":{"packages":["packages/*"],"nohoist":["**/x"]}}`)
	write(t, dir, "packages/a/package.json", `{"name":"a"}`)

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ws == nil {
		t.Fatal("the object form of workspaces must be recognized")
	}
	if got := paths(ws); !equalStrings(got, []string{"packages/a"}) {
		t.Errorf("packages = %v", got)
	}
}

func TestDetectPNPMWorkspace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pnpm-workspace.yaml", "packages:\n  - 'apps/*'\n  - 'packages/**'\n")
	write(t, dir, "apps/web/package.json", `{"name":"web"}`)
	write(t, dir, "packages/ui/package.json", `{"name":"ui"}`)
	write(t, dir, "packages/forms/select/package.json", `{"name":"select"}`)

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ws == nil || ws.Kind != models.WorkspacePNPM {
		t.Fatalf("expected a pnpm workspace, got %+v", ws)
	}
	want := []string{"apps/web", "packages/forms/select", "packages/ui"}
	if got := paths(ws); !equalStrings(got, want) {
		t.Errorf("packages = %v, want %v", got, want)
	}
}

func TestDetectLerna(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "lerna.json", `{"version":"1.0.0","packages":["modules/*"]}`)
	write(t, dir, "modules/one/package.json", `{"name":"one"}`)
	write(t, dir, "modules/two/package.json", `{"name":"two"}`)

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ws == nil || ws.Kind != models.WorkspaceLerna {
		t.Fatalf("expected a lerna workspace, got %+v", ws)
	}
	if got := paths(ws); !equalStrings(got, []string{"modules/one", "modules/two"}) {
		t.Errorf("packages = %v", got)
	}
}

func TestDetectGoWork(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.work", `go 1.22

use (
	./services/api
	./services/worker
	./tools/cmd
)
`)
	write(t, dir, "services/api/go.mod", "module example/api\n\ngo 1.22\n")
	write(t, dir, "services/worker/go.mod", "module example/worker\n\ngo 1.22\n")
	write(t, dir, "tools/cmd/go.mod", "module example/cmd\n\ngo 1.22\n")

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ws == nil || ws.Kind != models.WorkspaceGoWork {
		t.Fatalf("expected a go.work workspace, got %+v", ws)
	}
	want := []string{"services/api", "services/worker", "tools/cmd"}
	if got := paths(ws); !equalStrings(got, want) {
		t.Errorf("packages = %v, want %v", got, want)
	}
	if ws.Packages[0].Ecosystem != "gomod" {
		t.Errorf("Ecosystem = %q, want gomod", ws.Packages[0].Ecosystem)
	}
	// A Go module has no JSON name field; reporting the directory path is the
	// honest label.
	if ws.Packages[0].Name != "" {
		t.Errorf("Name = %q, want empty for a go.mod", ws.Packages[0].Name)
	}
}

// go.work also allows a single-line `use`.
func TestDetectGoWorkSingleLine(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.work", "go 1.22\n\nuse ./only\n")
	write(t, dir, "only/go.mod", "module example/only\n")

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ws == nil {
		t.Fatal("expected a workspace")
	}
	if got := paths(ws); !equalStrings(got, []string{"only"}) {
		t.Errorf("packages = %v", got)
	}
}

func TestDetectCargoWorkspace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Cargo.toml", "[workspace]\nmembers = [\n  \"crates/core\",\n  \"crates/cli\",\n]\n\n[package]\nname = \"root\"\n")
	write(t, dir, "crates/core/Cargo.toml", "[package]\nname = \"core\"\n")
	write(t, dir, "crates/cli/Cargo.toml", "[package]\nname = \"cli\"\n")

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ws == nil || ws.Kind != models.WorkspaceCargo {
		t.Fatalf("expected a cargo workspace, got %+v", ws)
	}
	if got := paths(ws); !equalStrings(got, []string{"crates/cli", "crates/core"}) {
		t.Errorf("packages = %v", got)
	}
}

// A Cargo.toml without [workspace] is a single package, not a workspace.
func TestCargoWithoutWorkspaceTableIsNotAWorkspace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "Cargo.toml", "[package]\nname = \"solo\"\n")
	write(t, dir, "src/main.rs", "fn main() {}\n")

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ws != nil {
		t.Errorf("a single Cargo package is not a workspace, got %+v", ws)
	}
}

// A declared pattern that matches nothing with a manifest is not a package.
func TestDirectoriesWithoutManifestsAreNotPackages(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"workspaces":["apps/*"]}`)
	write(t, dir, "apps/web/package.json", `{"name":"web"}`)
	write(t, dir, "apps/notes/readme.md", "no manifest here")

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got := paths(ws); !equalStrings(got, []string{"apps/web"}) {
		t.Errorf("a directory with no manifest must not be a package, got %v", got)
	}
}

// node_modules is never descended into, and it must never be reported.
func TestDependencyDirectoriesAreNeverPackages(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"workspaces":["**"]}`)
	write(t, dir, "apps/web/package.json", `{"name":"web"}`)
	write(t, dir, "node_modules/left-pad/package.json", `{"name":"left-pad"}`)
	write(t, dir, "apps/web/node_modules/dep/package.json", `{"name":"dep"}`)

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if got := paths(ws); !equalStrings(got, []string{"apps/web"}) {
		t.Errorf("vendored packages must not be reported, got %v", got)
	}
}

// A negated pattern excludes a package the workspace opted out of, and a
// pattern escaping the root is not a package of this repository.
func TestNegatedAndEscapingPatterns(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"workspaces":["../outside","apps/*","!apps/legacy"]}`)
	write(t, dir, "apps/web/package.json", `{"name":"web"}`)
	write(t, dir, "apps/legacy/package.json", `{"name":"legacy"}`)
	write(t, dir, "apps/legacy/nested/package.json", `{"name":"nested"}`)

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	// apps/legacy and everything under it are excluded; apps/web is not.
	if got := paths(ws); !equalStrings(got, []string{"apps/web"}) {
		t.Errorf("packages = %v, want [apps/web]", got)
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern, dir string
		want         bool
	}{
		{"apps/legacy", "apps/legacy", true},
		{"apps/*", "apps/web", true},
		{"apps/*", "apps/web/src", false},
		{"packages/**", "packages/ui", true},
		{"packages/**", "packages/forms/select", true},
		{"packages/**", "apps/ui", false},
		{"apps/legacy/**", "apps/legacy", true},
		{"apps/legacy/**", "apps/legacy/sub", true},
		{"apps/legacy/**", "apps/legacyish", false},
	}
	for _, c := range cases {
		if got := matchPattern(c.pattern, c.dir); got != c.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", c.pattern, c.dir, got, c.want)
		}
	}
}

// A malformed workspace manifest must not be guessed around.
func TestMalformedWorkspaceFileIsNotAWorkspace(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "pnpm-workspace.yaml", "packages:\n  - 'apps/*'\n   bad indentation: [")
	write(t, dir, "apps/web/package.json", `{"name":"web"}`)

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect must not fail on a malformed manifest: %v", err)
	}
	if ws != nil {
		t.Errorf("a malformed workspace file must not be interpreted, got %+v", ws)
	}
}

func TestDetectIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"workspaces":["packages/*","apps/*"]}`)
	for _, p := range []string{"packages/c", "packages/a", "packages/b", "apps/z", "apps/y"} {
		write(t, dir, p+"/package.json", `{"name":"`+p+`"}`)
	}

	first, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, err := Detect(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !equalStrings(paths(got), paths(first)) {
			t.Fatalf("run %d differs: %v vs %v", i, paths(got), paths(first))
		}
	}
}

func TestDetectMissingDirectoryErrors(t *testing.T) {
	if _, err := Detect(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a missing directory must be reported")
	}
}

func TestMatchSegment(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"*", "anything", true},
		{"*", "", true},
		{"web", "web", true},
		{"web", "webs", false},
		{"w*b", "web", true},
		{"w*b", "wb", true},
		{"w*b", "wxxxb", true},
		{"w*b", "bx", false},
		{"?eb", "web", true},
		{"?eb", "wb", false},
		{"*a*b*", "xxaxxbxx", true},
		{"*.go", "main.go", true},
		{"*.go", "main.g", false},
	}
	for _, c := range cases {
		if got := matchSegment(c.pattern, c.name); got != c.want {
			t.Errorf("matchSegment(%q, %q) = %v, want %v", c.pattern, c.name, got, c.want)
		}
	}
}

// A pathological pattern must not hang the walk. The iterative matcher is
// linear where a naive recursive one would blow up exponentially.
func TestMatchSegmentIsLinear(t *testing.T) {
	pattern := ""
	name := ""
	for i := 0; i < 40; i++ {
		pattern += "*a"
		name += "a"
	}
	// If this returns, it returned in polynomial time rather than hanging.
	_ = matchSegment(pattern, name+"b")
}

func TestInsidePackageBoundary(t *testing.T) {
	cases := []struct {
		file, pkg string
		want      bool
	}{
		{"apps/web/src/a.ts", "apps/web", true},
		{"apps/web", "apps/web", true},
		{"apps/web-legacy/src/a.ts", "apps/web", false},
		{"apps/webby/a.ts", "apps/web", false},
		{"apps/docs/a.ts", "apps/web", false},
		{"a.ts", "", false},
	}
	for _, c := range cases {
		if got := insidePackage(c.file, c.pkg); got != c.want {
			t.Errorf("insidePackage(%q, %q) = %v, want %v", c.file, c.pkg, got, c.want)
		}
	}
}

func TestLooksLikePath(t *testing.T) {
	for _, s := range []string{"a/b.go", "internal/x/y.ts", "main.go", "src/x.py", "Cargo.toml"} {
		if !looksLikePath(s) {
			t.Errorf("%q should be recognized as a path", s)
		}
	}
	// A module or package name is not a path, and treating it as one would
	// misattribute repository-wide risks to whichever package shares a prefix.
	for _, s := range []string{
		"react", "@scope/pkg", "github.com/spf13/cobra", "",
		"/abs/path.go", "C:/x/y.go", `C:\x\y.go`, "https://example.com/a.go",
	} {
		if looksLikePath(s) {
			t.Errorf("%q should not be recognized as a repo-relative file path", s)
		}
	}
}

func TestDedupeSorted(t *testing.T) {
	if got := dedupeSorted(nil); got != nil {
		t.Errorf("nil input must stay nil, got %v", got)
	}
	got := dedupeSorted([]string{"b", "a", "b", "c", "a"})
	want := []string{"a", "b", "c"}
	if !equalStrings(got, want) {
		t.Errorf("dedupeSorted = %v, want %v", got, want)
	}
}

func TestWorkspaceDetected(t *testing.T) {
	var nilWS *models.Workspace
	if nilWS.Detected() {
		t.Error("a nil workspace is not detected")
	}
	if (&models.Workspace{}).Detected() {
		t.Error("an empty workspace is not detected")
	}
	// A declared layout with no resolved packages is not a usable workspace.
	if (&models.Workspace{Kind: models.WorkspaceNPM}).Detected() {
		t.Error("a layout with no packages is not a usable workspace")
	}
	if !(&models.Workspace{Kind: models.WorkspaceNPM, Packages: []models.PackageHealth{{Path: "a"}}}).Detected() {
		t.Error("a layout with packages is detected")
	}
}
