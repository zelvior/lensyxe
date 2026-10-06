// Package report renders analysis results for humans.
//
// Rendering is side-effect free and deterministic: given the same Snapshot it
// always emits byte-identical output. All writes go to an io.Writer so the CLI
// stays testable and the JSON path never touches this package.
package report

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/zelvior/lensyxe/internal/metrics"
	"github.com/zelvior/lensyxe/pkg/models"
)

// Palette centralizes colors so severity and score coloring stay consistent.
var (
	styleTitle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	styleLabel    = lipgloss.NewStyle().Bold(true)
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	styleGood     = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	styleBad      = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	styleCritical = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("199"))
	styleRule     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// Render writes the full terminal report: header, health card, per-domain
// tables, risks, and findings.
func Render(w io.Writer, snap *models.Snapshot) error {
	if snap == nil {
		return fmt.Errorf("render: nil snapshot")
	}
	var b strings.Builder

	writeHeader(&b, snap)
	writeHealthCard(&b, snap.Health)
	// The workspace breakdown comes immediately after the health card: in a
	// monorepo the reader's first question is which package, and making them
	// scroll past the risk list to find out defeats the purpose.
	if snap.Workspace.Detected() {
		writeWorkspace(&b, snap.Workspace)
	}
	writeCodeSection(&b, snap.Code)
	writeGitSection(&b, snap.Git)
	writeDependencySection(&b, snap.Dependencies)
	writeRisks(&b, snap.Risks)
	writeFindings(&b, snap.Findings)
	writeFooter(&b, snap)

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("render terminal: %w", err)
	}
	return nil
}

func writeHeader(b *strings.Builder, snap *models.Snapshot) {
	title := fmt.Sprintf("%s %s", snap.Tool, snap.Version)
	b.WriteString(styleTitle.Render(title))
	b.WriteString(styleDim.Render("  engineering intelligence"))
	b.WriteByte('\n')
	b.WriteString(styleDim.Render(fmt.Sprintf("path    %s", snap.Root)))
	b.WriteByte('\n')
	b.WriteString(styleDim.Render(fmt.Sprintf("scanned %s in %d ms",
		snap.GeneratedAt.UTC().Format("2006-01-02 15:04:05Z"), snap.DurationMS)))
	b.WriteByte('\n')
	b.WriteString(rule())
}

// rule draws a horizontal divider sized to a sensible terminal width.
func rule() string {
	return styleRule.Render(strings.Repeat("-", 62)) + "\n"
}

// writeHealthCard renders the score, grade bar, and weighted metric rows.
func writeHealthCard(b *strings.Builder, h models.Health) {
	bar := scoreBar(h.Score)
	b.WriteString("\n")
	b.WriteString(styleHeader.Render("ENGINEERING HEALTH"))
	b.WriteByte('\n')
	b.WriteString(scoreStyle(h.Score).Render(fmt.Sprintf("  %5.1f / 100   %s", h.Score, h.Grade)))
	b.WriteByte('\n')
	b.WriteString("  " + bar)
	b.WriteByte('\n')
	b.WriteString(styleDim.Render("  " + h.Summary))
	b.WriteByte('\n')

	rows := make([][3]string, 0, len(h.Metrics))
	widths := [3]int{len("DIMENSION"), len("SCORE"), len("WEIGHT")}
	for _, m := range h.Metrics {
		score := "-"
		if m.Applicable {
			score = fmt.Sprintf("%.1f", m.Score)
		}
		weight := "-"
		if m.Applicable {
			weight = fmt.Sprintf("%.0f%%", m.Weight*100)
		}
		rows = append(rows, [3]string{m.Label, score, weight})
		widths[0] = maxInt(widths[0], len(m.Label))
		widths[1] = maxInt(widths[1], len(score))
		widths[2] = maxInt(widths[2], len(weight))
	}
	rows = append(rows, [3]string{"", "", ""})
	for _, r := range rows {
		if r[0] == "" {
			b.WriteByte('\n')
			continue
		}
		b.WriteString(fmt.Sprintf("  %-*s  %*s  %*s\n",
			widths[0], r[0], widths[1], r[1], widths[2], r[2]))
	}
	b.WriteByte('\n')
}

// scoreBar draws a 20-cell progress bar using block characters.
func scoreBar(score float64) string {
	const cells = 20
	filled := int(score / 100 * cells)
	if filled < 0 {
		filled = 0
	}
	if filled > cells {
		filled = cells
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", cells-filled)
	return scoreStyle(score).Render(bar)
}

func writeCodeSection(b *strings.Builder, c models.CodeStats) {
	b.WriteString(sectionTitle("CODE"))
	row(b, "files", fmt.Sprintf("%d source / %d test", c.SourceFiles, c.TestFiles))
	row(b, "lines", fmt.Sprintf("%d physical / %d code / %d blank+comment",
		c.TotalLines, c.CodeLines, c.BlankOrComment))
	row(b, "average", fmt.Sprintf("%.1f LOC per file", c.AverageLines))
	row(b, "largest", fmt.Sprintf("%d LOC in a single file", c.MaxFileLines))
	row(b, "test ratio", fmt.Sprintf("%.2f files, %.1f%% of lines",
		c.TestFileRatio, c.TestLineRatio*100))
	if c.Complexity.Measured {
		row(b, "complexity", fmt.Sprintf("avg %.1f, max %.1f (%s), nesting %d",
			c.Complexity.AverageComplexity, c.Complexity.MaxComplexity,
			c.Complexity.MaxComplexityFile, c.Complexity.MaxNesting))
	} else {
		row(b, "complexity", "not measured")
	}
	if c.Truncated {
		row(b, "note", "some oversized files were skipped")
	}

	if len(c.Languages) > 0 {
		b.WriteString("\n")
		b.WriteString("  languages\n")
		writeTable(b,
			[]string{"language", "files", "lines", "share"},
			func(idx int) []string {
				l := c.Languages[idx]
				share := 0.0
				if c.CodeLines > 0 {
					share = float64(l.Lines) / float64(c.CodeLines) * 100
				}
				return []string{l.Name,
					fmt.Sprintf("%d", l.Files),
					fmt.Sprintf("%d", l.Lines),
					fmt.Sprintf("%.1f%%", share)}
			}, len(c.Languages), []int{0})
	}

	if len(c.Complexity.WorstFiles) > 0 {
		b.WriteString("\n")
		b.WriteString("  worst-complexity files\n")
		writeTable(b,
			[]string{"path", "lines", "complexity", "level", "nesting"},
			func(idx int) []string {
				w := c.Complexity.WorstFiles[idx]
				return []string{w.Path,
					fmt.Sprintf("%d", w.Lines),
					fmt.Sprintf("%.1f", w.EstimatedComplexity),
					string(w.Level),
					fmt.Sprintf("%d", w.MaxNesting)}
			}, len(c.Complexity.WorstFiles), []int{0})
	}

	if len(c.Hotspots) > 0 {
		b.WriteString("\n")
		// Label the columns so the classification tokens are not cryptic.
		b.WriteString("  hotspots (classification = size|churn|complexity|confirmed)\n")
		writeTable(b,
			[]string{"path", "lines", "churn", "cx", "classification"},
			func(idx int) []string {
				h := c.Hotspots[idx]
				marker := h.Classification
				if h.Confirmed {
					marker = styleBad.Render(marker)
				}
				return []string{h.Path,
					fmt.Sprintf("%d", h.Lines),
					fmt.Sprintf("%d", h.Churn),
					fmt.Sprintf("%.1f", h.Complexity),
					marker}
			}, len(c.Hotspots), []int{0})
	}
	b.WriteByte('\n')
}

func writeGitSection(b *strings.Builder, g models.GitStats) {
	b.WriteString(sectionTitle("GIT"))

	if !g.IsRepository {
		b.WriteString(styleDim.Render("  not a git repository - git metrics skipped"))
		b.WriteString("\n\n")
		return
	}

	branch := g.Branch
	if branch == "" {
		branch = "unknown"
	}
	row(b, "branch", fmt.Sprintf("%s @ %s", branch, g.HeadCommit))
	row(b, "commits", fmt.Sprintf("%d total / %d in last %d days",
		g.TotalCommits, g.WindowCommits, g.WindowDays))
	row(b, "cadence", fmt.Sprintf("%.1f commits per week", g.CommitsPerWeek))
	row(b, "authors", fmt.Sprintf("%d authors, bus factor %d, top author share %.0f%%",
		g.Authors, g.BusFactor, g.TopAuthorShare*100))
	row(b, "last commit", fmt.Sprintf("%s (%d days ago)",
		g.LastCommitAt.UTC().Format("2006-01-02"), g.DaysSinceCommit))
	row(b, "churn", fmt.Sprintf("+%d / -%d lines across %d files, top-3 share %.1f%%, concentration %.2f",
		g.LinesAdded, g.LinesDeleted, g.ChurnFiles, g.ChurnHotspotRate*100, g.ChurnConcentration))
	if g.Note != "" {
		row(b, "note", g.Note)
	}

	if len(g.Churn) > 0 {
		b.WriteString("\n")
		b.WriteString("  high-churn files\n")
		writeTable(b,
			[]string{"path", "commits", "+lines", "-lines"},
			func(idx int) []string {
				c := g.Churn[idx]
				return []string{c.Path,
					fmt.Sprintf("%d", c.Commits),
					fmt.Sprintf("%d", c.Added),
					fmt.Sprintf("%d", c.Deleted)}
			}, len(g.Churn), []int{0})
	}
	b.WriteByte('\n')
}

func writeDependencySection(b *strings.Builder, d models.DependencyStats) {
	b.WriteString(sectionTitle("DEPENDENCIES"))

	if !d.Detected {
		b.WriteString(styleDim.Render("  " + d.Note))
		b.WriteString("\n\n")
		return
	}

	lockState := "locked"
	if !d.Locked {
		lockState = styleWarn.Render("UNLOCKED")
	}
	row(b, "manifests", fmt.Sprintf("%d ecosystem(s)", len(d.Ecosystems)))
	row(b, "counts", fmt.Sprintf("%d direct / %d dev / %d total / %d transitive",
		d.Direct, d.Dev, d.Total, d.Transitive))
	row(b, "reproducibility", lockState)

	b.WriteByte('\n')
	rows := make([][]string, 0, len(d.Ecosystems))
	for _, eco := range d.Ecosystems {
		lock := eco.Lockfile
		if lock == "" {
			lock = styleWarn.Render("none")
		}
		rows = append(rows, []string{eco.Name, eco.Manifest, lock,
			fmt.Sprintf("%d/%d/%d", eco.Direct, eco.Dev, eco.Transitive)})
	}
	writeTableRows(b,
		[]string{"ecosystem", "manifest", "lockfile", "direct/dev/trans"},
		rows, []int{0})
	b.WriteByte('\n')
}

// writeRisks renders derived risks with their evidence, or a reassuring line
// when there are none.
func writeRisks(b *strings.Builder, risks []models.Risk) {
	b.WriteString(sectionTitle(fmt.Sprintf("RISKS (%d)", len(risks))))
	if len(risks) == 0 {
		b.WriteString(styleGood.Render("  no risks detected"))
		b.WriteString("\n\n")
		return
	}
	for _, r := range risks {
		b.WriteString("  " + r.Severity.Emoji() + " " + severityBadge(r.Severity) + " " +
			styleLabel.Render(r.Title) +
			styleDim.Render(fmt.Sprintf("  (-%.1f pts)", r.Impact)))
		b.WriteByte('\n')
		if r.Subject != "" {
			b.WriteString(styleDim.Render("    subject  " + r.Subject))
			b.WriteByte('\n')
		}
		// Evidence is the point of the risk: print it so a terminal reader
		// can verify the claim without re-running any tool.
		for _, e := range r.Evidence {
			label := e.Label
			if e.Subject != "" {
				label = fmt.Sprintf("%s (%s)", e.Label, e.Subject)
			}
			b.WriteString(styleDim.Render(fmt.Sprintf("    %-16s %s", label, e.Detail)))
			b.WriteByte('\n')
		}
		if r.Recommendation != "" {
			b.WriteString(styleDim.Render("    -> " + r.Recommendation))
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
}

func writeFindings(b *strings.Builder, findings []models.Finding) {
	if len(findings) == 0 {
		return
	}
	b.WriteString(sectionTitle("FINDINGS"))
	for _, f := range findings {
		b.WriteString("  " + severityBadge(f.Severity) + " " + f.Title)
		if f.Subject != "" {
			b.WriteString(styleDim.Render("  " + f.Subject))
		}
		b.WriteByte('\n')
		b.WriteString(styleDim.Render("    " + f.Detail))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}

func writeFooter(b *strings.Builder, snap *models.Snapshot) {
	b.WriteString(rule())
	b.WriteString(styleDim.Render(fmt.Sprintf(
		"schema v%s - weights: code %d%%, dependencies %d%%, git %d%%",
		snap.SchemaVersion,
		int(metrics.WeightCode*100), int(metrics.WeightDeps*100), int(metrics.WeightGit*100))))
	b.WriteByte('\n')
}

// sectionTitle renders a section heading followed by a rule.
func sectionTitle(name string) string {
	return "\n" + styleHeader.Render(name) + "\n"
}

// row renders an aligned key/value pair.
func row(b *strings.Builder, key, value string) {
	b.WriteString(fmt.Sprintf("  %-14s %s\n", styleDim.Render(key), value))
}

// writeTable renders a table using an accessor that builds row i on demand,
// avoiding an intermediate slice for wide tables.
func writeTable(b *strings.Builder, headers []string, rowAt func(int) []string, count int, alignLeft []int) {
	cells := make([][]string, 0, count)
	for i := 0; i < count; i++ {
		cells = append(cells, rowAt(i))
	}
	writeTableCells(b, headers, cells, alignLeft)
}

// writeTableRows renders a pre-built [][]string table.
func writeTableRows(b *strings.Builder, headers []string, rows [][]string, alignLeft []int) {
	writeTableCells(b, headers, rows, alignLeft)
}

func writeTableCells(b *strings.Builder, headers []string, rows [][]string, alignLeft []int) {
	left := map[int]bool{}
	for _, i := range alignLeft {
		left[i] = true
	}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = lipgloss.Width(h)
	}
	for _, r := range rows {
		for i := range headers {
			if i < len(r) {
				widths[i] = maxInt(widths[i], lipgloss.Width(r[i]))
			}
		}
	}

	writeRow := func(cells []string, style lipgloss.Style) {
		var line strings.Builder
		line.WriteString("  ")
		for i, cell := range cells {
			pad := widths[i] - lipgloss.Width(cell)
			if pad < 0 {
				pad = 0
			}
			if i > 0 {
				line.WriteString("  ")
			}
			if left[i] {
				line.WriteString(cell)
				line.WriteString(strings.Repeat(" ", pad))
			} else {
				line.WriteString(strings.Repeat(" ", pad))
				line.WriteString(cell)
			}
		}
		b.WriteString(style.Render(strings.TrimRight(line.String(), " ")))
		b.WriteByte('\n')
	}

	writeRow(headers, styleLabel)
	for _, r := range rows {
		writeRow(r, lipgloss.NewStyle())
	}
}

// severityBadge renders a compact, colored severity marker.
func severityBadge(s models.Severity) string {
	label := strings.ToUpper(string(s))
	if len(label) > 4 {
		label = label[:4]
	}
	var style lipgloss.Style
	switch s {
	case models.SeverityCritical:
		style = styleCritical
	case models.SeverityHigh:
		style = styleBad
	case models.SeverityMedium:
		style = styleWarn
	case models.SeverityLow:
		style = styleDim
	default:
		style = styleDim
	}
	return style.Render(fmt.Sprintf("%-4s", label))
}

// scoreStyle picks a color for a 0..100 score using fixed thresholds.
func scoreStyle(score float64) lipgloss.Style {
	switch {
	case score >= 80:
		return styleGood
	case score >= 60:
		return styleWarn
	default:
		return styleBad
	}
}

// SortFindings orders findings by severity then title for stable output.
func SortFindings(findings []models.Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].Severity.Rank() != findings[j].Severity.Rank() {
			return findings[i].Severity.Rank() > findings[j].Severity.Rank()
		}
		return findings[i].Title < findings[j].Title
	})
}

// SortRisks orders risks by impact then title for stable output.
func SortRisks(risks []models.Risk) {
	sort.SliceStable(risks, func(i, j int) bool {
		if risks[i].Impact != risks[j].Impact {
			return risks[i].Impact > risks[j].Impact
		}
		return risks[i].Title < risks[j].Title
	})
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
