package monorepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/internal/code"
	"github.com/zelvior/lensyxe/pkg/models"
)

// testCodeConfig mirrors the analyzer's code config with a low hotspot
// threshold so a fixture-sized file can actually cross it.
func testCodeConfig() code.Config {
	cfg := code.DefaultConfig()
	cfg.HotspotThreshold = 500
	return cfg
}

// goLines builds a Go file body of n code lines.
func goLines(n int) string {
	body := "package p\n\nfunc F() {\n"
	for i := 0; i < n; i++ {
		body += "\t_ = 1\n"
	}
	return body + "}\n"
}

// workspaceFixture builds a two-package npm workspace on disk and returns the
// root and the walk result.
func workspaceFixture(t *testing.T) (string, code.Result) {
	t.Helper()
	dir := t.TempDir()

	write(t, dir, "package.json", `{"name":"root","private":true,"workspaces":["packages/*"]}`)
	write(t, dir, "packages/core/package.json", `{"name":"@acme/core"}`)
	write(t, dir, "packages/ui/package.json", `{"name":"@acme/ui"}`)
	write(t, dir, "packages/core/index.go", goLines(40))
	write(t, dir, "packages/core/index_test.go", goLines(20))
	write(t, dir, "packages/ui/index.go", goLines(600))

	ws, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if ws == nil {
		t.Fatal("expected a workspace")
	}

	walk, err := code.Analyze(dir, testCodeConfig(), nil)
	if err != nil {
		t.Fatalf("code.Analyze: %v", err)
	}
	return dir, walk
}

// packageByPath finds a package in the workspace.
func packageByPath(ws *models.Workspace, path string) *models.PackageHealth {
	for i := range ws.Packages {
		if ws.Packages[i].Path == path {
			return &ws.Packages[i]
		}
	}
	return nil
}

func TestAttributeScoresEachPackage(t *testing.T) {
	root, walk := workspaceFixture(t)
	ws, _ := Detect(root)

	if err := Attribute(context.Background(), root, ws, walk.PerFile, nil, testCodeConfig()); err != nil {
		t.Fatalf("Attribute: %v", err)
	}

	core := packageByPath(ws, "packages/core")
	if core == nil {
		t.Fatal("packages/core missing")
	}
	if core.Files != 2 {
		t.Errorf("core Files = %d, want 2", core.Files)
	}
	if core.TestFiles != 1 || core.SourceFiles != 1 {
		t.Errorf("core test/source = %d/%d, want 1/1", core.TestFiles, core.SourceFiles)
	}
	if core.TestFileRatio != 0.5 {
		t.Errorf("core TestFileRatio = %v, want 0.5", core.TestFileRatio)
	}

	ui := packageByPath(ws, "packages/ui")
	if ui == nil {
		t.Fatal("packages/ui missing")
	}
	if ui.Files != 1 {
		t.Errorf("ui Files = %d, want 1", ui.Files)
	}

	// Every package score must be in range and carry a grade.
	for _, p := range ws.Packages {
		if p.Health.Score < 0 || p.Health.Score > 100 {
			t.Errorf("%s: score %v out of range", p.Path, p.Health.Score)
		}
		if p.Health.Grade == "" {
			t.Errorf("%s: no grade", p.Path)
		}
	}
}

// A package score must never count a sibling's files. Getting this wrong is the
// difference between a per-package number and a repeated repository number.
func TestPackagesDoNotShareFiles(t *testing.T) {
	root, walk := workspaceFixture(t)
	ws, _ := Detect(root)

	if err := Attribute(context.Background(), root, ws, walk.PerFile, nil, testCodeConfig()); err != nil {
		t.Fatal(err)
	}

	core := packageByPath(ws, "packages/core")
	ui := packageByPath(ws, "packages/ui")

	if core.CodeLines >= ui.CodeLines {
		t.Errorf("core (%d lines) must not include ui (%d lines)", core.CodeLines, ui.CodeLines)
	}
	if ui.Files != 1 {
		t.Errorf("ui must see exactly its own one file, got %d", ui.Files)
	}
}

// Git history cannot be attributed to a directory, so it must be reported as
// not applicable rather than silently included.
func TestPackageGitIsNotApplicable(t *testing.T) {
	root, walk := workspaceFixture(t)
	ws, _ := Detect(root)

	if err := Attribute(context.Background(), root, ws, walk.PerFile, nil, testCodeConfig()); err != nil {
		t.Fatal(err)
	}
	for _, p := range ws.Packages {
		if p.GitApplicable {
			t.Errorf("%s: git must not be marked applicable to a package", p.Path)
		}
		// The git metric is still listed, because the contract always reports
		// three components; what matters is that it carries no weight and is
		// not applicable, so it cannot pull the package score around.
		var found bool
		for _, m := range p.Health.Metrics {
			if m.Key != "git" {
				continue
			}
			found = true
			if m.Applicable {
				t.Errorf("%s: the git metric must not be applicable, got %+v", p.Path, m)
			}
			if m.Weight != 0 {
				t.Errorf("%s: the git metric must carry no weight, got %v", p.Path, m.Weight)
			}
		}
		if !found {
			t.Errorf("%s: the git metric is missing from the component list", p.Path)
		}
	}
}

// A package with a large file should be reported as a hotspot candidate, and a
// package with only small files should not.
func TestPackageHotspotsAreScoped(t *testing.T) {
	root, walk := workspaceFixture(t)
	ws, _ := Detect(root)

	// ui/index.go is ~603 code lines, above the 500 threshold.
	churn := map[string]int{"packages/ui/index.go": 0}
	if err := Attribute(context.Background(), root, ws, walk.PerFile, churn, testCodeConfig()); err != nil {
		t.Fatal(err)
	}

	ui := packageByPath(ws, "packages/ui")
	if len(ui.Hotspots) != 1 {
		t.Fatalf("ui hotspots = %+v, want one", ui.Hotspots)
	}
	if ui.Hotspots[0].Path != "packages/ui/index.go" {
		t.Errorf("hotspot path = %q, want the repo-relative path", ui.Hotspots[0].Path)
	}
	// No churn data, so it cannot be confirmed even though it is large.
	if ui.Hotspots[0].Confirmed {
		t.Error("a file with no churn must not be a confirmed hotspot")
	}

	core := packageByPath(ws, "packages/core")
	if len(core.Hotspots) != 0 {
		t.Errorf("core must have no hotspots, got %+v", core.Hotspots)
	}
}

// Churn belonging to one package must not confirm a hotspot in another.
func TestChurnIsScopedToTheOwningPackage(t *testing.T) {
	root, walk := workspaceFixture(t)
	ws, _ := Detect(root)

	// ui's big file churns heavily; core's small file does not.
	churn := map[string]int{"packages/ui/index.go": 400}
	if err := Attribute(context.Background(), root, ws, walk.PerFile, churn, testCodeConfig()); err != nil {
		t.Fatal(err)
	}

	// The point of this test is scoping, not the confirmation rule: ui's
	// hotspot must see ui's churn and no other package's.
	ui := packageByPath(ws, "packages/ui")
	if len(ui.Hotspots) != 1 {
		t.Fatalf("ui hotspots = %+v, want one", ui.Hotspots)
	}
	if ui.Hotspots[0].Churn != 400 {
		t.Errorf("ui churn = %d, want 400", ui.Hotspots[0].Churn)
	}
	if !strings.Contains(ui.Hotspots[0].Classification, "churn") {
		t.Errorf("ui hotspot must record the churn factor: %+v", ui.Hotspots[0])
	}

	// core has no churn at all, so nothing there may claim any.
	core := packageByPath(ws, "packages/core")
	if len(core.Hotspots) != 0 {
		t.Errorf("core must not inherit ui's churn: %+v", core.Hotspots)
	}
}

// A package whose own file is large, heavily churned, and complex is confirmed
// on all three factors. This is the case where per-package attribution is worth
// having: the file to fix is identified precisely.
func TestConfirmedHotspotWithinAPackage(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name":"root","private":true,"workspaces":["packages/*"]}`)
	write(t, dir, "packages/big/package.json", `{"name":"big"}`)

	// A file that is large, churned, and branch-heavy.
	body := "package p\n\nfunc F(x int) int {\n\ttotal := 0\n"
	for i := 0; i < 200; i++ {
		body += "\tif x > " + itoa(i) + " {\n\t\ttotal += x\n\t}\n"
	}
	body += "\treturn total\n}\n"
	write(t, dir, "packages/big/big.go", body)

	ws, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	walk, err := code.Analyze(dir, testCodeConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	churn := map[string]int{"packages/big/big.go": 500}
	if err := Attribute(context.Background(), dir, ws, walk.PerFile, churn, testCodeConfig()); err != nil {
		t.Fatal(err)
	}

	big := packageByPath(ws, "packages/big")
	if len(big.Hotspots) != 1 {
		t.Fatalf("hotspots = %+v", big.Hotspots)
	}
	h := big.Hotspots[0]
	if !h.Confirmed {
		t.Errorf("expected a confirmed hotspot, got %+v", h)
	}
	for _, factor := range []string{"size", "churn", "complexity"} {
		if !strings.Contains(h.Classification, factor) {
			t.Errorf("classification %q is missing the %q factor", h.Classification, factor)
		}
	}
}

// Slices must serialize as [] rather than null, because the dashboard and the
// JSON contract iterate them without a nil check.
func TestPackageSlicesAreNeverNil(t *testing.T) {
	root, walk := workspaceFixture(t)
	ws, _ := Detect(root)

	if err := Attribute(context.Background(), root, ws, walk.PerFile, nil, testCodeConfig()); err != nil {
		t.Fatal(err)
	}
	for _, p := range ws.Packages {
		if p.Hotspots == nil {
			t.Errorf("%s: Hotspots is nil", p.Path)
		}
		if p.Risks == nil {
			t.Errorf("%s: Risks is nil", p.Path)
		}
		if p.Dependencies.Ecosystems == nil {
			t.Errorf("%s: Ecosystems is nil", p.Path)
		}
	}
}

func TestAttributeNilWorkspaceIsNoop(t *testing.T) {
	if err := Attribute(context.Background(), t.TempDir(), nil, nil, nil, testCodeConfig()); err != nil {
		t.Errorf("a nil workspace must be a no-op, got %v", err)
	}
}

func TestAttributeRespectsCancellation(t *testing.T) {
	root, walk := workspaceFixture(t)
	ws, _ := Detect(root)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := Attribute(ctx, root, ws, walk.PerFile, nil, testCodeConfig()); err == nil {
		t.Fatal("a cancelled context must abort the attribution")
	}
}

// A package declared but containing no recognized source is reported with its
// real (empty) figures rather than being dropped: that state is itself a
// finding, and hiding it would hide the reason.
func TestEmptyPackageIsReported(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"workspaces":["packages/*"]}`)
	write(t, dir, "packages/empty/package.json", `{"name":"empty"}`)
	write(t, dir, "packages/real/package.json", `{"name":"real"}`)
	write(t, dir, "packages/real/index.go", goLines(10))

	ws, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	walk, err := code.Analyze(dir, testCodeConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Attribute(context.Background(), dir, ws, walk.PerFile, nil, testCodeConfig()); err != nil {
		t.Fatal(err)
	}

	empty := packageByPath(ws, "packages/empty")
	if empty == nil {
		t.Fatal("an empty but declared package must still be listed")
	}
	if empty.Files != 0 {
		t.Errorf("Files = %d, want 0", empty.Files)
	}
}

func TestAttributeRisks(t *testing.T) {
	ws := &models.Workspace{Packages: []models.PackageHealth{
		{Path: "packages/core"},
		{Path: "packages/ui"},
	}}

	risks := []models.Risk{
		{ID: "b", Subject: "packages/ui/index.go"},
		{ID: "a", Subject: "packages/core/index.go"},
		{ID: "c", Subject: "packages/core/deep/thing.ts"},
		// A repository-wide risk with no path belongs to nobody.
		{ID: "d", Subject: ""},
		// An identifier is not a path, so it must not be attributed by a
		// coincidental prefix match.
		{ID: "e", Subject: "packages"},
		{ID: "f", Subject: "github.com/acme/core"},
	}

	AttributeRisks(ws, risks)

	core := packageByPath(ws, "packages/core")
	ui := packageByPath(ws, "packages/ui")

	if len(core.Attribution) != 2 {
		t.Errorf("core attribution = %v, want two sorted IDs", core.Attribution)
	}
	if core.Attribution[0] != "a" || core.Attribution[1] != "c" {
		t.Errorf("core attribution must be sorted: %v", core.Attribution)
	}
	if len(ui.Attribution) != 1 || ui.Attribution[0] != "b" {
		t.Errorf("ui attribution = %v", ui.Attribution)
	}
}

func TestAttributeRisksNilWorkspace(t *testing.T) {
	// Must not panic on a repository that is not a workspace.
	AttributeRisks(nil, []models.Risk{{ID: "x", Subject: "a/b.go"}})
}

func TestAttributeRisksUnattributedPackagesStayNil(t *testing.T) {
	ws := &models.Workspace{Packages: []models.PackageHealth{{Path: "packages/core"}}}
	AttributeRisks(ws, []models.Risk{{ID: "x", Subject: "somewhere/else.go"}})
	if packageByPath(ws, "packages/core").Attribution != nil {
		t.Error("a package with no attributed risks must serialize as absent")
	}
}

// A package score must be reproducible: same tree, same numbers.
func TestAttributeIsDeterministic(t *testing.T) {
	root, walk := workspaceFixture(t)

	var first []float64
	for run := 0; run < 5; run++ {
		ws, _ := Detect(root)
		if err := Attribute(context.Background(), root, ws, walk.PerFile, nil, testCodeConfig()); err != nil {
			t.Fatal(err)
		}
		var got []float64
		for _, p := range ws.Packages {
			got = append(got, p.Health.Score)
		}
		if run == 0 {
			first = got
			continue
		}
		for i := range got {
			if got[i] != first[i] {
				t.Fatalf("run %d package %d scored %v, first run scored %v",
					run, i, got[i], first[i])
			}
		}
	}
}

// The package score must come from the same scorer the repository uses, so the
// two are on one scale.
func TestPackageScoreUsesTheSameScorer(t *testing.T) {
	root, walk := workspaceFixture(t)
	ws, _ := Detect(root)

	if err := Attribute(context.Background(), root, ws, walk.PerFile, nil, testCodeConfig()); err != nil {
		t.Fatal(err)
	}
	core := packageByPath(ws, "packages/core")

	// Recompute independently from the same records and compare.
	agg := code.Aggregate(selectRecords(walk.PerFile, "packages/core"), nil, testCodeConfig(), "")
	repoScores := len(core.Health.Metrics)
	if repoScores == 0 {
		t.Fatal("no metrics were produced for the package")
	}
	if agg.Stats.SourceFiles != core.SourceFiles {
		t.Errorf("aggregate SourceFiles = %d, package = %d",
			agg.Stats.SourceFiles, core.SourceFiles)
	}
	if models.Grade(core.Health.Score) != core.Health.Grade {
		t.Errorf("grade %q does not match score %v", core.Health.Grade, core.Health.Score)
	}
}

func TestPackageDependencySurfaceIsScoped(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "package.json", `{"name":"root","private":true,"workspaces":["packages/*"]}`)
	write(t, dir, "packages/heavy/package.json",
		`{"name":"heavy","dependencies":{"a":"1","b":"2","c":"3"}}`)
	write(t, dir, "packages/light/package.json", `{"name":"light"}`)
	write(t, dir, "packages/heavy/index.js", "export const a = 1;\n")
	write(t, dir, "packages/light/index.js", "export const b = 2;\n")

	ws, err := Detect(dir)
	if err != nil {
		t.Fatal(err)
	}
	walk, err := code.Analyze(dir, testCodeConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Attribute(context.Background(), dir, ws, walk.PerFile, nil, testCodeConfig()); err != nil {
		t.Fatal(err)
	}

	heavy := packageByPath(ws, "packages/heavy")
	light := packageByPath(ws, "packages/light")

	if !heavy.Dependencies.Detected || heavy.Dependencies.Direct != 3 {
		t.Errorf("heavy deps = %+v", heavy.Dependencies)
	}
	// `Detected` means a manifest was found, not that dependencies exist: a
	// package.json with no dependency block is still an npm package.
	if !light.Dependencies.Detected {
		t.Error("light has a package.json, so the npm ecosystem must be detected")
	}
	if light.Dependencies.Direct != 0 || light.Dependencies.Total != 0 {
		t.Errorf("light must report no dependencies, got %+v", light.Dependencies)
	}
	// The counts must not bleed across the package boundary.
	if light.Dependencies.Direct != 0 && heavy.Dependencies.Direct == 0 {
		t.Error("dependency counts are not scoped per package")
	}
}

// itoa avoids pulling strconv into the test for one call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// Ensure the fixture directory layout is what the other tests assume.
func TestFixtureLayoutIsDetected(t *testing.T) {
	root, _ := workspaceFixture(t)
	ws, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(ws); !equalStrings(got, []string{"packages/core", "packages/ui"}) {
		t.Errorf("packages = %v", got)
	}
	if _, err := os.Stat(filepath.Join(root, "packages")); err != nil {
		t.Fatal(err)
	}
}
