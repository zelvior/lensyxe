package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/zelvior/lensyxe/internal/decay"
)

// RenderDecay writes the activity-decay report.
//
// The three findings are printed in separate sections with their own caveats,
// because they are not equally well founded. Orphaned files are a fact about the
// repository; silos overlap the bus factor the git analyzer already reports; and
// the half-life may be absent entirely, which is stated rather than papered over.
func RenderDecay(w io.Writer, res *decay.Result) error {
	if res == nil {
		return fmt.Errorf("render decay: nil result")
	}
	var b strings.Builder

	b.WriteString(styleTitle.Render("lensyxe decay"))
	b.WriteByte('\n')
	b.WriteString(styleDim.Render(fmt.Sprintf(
		"%d commit(s) spanning %d day(s), %d tracked file(s), window %d day(s)",
		res.Commits, res.HistoryDays, res.Files, res.WindowDays)))
	b.WriteByte('\n')
	b.WriteString(rule())

	// ---- half-life, first, because it may not exist and that governs trust ----
	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("ACTIVITY HALF-LIFE"))
	b.WriteByte('\n')
	switch {
	case res.HalfLife == nil:
		b.WriteString("  " + styleWarn.Render(res.HalfLifeUnavailable) + "\n")
	case res.HalfLife.Days < 0:
		b.WriteString("  " + styleDim.Render(
			"Not computable: no activity in the recent half of the history, so "+
				"there is no rate to halve.") + "\n")
	case res.HalfLife.Days == 0:
		b.WriteString("  " + styleDim.Render(
			"No decay: change activity did not fall over this history.") + "\n")
		b.WriteString("  " + styleDim.Render(fmt.Sprintf(
			"%.2f commits/day earlier, %.2f commits/day recently.",
			res.HalfLife.PriorPerDay, res.HalfLife.RecentPerDay)) + "\n")
	default:
		b.WriteString("  " + scoreStyle(100-decayToScore(res.HalfLife.Days)).Render(
			fmt.Sprintf("%5.1f days", res.HalfLife.Days)) +
			styleDim.Render("  until change activity halves") + "\n")
		b.WriteString("  " + styleDim.Render(fmt.Sprintf(
			"fitted over %d commit(s) spanning %d day(s); %.2f commits/day "+
				"earlier, %.2f recently. Both rates are shown so the figure can "+
				"be checked rather than taken on trust.",
			res.HalfLife.Points, res.HistoryDays,
			res.HalfLife.PriorPerDay, res.HalfLife.RecentPerDay)) + "\n")
	}

	// ---- orphans ----
	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("UNCHANGED FILES"))
	if len(res.Orphans) == 0 {
		b.WriteByte('\n')
		b.WriteString("  " + styleDim.Render(
			"Every tracked file was modified inside the window.") + "\n")
	} else {
		b.WriteString(styleDim.Render(fmt.Sprintf("  (%d)", len(res.Orphans))))
		b.WriteByte('\n')
		for _, o := range res.Orphans {
			b.WriteString(fmt.Sprintf("  %s %s\n",
				styleDim.Render(fmt.Sprintf("%5d d", o.AgeDays)),
				truncate(o.Path, 52)))
		}
		b.WriteString("  " + styleDim.Render(
			"Tracked files with no change in the window. An untouched file is "+
				"not dead code, and this is not a prediction that it will be "+
				"removed: it is a list of places nobody has asked a question in.") + "\n")
	}

	// ---- silos ----
	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("KNOWLEDGE SILOS"))
	if len(res.Silos) == 0 {
		b.WriteByte('\n')
		b.WriteString("  " + styleDim.Render(
			"No file's history is dominated by a single contributor.") + "\n")
	} else {
		b.WriteString(styleDim.Render(fmt.Sprintf("  (%d)", len(res.Silos))))
		b.WriteByte('\n')
		for _, s := range res.Silos {
			b.WriteString(fmt.Sprintf("  %s %s\n",
				styleWarn.Render(fmt.Sprintf("%3.0f%%", s.Share*100)),
				truncate(s.Path, 42)))
			b.WriteString("    " + styleDim.Render(fmt.Sprintf(
				"%s wrote %d of %d commit(s)", truncate(s.Author, 28),
				s.AuthorCommits, s.TotalCommits)) + "\n")
		}
		b.WriteString("  " + styleDim.Render(
			"A file where one contributor wrote almost every commit. This is the "+
				"per-file form of a bus factor and it overlaps the repository-wide "+
				"bus factor `lensyxe analyze` reports; it is not an independent "+
				"second signal. A single-author file is not unmaintained code.") + "\n")
	}

	b.WriteString(rule())
	b.WriteString(styleDim.Render(
		"Reads git history only. No finding here is a prediction: nothing in "+
			"this tool decides that code will be deleted, ignored, or rewritten. "+
			"These are places where a question has not been asked yet.") + "\n")

	_, err := io.WriteString(w, b.String())
	return err
}

// decayToScore maps a half-life in days onto a 0-100 bar.
//
// Purely presentational, so it is clamped rather than allowed to leave the
// range: a repository with no activity at all should render a low bar rather
// than a negative one.
func decayToScore(days float64) float64 {
	if days <= 0 {
		return 100
	}
	if days >= 720 {
		return 0
	}
	return days / 720 * 100
}
