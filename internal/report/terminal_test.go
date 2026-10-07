package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/internal/compare"
	"github.com/zelvior/lensyxe/internal/risk"
	"github.com/zelvior/lensyxe/pkg/models"
)

// sampleSnapshot returns a fully populated snapshot for render tests.
func sampleSnapshot() *models.Snapshot {
	return &models.Snapshot{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "0.2.0-test",
		Root:          "/tmp/demo",
		GeneratedAt:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		DurationMS:    42,
		Code: models.CodeStats{
			Files: 40, SourceFiles: 32, TestFiles: 8, HasTests: true,
			TestFileRatio: 0.2, TestLineRatio: 0.25,
			TotalLines: 3000, CodeLines: 2400, MaxFileLines: 900, AverageLines: 60,
			Languages: []models.LanguageStat{
				{Name: "Go", Files: 32, Lines: 2000, TestFiles: 8},
				{Name: "Shell", Files: 8, Lines: 400},
			},
			Hotspots: []models.Hotspot{
				{Path: "internal/engine.go", Lines: 900, Language: "Go", Churn: 220,
					Complexity: 42, Level: models.ComplexityVeryHigh, Confirmed: true,
					Classification: "size,churn,complexity,confirmed",
					Rationale:      "900 code lines; over every threshold."},
				{Path: "internal/legacy.go", Lines: 600, Language: "Go", Churn: 4,
					Complexity: 3, Level: models.ComplexityLow, Confirmed: false,
					Classification: "size", Rationale: "600 code lines; simple."},
			},
			Complexity: models.ComplexitySummary{
				Measured: true, Files: 40, Functions: 300, BranchPoints: 1200,
				AverageComplexity: 5.2, MaxComplexity: 42,
				MaxComplexityFile: "internal/engine.go", MaxNesting: 9,
				VeryHighFunctions: 2, HighFunctions: 4,
				WorstFiles: []models.FileComplexity{
					{Path: "internal/engine.go", Language: "Go", Lines: 900,
						Functions: 22, BranchPoints: 300, MaxNesting: 9,
						EstimatedComplexity: 42, MaxFunctionComplex: 61,
						Level: models.ComplexityVeryHigh, Density: 4.67},
					{Path: "internal/report.go", Language: "Go", Lines: 400,
						Functions: 14, BranchPoints: 60, MaxNesting: 4,
						EstimatedComplexity: 5.3, Level: models.ComplexityLow, Density: 1.33},
				},
			},
		},
		Git: models.GitStats{
			IsRepository: true, Branch: "main", HeadCommit: "abc123def456",
			TotalCommits: 100, WindowCommits: 20, WindowDays: 90, Authors: 3,
			BusFactor: 1, DaysSinceCommit: 1, CommitsPerWeek: 1.6,
			// The rate's denominator. A fixture that leaves this at zero models an
			// analyzer that never recorded it, which is not what the analyzer does
			// and would render the line without the span a reader needs.
			CadenceSpanDays: 90,
			LinesAdded:      500, LinesDeleted: 120, ChurnFiles: 15,
			ChurnHotspotRate: 0.4, ChurnConcentration: 0.2,
			Churn:        []models.ChurnEntry{{Path: "internal/engine.go", Commits: 5, Added: 100, Deleted: 20, Score: 125}},
			LastCommitAt: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC),
		},
		Dependencies: models.DependencyStats{
			Detected: true, Direct: 4, Dev: 2, Total: 6, Indirect: 1, Transitive: 48, Locked: true,
			Ecosystems: []models.EcosystemStats{
				{Name: "gomod", Manifest: "go.mod", Lockfile: "go.sum",
					LockfileFormat: "go-sum", Total: 6, Direct: 4, Dev: 2, Indirect: 1,
					Transitive: 48, Packages: []string{"example.com/x"}, Detected: true},
				{Name: "npm", Manifest: "package.json", Detected: true,
					Direct: 3, Dev: 1, Total: 4, Packages: []string{"react"},
					Drift: true, DriftReason: "package.json declares 3 direct dependencies with no lockfile"},
			},
		},
		Health: models.Health{
			Score: 71.5, Grade: "C", Components: 3,
			Summary: "Overall adequate.",
			Metrics: []models.Metric{
				{Key: "code", Label: "Code health", Score: 55, Weight: 0.4, Value: 60,
					Display: "60 LOC/file across 40 files", Detail: "size 40, hotspots 20% of lines, cohesion 50",
					Applicable: true},
				{Key: "dependency", Label: "Dependency health", Score: 90, Weight: 0.3, Value: 7,
					Display: "7 direct / 7 total", Detail: "count 86, reproducibility 80, spread 50",
					Applicable: true},
				{Key: "git", Label: "Maintainability (Git)", Score: 75, Weight: 0.3, Value: 1.6,
					Display: "1.6 commits/week", Detail: "cadence 20, freshness 100, churn 83, bus factor 85",
					Applicable: true},
			},
		},
		Findings: []models.Finding{
			{Severity: models.SeverityHigh, Category: models.CategoryDependency,
				Title: "Missing lockfile for package.json", Detail: "no lockfile", Subject: "package.json"},
			{Severity: models.SeverityMedium, Category: models.CategoryTesting,
				Title: "No test files detected", Detail: "none"},
		},
		Risks: []models.Risk{
			{
				ID: "code.hotspot.internal/engine.go", Severity: models.SeverityCritical,
				Category: models.CategoryCode, Title: "Confirmed hotspot",
				Detail:  "Hotspot detected in internal/engine.go.",
				Subject: "internal/engine.go", Impact: 9.4,
				Evidence: []models.Evidence{
					models.NewEvidence("file", "LOC", "900 code lines", 900, "internal/engine.go"),
					models.NewEvidence("metric", "Churn", "220 lines modified in the git window", 220, "internal/engine.go"),
					models.NewEvidence("metric", "Complexity", "42 estimated cyclomatic complexity", 42, "internal/engine.go"),
				},
				Recommendation: "Split internal/engine.go until no unit exceeds the size threshold.",
			},
			{
				ID: "dependency.drift.npm", Severity: models.SeverityHigh,
				Category: models.CategoryDependency, Title: "Missing lockfile",
				Detail: "Missing lockfile for package.json.", Subject: "package.json",
				Impact: 4.5,
				Evidence: []models.Evidence{
					models.NewEvidence("lockfile", "Lockfile", "no lockfile found", 0, "package.json"),
				},
			},
		},
	}
}

func TestRenderProducesAllSections(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleSnapshot()); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"ENGINEERING HEALTH", "CODE", "GIT", "DEPENDENCIES", "RISKS", "FINDINGS",
		"71.5", "main", "abc123def456", "gomod", "go.sum",
		"Missing lockfile for package.json", "schema v",
		// Phase 2 surfaces.
		"test ratio", "complexity", "worst-complexity", "hotspots",
		"transitive", "900 code lines", "LOC",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	var first bytes.Buffer
	if err := Render(&first, sampleSnapshot()); err != nil {
		t.Fatalf("Render: %v", err)
	}
	for i := 0; i < 5; i++ {
		var got bytes.Buffer
		if err := Render(&got, sampleSnapshot()); err != nil {
			t.Fatalf("Render: %v", err)
		}
		if got.String() != first.String() {
			t.Fatalf("run %d output differs", i)
		}
	}
}

// JSON output must carry the full risk evidence payloads, not a summary.
func TestRenderJSONIncludesFullRiskEvidence(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, sampleSnapshot()); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}

	var decoded models.Snapshot
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Risks) != 2 {
		t.Fatalf("risks = %d, want 2", len(decoded.Risks))
	}
	hotspot := decoded.Risks[0]
	if len(hotspot.Evidence) != 3 {
		t.Fatalf("evidence = %d, want 3", len(hotspot.Evidence))
	}
	for _, e := range hotspot.Evidence {
		if e.Kind == "" || e.Label == "" || e.Detail == "" {
			t.Errorf("incomplete evidence entry: %+v", e)
		}
		if e.Value == nil {
			t.Errorf("evidence %q should carry a numeric value", e.Label)
		}
	}
	if hotspot.Recommendation == "" {
		t.Error("recommendation should survive JSON round-trip")
	}
	if hotspot.ID == "" {
		t.Error("risk ID should survive JSON round-trip")
	}
}

// Hotspot payloads must be complete, including the confirmation flag and the
// numbers behind the classification.
func TestRenderJSONIncludesHotspotDetail(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderJSON(&buf, sampleSnapshot()); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var decoded models.Snapshot
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Code.Hotspots) != 2 {
		t.Fatalf("hotspots = %d, want 2", len(decoded.Code.Hotspots))
	}
	h := decoded.Code.Hotspots[0]
	if h.Churn == 0 || h.Complexity == 0 || h.Rationale == "" {
		t.Errorf("hotspot payload incomplete: %+v", h)
	}
	if !strings.Contains(h.Classification, "confirmed") {
		t.Errorf("classification = %q", h.Classification)
	}
	if !decoded.Code.Complexity.Measured {
		t.Error("complexity summary should be present")
	}
	if len(decoded.Code.Complexity.WorstFiles) != 2 {
		t.Errorf("worst files = %d, want 2", len(decoded.Code.Complexity.WorstFiles))
	}
	if decoded.Code.Complexity.WorstFiles[0].Level != models.ComplexityVeryHigh {
		t.Errorf("worst file level = %v", decoded.Code.Complexity.WorstFiles[0].Level)
	}
}

func TestRenderJSONIsStable(t *testing.T) {
	var first bytes.Buffer
	if err := RenderJSON(&first, sampleSnapshot()); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	for i := 0; i < 5; i++ {
		var got bytes.Buffer
		if err := RenderJSON(&got, sampleSnapshot()); err != nil {
			t.Fatalf("RenderJSON: %v", err)
		}
		if got.String() != first.String() {
			t.Fatalf("run %d JSON differs", i)
		}
	}
}

// Markdown must contain no ANSI escapes so it pastes cleanly into a PR.
func TestRenderMarkdownHasNoANSIEscapes(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderMarkdown(&buf, sampleSnapshot(), MarkdownOptions{}); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if strings.Contains(buf.String(), "\x1b[") {
		t.Error("markdown output must not contain ANSI escape sequences")
	}
}

func TestRenderMarkdownStructure(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderMarkdown(&buf, sampleSnapshot(), MarkdownOptions{}); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"# demo: engineering health",              // title from the root path
		"## Health score",                         //
		"| Dimension | Score | Weight | Detail |", // metric table header
		"**71.5 / 100** (grade C)",                //
		"## Code",                                 //
		"### Hotspots",                            //
		"**confirmed**",                           //
		"Worst-complexity files",                  //
		"## Dependencies",                         //
		"## Git",                                  //
		"## Risks (2)",                            //
		"### 🔴 CRITICAL Confirmed hotspot",        // severity glyph and label
		"| Evidence | Measurement | Value |",      // evidence table
		"900 code lines",                          //
		"**Recommendation**:",                     //
	} {
		if !strings.Contains(out, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, out)
		}
	}
}

// Findings are opt-in: noisy for a PR comment, useful for a docs export.
func TestRenderMarkdownFindingsOptIn(t *testing.T) {
	var without bytes.Buffer
	if err := RenderMarkdown(&without, sampleSnapshot(), MarkdownOptions{}); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if strings.Contains(without.String(), "## Findings") {
		t.Error("findings should be omitted by default")
	}

	var with bytes.Buffer
	if err := RenderMarkdown(&with, sampleSnapshot(), MarkdownOptions{IncludeFindings: true}); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(with.String(), "## Findings (2)") {
		t.Error("expected a findings section when requested")
	}
}

// A risk cap keeps a PR comment short, and the omission must be disclosed.
func TestRenderMarkdownRiskCap(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderMarkdown(&buf, sampleSnapshot(), MarkdownOptions{MaxRisks: 1}); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "1 additional risk(s) omitted") {
		t.Errorf("expected a disclosure of the omitted risks:\n%s", out)
	}
	if strings.Contains(out, "Missing lockfile\n") {
		t.Error("the capped risk should not be detailed")
	}
}

func TestRenderMarkdownIsDeterministic(t *testing.T) {
	var first bytes.Buffer
	if err := RenderMarkdown(&first, sampleSnapshot(), MarkdownOptions{IncludeFindings: true}); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	for i := 0; i < 5; i++ {
		var got bytes.Buffer
		if err := RenderMarkdown(&got, sampleSnapshot(), MarkdownOptions{IncludeFindings: true}); err != nil {
			t.Fatalf("RenderMarkdown: %v", err)
		}
		if got.String() != first.String() {
			t.Fatalf("run %d markdown differs", i)
		}
	}
}

// A pipe inside a cell would silently split the row into extra columns.
func TestEscapePipes(t *testing.T) {
	if got := escapePipes("a|b"); got != `a\|b` {
		t.Errorf("escapePipes = %q", got)
	}
	if got := escapePipes("line1\nline2"); got != "line1 line2" {
		t.Errorf("escapePipes = %q", got)
	}
	if got := escapePipes(""); got != "" {
		t.Errorf("escapePipes = %q", got)
	}
}

func TestMarkdownTitleFallsBackForRootPaths(t *testing.T) {
	cases := map[string]string{
		"/tmp/demo":     "demo: engineering health",
		"/tmp/demo/":    "demo: engineering health",
		"C:\\tmp\\demo": "demo: engineering health",
		"":              "lensyxe: engineering health",
	}
	for root, want := range cases {
		snap := sampleSnapshot()
		snap.Root = root
		var buf bytes.Buffer
		if err := RenderMarkdown(&buf, snap, MarkdownOptions{}); err != nil {
			t.Fatalf("RenderMarkdown: %v", err)
		}
		if !strings.HasPrefix(buf.String(), "# "+want) {
			t.Errorf("root %q produced %q, want it to start with %q",
				root, strings.SplitN(buf.String(), "\n", 2)[0], "# "+want)
		}
	}
}

func TestMarkdownCustomTitle(t *testing.T) {
	var buf bytes.Buffer
	opts := MarkdownOptions{Title: "Custom Report"}
	if err := RenderMarkdown(&buf, sampleSnapshot(), opts); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "# Custom Report\n") {
		t.Errorf("custom title not used: %q", strings.SplitN(buf.String(), "\n", 2)[0])
	}
}

func TestRenderMarkdownNonGitRepo(t *testing.T) {
	snap := sampleSnapshot()
	snap.Git = models.GitStats{WindowDays: 90}

	var buf bytes.Buffer
	if err := RenderMarkdown(&buf, snap, MarkdownOptions{}); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(buf.String(), "Not a git repository") {
		t.Error("expected a note that git metrics were skipped")
	}
}

func TestRenderMarkdownNilSnapshot(t *testing.T) {
	if err := RenderMarkdown(&bytes.Buffer{}, nil, MarkdownOptions{}); err == nil {
		t.Error("expected an error for a nil snapshot")
	}
}

func TestRenderNilSnapshot(t *testing.T) {
	if err := Render(&bytes.Buffer{}, nil); err == nil {
		t.Error("expected an error for a nil snapshot")
	}
}

func TestScoreBarEdges(t *testing.T) {
	for _, score := range []float64{0, 50, 99.9, 100, -10, 1000} {
		bar := scoreBar(score)
		if got := strings.Count(bar, "█") + strings.Count(bar, "░"); got != 20 {
			t.Errorf("scoreBar(%v) drew %d cells, want 20", score, got)
		}
	}
}

func TestSortHelpers(t *testing.T) {
	findings := []models.Finding{
		{Severity: models.SeverityLow, Title: "b"},
		{Severity: models.SeverityCritical, Title: "a"},
		{Severity: models.SeverityHigh, Title: "c"},
	}
	SortFindings(findings)
	if findings[0].Severity != models.SeverityCritical {
		t.Errorf("findings[0] = %v, want critical", findings[0].Severity)
	}

	risks := []models.Risk{{ID: "a", Title: "a", Impact: 5}, {ID: "b", Title: "b", Impact: 50}}
	SortRisks(risks)
	if risks[0].Title != "b" {
		t.Errorf("risks[0] = %q, want the highest impact", risks[0].Title)
	}

	SortRisksStable(risks)
	if risks[0].Impact != 50 {
		t.Errorf("SortRisksStable did not preserve ordering: %+v", risks)
	}
}

func TestRiskBandLabels(t *testing.T) {
	got := RiskBandLabels()
	for _, want := range []string{"critical", "high", "medium"} {
		if !strings.Contains(got, want) {
			t.Errorf("RiskBandLabels = %q, missing %q", got, want)
		}
	}
}

// Compare renderers must handle every risk bucket without panicking.
func TestRenderCompareVariants(t *testing.T) {
	cases := []struct {
		name string
		res  *compare.Result
	}{
		{"empty", &compare.Result{SchemaVersion: models.SchemaVersion}},
		{"metrics only", &compare.Result{
			SchemaVersion: models.SchemaVersion, ScoreA: 87, ScoreB: 84, ScoreDelta: -3,
			GradeA: "B", GradeB: "B",
			Metrics: []compare.MetricDelta{
				{Label: "Code lines", A: 100, B: 120, Delta: 20, Unit: "LOC", Better: 1,
					Display: "Code lines: 100 -> 120 (+20) LOC"},
				{Label: "Direct deps", A: 10, B: 13, Delta: 3, Better: -1},
			},
		}},
		{"all risk buckets", &compare.Result{
			SchemaVersion: models.SchemaVersion, ScoreA: 90, ScoreB: 65, ScoreDelta: -25,
			GradeA: "A", GradeB: "D", Verdict: "Significant regression.",
			RisksAdded:    []models.Risk{{ID: "new", Title: "New thing", Severity: models.SeverityCritical, Impact: 9}},
			RisksResolved: []models.Risk{{ID: "gone", Title: "Fixed thing", Severity: models.SeverityHigh, Impact: 6}},
			RisksChanged: []compare.RiskChange{{
				ID: "changed", Title: "Worse thing", Severity: models.SeverityLow,
				After: models.SeverityHigh, ImpactA: 2, ImpactB: 8, Delta: 6, Better: -1,
			}},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var term, md, js bytes.Buffer
			if err := RenderCompare(&term, tc.res); err != nil {
				t.Fatalf("RenderCompare: %v", err)
			}
			if err := RenderCompareMarkdown(&md, tc.res); err != nil {
				t.Fatalf("RenderCompareMarkdown: %v", err)
			}
			if err := RenderCompareJSON(&js, tc.res); err != nil {
				t.Fatalf("RenderCompareJSON: %v", err)
			}
			var decoded compare.Result
			if err := json.Unmarshal(js.Bytes(), &decoded); err != nil {
				t.Fatalf("compare JSON is invalid: %v", err)
			}
			if decoded.ScoreA != tc.res.ScoreA {
				t.Errorf("ScoreA round-trip = %v, want %v", decoded.ScoreA, tc.res.ScoreA)
			}
			if strings.Contains(md.String(), "\x1b[") {
				t.Error("compare markdown must not contain ANSI escapes")
			}
		})
	}
}

func TestRenderCompareNilResult(t *testing.T) {
	if err := RenderCompare(&bytes.Buffer{}, nil); err == nil {
		t.Error("expected an error for a nil result")
	}
	if err := RenderCompareMarkdown(&bytes.Buffer{}, nil); err == nil {
		t.Error("expected an error for a nil result")
	}
}

func TestCompareMarkdownShowsRiskDiff(t *testing.T) {
	res := &compare.Result{
		SchemaVersion: models.SchemaVersion, ScoreA: 90, ScoreB: 70, ScoreDelta: -20,
		GradeA: "A", GradeB: "D", Verdict: "Significant regression.",
		RisksAdded: []models.Risk{{
			ID: "code.hotspot.a.go", Title: "Confirmed hotspot", Subject: "a.go",
			Severity: models.SeverityCritical, Impact: 9,
			Detail: "Hotspot detected in a.go.",
			Evidence: []models.Evidence{
				models.NewEvidence("file", "LOC", "900 code lines", 900, "a.go"),
			},
		}},
	}
	var buf bytes.Buffer
	if err := RenderCompareMarkdown(&buf, res); err != nil {
		t.Fatalf("RenderCompareMarkdown: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"## New risks (1)", "🔴 CRITICAL", "a.go", "900 code lines", "**Delta: -20.0.**",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("compare markdown missing %q", want)
		}
	}
}

func TestCompareRiskBandsAreExported(t *testing.T) {
	// The renderer documents bands via this helper; guard the constants.
	if risk.CriticalImpact <= risk.HighImpact || risk.HighImpact <= risk.MediumImpact {
		t.Fatal("risk impact bands must be strictly descending")
	}
}
