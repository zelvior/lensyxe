package report

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/zelvior/lensyxe/internal/blast"
)

// RenderBlast writes the co-change report.
//
// The ordering of the sections is the argument the report makes: what was
// measured, whether it is trustworthy, then the predictions. A warning shown
// before the reader knows the analysis was sound is just noise, and a warning
// shown after is advice.
func RenderBlast(w io.Writer, res *blast.Result) error {
	if res == nil {
		return fmt.Errorf("render blast: nil result")
	}
	var b strings.Builder

	b.WriteString(styleTitle.Render("lensyxe blast"))
	b.WriteByte('\n')
	b.WriteString(styleDim.Render(fmt.Sprintf(
		"%d commit(s) of history, %s .. %s",
		res.Commits,
		shortDate(res.FirstCommitAt), shortDate(res.LastCommitAt))))
	b.WriteByte('\n')
	b.WriteString(rule())

	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("CHANGESET"))
	b.WriteByte('\n')
	for _, f := range res.Changeset {
		if contains(res.Missing, f) {
			b.WriteString("  " + styleBad.Render(f) + " " +
				styleDim.Render("never committed; no coupling can exist for it") + "\n")
			continue
		}
		b.WriteString("  " + f + "\n")
	}

	// The data limit comes before every number, because it governs them all.
	if res.InsufficientHistory != "" {
		b.WriteByte('\n')
		b.WriteString(styleHeader.Render("NO COUPLING REPORTED"))
		b.WriteByte('\n')
		b.WriteString("  " + styleWarn.Render(res.InsufficientHistory) + "\n")
		b.WriteString(rule())
		_, err := io.WriteString(w, b.String())
		return err
	}

	if len(res.Predictions) == 0 {
		b.WriteByte('\n')
		b.WriteString(styleHeader.Render("PREDICTED AFFECTED FILES"))
		b.WriteByte('\n')
		b.WriteString("  " + styleDim.Render(
			"None. No file in this changeset has historically co-changed with "+
				"another file above the coupling threshold."))
		b.WriteString("\n")
		b.WriteString(rule())
		_, err := io.WriteString(w, b.String())
		return err
	}

	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("PREDICTED AFFECTED FILES"))
	b.WriteByte('\n')
	b.WriteString(styleDim.Render(
		"Not in this changeset, but co-changed with it before. Ordered by " +
			"distance, then by how strongly the two files move together."))
	b.WriteByte('\n')
	for _, p := range res.Predictions {
		b.WriteString("  " + couplingBar(p.Coupling) + " " + styleWarn.Render(p.Path) + "\n")
		b.WriteString("    " + styleDim.Render(fmt.Sprintf(
			"%s  co-changed in %d commit(s)  via %s",
			hopLabel(p.Hops), p.SharedCommits, p.Via)) + "\n")
	}

	if len(res.Pairs) > 0 {
		b.WriteByte('\n')
		b.WriteString(styleHeader.Render("STRONGEST COUPLINGS IN THE REPOSITORY"))
		b.WriteByte('\n')
		for i, p := range res.Pairs {
			if i >= topPairsShown {
				break
			}
			b.WriteString(fmt.Sprintf("  %s %s\n", couplingBar(p.Coupling),
				styleLabel.Render(fmt.Sprintf("%.0f%%", p.Coupling*100))))
			b.WriteString("    " + truncate(p.A, 44) + "\n")
			b.WriteString("    " + truncate(p.B, 44) + "\n")
			b.WriteString("    " + styleDim.Render(fmt.Sprintf(
				"co-changed in %d commit(s); individually %d and %d",
				p.SharedCommits, p.CommitsA, p.CommitsB)) + "\n")
		}
		if n := len(res.Pairs) - topPairsShown; n > 0 {
			b.WriteString("  " + styleDim.Render(
				fmt.Sprintf("... and %d more pair(s)", n)) + "\n")
		}
	}

	b.WriteString(rule())
	b.WriteString(styleDim.Render(
		"Co-change is an association observed in commits, not a dependency " +
			"analysis. Two files that always changed together may have been " +
			"bumped together rather than refactored together, and this cannot " +
			"tell the difference."))
	b.WriteByte('\n')

	_, err := io.WriteString(w, b.String())
	return err
}

// topPairsShown bounds the coupling list. The predictions are the answer; this
// section is the evidence behind them.
const topPairsShown = 5

// couplingBar renders a 0..1 value as a fixed-width bar.
//
// Fixed width rather than proportional, so the numbers in the column stay
// aligned and the eye can compare them without decoding bar lengths.
func couplingBar(v float64) string {
	const cells = 10
	filled := int(v*cells + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > cells {
		filled = cells
	}
	return styleDim.Render("[" + strings.Repeat("#", filled) +
		strings.Repeat(".", cells-filled) + "]")
}

func hopLabel(h int) string {
	switch h {
	case 1:
		return "1 hop"
	case 2:
		return "2 hops"
	default:
		return fmt.Sprintf("%d hops", h)
	}
}

func shortDate(t time.Time) string {
	if t.IsZero() {
		return "no history"
	}
	return t.Format("2006-01-02")
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
