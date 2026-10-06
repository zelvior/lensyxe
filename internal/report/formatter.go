// This file implements the machine-readable and text-export renderers:
// JSON (with full risk evidence payloads) and Markdown (for PR comments and
// documentation export).
//
// Both are deterministic: given the same snapshot they emit byte-identical
// output. Markdown deliberately avoids ANSI escapes so it pastes cleanly into
// a pull request, an issue, or a docs site.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/zelvior/lensyxe/internal/metrics"
	"github.com/zelvior/lensyxe/internal/risk"
	"github.com/zelvior/lensyxe/pkg/models"
)

// RenderJSON writes the snapshot as indented JSON.
//
// Risk evidence and hotspot payloads are included in full: they are the point
// of the format for programmatic consumers, so nothing is summarized away.
// SetEscapeHTML(false) keeps URLs and paths readable, since Go's default would
// escape <, >, and & into < sequences.
func RenderJSON(w io.Writer, snap *models.Snapshot) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(snap); err != nil {
		return fmt.Errorf("render json: %w", err)
	}
	return nil
}

// MarkdownOptions tune the Markdown renderer.
type MarkdownOptions struct {
	// Title overrides the generated heading. Empty uses a default derived
	// from the repository name.
	Title string
	// IncludeFindings adds the raw findings list after the risks. Useful for a
	// documentation export, noisy for a PR comment.
	IncludeFindings bool
	// MaxRisks caps the rendered risks. Zero renders all of them.
	MaxRisks int
}

// RenderMarkdown writes the snapshot as a Markdown document.
//
// The layout is ordered for how a reviewer reads it: verdict, score, the three
// weighted dimensions, hotspots, risks with evidence, then supporting counts.
func RenderMarkdown(w io.Writer, snap *models.Snapshot, opts MarkdownOptions) error {
	if snap == nil {
		return fmt.Errorf("render markdown: nil snapshot")
	}
	var b strings.Builder

	title := opts.Title
	if title == "" {
		title = markdownTitle(snap)
	}
	b.WriteString("# " + title + "\n\n")
	b.WriteString(markdownVerdict(snap))
	b.WriteString("\n")

	// Health score and grade.
	b.WriteString("## Health score\n\n")
	fmt.Fprintf(&b, "**%.1f / 100** (grade %s)\n\n", snap.Health.Score, snap.Health.Grade)
	b.WriteString("| Dimension | Score | Weight | Detail |\n")
	b.WriteString("| --- | ---: | ---: | --- |\n")
	for _, m := range snap.Health.Metrics {
		if !m.Applicable {
			continue
		}
		fmt.Fprintf(&b, "| %s | %.1f | %.0f%% | %s |\n",
			escapePipes(m.Label), m.Score, m.Weight*100, escapePipes(m.Detail))
	}
	b.WriteString("\n")

	// Placed before the code section for the same reason as in the terminal
	// renderer: the package matrix is the first thing a monorepo reader needs.
	if snap.Workspace.Detected() {
		markdownWorkspace(&b, snap.Workspace)
	}

	markdownCode(&b, snap.Code)
	markdownDependencies(&b, snap.Dependencies)
	markdownGit(&b, snap.Git)
	markdownRisks(&b, snap.Risks, opts.MaxRisks)
	if opts.IncludeFindings {
		markdownFindings(&b, snap.Findings)
	}

	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "_Lensyxe %s - schema v%s - deterministic local analysis, weights: code %d%%, dependencies %d%%, git %d%%._\n",
		snap.Version, snap.SchemaVersion,
		int(metrics.WeightCode*100), int(metrics.WeightDeps*100), int(metrics.WeightGit*100))

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("render markdown: %w", err)
	}
	return nil
}

// markdownTitle derives a heading from the repository path.
func markdownTitle(snap *models.Snapshot) string {
	name := snap.Root
	// Trim a trailing separator so "/" becomes empty and the heading falls
	// back to the tool name.
	name = strings.TrimRight(name, "/\\")
	if idx := strings.LastIndexAny(name, "/\\"); idx >= 0 && idx < len(name)-1 {
		name = name[idx+1:]
	}
	if name == "" {
		name = snap.Tool
	}
	return fmt.Sprintf("%s: engineering health", name)
}

// markdownVerdict renders the one-line summary with severity context.
func markdownVerdict(snap *models.Snapshot) string {
	verdict := snap.Health.Summary
	switch len(snap.Risks) {
	case 0:
		return verdict + " No risks detected."
	default:
		worst := snap.Risks[0]
		return fmt.Sprintf("%s %d risk(s) detected; highest is %s %s (%s).",
			verdict, len(snap.Risks),
			worst.Severity.Emoji(), strings.ToUpper(string(worst.Severity)),
			escapePipes(worst.Title))
	}
}

func markdownCode(b *strings.Builder, c models.CodeStats) {
	b.WriteString("## Code\n\n")
	fmt.Fprintf(b, "- **Files**: %d source, %d test (ratio %.2f)\n",
		c.SourceFiles, c.TestFiles, c.TestFileRatio)
	fmt.Fprintf(b, "- **Lines**: %d code, %d blank/comment, %d total\n",
		c.CodeLines, c.BlankOrComment, c.TotalLines)
	fmt.Fprintf(b, "- **Average**: %.1f LOC per file, largest %d LOC\n",
		c.AverageLines, c.MaxFileLines)

	if len(c.Languages) > 0 {
		b.WriteString("\n| Language | Files | Lines | Share |\n")
		b.WriteString("| --- | ---: | ---: | ---: |\n")
		for _, l := range c.Languages {
			share := 0.0
			if c.CodeLines > 0 {
				share = float64(l.Lines) / float64(c.CodeLines) * 100
			}
			fmt.Fprintf(b, "| %s | %d | %d | %.1f%% |\n",
				escapePipes(l.Name), l.Files, l.Lines, share)
		}
	}

	if c.Complexity.Measured {
		fmt.Fprintf(b, "\n- **Complexity** (lexical estimate): average %.1f, max %.1f in `%s`, max nesting %d\n",
			c.Complexity.AverageComplexity, c.Complexity.MaxComplexity,
			escapePipes(c.Complexity.MaxComplexityFile), c.Complexity.MaxNesting)
		if len(c.Complexity.WorstFiles) > 0 {
			b.WriteString("\n| Worst-complexity files | Lines | Complexity | Level | Nesting |\n")
			b.WriteString("| --- | ---: | ---: | --- | ---: |\n")
			for _, w := range c.Complexity.WorstFiles {
				fmt.Fprintf(b, "| `%s` | %d | %.1f | %s | %d |\n",
					escapePipes(w.Path), w.Lines, w.EstimatedComplexity, w.Level, w.MaxNesting)
			}
		}
	}

	if len(c.Hotspots) > 0 {
		b.WriteString("\n### Hotspots\n\n")
		b.WriteString("| File | Lines | Churn | Complexity | Classification |\n")
		b.WriteString("| --- | ---: | ---: | ---: | --- |\n")
		for _, h := range c.Hotspots {
			marker := ""
			if h.Confirmed {
				// A confirmed hotspot is the one that satisfies all three
				// factors, so it gets an explicit marker in the table.
				marker = " **confirmed**"
			}
			fmt.Fprintf(b, "| `%s` | %d | %d | %.1f | %s%s |\n",
				escapePipes(h.Path), h.Lines, h.Churn, h.Complexity,
				escapePipes(h.Classification), marker)
		}
	}
	b.WriteString("\n")
}

func markdownDependencies(b *strings.Builder, d models.DependencyStats) {
	b.WriteString("## Dependencies\n\n")
	if !d.Detected {
		b.WriteString(d.Note + "\n\n")
		return
	}
	fmt.Fprintf(b, "- **Counts**: %d direct, %d dev, %d total, %d transitive\n",
		d.Direct, d.Dev, d.Total, d.Transitive)
	if d.Locked {
		b.WriteString("- **Reproducibility**: all ecosystems locked\n\n")
	} else {
		b.WriteString("- **Reproducibility**: drift detected\n\n")
	}
	b.WriteString("| Ecosystem | Manifest | Lockfile | Direct | Dev | Transitive |\n")
	b.WriteString("| --- | --- | --- | ---: | ---: | ---: |\n")
	for _, eco := range d.Ecosystems {
		lock := eco.Lockfile
		if lock == "" {
			lock = "**none**"
		}
		fmt.Fprintf(b, "| %s | `%s` | `%s` | %d | %d | %d |\n",
			escapePipes(eco.Name), escapePipes(eco.Manifest),
			escapePipes(lock), eco.Direct, eco.Dev, eco.Transitive)
	}
	b.WriteString("\n")
}

func markdownGit(b *strings.Builder, g models.GitStats) {
	b.WriteString("## Git\n\n")
	if !g.IsRepository {
		b.WriteString("Not a git repository; git metrics skipped.\n\n")
		return
	}
	fmt.Fprintf(b, "- **Branch**: `%s` at `%s`\n",
		escapePipes(g.Branch), escapePipes(g.HeadCommit))
	fmt.Fprintf(b, "- **Commits**: %d total, %d in the last %d days (%.1f/week)\n",
		g.TotalCommits, g.WindowCommits, g.WindowDays, g.CommitsPerWeek)
	fmt.Fprintf(b, "- **Authors**: %d, bus factor %d, top author share %.0f%%\n",
		g.Authors, g.BusFactor, g.TopAuthorShare*100)
	fmt.Fprintf(b, "- **Churn**: +%d / -%d lines across %d files, concentration %.2f\n\n",
		g.LinesAdded, g.LinesDeleted, g.ChurnFiles, g.ChurnConcentration)

	if len(g.Churn) > 0 {
		b.WriteString("| High-churn files | Commits | +Lines | -Lines |\n")
		b.WriteString("| --- | ---: | ---: | ---: |\n")
		for _, e := range g.Churn {
			fmt.Fprintf(b, "| `%s` | %d | %d | %d |\n",
				escapePipes(e.Path), e.Commits, e.Added, e.Deleted)
		}
		b.WriteString("\n")
	}
}

// markdownRisks renders the risk list with full evidence payloads.
//
// The evidence table is the core of the format: a reviewer in a PR thread
// should be able to verify every claim without running any tool.
func markdownRisks(b *strings.Builder, risks []models.Risk, max int) {
	fmt.Fprintf(b, "## Risks (%d)\n\n", len(risks))
	if len(risks) == 0 {
		b.WriteString("No risks detected.\n\n")
		return
	}

	// Summary table first, so the section is skimmable.
	b.WriteString("| Severity | Risk | Category | Impact |\n")
	b.WriteString("| --- | --- | --- | ---: |\n")
	for _, r := range risks {
		fmt.Fprintf(b, "| %s %s | %s | %s | %.1f |\n",
			r.Severity.Emoji(), strings.ToUpper(string(r.Severity)),
			escapePipes(r.Title), escapePipes(string(r.Category)), r.Impact)
	}
	b.WriteString("\n")

	limit := len(risks)
	if max > 0 && limit > max {
		limit = max
	}
	for _, r := range risks[:limit] {
		fmt.Fprintf(b, "### %s %s %s\n\n", r.Severity.Emoji(),
			strings.ToUpper(string(r.Severity)), escapePipes(r.Title))
		if r.Subject != "" {
			fmt.Fprintf(b, "Subject: `%s`\n\n", escapePipes(r.Subject))
		}
		b.WriteString(r.Detail + "\n\n")
		if len(r.Evidence) > 0 {
			b.WriteString("| Evidence | Measurement | Value |\n")
			b.WriteString("| --- | --- | ---: |\n")
			for _, e := range r.Evidence {
				value := "-"
				if e.Value != nil {
					value = fmt.Sprintf("%.2f", *e.Value)
				}
				fmt.Fprintf(b, "| %s | %s | %s |\n",
					escapePipes(e.Label), escapePipes(e.Detail), value)
			}
			b.WriteString("\n")
		}
		if r.Recommendation != "" {
			b.WriteString("**Recommendation**: " + r.Recommendation + "\n\n")
		}
	}
	if limit < len(risks) {
		fmt.Fprintf(b, "_%d additional risk(s) omitted; run with JSON output for the full set._\n\n",
			len(risks)-limit)
	}
}

func markdownFindings(b *strings.Builder, findings []models.Finding) {
	if len(findings) == 0 {
		return
	}
	fmt.Fprintf(b, "## Findings (%d)\n\n", len(findings))
	b.WriteString("| Severity | Finding | Detail |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, f := range findings {
		fmt.Fprintf(b, "| %s %s | %s | %s |\n",
			f.Severity.Emoji(), strings.ToUpper(string(f.Severity)),
			escapePipes(f.Title), escapePipes(f.Detail))
	}
	b.WriteString("\n")
}

// escapePipes protects Markdown table cells from embedded pipes and newlines.
//
// A pipe inside a cell would silently split the row into extra columns, and a
// raw newline would terminate the row, so both must be neutralized.
func escapePipes(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// SortRisksStable orders risks for export. Exposed so callers rendering a
// subset (for example a PR comment with a risk cap) get the same order as the
// full report.
func SortRisksStable(risks []models.Risk) {
	sort.SliceStable(risks, func(i, j int) bool {
		if risks[i].Severity != risks[j].Severity {
			return risks[i].Severity.Rank() > risks[j].Severity.Rank()
		}
		if risks[i].Impact != risks[j].Impact {
			return risks[i].Impact > risks[j].Impact
		}
		return risks[i].ID < risks[j].ID
	})
}

// RiskBandLabels documents the classification bands for the Markdown footer,
// so an exported document is self-describing.
func RiskBandLabels() string {
	return fmt.Sprintf("critical >= %.0f impact, high >= %.0f, medium >= %.0f",
		risk.CriticalImpact, risk.HighImpact, risk.MediumImpact)
}
