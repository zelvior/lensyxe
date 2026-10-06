package report

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/zelvior/lensyxe/internal/compare"
	"github.com/zelvior/lensyxe/internal/golden"
	"github.com/zelvior/lensyxe/pkg/models"
)

// TestMain pins the terminal color profile for the whole package.
//
// lipgloss detects the profile from os.Stdout when it initializes. That means
// the same snapshot renders ANSI codes when `go test` is attached to a terminal
// and plain text when it is piped, which would make a golden file match locally
// and fail in CI, or the reverse. Forcing the profile once, here, is what makes
// the goldens byte-reproducible.
//
// Ascii is the profile a pipe would produce, so this changes nothing for the
// tests that were already running with output captured.
func TestMain(m *testing.M) {
	lipgloss.SetColorProfile(termenv.Ascii)
	os.Exit(m.Run())
}

// goldenDir is where the expected outputs live.
const goldenDir = "testdata/golden"

// assertGolden compares got against a golden file, reporting what -update did.
func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	res, err := golden.Check(filepath.Join(goldenDir, name), got)
	if err != nil {
		t.Fatal(err)
	}
	if res.Wrote {
		t.Logf("wrote %s", filepath.Join(goldenDir, name))
	}
}

// goldenSnapshot builds a snapshot with every field pinned.
//
// Nothing here may depend on the clock, the filesystem, or map iteration order.
// A golden file whose content drifts on an unrelated machine is worse than no
// golden file, because it trains the team to re-run -update without reading.
func goldenSnapshot() *models.Snapshot {
	snap := sampleSnapshot()

	// Pin the fields that would otherwise vary per run or per machine.
	snap.GeneratedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	snap.DurationMS = 42
	snap.Root = "/tmp/demo"
	snap.Version = "0.3.0-test"

	// Make the line counts arithmetically consistent. sampleSnapshot sets
	// TotalLines and CodeLines independently, which leaves BlankOrComment at
	// zero and produces a report claiming 3000 physical lines contain no blank
	// lines or comments. A golden file is a reference document; it should not
	// show an impossible number.
	snap.Code.BlankOrComment = snap.Code.TotalLines - snap.Code.CodeLines

	if snap.Git.LastCommitAt.IsZero() {
		snap.Git.LastCommitAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if snap.Git.FirstCommitAt.IsZero() {
		snap.Git.FirstCommitAt = time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	}
	return snap
}

// ---------------------------------------------------------------- terminal

func TestGoldenTerminal(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, goldenSnapshot()); err != nil {
		t.Fatalf("Render: %v", err)
	}
	assertGolden(t, "terminal.txt", buf.Bytes())
}

// A healthy repository renders a different layout than a risky one: the health
// bar, the risk count, and the coloring all move. Pinning only the "interesting"
// case would leave the common one free to regress.
func TestGoldenTerminalHealthy(t *testing.T) {
	snap := goldenSnapshot()
	snap.Health.Score = 96
	snap.Health.Grade = "A"
	snap.Health.Summary = "Strong across the measured dimensions."
	snap.Risks = nil

	var buf bytes.Buffer
	if err := Render(&buf, snap); err != nil {
		t.Fatalf("Render: %v", err)
	}
	assertGolden(t, "terminal_healthy.txt", buf.Bytes())
}

// The empty state is the one users see first on a fresh repository, and it has
// the most conditional branches: no risks, no findings, no hotspots, no git.
func TestGoldenTerminalEmpty(t *testing.T) {
	empty := &models.Snapshot{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "0.3.0-test",
		Root:          "/tmp/empty",
		GeneratedAt:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		DurationMS:    7,
		Health: models.Health{
			Score: 50, Grade: "F", Summary: "Nothing to measure.",
			Metrics: []models.Metric{}, Components: 0,
		},
		Code: models.CodeStats{
			Languages:  []models.LanguageStat{},
			Hotspots:   []models.Hotspot{},
			Complexity: models.ComplexitySummary{WorstFiles: []models.FileComplexity{}},
		},
		Git:          models.GitStats{IsRepository: false, Churn: []models.ChurnEntry{}},
		Dependencies: models.DependencyStats{Ecosystems: []models.EcosystemStats{}},
		Findings:     []models.Finding{},
		Risks:        []models.Risk{},
	}

	var buf bytes.Buffer
	if err := Render(&buf, empty); err != nil {
		t.Fatalf("Render: %v", err)
	}
	assertGolden(t, "terminal_empty.txt", buf.Bytes())
}

// The monorepo matrix is a separate section with its own alignment and
// truncation logic, so it gets its own golden.
func TestGoldenTerminalWorkspace(t *testing.T) {
	snap := goldenSnapshot()
	snap.Workspace = &models.Workspace{
		Kind:     models.WorkspaceNPM,
		Manifest: "package.json",
		Packages: []models.PackageHealth{
			{
				Path: "apps/web", Manifest: "apps/web/package.json", Name: "@acme/web",
				Ecosystem: "npm", Files: 30, SourceFiles: 28, TestFiles: 2,
				TestFileRatio: 0.07, CodeLines: 4200, Health: models.Health{Score: 91, Grade: "A"},
				Hotspots: []models.Hotspot{},
				Risks:    []models.Risk{},
			},
			{
				Path: "packages/a-very-long-package-name", Manifest: "packages/a-very-long-package-name/package.json",
				Name: "@acme/legacy", Ecosystem: "npm", Files: 12, SourceFiles: 12, TestFiles: 0,
				TestFileRatio: 0, CodeLines: 900, Health: models.Health{Score: 62, Grade: "D"},
				Dependencies: models.DependencyStats{Detected: true, Drift: true},
				Hotspots: []models.Hotspot{
					{Path: "packages/a-very-long-package-name/legacy.go", Lines: 700,
						Churn: 900, Confirmed: true, Classification: "size,churn,complexity,confirmed"},
				},
				Risks: []models.Risk{},
			},
		},
	}

	var buf bytes.Buffer
	if err := Render(&buf, snap); err != nil {
		t.Fatalf("Render: %v", err)
	}
	assertGolden(t, "terminal_workspace.txt", buf.Bytes())
}

// ----------------------------------------------------------------- markdown

func TestGoldenMarkdown(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderMarkdown(&buf, goldenSnapshot(), MarkdownOptions{}); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	assertGolden(t, "markdown.md", buf.Bytes())
}

// Findings are opt-in, so the default and the opt-in renderings differ and both
// need pinning.
func TestGoldenMarkdownWithFindings(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderMarkdown(&buf, goldenSnapshot(), MarkdownOptions{IncludeFindings: true}); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	assertGolden(t, "markdown_findings.md", buf.Bytes())
}

func TestGoldenMarkdownWorkspace(t *testing.T) {
	snap := goldenSnapshot()
	snap.Workspace = &models.Workspace{
		Kind:     models.WorkspaceNPM,
		Manifest: "pnpm-workspace.yaml",
		Packages: []models.PackageHealth{
			{
				Path: "apps/web", Files: 30, SourceFiles: 28, TestFiles: 3,
				TestFileRatio: 0.1, CodeLines: 4200,
				Health:      models.Health{Score: 91, Grade: "A"},
				Attribution: []string{"code.hotspot.a", "code.hotspot.b"},
			},
			{
				Path: "packages/ui", Files: 12, SourceFiles: 10, TestFiles: 4,
				TestFileRatio: 0.25, CodeLines: 900,
				Health: models.Health{Score: 94, Grade: "A"},
			},
		},
	}

	var buf bytes.Buffer
	if err := RenderMarkdown(&buf, snap, MarkdownOptions{}); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	assertGolden(t, "markdown_workspace.md", buf.Bytes())
}

// --------------------------------------------------------------------- json

func TestGoldenJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, goldenSnapshot()); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	assertGolden(t, "snapshot.json", buf.Bytes())
}

func TestGoldenJSONWorkspace(t *testing.T) {
	snap := goldenSnapshot()
	snap.Workspace = &models.Workspace{
		Kind:     models.WorkspacePNPM,
		Manifest: "pnpm-workspace.yaml",
		Packages: []models.PackageHealth{
			{
				Path: "packages/core", Manifest: "packages/core/package.json",
				Name: "@acme/core", Ecosystem: "npm", Files: 20, SourceFiles: 16,
				TestFiles: 4, TestFileRatio: 0.2, CodeLines: 3000,
				Health:        models.Health{Score: 84, Grade: "B"},
				GitApplicable: false,
				Hotspots:      []models.Hotspot{},
				Risks:         []models.Risk{},
				Dependencies:  models.DependencyStats{Ecosystems: []models.EcosystemStats{}},
				Attribution:   []string{"code.hotspot.core"},
			},
		},
	}

	var buf bytes.Buffer
	if err := RenderJSON(&buf, snap); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	assertGolden(t, "snapshot_workspace.json", buf.Bytes())
}

// ------------------------------------------------------------------ compare

func TestGoldenCompareTerminal(t *testing.T) {
	result := &compare.Result{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "0.3.0-test",
		Root:          "/tmp/demo",
		A: compare.RevisionInfo{
			Ref: "v1.0.0", Commit: "1111111111111111111111111111111111111111",
			ShortSHA: "1111111",
			Date:     "2025-11-01T00:00:00Z",
			Subject:  "release: v1.0.0",
		},
		B: compare.RevisionInfo{
			Ref: "v1.1.0", Commit: "2222222222222222222222222222222222222222",
			ShortSHA: "2222222",
			Date:     "2025-12-01T00:00:00Z",
			Subject:  "release: v1.1.0",
		},
		ScoreA:      88,
		ScoreB:      84,
		ScoreDelta:  -4,
		GradeA:      "B",
		GradeB:      "B",
		GradeChange: "=",
		Verdict:     "The score fell 4 points.",
		Metrics: []compare.MetricDelta{
			{Label: "Code health", A: 91, B: 86, Delta: -5, Unit: "", Better: -1,
				Display: "91.0 -> 86.0 (-5.0)"},
		},
		RisksAdded: []models.Risk{
			{
				ID: "r-added", Severity: models.SeverityHigh, Category: models.CategoryCode,
				Title: "Confirmed hotspot", Detail: "newly confirmed", Impact: 7,
				Evidence: []models.Evidence{},
			},
		},
		RisksResolved: []models.Risk{
			{
				ID: "r-removed", Severity: models.SeverityLow, Category: models.CategoryCode,
				Title: "Large file candidate", Detail: "was a candidate", Impact: 2,
				Evidence: []models.Evidence{},
			},
		},
		RisksChanged: []compare.RiskChange{},
	}

	var buf bytes.Buffer
	if err := RenderCompare(&buf, result); err != nil {
		t.Fatalf("RenderCompare: %v", err)
	}
	assertGolden(t, "compare.txt", buf.Bytes())
}

func TestGoldenCompareMarkdown(t *testing.T) {
	res := &compare.Result{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "0.3.0-test",
		Root:          "/tmp/demo",
		A: compare.RevisionInfo{
			Ref: "v1.0.0", Commit: "1111111111111111111111111111111111111111",
			ShortSHA: "1111111", Date: "2025-11-01T00:00:00Z", Subject: "release: v1.0.0",
		},
		B: compare.RevisionInfo{
			Ref: "v1.1.0", Commit: "2222222222222222222222222222222222222222",
			ShortSHA: "2222222", Date: "2025-12-01T00:00:00Z", Subject: "release: v1.1.0",
		},
		ScoreA:      88,
		ScoreB:      84,
		ScoreDelta:  -4,
		GradeA:      "B",
		GradeB:      "B",
		GradeChange: "=",
		Verdict:     "The score fell 4 points.",
		Metrics: []compare.MetricDelta{
			{Label: "Code health", A: 91, B: 86, Delta: -5, Better: -1,
				Display: "91.0 -> 86.0 (-5.0)"},
		},
		RisksAdded:    []models.Risk{},
		RisksResolved: []models.Risk{},
		RisksChanged:  []compare.RiskChange{},
	}

	var buf bytes.Buffer
	if err := RenderCompareMarkdown(&buf, res); err != nil {
		t.Fatalf("RenderCompareMarkdown: %v", err)
	}
	assertGolden(t, "compare.md", buf.Bytes())
}

func TestGoldenCompareJSON(t *testing.T) {
	res := &compare.Result{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "0.3.0-test",
		Root:          "/tmp/demo",
		A: compare.RevisionInfo{
			Ref: "v1.0.0", Commit: "1111111111111111111111111111111111111111",
			ShortSHA: "1111111", Date: "2025-11-01T00:00:00Z", Subject: "release: v1.0.0",
		},
		B: compare.RevisionInfo{
			Ref: "v1.1.0", Commit: "2222222222222222222222222222222222222222",
			ShortSHA: "2222222", Date: "2025-12-01T00:00:00Z", Subject: "release: v1.1.0",
		},
		ScoreA:      88,
		ScoreB:      84,
		ScoreDelta:  -4,
		GradeA:      "B",
		GradeB:      "B",
		GradeChange: "=",
		Verdict:     "The score fell 4 points.",
		Metrics: []compare.MetricDelta{
			{Label: "Code health", A: 91, B: 86, Delta: -5, Better: -1,
				Display: "91.0 -> 86.0 (-5.0)"},
		},
		RisksAdded:    []models.Risk{},
		RisksResolved: []models.Risk{},
		RisksChanged:  []compare.RiskChange{},
	}

	var buf bytes.Buffer
	if err := RenderCompareJSON(&buf, res); err != nil {
		t.Fatalf("RenderCompareJSON: %v", err)
	}
	assertGolden(t, "compare.json", buf.Bytes())
}

// -------------------------------------------------------------- stability

// The whole point of a golden file is regression detection, which only works if
// the renderer is deterministic. This asserts that property directly, so a
// non-deterministic renderer fails here rather than as an unexplained golden
// mismatch somewhere else.
func TestRenderersAreDeterministic(t *testing.T) {
	if golden.UpdateEnabled() {
		t.Skip("determinism is not meaningful while rewriting goldens")
	}

	render := map[string]func() []byte{
		"terminal": func() []byte {
			var b bytes.Buffer
			_ = Render(&b, goldenSnapshot())
			return b.Bytes()
		},
		"markdown": func() []byte {
			var b bytes.Buffer
			_ = RenderMarkdown(&b, goldenSnapshot(), MarkdownOptions{IncludeFindings: true})
			return b.Bytes()
		},
		"json": func() []byte {
			var b bytes.Buffer
			_ = RenderJSON(&b, goldenSnapshot())
			return b.Bytes()
		},
	}

	for name, fn := range render {
		first := fn()
		for i := 1; i < 5; i++ {
			if got := fn(); !bytes.Equal(first, got) {
				t.Errorf("%s renderer is not deterministic: run %d differs", name, i)
				break
			}
		}
		if len(first) == 0 {
			t.Errorf("%s renderer produced no output", name)
		}
	}
}

// Every golden file must be referenced by a test. An orphaned golden file is a
// stale expectation that no longer protects anything, and it makes a diff look
// meaningful when it is not.
func TestNoOrphanGoldenFiles(t *testing.T) {
	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("no golden directory yet; run with -update")
		}
		t.Fatalf("read %s: %v", goldenDir, err)
	}

	// Collect every name the test source references.
	self, err := os.ReadFile("golden_test.go")
	if err != nil {
		t.Fatal(err)
	}
	referenced := string(self)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.Contains(referenced, `"`+name+`"`) {
			t.Errorf("golden file %s is not referenced by any test in this file", name)
		}
	}
}

// The goldens must not contain a color escape sequence. If they ever do, the
// profile pinning in TestMain has stopped working and every comparison becomes
// dependent on the terminal that ran it.
func TestGoldensHaveNoANSIEscapes(t *testing.T) {
	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("no golden directory yet; run with -update")
		}
		t.Fatalf("read %s: %v", goldenDir, err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "terminal") &&
			!strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(goldenDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.ContainsRune(data, 0x1b) {
			t.Errorf("%s contains an ANSI escape; the color profile pin has stopped working",
				e.Name())
		}
	}
}
