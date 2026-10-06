package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/zelvior/lensyxe/internal/topology"
)

// writeTopologyJSONValue serialises the selected sections.
//
// Indented, because the output is meant to be read by a person piping it into a
// file as often as by a tool consuming it.
func writeTopologyJSONValue(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// TopologyResult bundles the three analyses so one command can print them.
type TopologyResult struct {
	Root     string                   `json:"root"`
	Bus      topology.BusFactorResult `json:"bus_factor"`
	Coupling topology.CouplingResult  `json:"coupling"`
	Graph    topology.Graph           `json:"graph"`
	Now      string                   `json:"now"`
}

// RenderTopology writes the architectural report.
//
// The three sections are separated because they answer different questions, and
// a reader who merges them will attribute a finding to the wrong analysis. Each
// section states its own caveat: a bus-factor number is conditional on history
// depth, a co-change warning is an association rather than a dependency, and a
// boundary violation is a rule about layout rather than a judgement about design.
func RenderTopology(w io.Writer, res *TopologyResult, format string, only []string) error {
	if res == nil {
		return fmt.Errorf("render topology: nil result")
	}

	switch format {
	case "json":
		return writeTopologyJSON(w, res, only)
	case "", "table", "terminal":
		return renderTopologyTable(w, res, only)
	default:
		return fmt.Errorf("unknown format %q: use table or json", format)
	}
}

// writeTopologyJSON emits only the sections the caller asked for, so
// --bus-factor --format json does not carry the whole analysis.
func writeTopologyJSON(w io.Writer, res *TopologyResult, only []string) error {
	out := map[string]any{"root": res.Root, "now": res.Now}
	if wants(only, "bus") {
		out["bus_factor"] = res.Bus
	}
	if wants(only, "temporal") {
		out["coupling"] = res.Coupling
	}
	if wants(only, "boundary", "boundaries") {
		out["graph"] = res.Graph
	}
	if wants(only, "all") || len(only) == 0 {
		out["bus_factor"] = res.Bus
		out["coupling"] = res.Coupling
		out["graph"] = res.Graph
	}
	return writeTopologyJSONValue(w, out)
}

// wants reports whether any of the given names was requested.
//
// A flag naming one thing also selects the thing it is a synonym for, so
// --boundaries and --boundary mean the same thing to a reader and must mean the
// same thing to the code.
func wants(only []string, names ...string) bool {
	if len(only) == 0 {
		return true
	}
	for _, o := range only {
		if o == "all" {
			return true
		}
		for _, n := range names {
			if o == n {
				return true
			}
		}
	}
	return false
}

func renderTopologyTable(w io.Writer, res *TopologyResult, only []string) error {
	var b strings.Builder

	b.WriteString(styleTitle.Render("lensyxe topology"))
	b.WriteByte('\n')
	b.WriteString(styleDim.Render(res.Root))
	b.WriteByte('\n')
	b.WriteString(rule())

	if wants(only, "boundary", "boundaries", "all") || len(only) == 0 {
		renderBoundaries(&b, &res.Graph)
	}
	if (wants(only, "bus", "all")) || len(only) == 0 {
		renderBusFactor(&b, &res.Bus)
	}
	if (wants(only, "temporal", "all")) || len(only) == 0 {
		renderCoupling(&b, &res.Coupling)
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// ---- boundaries ----

func renderBoundaries(b *strings.Builder, g *topology.Graph) {
	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("ARCHITECTURE BOUNDARIES"))
	b.WriteByte('\n')

	if len(g.Boundaries) == 0 {
		b.WriteString("  " + styleDim.Render("No Go packages were found, so no boundaries "+
			"could be inferred. The boundary audit reads Go source only.") + "\n")
		return
	}

	packages := 0
	for _, bd := range g.Boundaries {
		packages += bd.Packages
	}
	b.WriteString(styleDim.Render(fmt.Sprintf(
		"  %d package(s) in %d file(s), %d region(s)%s",
		packages, g.Files, len(g.Boundaries), moduleSuffix(g.Module))))
	b.WriteByte('\n')
	for _, bd := range g.Boundaries {
		name := bd.Name
		if name == "" {
			name = "(root)"
		}
		b.WriteString(fmt.Sprintf("  %-14s %s\n",
			styleLabel.Render(truncate(name, 14)),
			styleDim.Render(fmt.Sprintf(
				"%d pkg, %d file(s)%s", bd.Packages, bd.Files, dependsOn(bd)))))
	}

	if len(g.Violations) == 0 {
		b.WriteByte('\n')
		b.WriteString("  " + styleGood.Render("No boundary violations.") + "\n")
		b.WriteString("  " + styleDim.Render(
			"Rules checked: nothing below cmd/ imports it, no package imports the "+
				"module root, and no import cycle among local packages.") + "\n")
		return
	}

	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("⚠ ARCHITECTURE BOUNDARY VIOLATIONS"))
	b.WriteByte('\n')
	for _, v := range g.Violations {
		b.WriteString("  " + styleBad.Render("⚠") + " " +
			styleLabel.Render(string(v.Kind)) + " " +
			styleDim.Render(truncate(v.Package, 52)) + "\n")
		b.WriteString("    " + styleDim.Render(v.Detail) + "\n")
	}
	b.WriteString("  " + styleDim.Render(
		"These are layout rules, not judgements about design. cmd/ is the binary "+
			"layer and sits at the top of the dependency graph; a cycle is a "+
			"relation the Go compiler rejects outright.") + "\n")
}

func moduleSuffix(m string) string {
	if m == "" {
		return " (no go.mod found)"
	}
	return " in " + m
}

func dependsOn(bd topology.Boundary) string {
	if len(bd.DependsOn) == 0 {
		return ""
	}
	return ", imports from " + strings.Join(bd.DependsOn, ", ")
}

// ---- bus factor ----

func renderBusFactor(b *strings.Builder, res *topology.BusFactorResult) {
	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("🔴 HIGH BUS FACTOR RISK MODULES"))
	b.WriteByte('\n')

	b.WriteString(styleDim.Render(fmt.Sprintf(
		"  %d commit(s) between %s and %s",
		res.Commits, shortDate(res.FirstCommitAt), shortDate(res.LastCommitAt))))
	b.WriteByte('\n')

	if res.Commits == 0 {
		b.WriteString("  " + styleDim.Render(
			"No commits, so nothing can be attributed to anyone.") + "\n")
		return
	}

	if res.InsufficientHistory != "" {
		b.WriteString("  " + styleWarn.Render(res.InsufficientHistory) + "\n")
	}

	// Repository-wide ownership first: a file-level list without it is a set of
	// accusations with no denominator.
	if len(res.Authors) > 0 {
		b.WriteString("\n  " + styleDim.Render("Repository ownership, weighted by recency:") + "\n")
		for _, a := range res.Authors {
			b.WriteString(fmt.Sprintf("    %-20s %s %s %s\n",
				truncate(a.Author, 20),
				styleDim.Render(fmt.Sprintf("%3.0f%%", a.WeightedShare*100)),
				styleDim.Render(fmt.Sprintf("raw %3.0f%%", a.RawShare*100)),
				styleDim.Render(fmt.Sprintf("last %s", shortDate(a.LastCommit)))))
		}
	}

	if len(res.StaleFiles) == 0 {
		if res.InsufficientHistory == "" {
			b.WriteString("\n  " + styleGood.Render(
				"No file is dominated by an author who has gone quiet.") + "\n")
		}
	} else {
		b.WriteByte('\n')
		b.WriteString(styleDim.Render(fmt.Sprintf("  (%d file(s))", len(res.StaleFiles))))
		b.WriteByte('\n')
		for _, f := range res.StaleFiles {
			b.WriteString(fmt.Sprintf("    %s %s %s\n",
				styleBad.Render(fmt.Sprintf("%3.0f%%", f.DominantShare*100)),
				styleLabel.Render(truncate(f.Path, 44)),
				styleDim.Render(fmt.Sprintf("bus factor %d", f.BusFactor))))
			b.WriteString("      " + styleDim.Render(f.RiskReason) + "\n")
		}
	}

	if len(res.Directories) > 0 {
		b.WriteByte('\n')
		b.WriteString(styleDim.Render("  Most concentrated directories:"))
		b.WriteByte('\n')
		shown := res.Directories
		if len(shown) > 5 {
			shown = shown[:5]
		}
		for _, d := range shown {
			marker := " "
			if d.AtRisk {
				marker = "!"
			}
			b.WriteString(fmt.Sprintf("    %s %-28s %s %s\n",
				marker, truncate(d.Path, 28),
				styleDim.Render(fmt.Sprintf("%3.0f%%", d.DominantShare*100)),
				styleDim.Render(fmt.Sprintf("%s, bus factor %d, %d file(s)",
					truncate(d.Dominant.Author, 16), d.BusFactor, d.Files))))
		}
		b.WriteString("  " + styleDim.Render(
			"A file is at risk only when one author both holds the majority and "+
				"has stopped committing. Single-author is not the same as "+
				"unmaintained, and the two conditions are checked separately.") + "\n")
	}
}

// ---- coupling ----

func renderCoupling(b *strings.Builder, res *topology.CouplingResult) {
	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("🔗 HIDDEN CO-CHANGE COUPLED FILE PAIRS"))
	b.WriteByte('\n')

	if res.InsufficientHistory != "" {
		b.WriteString("  " + styleWarn.Render(res.InsufficientHistory) + "\n")
		return
	}

	if len(res.HiddenPairs) == 0 {
		b.WriteString("  " + styleGood.Render(
			"No pair co-changes above the threshold without an import between them.") + "\n")
		b.WriteString("  " + styleDim.Render(
			"Co-change is an association observed in commits, not a dependency "+
				"analysis. Two files always bumped together score the same as "+
				"two files always refactored together, and this cannot tell "+
				"those apart.") + "\n")
		return
	}

	b.WriteString(styleDim.Render(fmt.Sprintf(
		"  %d pair(s) above the coupling threshold with no import between them.",
		len(res.HiddenPairs))))
	b.WriteByte('\n')
	for _, p := range res.HiddenPairs {
		b.WriteString(fmt.Sprintf("    %s %s\n",
			styleWarn.Render(fmt.Sprintf("%3.0f%%", p.Coupling*100)),
			truncate(p.A, 44)))
		b.WriteString("      " + truncate(p.B, 44) + "\n")
		detail := fmt.Sprintf("co-changed in %d of the %d/%d commits touching each",
			p.SharedCommits, p.CommitsA, p.CommitsB)
		if p.SameBoundary {
			detail += "; both inside one architectural region"
		}
		if p.SamePackage {
			detail += " (same package)"
		}
		b.WriteString("      " + styleDim.Render(detail) + "\n")
	}
	b.WriteString("  " + styleDim.Render(
		"No import means no compiler enforces this: the coupling lives in a "+
			"convention or a review habit. Reviewing these pairs together is "+
			"worth doing; assuming they must always change together is not "+
			"justified.") + "\n")
}
