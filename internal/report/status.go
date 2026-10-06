package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/zelvior/lensyxe/pkg/models"
)

// RenderStatus writes the short "where does this repository stand" view.
//
// It exists because `analyze` is a full report and nobody reads a full report
// when they only want the current state, which is the same reason git has
// `status` alongside everything else. Deliberately different from analyze: this
// omits the language table, the hotspot list, the churn detail, and the evidence
// behind each risk, and ends by naming the command that shows them.
//
// The layout borrows from `git status` rather than from the rest of this tool's
// output, because that is the mental model someone arriving from git brings with
// them: a headline, the facts that matter, then what to do next.
func RenderStatus(w io.Writer, snap *models.Snapshot) error {
	if snap == nil {
		return fmt.Errorf("render status: nil snapshot")
	}
	var b strings.Builder

	// snap.Tool is already the executable name, so it is not prefixed again.
	b.WriteString(styleTitle.Render(fmt.Sprintf("%s %s  engineering health",
		snap.Tool, snap.Version)))
	b.WriteByte('\n')
	b.WriteString(styleDim.Render("path     " + snap.Root))
	b.WriteByte('\n')
	if snap.Git.IsRepository {
		b.WriteString(styleDim.Render(fmt.Sprintf("branch   %s @ %s",
			orUnknown(snap.Git.Branch), orNone(snap.Git.HeadCommit))))
		b.WriteByte('\n')
	}
	b.WriteString(rule())

	// The headline, then the three dimensions. A metric that could not be
	// measured is stated as such rather than drawn as a zero, for the same
	// reason the full report does it: a blind spot is not a finding.
	headline := fmt.Sprintf("%.1f / 100   %s", snap.Health.Score, snap.Health.Grade)
	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("HEALTH"))
	b.WriteByte('\n')
	b.WriteString("  " + scoreStyle(snap.Health.Score).Render(headline))
	b.WriteByte('\n')

	weakest := weakestMetric(snap.Health.Metrics)
	for _, m := range snap.Health.Metrics {
		if !m.Applicable {
			fmt.Fprintf(&b, "  %-24s %s\n", truncate(m.Label, 24),
				styleDim.Render("not measured"))
			continue
		}
		fmt.Fprintf(&b, "  %-24s %s %s\n",
			truncate(m.Label, 24),
			scoreStyle(m.Score).Render(fmt.Sprintf("%5.1f", m.Score)),
			styleDim.Render(fmt.Sprintf("%4.0f%%", m.Weight*100)))
	}

	// Risks, summarised by severity and then listed. The count line is what
	// someone scanning output actually reads, so it leads.
	if len(snap.Risks) > 0 {
		b.WriteByte('\n')
		crit, high := 0, 0
		for _, r := range snap.Risks {
			switch r.Severity {
			case models.SeverityCritical:
				crit++
			case models.SeverityHigh:
				high++
			}
		}
		fmt.Fprintf(&b, "  %d risk(s)", len(snap.Risks))
		if crit > 0 {
			b.WriteString(", " + styleCritical.Render(fmt.Sprintf("%d critical", crit)))
		}
		if high > 0 {
			b.WriteString(", " + styleWarn.Render(fmt.Sprintf("%d high", high)))
		}
		b.WriteByte('\n')
		for _, r := range topRisks(snap.Risks, 3) {
			b.WriteString("    " + severityStyle(r.Severity).Render(
				fmt.Sprintf("%-9s", strings.ToUpper(string(r.Severity)))) +
				truncate(r.Title, 60) + "\n")
		}
		if rest := len(snap.Risks) - 3; rest > 0 {
			fmt.Fprintf(&b, "    %s\n", styleDim.Render(
				fmt.Sprintf("... and %d more", rest)))
		}
	}

	// The "what now" line. Naming the weakest dimension is the difference
	// between a number and something actionable.
	if weakest != nil {
		b.WriteByte('\n')
		fmt.Fprintf(&b, "  %s %s at %.1f\n", styleDim.Render("weakest:"),
			weakest.Label, weakest.Score)
	}
	b.WriteString("  " + styleDim.Render("full report:  lensyxe analyze .") + "\n")
	b.WriteString(rule())
	fmt.Fprintf(&b, "%s\n", styleDim.Render(
		fmt.Sprintf("schema %s - analysis made no network call", snap.SchemaVersion)))

	_, err := io.WriteString(w, b.String())
	return err
}

// weakestMetric returns the lowest-scoring applicable metric, or nil.
func weakestMetric(metrics []models.Metric) *models.Metric {
	var worst *models.Metric
	for i := range metrics {
		m := &metrics[i]
		if !m.Applicable {
			continue
		}
		if worst == nil || m.Score < worst.Score {
			worst = m
		}
	}
	return worst
}

// topRisks returns the most severe risks, worst first, capped at n.
//
// The ordering is by severity and then by descending impact so the same risks
// appear first on every run; without the impact tiebreak, risks of equal
// severity would reorder between runs and the list would look unstable.
func topRisks(risks []models.Risk, n int) []models.Risk {
	sorted := make([]models.Risk, len(risks))
	copy(sorted, risks)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0; j-- {
			a, b := sorted[j-1], sorted[j]
			if severityRank(b.Severity) > severityRank(a.Severity) ||
				(severityRank(b.Severity) == severityRank(a.Severity) && b.Impact > a.Impact) {
				sorted[j-1], sorted[j] = b, a
				continue
			}
			break
		}
	}
	if len(sorted) > n {
		sorted = sorted[:n]
	}
	return sorted
}

func severityRank(s models.Severity) int {
	switch s {
	case models.SeverityCritical:
		return 4
	case models.SeverityHigh:
		return 3
	case models.SeverityMedium:
		return 2
	case models.SeverityLow:
		return 1
	default:
		return 0
	}
}

func severityStyle(s models.Severity) lipgloss.Style {
	switch s {
	case models.SeverityCritical:
		return styleCritical
	case models.SeverityHigh:
		return styleWarn
	default:
		return styleDim
	}
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}
