package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/zelvior/lensyxe/internal/gap"
)

// RenderGap writes the execution-reality-gap report.
//
// The three sections are separated because they call for opposite actions. Hot
// code is where complexity is being paid for on every request. Debt is complexity
// nobody is currently paying for. Phantom code is code with no evidence of ever
// running, which is the strongest claim here and the one most easily made by
// accident, so the section that needs it is gated on attribution coverage.
func RenderGap(w io.Writer, res *gap.Result, format string) error {
	if res == nil {
		return fmt.Errorf("render gap: nil result")
	}
	switch format {
	case "json":
		return renderGapJSON(w, res)
	case "", "table", "terminal":
		return renderGapTable(w, res)
	default:
		return fmt.Errorf("unknown format %q: use table or json", format)
	}
}

func renderGapJSON(w io.Writer, res *gap.Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

func renderGapTable(w io.Writer, res *gap.Result) error {
	var b strings.Builder

	b.WriteString(styleTitle.Render("lensyxe gap"))
	b.WriteByte('\n')
	b.WriteString(styleDim.Render(res.Root))
	b.WriteByte('\n')
	b.WriteString(rule())

	// The runtime evidence comes first, because every number below is bounded by
	// it. A reader who sees "0 hits" without knowing 30% of the profile resolved
	// has been told the most misleading thing this tool can say.
	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("RUNTIME EVIDENCE"))
	b.WriteByte('\n')
	p := res.Profile
	field := func(label, value string) {
		b.WriteString("  " + styleLabel.Render(fmt.Sprintf("%-14s", label)) + value + "\n")
	}
	field("source", string(p.Source)+styleDim.Render("  "+p.Path))
	field("observations", fmt.Sprintf("%d, %d resolved to source (%.0f%%)",
		p.Observations, p.Attributed, p.Coverage*100))
	field("attribution", string(p.Attribution))
	if p.WindowStart != "" {
		window := p.WindowStart
		if p.WindowEnd != "" {
			window += " .. " + p.WindowEnd
		}
		field("window", styleDim.Render(window))
	} else {
		field("window", styleDim.Render("not recorded by this profile format"))
	}

	if p.Stale != "" {
		b.WriteString("  " + styleWarn.Render(p.Stale) + "\n")
	}
	// The loader's notes and the analysis's warnings are separate lists, so
	// printing both cannot duplicate anything.
	for _, note := range p.Notes {
		b.WriteString("  " + styleDim.Render("note: "+note) + "\n")
	}
	for _, warn := range res.Warnings {
		b.WriteString("  " + styleDim.Render("note: "+warn) + "\n")
	}

	// ---- hot ----
	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("🔥 CRITICAL PATH HOTSPOTS"))
	b.WriteByte('\n')
	if len(res.Hot) == 0 {
		b.WriteString("  " + styleDim.Render(
			"No file is both complicated and observed running.") + "\n")
	} else {
		b.WriteString(gapTableHeader())
		for _, e := range res.Hot {
			b.WriteString(gapRow(e))
		}
		b.WriteString("  " + styleDim.Render(
			"Complicated code that actually executes. This is where complexity is "+
				"being paid for on every request.") + "\n")
	}

	// ---- debt ----
	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("💤 DEPRIORITIZED DEBT"))
	b.WriteByte('\n')
	if len(res.Debt) == 0 {
		b.WriteString("  " + styleDim.Render(
			"No complicated code is sitting unused.") + "\n")
	} else {
		b.WriteString(gapTableHeader())
		for _, e := range res.Debt {
			b.WriteString(gapRow(e))
		}
		b.WriteString("  " + styleDim.Render(
			"Complicated code with no runtime evidence. Leaving it alone is "+
				"defensible; refactoring it is not currently worth the risk.") + "\n")
	}

	// ---- phantom ----
	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("👻 PHANTOM CODE CANDIDATES"))
	b.WriteByte('\n')
	if len(res.Phantom) == 0 {
		b.WriteString("  " + styleDim.Render(
			"No source file was observed running at all.") + "\n")
	} else {
		b.WriteString(gapTableHeader())
		for _, e := range res.Phantom {
			b.WriteString(gapRow(e))
		}
		b.WriteString("  " + styleDim.Render(
			"Code with no runtime evidence in this window. Absence is only "+
				"claimable where the profile could see the source at all, so "+
				"this section is empty unless at least half the observations "+
				"resolved to a function. An untested path, a feature flag that is "+
				"off, and a caller outside the profiled process all look identical "+
				"here.") + "\n")
	}

	b.WriteString(rule())
	b.WriteString(styleDim.Render(
		"Critical Path Risk = Static Complexity x log10(runtime hits + 1). The "+
			"logarithm stops one very hot function from swamping the ranking, and "+
			"at zero hits it is log10(1) = 0, so never-running code scores zero "+
			"here by construction and is reported separately instead.") + "\n")
	b.WriteString(styleDim.Render(
		"Static Complexity is measured by the same analyzer lensyxe analyze uses: "+
			"45% estimated complexity, 30% size, 25% churn. This ranks what was "+
			"observed over one window. It does not forecast demand, and a profile "+
			"is a sample of one window rather than a description of the program.") + "\n")

	_, err := io.WriteString(w, b.String())
	return err
}

func gapTableHeader() string {
	return "    " +
		styleDim.Render(fmt.Sprintf("%-6s %-6s %-9s %-7s %-8s %s",
			"risk", "static", "hits", "Cx", "lines", "file")) + "\n"
}

func gapRow(e gap.Entry) string {
	return fmt.Sprintf("    %s %s %s %s %s %s\n",
		scoreStyle(e.CriticalRisk/2).Render(fmt.Sprintf("%6.1f", e.CriticalRisk)),
		styleDim.Render(fmt.Sprintf("%6.1f", e.Static)),
		styleDim.Render(fmt.Sprintf("%9d", e.Hits)),
		styleDim.Render(fmt.Sprintf("%7.1f", e.ComplexityScore)),
		styleDim.Render(fmt.Sprintf("%8d", e.CodeLines)),
		styleLabel.Render(truncate(e.Path, 46)))
}
