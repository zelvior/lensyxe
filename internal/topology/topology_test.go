package topology

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/internal/gitlog"
)

var refNow = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// ---- boundary inference ----

func TestBoundaryOfInfersRegions(t *testing.T) {
	cases := map[string]string{
		"internal/git/analyzer.go":   "internal",
		"pkg/models/models.go":       "pkg",
		"cmd/lensyxe/main.go":        "cmd",
		"apps/web/app.ts":            "apps",
		"services/api/main.go":       "services",
		"dashboard/src/app/page.tsx": "dashboard",
		"docs/CLI_REFERENCE.md":      "docs",
		"main.go":                    "",
	}
	for path, want := range cases {
		if got := boundaryOf(path); got != want {
			t.Errorf("boundaryOf(%q) = %q, want %q", path, got, want)
		}
	}
}

// A deep tree must not create one boundary per package; the top level defines the
// region or a repository acquires hundreds of them.
func TestBoundaryOfCollapsesToTopLevel(t *testing.T) {
	if got := boundaryOf("internal/git/deep/nested/file.go"); got != "internal" {
		t.Errorf("got %q, want internal", got)
	}
}

// A real repository with no cmd-to-library import must report no violation. A
// false positive here would send someone refactoring correct code.
func TestGraphOnACleanRepositoryFindsNoCmdImport(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"cmd/tool/main.go": "package main\n\nimport \"example.com/m/internal/lib\"\n\n" +
			"func main() { lib.Run() }\n",
		"internal/lib/lib.go": "package lib\n\nfunc Run() {}\n",
	})

	g, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	for _, v := range g.Violations {
		if v.Kind == ViolationCmdImported {
			t.Errorf("a correct cmd -> internal import was flagged: %+v", v)
		}
	}
	if g.Module != "example.com/m" {
		t.Errorf("module = %q, want example.com/m", g.Module)
	}
}

// The dependency direction rule: nothing below cmd/ may import cmd/.
func TestLibraryImportingCmdIsAViolation(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod":           "module example.com/m\n\ngo 1.22\n",
		"cmd/tool/main.go": "package main\n\nfunc main() {}\n",
		"internal/lib/lib.go": "package lib\n\nimport \"example.com/m/cmd/tool\"\n\n" +
			"var _ = tool.X\n",
	})

	g, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	found := false
	for _, v := range g.Violations {
		if v.Kind == ViolationCmdImported {
			found = true
		}
	}
	if !found {
		t.Errorf("a library importing cmd/ was not flagged: %+v", g.Violations)
	}
}

func TestImportCycleIsDetectedOnce(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"internal/a/a.go": "package a\n\nimport \"example.com/m/internal/b\"\n\n" +
			"var _ = b.X\n",
		"internal/b/b.go": "package b\n\nimport \"example.com/m/internal/a\"\n\n" +
			"var _ = a.X\n",
	})

	g, err := BuildGraph(dir)
	if err != nil {
		t.Fatalf("BuildGraph: %v", err)
	}
	cycles := 0
	for _, v := range g.Violations {
		if v.Kind == ViolationCycle {
			cycles++
		}
	}
	if cycles != 1 {
		t.Errorf("got %d cycle violations, want 1: %+v", cycles, g.Violations)
	}
}

func TestAcyclicGraphReportsNoCycle(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"internal/a/a.go": "package a\n\nimport \"example.com/m/internal/b\"\n\n" +
			"var _ = b.X\n",
		"internal/b/b.go": "package b\n\nimport \"example.com/m/internal/c\"\n\n" +
			"var _ = c.X\n",
		"internal/c/c.go": "package c\n\nvar X = 1\n",
	})

	g, _ := BuildGraph(dir)
	for _, v := range g.Violations {
		if v.Kind == ViolationCycle {
			t.Errorf("an acyclic graph reported a cycle: %+v", v)
		}
	}
}

// Boundaries must report their real dependencies.
func TestBoundariesRecordDependencies(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.22\n",
		"cmd/tool/main.go": "package main\n\nimport \"example.com/m/internal/lib\"\n\n" +
			"func main() { lib.Run() }\n",
		"internal/lib/lib.go": "package lib\n\nfunc Run() {}\n",
	})

	g, _ := BuildGraph(dir)
	byName := map[string]Boundary{}
	for _, b := range g.Boundaries {
		byName[b.Name] = b
	}
	cmdB, ok := byName["cmd"]
	if !ok {
		t.Fatalf("no cmd boundary: %+v", g.Boundaries)
	}
	if len(cmdB.DependsOn) != 1 || cmdB.DependsOn[0] != "internal" {
		t.Errorf("cmd depends on %v, want [internal]", cmdB.DependsOn)
	}
	libB := byName["internal"]
	if len(libB.DependsOn) != 0 {
		t.Errorf("internal depends on %v, want nothing", libB.DependsOn)
	}
	if libB.Files != 1 || libB.Packages != 1 {
		t.Errorf("internal counts = %d files / %d packages, want 1/1",
			libB.Files, libB.Packages)
	}
}

// Vendored and generated trees must not be parsed: they would contribute
// thousands of packages that are not this repository's architecture.
func TestVendorDirectoriesArePruned(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"go.mod":              "module example.com/m\n\ngo 1.22\n",
		"internal/lib/lib.go": "package lib\n\nfunc Run() {}\n",
		"vendor/dep/dep.go":   "package dep\n\nimport \"example.com/m/cmd/tool\"\n\nvar _ = tool.X\n",
		"cmd/tool/main.go":    "package main\n\nfunc main() {}\n",
	})

	g, _ := BuildGraph(dir)
	for _, v := range g.Violations {
		if strings.Contains(v.Package, "vendor") {
			t.Errorf("a vendored package was audited: %+v", v)
		}
	}
	if g.Files != 2 {
		t.Errorf("parsed %d files, want 2 (vendor pruned)", g.Files)
	}
}

func TestBoundarySeverityRanksCyclesFirst(t *testing.T) {
	// Lower is more severe.
	if boundarySeverity(ViolationCycle) >= boundarySeverity(ViolationCmdImported) {
		t.Error("a cycle must outrank a cmd import")
	}
	if boundarySeverity(ViolationRootImport) <= boundarySeverity(ViolationCmdImported) {
		t.Error("a root import must rank below a cmd import")
	}
}

// ---- bus factor ----

// commit builds a synthetic commit.
func commit(days int, author string, files ...string) gitlog.Commit {
	return gitlog.Commit{
		Author: author, When: refNow.AddDate(0, 0, -days), Files: files,
	}
}

func TestDecayWeightIsExponentialAndNeverZero(t *testing.T) {
	// A commit made today weighs exactly 1.
	if got := DecayWeight(refNow, refNow, 90); got != 1 {
		t.Errorf("weight of a commit made now = %v, want 1", got)
	}
	// One half-life ago weighs one half.
	half := DecayWeight(refNow.AddDate(0, 0, -90), refNow, 90)
	if half < 0.49 || half > 0.51 {
		t.Errorf("weight at one half-life = %v, want ~0.5", half)
	}
	// Old work stays visible: the weight decays but never reaches zero.
	old := DecayWeight(refNow.AddDate(-1, 0, 0), refNow, 90)
	if old <= 0 {
		t.Errorf("weight a year old = %v, want a small positive number", old)
	}
	if old >= half {
		t.Error("an older commit did not decay further than a younger one")
	}
}

// The recency decay is the point: a long-ago owner must not outrank the person
// working on the file now.
func TestRecencyDecayShiftsOwnershipToTheRecentAuthor(t *testing.T) {
	commits := []gitlog.Commit{
		// The founder wrote it nine times, a year ago.
		commit(360, "founder", "f.go"),
		commit(355, "founder", "f.go"),
		commit(350, "founder", "f.go"),
		commit(345, "founder", "f.go"),
		commit(340, "founder", "f.go"),
		commit(335, "founder", "f.go"),
		commit(330, "founder", "f.go"),
		commit(325, "founder", "f.go"),
		commit(320, "founder", "f.go"),
		// The current maintainer has touched it once, recently.
		commit(5, "current", "f.go"),
	}

	res := AggregateBusFactor("/r", DefaultBusFactorConfig(), commits, refNow)
	if len(res.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(res.Files))
	}
	f := res.Files[0]
	if f.Dominant.Author != "current" {
		t.Errorf("dominant author = %q, want current: "+
			"one commit this week outranks nine from a year ago under a 90-day "+
			"half-life (shares: %+v)", f.Dominant.Author, f.Authors)
	}
	// The raw share still shows the founder's volume, which is the point of
	// reporting both.
	if len(f.Authors) != 2 {
		t.Fatalf("authors = %+v, want 2", f.Authors)
	}
	if f.Authors[1].Author != "founder" {
		t.Errorf("second author = %q, want founder", f.Authors[1].Author)
	}
}

// Both conditions are required: dominant AND stale. A single-author file that is
// actively maintained is not at risk.
func TestAtRiskRequiresDominanceAndStaleness(t *testing.T) {
	cfg := DefaultBusFactorConfig()

	// Dominant and stale: at risk.
	commits := []gitlog.Commit{
		commit(300, "gone", "a.go"),
		commit(295, "gone", "a.go"),
		commit(290, "gone", "a.go"),
		commit(285, "gone", "a.go"),
	}
	res := AggregateBusFactor("/r", cfg, commits, refNow)
	if !res.Files[0].AtRisk {
		t.Errorf("a dominant author who left was not flagged: %+v", res.Files[0])
	}
	if res.Files[0].RiskReason == "" {
		t.Error("a flagged file gave no reason")
	}
	if !strings.Contains(res.Files[0].RiskReason, "gone") {
		t.Errorf("the reason does not name the author: %q", res.Files[0].RiskReason)
	}

	// Dominant but active: not at risk.
	commits = []gitlog.Commit{
		commit(300, "active", "b.go"),
		commit(10, "active", "b.go"),
		commit(5, "active", "b.go"),
		commit(1, "active", "b.go"),
	}
	res = AggregateBusFactor("/r", cfg, commits, refNow)
	if res.Files[0].AtRisk {
		t.Errorf("an actively maintained single-author file was flagged: %+v",
			res.Files[0].RiskReason)
	}
}

func TestSharedFileIsNotAtRisk(t *testing.T) {
	cfg := DefaultBusFactorConfig()
	commits := []gitlog.Commit{
		commit(300, "ann", "c.go"),
		commit(295, "bob", "c.go"),
		commit(290, "cy", "c.go"),
		commit(285, "ann", "c.go"),
	}
	res := AggregateBusFactor("/r", cfg, commits, refNow)
	if res.Files[0].AtRisk {
		t.Errorf("a file with three contributors was flagged: %+v", res.Files[0].RiskReason)
	}
	if res.Files[0].BusFactor > 2 {
		t.Errorf("bus factor = %d, want 2", res.Files[0].BusFactor)
	}
}

// The staleness verdict needs history to be meaningful. This repository's own
// history is a day long, so the gate must fire and withhold the verdict while
// still reporting the shares.
func TestStalenessVerdictIsWithheldOnShortHistory(t *testing.T) {
	commits := []gitlog.Commit{
		commit(1, "ann", "a.go"),
		commit(1, "ann", "a.go"),
		commit(0, "ann", "a.go"),
	}
	res := AggregateBusFactor("/r", DefaultBusFactorConfig(), commits, refNow)

	if res.InsufficientHistory == "" {
		t.Fatal("a one-day history offered a staleness verdict")
	}
	if !strings.Contains(res.InsufficientHistory, "limit of the data") {
		t.Errorf("the gate does not distinguish a data limit from a finding: %q",
			res.InsufficientHistory)
	}
	for _, f := range res.Files {
		if f.AtRisk {
			t.Error("a file was flagged despite the gate")
		}
	}
	// The shares must still be reported: withholding a verdict is not a reason to
	// withhold what can be measured.
	if len(res.Files[0].Authors) == 0 {
		t.Error("ownership shares were suppressed by the gate")
	}
}

func TestBusFactorCountsAuthorsToThreshold(t *testing.T) {
	cfg := DefaultBusFactorConfig()
	// Five authors contributing roughly equally: several are needed to reach
	// 70%.
	var commits []gitlog.Commit
	names := []string{"a", "b", "c", "d", "e"}
	for i := 0; i < 10; i++ {
		commits = append(commits, commit(200-i, names[i%len(names)], "f.go"))
	}
	res := AggregateBusFactor("/r", cfg, commits, refNow)
	bf := res.Files[0].BusFactor
	if bf < 3 || bf > 5 {
		t.Errorf("bus factor = %d, want 3..5 for five near-equal authors:\n%+v",
			bf, res.Files[0].Authors)
	}
}

func TestDirectoryRollupCountsFilesAndShares(t *testing.T) {
	commits := []gitlog.Commit{
		commit(200, "ann", "pkg/a/one.go"),
		commit(190, "ann", "pkg/a/two.go"),
		commit(180, "bob", "pkg/a/three.go"),
	}
	res := AggregateBusFactor("/r", DefaultBusFactorConfig(), commits, refNow)
	if len(res.Directories) != 1 {
		t.Fatalf("directories = %+v, want one", res.Directories)
	}
	d := res.Directories[0]
	if d.Path != "pkg/a" {
		t.Errorf("path = %q, want pkg/a", d.Path)
	}
	if d.Files != 3 {
		t.Errorf("files = %d, want 3", d.Files)
	}
	if d.Boundary != "pkg" {
		t.Errorf("boundary = %q, want pkg", d.Boundary)
	}
}

// A commit listing the same path twice must not double an author's ownership.
func TestDuplicatePathsInOneCommitCountOnce(t *testing.T) {
	commits := []gitlog.Commit{
		commit(10, "ann", "a.go", "a.go"),
		commit(9, "bob", "a.go"),
		commit(8, "cy", "a.go"),
	}
	res := AggregateBusFactor("/r", DefaultBusFactorConfig(), commits, refNow)
	if res.Files[0].Commits != 3 {
		t.Errorf("commits = %d, want 3: a repeated path was counted twice",
			res.Files[0].Commits)
	}
}

func TestNoCommitsIsHandled(t *testing.T) {
	res := AggregateBusFactor("/r", DefaultBusFactorConfig(), nil, refNow)
	if res.Note == "" {
		t.Error("no commits gave no explanation")
	}
	if len(res.Files) != 0 {
		t.Errorf("no commits produced files: %+v", res.Files)
	}
}

// ---- coupling ----

func TestCouplingFindsHiddenPairs(t *testing.T) {
	cfg := DefaultCouplingConfig()
	var commits []gitlog.Commit
	for i := 0; i < 12; i++ {
		commits = append(commits, commit(i, "ann", "pkg/a/a.go", "pkg/b/b.go"))
	}

	// Two unrelated packages. Nothing in the import graph ties them together, so
	// their co-change is enforced only by convention.
	graph := &Graph{
		Module:   "example.com/m",
		Packages: map[string]string{"example.com/m/pkg/a": "pkg", "example.com/m/pkg/b": "pkg"},
		Imports: map[string][]string{
			"example.com/m/pkg/a": nil,
			"example.com/m/pkg/b": nil,
		},
	}

	res := AggregateCoupling("/r", cfg, commits, graph)
	if res.InsufficientHistory != "" {
		t.Fatalf("twelve commits were rejected: %s", res.InsufficientHistory)
	}
	if len(res.HiddenPairs) != 1 {
		t.Fatalf("hidden pairs = %+v, want one", res.HiddenPairs)
	}
	p := res.HiddenPairs[0]
	if p.Coupling != 1 {
		t.Errorf("coupling = %v, want 1", p.Coupling)
	}
	if p.Imports {
		t.Error("a pair with no import was marked as importing")
	}
	if len(res.HiddenFiles) != 2 {
		t.Errorf("hidden files = %v, want both", res.HiddenFiles)
	}
}

// Two files in one package are not "hidden": they share a compilation unit and a
// namespace, so the relationship is already unavoidable rather than conventional.
func TestSamePackageFilesAreNotHiddenCoupling(t *testing.T) {
	cfg := DefaultCouplingConfig()
	var commits []gitlog.Commit
	for i := 0; i < 12; i++ {
		commits = append(commits, commit(i, "ann", "pkg/a/one.go", "pkg/a/two.go"))
	}
	graph := &Graph{
		Module:   "example.com/m",
		Packages: map[string]string{"example.com/m/pkg/a": "pkg"},
		Imports:  map[string][]string{"example.com/m/pkg/a": nil},
	}
	res := AggregateCoupling("/r", cfg, commits, graph)
	if len(res.HiddenPairs) != 0 {
		t.Errorf("same-package files were called hidden: %+v", res.HiddenPairs[0])
	}
}

// A pair that one package imports is not hidden: a compiler already enforces it.
func TestImportingPairsAreNotHidden(t *testing.T) {
	cfg := DefaultCouplingConfig()
	var commits []gitlog.Commit
	for i := 0; i < 12; i++ {
		commits = append(commits, commit(i, "ann", "pkg/a/a.go", "pkg/b/b.go"))
	}
	graph := &Graph{
		Module:   "example.com/m",
		Packages: map[string]string{"example.com/m/pkg/a": "pkg", "example.com/m/pkg/b": "pkg"},
		Imports: map[string][]string{
			"example.com/m/pkg/a": {"example.com/m/pkg/b"},
			"example.com/m/pkg/b": nil,
		},
	}

	res := AggregateCoupling("/r", cfg, commits, graph)
	if len(res.Pairs) != 1 {
		t.Fatalf("pairs = %+v, want one", res.Pairs)
	}
	if len(res.HiddenPairs) != 0 {
		t.Errorf("an importing pair was reported as hidden: %+v", res.HiddenPairs[0])
	}
}

// Without a parsed graph, no pair may be called unimported: the claim is only
// meaningful once both files have been read.
func TestWithoutAGraphNoPairIsHidden(t *testing.T) {
	cfg := DefaultCouplingConfig()
	var commits []gitlog.Commit
	for i := 0; i < 12; i++ {
		commits = append(commits, commit(i, "ann", "a.go", "b.go"))
	}
	res := AggregateCoupling("/r", cfg, commits, nil)

	if len(res.Pairs) != 1 {
		t.Fatalf("pairs = %+v, want one", res.Pairs)
	}
	if len(res.HiddenPairs) != 0 {
		t.Errorf("pairs were called hidden with no import graph read: %+v",
			res.HiddenPairs)
	}
}

func TestCouplingIsGatedOnHistoryDepth(t *testing.T) {
	cfg := DefaultCouplingConfig()
	res := AggregateCoupling("/r", cfg, []gitlog.Commit{
		commit(1, "ann", "a.go", "b.go"),
		commit(2, "ann", "a.go", "b.go"),
		commit(3, "ann", "a.go", "b.go"),
	}, nil)

	if res.InsufficientHistory == "" {
		t.Fatal("a three-commit history produced coupling")
	}
	if len(res.Pairs) != 0 {
		t.Errorf("pairs were produced from insufficient history: %+v", res.Pairs)
	}
	if !strings.Contains(res.InsufficientHistory, "limit of the data") {
		t.Errorf("the gate message is wrong: %q", res.InsufficientHistory)
	}
}

func TestCouplingIsDeterministic(t *testing.T) {
	cfg := DefaultCouplingConfig()
	var commits []gitlog.Commit
	for i := 0; i < 12; i++ {
		commits = append(commits, commit(i, "ann", "a.go", "b.go", "c.go"))
	}
	first := formatCoupling(AggregateCoupling("/r", cfg, commits, nil))
	for i := 0; i < 20; i++ {
		if again := formatCoupling(AggregateCoupling("/r", cfg, commits, nil)); again != first {
			t.Fatalf("run %d differs", i)
		}
	}
}

// ---- shared helpers ----

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func formatCoupling(r CouplingResult) string {
	var b strings.Builder
	for _, p := range r.Pairs {
		b.WriteString(p.A + "|" + p.B + "|" + itoa(p.SharedCommits) + "\n")
	}
	return b.String()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
