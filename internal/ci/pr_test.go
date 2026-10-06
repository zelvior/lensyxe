package ci

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

// snapshot builds a minimal but realistic snapshot for rendering tests.
func snapshot(score float64, ratio float64, risks ...models.Risk) *models.Snapshot {
	return &models.Snapshot{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "0.3.0",
		Root:          "/repo",
		Health: models.Health{
			Score:   score,
			Grade:   models.Grade(score),
			Summary: "synthetic fixture",
			Metrics: []models.Metric{
				{Key: "code", Label: "Code", Score: 91, Applicable: true, Weight: 0.5},
				{Key: "dependency", Label: "Dependencies", Score: 94, Applicable: true, Weight: 0.3},
				{Key: "git", Label: "Maintainability", Score: 78, Applicable: true, Weight: 0.2},
			},
		},
		Code: models.CodeStats{
			TestFileRatio: ratio,
			SourceFiles:   100,
		},
		Risks: risks,
	}
}

// criticalRisk is a fully populated risk so rendering can be checked end to end.
func criticalRisk() models.Risk {
	return models.Risk{
		ID:       "code.hotspot.internal/engine",
		Severity: models.SeverityCritical,
		Category: models.CategoryCode,
		Title:    "Confirmed hotspot",
		Detail:   "size, churn and complexity all exceeded their thresholds",
		Subject:  "internal/engine.go",
		Impact:   12.5,
		Evidence: []models.Evidence{
			models.NewEvidence("metric", "LOC", "742 code lines", 742, "internal/engine.go"),
		},
		Recommendation: "Reduce the file below 500 lines",
	}
}

func render(t *testing.T, snap *models.Snapshot, opts CommentOptions) string {
	t.Helper()
	var b strings.Builder
	if err := RenderComment(&b, snap, opts); err != nil {
		t.Fatalf("RenderComment: %v", err)
	}
	return b.String()
}

// The marker must be the first line so a poster can find and update its own
// comment.
func TestCommentStartsWithMarker(t *testing.T) {
	out := render(t, snapshot(88, 0.25), CommentOptions{})
	if !strings.HasPrefix(out, CommentMarker) {
		t.Errorf("comment must start with the marker, got:\n%s", out)
	}
	if !strings.Contains(out, CommentTitle) {
		t.Errorf("comment must contain the title, got:\n%s", out)
	}
}

func TestRenderRejectsNilSnapshot(t *testing.T) {
	var b strings.Builder
	if err := RenderComment(&b, nil, CommentOptions{}); err == nil {
		t.Fatal("expected an error for a nil snapshot")
	}
}

// Without a baseline there is nothing to diff against, so no arrow and no
// delta column values may be invented.
func TestCommentWithoutBaselineShowsNoDeltas(t *testing.T) {
	out := render(t, snapshot(88, 0.25), CommentOptions{})
	if !strings.Contains(out, "`88.0`") {
		t.Errorf("absolute score must be shown, got:\n%s", out)
	}
	if strings.Contains(out, "→") {
		t.Errorf("no arrow may appear without a baseline:\n%s", out)
	}
	if strings.Contains(out, "No baseline") == false {
		t.Errorf("rows without a baseline must say so:\n%s", out)
	}
}

func TestCommentWithBaselineShowsDeltas(t *testing.T) {
	cur := snapshot(86, 0.25, criticalRisk())
	base := snapshot(88, 0.25)

	out := render(t, cur, CommentOptions{Baseline: base, BaselineLabel: "main"})

	if !strings.Contains(out, "`88.0 → 86.0`") {
		t.Errorf("headline must show the movement, got:\n%s", out)
	}
	// The arrow carries the direction, so the magnitude is unsigned here.
	if !strings.Contains(out, "(↓2)") {
		t.Errorf("headline must show the drop magnitude, got:\n%s", out)
	}
	if !strings.Contains(out, "main") {
		t.Errorf("the baseline label must be named in the footer:\n%s", out)
	}
}

// The specification's example table implies line coverage and build duration.
// Lensyxe measures neither, so the comment must say so rather than print a
// plausible-looking number nobody can verify.
func TestCommentDoesNotFabricateCoverageOrBuildDuration(t *testing.T) {
	out := render(t, snapshot(86, 0.25), CommentOptions{Baseline: snapshot(88, 0.25)})

	for _, banned := range []string{
		"coverage dropped",
		"build duration",
		"Build duration",
		"+21%",
		"3.2%",
	} {
		if strings.Contains(out, banned) {
			t.Errorf("comment contains fabricated metric %q:\n%s", banned, out)
		}
	}

	// Build must be present but explicitly unmeasured.
	if !strings.Contains(out, "Not measured") {
		t.Errorf("Build must be reported as not measured:\n%s", out)
	}
	// Testing must be labelled as a file ratio, not coverage.
	if !strings.Contains(out, "test-to-code file ratio") {
		t.Errorf("Testing must disclose that it is a file ratio:\n%s", out)
	}
}

func TestCategoriesIncludesEveryRow(t *testing.T) {
	cats := Categories(snapshot(90, 0.4))
	want := []string{"Code", "Dependencies", "Testing", "Maintainability", "Build"}
	if len(cats) != len(want) {
		t.Fatalf("got %d categories, want %d", len(cats), len(want))
	}
	for i, label := range want {
		if cats[i].Label != label {
			t.Errorf("category %d = %q, want %q", i, cats[i].Label, label)
		}
	}
	// Only Build lacks a value.
	for _, c := range cats {
		if c.HasValue == (c.Label == "Build") {
			t.Errorf("category %q HasValue = %v, unexpected", c.Label, c.HasValue)
		}
	}
}

func TestCategoriesNilSnapshot(t *testing.T) {
	cats := Categories(nil)
	if len(cats) == 0 {
		t.Fatal("categories must still be listed without a snapshot")
	}
	for _, c := range cats {
		if c.HasValue {
			t.Errorf("category %q must have no value without a snapshot", c.Label)
		}
	}
}

func TestCategoriesIgnoresInapplicableMetrics(t *testing.T) {
	snap := snapshot(90, 0.4)
	// A non-git directory still carries a zeroed git metric in the snapshot;
	// it must not be reported as a score of 0.
	for i := range snap.Health.Metrics {
		if snap.Health.Metrics[i].Key == "git" {
			snap.Health.Metrics[i].Applicable = false
		}
	}
	out := render(t, snap, CommentOptions{Baseline: snapshot(90, 0.4)})
	if !strings.Contains(out, "No baseline") && !strings.Contains(out, "Maintainability") {
		t.Errorf("Maintainability row should still be present:\n%s", out)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name         string
		delta        float64
		higherBetter bool
		wantLabel    string
		wantWorse    bool
	}{
		{name: "stable near zero", delta: 0.2, higherBetter: true, wantLabel: "Stable"},
		{name: "improved", delta: 3, higherBetter: true, wantLabel: "Improved"},
		{name: "improved lower-is-better", delta: -3, higherBetter: false, wantLabel: "Improved"},
		{name: "regressed lower-is-better", delta: 3, higherBetter: false, wantLabel: "Dropped", wantWorse: true},
		{name: "small regression", delta: -3, higherBetter: true, wantLabel: "Dropped", wantWorse: true},
		{name: "large regression", delta: -12, higherBetter: true, wantLabel: "Regression", wantWorse: true},
		{name: "large drop is worse", delta: 12, higherBetter: false, wantLabel: "Regression", wantWorse: true},
		{name: "exact zero", delta: 0, higherBetter: true, wantLabel: "Stable"},
		{name: "just under threshold", delta: -0.49, higherBetter: true, wantLabel: "Stable"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.delta, tc.higherBetter)
			if got.Label != tc.wantLabel {
				t.Errorf("Label = %q, want %q", got.Label, tc.wantLabel)
			}
			if got.Worse != tc.wantWorse {
				t.Errorf("Worse = %v, want %v", got.Worse, tc.wantWorse)
			}
			if got.Glyph == "" {
				t.Error("every status needs a glyph")
			}
		})
	}
}

func TestRenderRisksEmpty(t *testing.T) {
	out := render(t, snapshot(90, 0.3), CommentOptions{})
	if !strings.Contains(out, "Detected Risks") || !strings.Contains(out, "None.") {
		t.Errorf("an empty risk list must be stated plainly:\n%s", out)
	}
}

func TestRenderRisksShowsEvidence(t *testing.T) {
	out := render(t, snapshot(70, 0.1, criticalRisk()), CommentOptions{})
	if !strings.Contains(out, "Confirmed hotspot") {
		t.Errorf("the risk title must appear:\n%s", out)
	}
	// The evidence line is what makes the claim checkable.
	if !strings.Contains(out, "742 code lines") {
		t.Errorf("the evidence must be included:\n%s", out)
	}
	if !strings.Contains(out, "internal/engine.go") {
		t.Errorf("the risk subject must appear:\n%s", out)
	}
}

func TestRenderRisksRespectsMaxRisks(t *testing.T) {
	var risks []models.Risk
	for i := 0; i < 12; i++ {
		r := criticalRisk()
		r.ID = string(rune('a' + i))
		risks = append(risks, r)
	}
	out := render(t, snapshot(70, 0.1, risks...), CommentOptions{MaxRisks: 3})
	if !strings.Contains(out, "Detected Risks (12)") {
		t.Errorf("the full count must be stated:\n%s", out)
	}
	if !strings.Contains(out, "9 further risk(s) omitted") {
		t.Errorf("the omission must be stated:\n%s", out)
	}
}

func TestEscapeCellProtectsTable(t *testing.T) {
	if got := escapeCell("a|b"); got != `a\|b` {
		t.Errorf("escapeCell = %q, want %q", got, `a\|b`)
	}
	if got := escapeCell("a\nb\r\nc"); strings.ContainsAny(got, "\n\r") {
		t.Errorf("escapeCell must strip newlines, got %q", got)
	}
}

func TestSigned(t *testing.T) {
	cases := map[float64]string{
		0:    "0",
		2:    "+2",
		-3.5: "-3.5",
		0.25: "+0.3",
	}
	for in, want := range cases {
		if got := signed(in); got != want {
			t.Errorf("signed(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestReadEventPullRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "event.json")
	body := `{
	  "action": "synchronize",
	  "number": 42,
	  "repository": {"full_name": "acme/widget"},
	  "pull_request": {
	    "number": 42,
	    "base": {"ref": "main", "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	    "head": {"ref": "feature/x", "sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	  }
	}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, err := ReadEvent(path)
	if err != nil {
		t.Fatalf("ReadEvent: %v", err)
	}
	if !ctx.IsPullRequest || ctx.Number != 42 {
		t.Errorf("unexpected PR context: %+v", ctx)
	}
	if ctx.BaseRef != "main" || ctx.HeadSHA[:4] != "bbbb" {
		t.Errorf("refs not parsed: %+v", ctx)
	}
	if ctx.Repository != "acme/widget" {
		t.Errorf("Repository = %q", ctx.Repository)
	}
	if ctx.Action != "synchronize" {
		t.Errorf("Action = %q", ctx.Action)
	}
}

// A push event has no pull_request object; that is not an error.
func TestReadEventNonPullRequest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "event.json")
	if err := os.WriteFile(path, []byte(`{"ref":"refs/heads/main"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, err := ReadEvent(path)
	if err != nil {
		t.Fatalf("ReadEvent: %v", err)
	}
	if ctx.IsPullRequest {
		t.Error("a push event must not be treated as a pull request")
	}
}

// A missing event file degrades to "no PR metadata", never to a failure: the
// comment can still be rendered from the snapshot alone.
func TestReadEventMissingFile(t *testing.T) {
	ctx, err := ReadEvent(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("a missing event file must not be an error: %v", err)
	}
	if ctx.IsPullRequest {
		t.Error("expected an empty context")
	}
}

func TestReadEventEmptyPath(t *testing.T) {
	ctx, err := ReadEvent("")
	if err != nil || ctx.IsPullRequest {
		t.Errorf("an empty path must yield an empty context, got %+v %v", ctx, err)
	}
}

func TestReadEventMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "event.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvent(path); err == nil {
		t.Fatal("malformed JSON must be reported, not silently ignored")
	}
}

// mustJSON serializes a snapshot the way `analyze --format json` does.
func mustJSON(t *testing.T, snap *models.Snapshot) []byte {
	t.Helper()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return data
}

func TestLoadBaselineRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")

	if err := os.WriteFile(path, mustJSON(t, snapshot(88, 0.25)), 0o644); err != nil {
		t.Fatal(err)
	}

	base, err := LoadBaseline(path)
	if err != nil {
		t.Fatalf("LoadBaseline: %v", err)
	}
	if base.Health.Score != 88 {
		t.Errorf("Score = %v, want 88", base.Health.Score)
	}
}

func TestLoadBaselineEmptyPath(t *testing.T) {
	base, err := LoadBaseline("")
	if err != nil || base != nil {
		t.Errorf("an empty path must yield a nil baseline, got %v %v", base, err)
	}
}

// A JSON file that is not an Lensyxe snapshot must be rejected clearly,
// otherwise the comparison would silently compare garbage.
func TestLoadBaselineRejectsForeignJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "other.json")
	if err := os.WriteFile(path, []byte(`{"hello":"world"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBaseline(path); err == nil {
		t.Fatal("a foreign JSON file must be rejected")
	}
}

func TestLoadBaselineMissingFile(t *testing.T) {
	if _, err := LoadBaseline(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("a missing baseline must be reported")
	}
}

func TestShortSHA(t *testing.T) {
	if got := shortSHA("abcdef1234567890"); got != "abcdef12" {
		t.Errorf("shortSHA = %q", got)
	}
	if got := shortSHA("abc"); got != "abc" {
		t.Errorf("a short input must pass through, got %q", got)
	}
}

func TestCommentIsDeterministic(t *testing.T) {
	snap := snapshot(86, 0.25, criticalRisk(), criticalRisk())
	opts := CommentOptions{Baseline: snapshot(88, 0.25), BaselineLabel: "main"}
	first := render(t, snap, opts)
	for i := 0; i < 5; i++ {
		if got := render(t, snap, opts); got != first {
			t.Fatalf("run %d differs from the first render", i)
		}
	}
}
