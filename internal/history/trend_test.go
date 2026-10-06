package history

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/internal/storage"
)

// rec builds a storage row for rendering tests.
func rec(root string, at time.Time, score float64, risks, hotspots int) storage.Record {
	return storage.Record{
		RecordedAt:   at,
		Root:         root,
		Commit:       "0123456789abcdef",
		Score:        score,
		Grade:        gradeOf(score),
		RiskCount:    risks,
		HotspotCount: hotspots,
		TestFiles:    10,
	}
}

// series returns rows climbing then falling, to exercise the graph.
func series() []storage.Record {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	scores := []float64{62, 71, 74, 78, 81, 85, 88, 84, 79, 66}
	out := make([]storage.Record, 0, len(scores))
	for i, s := range scores {
		out = append(out, rec("/repo", base.Add(time.Duration(i)*time.Hour), s, 3+i%2, i%3))
	}
	return out
}

func now() time.Time { return time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC) }

func TestBuildPointsMarksTrendDirection(t *testing.T) {
	pts := BuildPoints(series(), now())
	if len(pts) != 10 {
		t.Fatalf("points = %d, want 10", len(pts))
	}
	want := []string{"●", "▲", "▲", "▲", "▲", "▲", "▲", "▼", "▼", "▼"}
	for i, w := range want {
		if pts[i].Trend.Glyph != w {
			t.Errorf("point %d glyph = %q, want %q", i, pts[i].Trend.Glyph, w)
		}
	}
	// The first point has nothing to compare against.
	if pts[0].Trend.HasPrevious {
		t.Error("the first point must not claim a previous run")
	}
	if pts[0].Trend.Delta != 0 {
		t.Errorf("first delta = %v, want 0", pts[0].Trend.Delta)
	}
	if !pts[1].Trend.HasPrevious {
		t.Error("later points must have a baseline")
	}
	if pts[1].Trend.Delta != 9 { // 71 - 62
		t.Errorf("delta = %v, want 9", pts[1].Trend.Delta)
	}
}

func TestBuildPointsFlatUsesNoChangeGlyph(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := []storage.Record{
		rec("/repo", base, 80, 1, 0),
		rec("/repo", base.Add(time.Hour), 80, 1, 0),
		rec("/repo", base.Add(2*time.Hour), 80, 1, 0),
	}
	pts := BuildPoints(rows, now())
	for i, p := range pts[1:] {
		if p.Trend.Glyph != "▬" {
			t.Errorf("point %d glyph = %q, want ▬", i+1, p.Trend.Glyph)
		}
		if p.Trend.Delta != 0 {
			t.Errorf("point %d delta = %v, want 0", i+1, p.Trend.Delta)
		}
	}
}

func TestBuildPointsRiskDelta(t *testing.T) {
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rows := []storage.Record{
		rec("/repo", base, 80, 5, 0),
		rec("/repo", base.Add(time.Hour), 80, 8, 1),
	}
	pts := BuildPoints(rows, now())
	if pts[0].Record.HasRiskDelta {
		t.Error("the first run has no risk delta")
	}
	if !pts[1].Record.HasRiskDelta {
		t.Fatal("later runs must carry a risk delta")
	}
	if pts[1].Record.RiskDelta != 3 {
		t.Errorf("RiskDelta = %d, want 3", pts[1].Record.RiskDelta)
	}
}

func TestRenderGraphShape(t *testing.T) {
	pts := BuildPoints(series(), now())
	lines, lo, hi := RenderGraph(pts, false)

	if len(lines) != graphHeight+1 {
		t.Fatalf("lines = %d, want %d (%d rows plus a baseline)",
			len(lines), graphHeight+1, graphHeight)
	}
	// Axis bounds must bracket every sample.
	for _, p := range pts {
		if p.Record.Score < lo || p.Record.Score > hi {
			t.Errorf("score %v outside axis %v..%v", p.Record.Score, lo, hi)
		}
	}
	if lo < 0 || hi > 100 {
		t.Errorf("axis %v..%v must stay within 0..100", lo, hi)
	}
	// Every data point must be plotted exactly once.
	plotted := 0
	for _, line := range lines {
		plotted += strings.Count(line, "●")
	}
	if plotted != len(pts) {
		t.Errorf("plotted %d points, want %d", plotted, len(pts))
	}
	// The axis must be labelled.
	if !strings.Contains(lines[0], "┤") {
		t.Errorf("first line missing axis marker: %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "└") {
		t.Errorf("baseline missing corner: %q", lines[len(lines)-1])
	}
}

func TestRenderGraphIsDeterministic(t *testing.T) {
	pts := BuildPoints(series(), now())
	first, _, _ := RenderGraph(pts, false)
	for i := 0; i < 10; i++ {
		got, _, _ := RenderGraph(pts, false)
		if strings.Join(got, "\n") != strings.Join(first, "\n") {
			t.Fatalf("run %d graph differs", i)
		}
	}
}

// A single point must still render, with an axis that does not divide by zero.
func TestRenderGraphSinglePoint(t *testing.T) {
	pts := BuildPoints([]storage.Record{rec("/repo", now(), 77, 1, 0)}, now())
	lines, lo, hi := RenderGraph(pts, false)
	if len(lines) != graphHeight+1 {
		t.Fatalf("lines = %d", len(lines))
	}
	if hi <= lo {
		t.Errorf("axis %v..%v must have positive span", lo, hi)
	}
	joined := strings.Join(lines, "\n")
	if strings.Count(joined, "●") != 1 {
		t.Errorf("expected exactly one plotted point:\n%s", joined)
	}
}

func TestAxisBoundsMinimumSpan(t *testing.T) {
	// A nearly-flat series must not be drawn as a cliff.
	lo, hi := axisBounds(80, 81)
	if hi-lo < 20 {
		t.Errorf("span %v..%v = %v, want at least 20", lo, hi, hi-lo)
	}
}

func TestAxisBoundsClampsToDomain(t *testing.T) {
	lo, hi := axisBounds(-5, 250)
	if lo < 0 {
		t.Errorf("lo = %v, want >= 0", lo)
	}
	if hi > 100 {
		t.Errorf("hi = %v, want <= 100", hi)
	}
}

func TestRenderGraphCompressesLongSeries(t *testing.T) {
	// More runs than the graph is wide must still fit inside the width.
	var rows []storage.Record
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < graphWidth*3; i++ {
		rows = append(rows, rec("/repo", base.Add(time.Duration(i)*time.Minute), 50+float64(i%40), 1, 0))
	}
	pts := BuildPoints(rows, now())
	lines, _, _ := RenderGraph(pts, false)
	// Every rendered row must fit the expected width: 6 label + 1 marker +
	// graphWidth cells.
	maxWidth := 6 + 1 + graphWidth
	for _, line := range lines {
		if w := len([]rune(line)); w > maxWidth {
			t.Errorf("line width %d exceeds %d: %q", w, maxWidth, line)
		}
	}
	// The final column must always reflect the most recent sample.
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "●") {
		t.Error("no points plotted")
	}
}

// A compressed graph must end at the latest value, not a stale bucket.
func TestRenderGraphLastColumnIsLatest(t *testing.T) {
	var rows []storage.Record
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	// Flat 90 everywhere except the final sample, which collapses to 10.
	for i := 0; i < graphWidth*2; i++ {
		score := 90.0
		if i == graphWidth*2-1 {
			score = 10
		}
		rows = append(rows, rec("/repo", base.Add(time.Duration(i)*time.Minute), score, 1, 0))
	}
	pts := BuildPoints(rows, now())
	lines, _, _ := RenderGraph(pts, false)

	// The lowest plotted row must contain the final column's marker, proving the
	// newest sample is on the right.
	found := false
	for _, line := range lines {
		cells := []rune(line)
		if len(cells) < 7 {
			continue
		}
		tail := cells[7:]
		if len(tail) > 0 && tail[len(tail)-1] == '●' && strings.Contains(line, " 10") {
			found = true
		}
	}
	if !found {
		t.Errorf("the newest (lowest) sample should appear in the rightmost column:\n%s",
			strings.Join(lines, "\n"))
	}
}

func TestRenderProducesFullReport(t *testing.T) {
	rows := series()
	pts := BuildPoints(rows, now())
	delta := storage.ScoreDelta{
		HasPrevious: true,
		Latest:      rows[len(rows)-1],
		Previous:    rows[len(rows)-2],
		Delta:       66 - 79,
		Trend:       "declining",
	}

	var buf bytes.Buffer
	if err := Render(&buf, pts, delta, Options{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"Engineering health timeline",
		"SUMMARY", "first", "latest", "best", "worst", "change",
		"last delta", "RUNS (newest first)",
		"axis", "run(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
	// No ANSI escapes: this output is meant for issues and commit messages.
	if strings.Contains(out, "\x1b[") {
		t.Error("history output must not contain ANSI escapes")
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	rows := series()
	pts := BuildPoints(rows, now())
	delta := storage.ScoreDelta{HasPrevious: true, Latest: rows[9], Previous: rows[8], Delta: -13}

	var first bytes.Buffer
	if err := Render(&first, pts, delta, Options{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	for i := 0; i < 10; i++ {
		var got bytes.Buffer
		if err := Render(&got, BuildPoints(rows, now()), delta, Options{}); err != nil {
			t.Fatalf("Render: %v", err)
		}
		if got.String() != first.String() {
			t.Fatalf("run %d differs", i)
		}
	}
}

func TestRenderEmptyHistory(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, nil, storage.ScoreDelta{}, Options{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), "No history recorded yet") {
		t.Errorf("expected a guidance message, got %q", buf.String())
	}
}

func TestRenderSingleRunHasNoDelta(t *testing.T) {
	pts := BuildPoints([]storage.Record{rec("/repo", now(), 80, 2, 1)}, now())
	var buf bytes.Buffer
	if err := Render(&buf, pts, storage.ScoreDelta{HasPrevious: false}, Options{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), "only one run recorded") {
		t.Errorf("expected an n/a delta note, got %q", buf.String())
	}
}

func TestRenderGridOption(t *testing.T) {
	pts := BuildPoints(series(), now())
	var with, without bytes.Buffer
	if err := Render(&with, pts, storage.ScoreDelta{}, Options{ShowGrid: true}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := Render(&without, pts, storage.ScoreDelta{}, Options{ShowGrid: false}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if with.String() == without.String() {
		t.Error("ShowGrid must change the output")
	}
}

func TestRenderCustomTitle(t *testing.T) {
	pts := BuildPoints(series(), now())
	var buf bytes.Buffer
	if err := Render(&buf, pts, storage.ScoreDelta{}, Options{Title: "My Trend"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "My Trend\n") {
		t.Errorf("custom title not used: %q", buf.String()[:20])
	}
}

func TestFormatTimestampAddsYearOnlyWhenDifferent(t *testing.T) {
	sameYear := time.Date(2026, 5, 5, 10, 0, 0, 0, time.UTC)
	got := formatTimestamp(sameYear, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if got != "2026-05-05 10:00:00" {
		t.Errorf("same-year format = %q", got)
	}
	otherYear := time.Date(2025, 5, 5, 10, 0, 0, 0, time.UTC)
	if got := formatTimestamp(otherYear, sameYear); got != "2025-05-05" {
		t.Errorf("other-year format = %q", got)
	}
	if got := formatTimestamp(time.Time{}, sameYear); got != "unknown" {
		t.Errorf("zero time = %q, want unknown", got)
	}
}

func TestShortCommit(t *testing.T) {
	if got := shortCommit("0123456789abcdef"); got != "01234567" {
		t.Errorf("shortCommit = %q", got)
	}
	if got := shortCommit("abc"); got != "abc" {
		t.Errorf("shortCommit = %q", got)
	}
	if got := shortCommit(""); got != "-" {
		t.Errorf("shortCommit(empty) = %q, want -", got)
	}
}

func TestSigned(t *testing.T) {
	cases := map[float64]string{
		5: "+5.0", -3: "-3.0", 0: "0.0",
		// Sub-epsilon movement must render as no change, matching the glyph.
		0.04: "0.0", -0.04: "0.0", 0.05: "0.0", -0.05: "0.0",
		0.06: "+0.1", -0.06: "-0.1",
	}
	for in, want := range cases {
		if got := signed(in); got != want {
			t.Errorf("signed(%v) = %q, want %q", in, got, want)
		}
	}
}

// The glyph and the signed text must never disagree about direction.
func TestGlyphAndTextAgreeOnDirection(t *testing.T) {
	rows := []storage.Record{
		rec("/repo", now(), 80.00, 1, 0),
		rec("/repo", now().Add(time.Hour), 80.04, 1, 0),   // +0.04: noise
		rec("/repo", now().Add(2*time.Hour), 80.10, 1, 0), // +0.06: real
	}
	pts := BuildPoints(rows, now())
	if pts[1].Trend.Delta != 0.04 {
		t.Fatalf("precondition: delta = %v, want 0.04", pts[1].Trend.Delta)
	}
	if pts[1].Trend.Glyph != "▬" {
		t.Errorf("0.04 glyph = %q, want ▬", pts[1].Trend.Glyph)
	}
	if got := signed(pts[1].Trend.Delta); got != "0.0" {
		t.Errorf("0.04 signed = %q, want 0.0", got)
	}
	if pts[2].Trend.Delta != 0.06 {
		t.Fatalf("precondition: delta = %v, want 0.06", pts[2].Trend.Delta)
	}
	if pts[2].Trend.Glyph != "▲" {
		t.Errorf("0.06 glyph = %q, want ▲", pts[2].Trend.Glyph)
	}
	if got := signed(pts[2].Trend.Delta); got != "+0.1" {
		t.Errorf("0.06 signed = %q, want +0.1", got)
	}
}

// Storage rows and rendered output must agree on root-independent values.
func TestPointsPreserveScore(t *testing.T) {
	rows := series()
	pts := BuildPoints(rows, now())
	for i := range rows {
		if pts[i].Record.Score != rows[i].Score {
			t.Errorf("point %d score = %v, want %v", i, pts[i].Record.Score, rows[i].Score)
		}
		if pts[i].Record.Root != rows[i].Root {
			t.Errorf("point %d root = %q", i, pts[i].Record.Root)
		}
	}
}

// Guard the storage contract this renderer depends on: records must be
// constructible without the database so the renderer stays testable in
// isolation.
func TestRenderDoesNotRequireStorage(t *testing.T) {
	rows := []storage.Record{rec(filepath.ToSlash("/repo"), now(), 50, 0, 0)}
	var buf bytes.Buffer
	if err := Render(&buf, BuildPoints(rows, now()), storage.ScoreDelta{}, Options{}); err != nil {
		t.Fatalf("Render must not touch the database: %v", err)
	}
}
