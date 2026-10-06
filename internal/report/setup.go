package report

import (
	"io"
	"strings"

	"github.com/zelvior/lensyxe/internal/setup"
)

// RenderSetup writes a configuration proposal.
//
// The styling is deliberately confined to the section headings. The body of
// this output is a YAML file, and a config file displayed in colour is a config
// file that gets copied with escape codes in it. Colouring the headings and
// nothing else keeps the block copyable byte for byte.
func RenderSetup(w io.Writer, target string, p setup.Proposal) error {
	var b strings.Builder

	b.WriteString(styleTitle.Render("lensyxe setup"))
	b.WriteByte('\n')
	b.WriteString(styleDim.Render(target))
	b.WriteString("\n\n")

	// The CI gate comes first because it is the part that requires a decision
	// rather than a file. It is a command line, and it is labelled as one,
	// because the thresholds are flags on analyze rather than config keys.
	if p.CISuggestion != "" {
		b.WriteString(styleHeader.Render("CI GATE (a command line, not a config key)"))
		b.WriteByte('\n')
		b.WriteString("  " + p.CISuggestion + "\n")
		b.WriteString("  " + styleDim.Render(
			"placed below the current score, so it catches regressions rather than "+
				"failing on the next unrelated commit") + "\n\n")
	}

	b.WriteString(styleHeader.Render("PROPOSED .lensyxe.yml"))
	b.WriteByte('\n')
	for _, line := range strings.Split(strings.TrimRight(p.Render(), "\n"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") || strings.TrimSpace(line) == "" {
			b.WriteString("  " + styleDim.Render(line) + "\n")
			continue
		}
		b.WriteString("  " + line + "\n")
	}

	// What was deliberately left alone, and why. This section is the point of
	// the command: a proposal that silently omits a key reads as an oversight
	// rather than as a decision.
	if len(p.Warnings) > 0 {
		b.WriteByte('\n')
		b.WriteString(styleHeader.Render("NOT CHANGED, AND WHY"))
		b.WriteByte('\n')
		for _, warn := range p.Warnings {
			b.WriteString("  " + styleWarn.Render("* ") + warn + "\n")
		}
	}

	_, err := io.WriteString(w, b.String())
	return err
}

// RenderSetupFooter is the advice printed after a proposal, which differs
// depending on whether anything was written.
func RenderSetupFooter(w io.Writer, written bool, path string) error {
	msg := "\nNothing was written. Re-run with --write to create " + path + ".\n"
	if written {
		msg = "\nWrote " + path + "\n" +
			"No reported score changes: nothing in this file affects scoring.\n"
	}
	_, err := io.WriteString(w, msg)
	return err
}
