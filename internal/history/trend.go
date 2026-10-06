// Package history renders stored analysis snapshots as a terminal timeline.
//
// The renderer is deterministic: the same records always produce byte-identical
// output, with no ANSI colour. Colour would be hostile here because the output
// is meant to be pasted into an issue or a commit message.
package history

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/zelvior/lensyxe/internal/storage"
	"github.com/zelvior/lensyxe/pkg/models"
)

// Graph dimensions. The width is fixed so successive runs line up vertically in
// a terminal, which matters when comparing two reports side by side.
const (
	graphWidth  = 60
	graphHeight = 11
)

// Options tune the timeline rendering.
type Options struct {
	// Title overrides the generated heading.
	Title string
	// ShowGrid adds vertical axis rules between rows.
	ShowGrid bool
}

// Trend is a single run's movement relative to the one before it.
type Trend struct {
	Score float64
	Delta float64
	// HasPrevious is false for the first entry, which has nothing to compare to.
	HasPrevious bool
	// Glyph summarizes the direction: ▲ improved, ▼ declined, ● first run,
	// ▬ unchanged.
	Glyph string
}

// Point is one entry in the rendered timeline.
type Point struct {
	Record Record
	Trend  Trend
}

// Record wraps a storage row with the derived values the renderer needs.
type Record struct {
	storage.Record
	// Timestamp is a pre-formatted UTC label.
	Timestamp string
	// CommitShort is the abbreviated commit hash.
	CommitShort string
	// RiskDelta is the change in risk count versus the previous run.
	RiskDelta int
	// HasRiskDelta is false for the first entry.
	HasRiskDelta bool
}

// BuildPoints converts storage rows into renderable points.
func BuildPoints(records []storage.Record, now time.Time) []Point {
	out := make([]Point, 0, len(records))
	for i, rec := range records {
		p := Point{
			Record: Record{
				Record:      rec,
				Timestamp:   formatTimestamp(rec.RecordedAt, now),
				CommitShort: shortCommit(rec.Commit),
			},
		}
		p.Trend = Trend{Score: rec.Score}
		if i > 0 {
			prev := records[i-1]
			p.Trend.HasPrevious = true
			p.Trend.Delta = round2(rec.Score - prev.Score)
			p.Record.HasRiskDelta = true
			p.Record.RiskDelta = rec.RiskCount - prev.RiskCount
		}
		switch {
		case i == 0:
			p.Trend.Glyph = "●"
		case p.Trend.Delta > epsilon:
			p.Trend.Glyph = "▲"
		case p.Trend.Delta < -epsilon:
			p.Trend.Glyph = "▼"
		default:
			p.Trend.Glyph = "▬"
		}
		out = append(out, p)
	}
	return out
}

// Render writes the timeline to w.
func Render(w io.Writer, points []Point, delta storage.ScoreDelta, opts Options) error {
	if len(points) == 0 {
		_, err := io.WriteString(w, "No history recorded yet. Run `lensyxe analyze` to record a snapshot.\n")
		return err
	}

	var b strings.Builder

	title := opts.Title
	if title == "" {
		title = "Engineering health timeline"
	}
	b.WriteString(title + "\n\n")

	// The score axis is the headline. It renders first so a reader sees the
	// trend before any detail.
	graph, lo, hi := RenderGraph(points, opts.ShowGrid)
	for _, line := range graph {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	// Axis range, so the vertical scale is not a mystery.
	b.WriteString(fmt.Sprintf("  axis %.0f..%.0f across %d run(s)\n\n", lo, hi, len(points)))

	// Summary statistics.
	first, last := points[0], points[len(points)-1]
	total := round2(last.Record.Score - first.Record.Score)
	best, worst := points[0], points[0]
	for _, p := range points {
		if p.Record.Score > best.Record.Score {
			best = p
		}
		if p.Record.Score < worst.Record.Score {
			worst = p
		}
	}

	b.WriteString("SUMMARY\n")
	b.WriteString(fmt.Sprintf("  first    %6.1f  %s  %s\n",
		first.Record.Score, first.Record.Timestamp, first.Record.CommitShort))
	b.WriteString(fmt.Sprintf("  latest   %6.1f  %s  %s\n",
		last.Record.Score, last.Record.Timestamp, last.Record.CommitShort))
	b.WriteString(fmt.Sprintf("  best     %6.1f  %s\n", best.Record.Score, best.Record.Timestamp))
	b.WriteString(fmt.Sprintf("  worst    %6.1f  %s\n", worst.Record.Score, worst.Record.Timestamp))
	b.WriteString(fmt.Sprintf("  change   %s over %d run(s), average %s per run\n",
		signed(total), len(points), signed(round2(total/float64(len(points))))))

	// Latest delta against the stored baseline.
	b.WriteByte('\n')
	if delta.HasPrevious {
		b.WriteString(fmt.Sprintf("  last delta  %s (%s -> %s, %s)\n",
			signed(delta.Delta),
			fmt.Sprintf("%.1f", delta.Previous.Score),
			fmt.Sprintf("%.1f", delta.Latest.Score),
			delta.Trend))
	} else {
		b.WriteString("  last delta  n/a (only one run recorded)\n")
	}
	b.WriteByte('\n')

	// Per-run table.
	b.WriteString("RUNS (newest first)\n")
	b.WriteString(fmt.Sprintf("  %-6s %-20s %-10s %-14s %6s %6s %6s %5s\n",
		"", "WHEN", "COMMIT", "TREND", "SCORE", "RISKS", "HOTSP", "TESTS"))
	for i := len(points) - 1; i >= 0; i-- {
		p := points[i]
		trend := p.Trend.Glyph + " " + firstRun(p)
		b.WriteString(fmt.Sprintf("  %-20s %-10s %-14s %6.1f %6s %6d %5d\n",
			p.Record.Timestamp,
			p.Record.CommitShort,
			trend,
			p.Record.Score,
			riskDeltaText(p.Record),
			p.Record.HotspotCount,
			p.Record.TestFiles))
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("history: render: %w", err)
	}
	return nil
}

// RenderGraph draws the score timeline and returns the lines plus the axis
// bounds it used.
//
// The chart is a step plot: each run occupies one column, and a change between
// runs is drawn as a vertical connector. A step plot is the honest choice here
// because each column is a discrete measurement, not a sampled series;
// interpolating between them would invent data that was never observed.
func RenderGraph(points []Point, showGrid bool) (lines []string, lo, hi float64) {
	if len(points) == 0 {
		return nil, 0, 0
	}

	scores := make([]float64, len(points))
	for i, p := range points {
		scores[i] = p.Record.Score
	}
	lo, hi = bounds(scores)

	// Scale the y axis to the data, rounded outward to a friendly step so the
	// axis labels are round numbers an engineer recognises.
	loStep, hiStep := axisBounds(lo, hi)

	// Column per run, capped at the graph width; extra runs collapse into the
	// rightmost column rather than being silently dropped.
	width := graphWidth
	if width > len(points) {
		width = len(points)
	}
	if width < 1 {
		width = 1
	}

	// Bucket the scores into columns, keeping the last sample in each bucket so
	// a compressed graph still ends at the most recent value.
	colScore := make([]float64, width)
	colIdx := make([]int, width)
	for i := range colScore {
		// Default each column to the sample nearest its centre.
		centre := (float64(i) + 0.5) * float64(len(points)) / float64(width)
		idx := int(centre)
		if idx >= len(points) {
			idx = len(points) - 1
		}
		colIdx[i] = idx
		colScore[i] = scores[idx]
	}
	// The final column must always be the final sample.
	colIdx[width-1] = len(points) - 1
	colScore[width-1] = scores[len(points)-1]

	// Map score to row index, 0 at the top.
	toRow := func(score float64) int {
		if hiStep == loStep {
			return 0
		}
		frac := (score - loStep) / (hiStep - loStep)
		frac = clamp01(frac)
		row := int(round2((1 - frac) * float64(graphHeight-1)))
		if row < 0 {
			row = 0
		}
		if row > graphHeight-1 {
			row = graphHeight - 1
		}
		return row
	}

	rows := make([][]rune, graphHeight)
	for i := range rows {
		rows[i] = make([]rune, width)
		for j := range rows[i] {
			rows[i][j] = ' '
		}
	}

	for i := 0; i < width; i++ {
		row := toRow(colScore[i])
		rows[row][i] = '●'
		if i > 0 {
			prevRow := toRow(colScore[i-1])
			// Draw the vertical connector between consecutive samples.
			loR, hiR := prevRow, row
			if loR > hiR {
				loR, hiR = hiR, loR
			}
			for r := loR + 1; r < hiR; r++ {
				if rows[r][i] == ' ' {
					rows[r][i] = '│'
				}
			}
			// Extend the previous column horizontally toward this one so the
			// line reads as continuous when values are close.
			if prevRow == row {
				rows[row][i-1] = '─'
			}
		}
	}

	lines = make([]string, 0, graphHeight)
	for r, cells := range rows {
		// Label every other row to keep the axis readable without crowding.
		labelValue := hiStep - (hiStep-loStep)*float64(r)/float64(graphHeight-1)
		var b strings.Builder
		if r%2 == 0 || r == graphHeight-1 {
			b.WriteString(fmt.Sprintf("%5.0f ┤", labelValue))
		} else {
			b.WriteString("      │")
		}
		if showGrid {
			b.WriteRune('│')
		}
		b.WriteString(string(cells))
		lines = append(lines, strings.TrimRight(b.String(), " "))
	}

	// Baseline plus a commit axis.
	base := "      └" + strings.Repeat("─", width)
	lines = append(lines, strings.TrimRight(base, " "))
	return lines, loStep, hiStep
}

// bounds returns the min and max score across samples.
func bounds(scores []float64) (float64, float64) {
	if len(scores) == 0 {
		return 0, 100
	}
	lo, hi := scores[0], scores[0]
	for _, s := range scores {
		if s < lo {
			lo = s
		}
		if s > hi {
			hi = s
		}
	}
	return lo, hi
}

// axisBounds widens the data range outward to multiples of 10, with a minimum
// span of 20 so a nearly-flat series is not drawn as a cliff.
func axisBounds(lo, hi float64) (float64, float64) {
	if hi-lo < 20 {
		mid := (lo + hi) / 2
		lo = mid - 10
		hi = mid + 10
	}
	// Clamp into the valid score domain.
	if lo < 0 {
		lo = 0
	}
	if hi > 100 {
		hi = 100
	}
	step := 10.0
	newLo := float64(int(lo/step)) * step
	newHi := float64(int(hi/step)+1) * step
	if newHi > 100 {
		newHi = 100
	}
	if newHi-newLo < step {
		newHi = newLo + step
	}
	return newLo, newHi
}

// riskDeltaText renders the risk-count change against the previous run.
func riskDeltaText(r Record) string {
	if !r.HasRiskDelta {
		return "-"
	}
	if r.RiskDelta == 0 {
		return "="
	}
	return signedInt(r.RiskDelta)
}

func firstRun(p Point) string {
	if !p.Trend.HasPrevious {
		return "first run"
	}
	if p.Trend.Delta > -epsilon && p.Trend.Delta < epsilon {
		return "no change"
	}
	return fmt.Sprintf("%s %s", p.Trend.Glyph, signed(p.Trend.Delta))
}

// formatTimestamp renders a UTC time, adding the year only when it differs
// from the reference time so repeated runs stay narrow.
func formatTimestamp(t, now time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	t = t.UTC()
	if t.Year() == now.UTC().Year() {
		return t.Format("2006-01-02 15:04:05")
	}
	return t.Format("2006-01-02")
}

func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	if commit == "" {
		return "-"
	}
	return commit
}

// epsilon is the smallest movement treated as a real change. It matches the
// threshold used by storage when classifying a trend, so a delta can never be
// rendered as "+0.0" alongside a "no change" glyph.
const epsilon = 0.05

func signed(v float64) string {
	if v > epsilon {
		return fmt.Sprintf("+%.1f", v)
	}
	if v < -epsilon {
		return fmt.Sprintf("%.1f", v)
	}
	return "0.0"
}

func signedInt(v int) string {
	if v > 0 {
		return fmt.Sprintf("+%d", v)
	}
	return fmt.Sprintf("%d", v)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func round2(v float64) float64 {
	scaled := v * 100
	if scaled >= 0 {
		scaled = float64(int64(scaled + 0.5))
	} else {
		scaled = float64(int64(scaled - 0.5))
	}
	return scaled / 100
}

// gradeOf is exposed for callers that render a legend.
func gradeOf(score float64) string { return models.Grade(score) }
