// This file renders the workspace breakdown: one row per package, in both the
// terminal and Markdown reports.
//
// The breakdown is deliberately placed directly after the repository health
// card rather than at the end of the report. In a monorepo the first question
// is always "which package", and burying the answer below the risk list makes
// the reader do the attribution work that is the whole point of the feature.
package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/zelvior/lensyxe/pkg/models"
)

// writeWorkspace renders the per-package health matrix.
//
// A workspace with no detected packages renders nothing at all rather than an
// empty table: an empty table reads as "the workspace has no packages", which
// is a different and wrong claim.
func writeWorkspace(b *strings.Builder, ws *models.Workspace) {
	if len(ws.Packages) == 0 {
		return
	}

	b.WriteString(sectionTitle(fmt.Sprintf("WORKSPACE (%s, %d package%s)",
		ws.Kind, len(ws.Packages), plural(len(ws.Packages)))))

	// One package per line, aligned into columns. A table would need column
	// widths computed from the widest path; a fixed two-space gap keeps the
	// rows stable regardless of path length and costs nothing to read.
	for _, pkg := range ws.Packages {
		b.WriteString("  ")
		b.WriteString(styleLabel.Render(fmt.Sprintf("%-24s", truncatePath(pkg.Path, 24))))
		fmt.Fprintf(b, "%5s", scoreText(pkg.Health.Score))
		b.WriteString("  ")
		b.WriteString(scoreStyle(pkg.Health.Score).Render(pkg.Health.Grade))
		if note := packageNote(pkg); note != "" {
			b.WriteString(styleDim.Render(" " + note))
		}
		b.WriteByte('\n')
	}

	// The excluded dimension is stated rather than left for the reader to
	// infer from a missing column. A package score that silently omitted git
	// would be read as covering all three dimensions.
	b.WriteString(styleDim.Render(fmt.Sprintf(
		"\n  package scores use the code and dependency weights only; " +
			"git history is a repository-level signal and is not attributed per package\n\n")))
}

// packageNote renders the one-line reason a package deserves attention, or an
// empty string when nothing stands out.
//
// Every note is a measured fact, never an impression: a package with two
// confirmed hotspots says so, and a healthy one says nothing at all.
func packageNote(pkg models.PackageHealth) string {
	confirmed := 0
	for _, h := range pkg.Hotspots {
		if h.Confirmed {
			confirmed++
		}
	}

	critical := 0
	for _, r := range pkg.Risks {
		if r.Severity == models.SeverityCritical {
			critical++
		}
	}

	switch {
	case critical > 0:
		return fmt.Sprintf("(%d critical risk%s)", critical, plural(critical))
	case confirmed > 0:
		return fmt.Sprintf("(%d confirmed hotspot%s)", confirmed, plural(confirmed))
	case pkg.Dependencies.Drift:
		return "(unlocked dependencies)"
	case pkg.Files == 0:
		return "(no source files)"
	}
	return ""
}

// markdownWorkspace renders the workspace table for Markdown output.
func markdownWorkspace(b *strings.Builder, ws *models.Workspace) {
	if len(ws.Packages) == 0 {
		return
	}

	// A Sprintf with no arguments is what makes the two checks below conflict: one
	// wants Fprintf, the other wants the Sprintf dropped entirely. Dropping it is
	// the better resolution, since a formatted constant string is pointless.
	b.WriteString("\n## Workspace Health Breakdown\n\n")
	fmt.Fprintf(b,
		"Detected `%s` workspace from `%s`, %d package%s.\n\n",
		ws.Kind, ws.Manifest, len(ws.Packages), plural(len(ws.Packages)))

	b.WriteString("| Package | Score | Grade | Files | Tests | Code lines | Attention |\n")
	b.WriteString("| :--- | ---: | :---: | ---: | ---: | ---: | :--- |\n")

	for _, pkg := range ws.Packages {
		grade := pkg.Health.Grade
		if grade == "" {
			grade = models.Grade(pkg.Health.Score)
		}
		tests := "—"
		if pkg.TestFiles > 0 {
			tests = fmt.Sprintf("%d (%.0f%%)", pkg.TestFiles, pkg.TestFileRatio*100)
		}
		fmt.Fprintf(b, "| %s | %s | %s | %d | %s | %d | %s |\n",
			escapePipes(pkg.Path),
			scoreText(pkg.Health.Score),
			escapePipes(grade),
			pkg.Files,
			tests,
			pkg.CodeLines,
			escapePipes(packageNote(pkg)))
	}

	b.WriteString("\n> Package scores are computed from each package's own files and " +
		"manifests. Git history is a repository-level signal and is not attributed " +
		"per package, so package scores carry the code and dependency weights only.\n")

	// A package with risks attributed to it is the actionable part of the
	// breakdown: those are the risks a maintainer fixing that package should
	// read first.
	if rows := attributedRisksRows(ws); rows != "" {
		b.WriteString("\n### Risks by package\n\n")
		b.WriteString(rows)
	}
}

// attributedRisksRows renders the repository risks mapped onto packages.
func attributedRisksRows(ws *models.Workspace) string {
	var b strings.Builder
	wrote := false
	for _, pkg := range ws.Packages {
		if len(pkg.Attribution) == 0 {
			continue
		}
		if !wrote {
			b.WriteString("| Package | Attributed risks |\n| :--- | :--- |\n")
			wrote = true
		}
		fmt.Fprintf(&b, "| %s | %d |\n", escapePipes(pkg.Path), len(pkg.Attribution))
	}
	return b.String()
}

// RenderWorkspace writes only the workspace matrix, for callers that want the
// breakdown without the rest of the report.
func RenderWorkspace(w io.Writer, ws *models.Workspace) error {
	if ws == nil {
		return nil
	}
	var b strings.Builder
	writeWorkspace(&b, ws)
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("render workspace: %w", err)
	}
	return nil
}

// scoreText renders a 0..100 score without a trailing ".0".
func scoreText(score float64) string {
	if score == float64(int(score)) {
		return fmt.Sprintf("%d", int(score))
	}
	return fmt.Sprintf("%.1f", score)
}

// plural returns "s" for a count other than one.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// truncatePath shortens a path for column alignment, marking the cut.
//
// A truncated path must be visibly truncated: silently shortening
// "packages/very-long-name/ui" to "packages/very-lon…" reads as a different
// package name.
func truncatePath(p string, width int) string {
	if len(p) <= width {
		return p
	}
	if width <= 1 {
		return p[:width]
	}
	return p[:width-1] + "…"
}
