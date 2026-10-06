package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/zelvior/lensyxe/internal/cognitive"
)

// RenderCognitive writes the Cognitive Friction report.
//
// There is no reading-time field, and the footer says so. The index is a defined
// quantity derived from three documented structural measures; converting it into
// minutes would require a validated mapping that does not exist, and printing a
// duration nobody measured is the one thing this tool must never do.
func RenderCognitive(w io.Writer, res *cognitive.Result) error {
	if res == nil {
		return fmt.Errorf("render cognitive: nil result")
	}
	var b strings.Builder

	b.WriteString(styleTitle.Render("lensyxe cognitive"))
	b.WriteByte('\n')
	b.WriteString(styleDim.Render(fmt.Sprintf(
		"%d file(s) measured, %d not measurable",
		res.Analyzed, res.Failed)))
	b.WriteByte('\n')
	b.WriteString(rule())

	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("FRICTION INDEX"))
	b.WriteByte('\n')
	b.WriteString("  " + scoreStyle(res.MedianScore).Render(
		fmt.Sprintf("%5.1f / 100", res.MedianScore)) + styleDim.Render("  median") + "\n")
	b.WriteString("  " + scoreStyle(res.WorstScore).Render(
		fmt.Sprintf("%5.1f / 100", res.WorstScore)) + styleDim.Render("  worst file") + "\n")

	if len(res.Files) == 0 {
		b.WriteByte('\n')
		b.WriteString("  " + styleDim.Render(
			"No recognised source files were found."))
		b.WriteString("\n")
		b.WriteString(rule())
		b.WriteString(cognitiveFooter(res))
		_, err := io.WriteString(w, b.String())
		return err
	}

	b.WriteByte('\n')
	b.WriteString(styleHeader.Render("HIGHEST-FRICTION FILES"))
	b.WriteByte('\n')
	b.WriteString(styleDim.Render(
		"Measured columns: median variable lifetime in lines, call targets per " +
			"hundred lines, and deepest block nesting."))
	b.WriteByte('\n')
	for _, f := range res.Files {
		if f.Error != "" && f.Functions == 0 {
			// A file that could not be measured still belongs in the list, with
			// the reason. Dropping it would hide the fact that it exists.
			b.WriteString(fmt.Sprintf("  %s %s\n",
				styleDim.Render("  --  "), truncate(f.Path, 44)))
			b.WriteString("      " + styleDim.Render(f.Error) + "\n")
			continue
		}
		b.WriteString(fmt.Sprintf("  %s %s %s\n",
			scoreStyle(f.Score).Render(fmt.Sprintf("%5.1f", f.Score)),
			styleLabel.Render(truncate(f.Path, 44)),
			styleDim.Render(methodLabel(f.Method))))
		b.WriteString("    " + styleDim.Render(fmt.Sprintf(
			"lifetime %-5s  density %-5s  depth %-3s  functions %d",
			trimFloat(f.MedianLifetime), trimFloat(f.MedianDensity),
			trimInt(f.MaxScopeDepth), f.Functions)) + "\n")

		for _, fn := range f.Worst {
			if fn.Score <= 0 {
				continue
			}
			b.WriteString("    " + styleDim.Render(fmt.Sprintf(
				"  %.1f  %s:%d  %s",
				fn.Score, fn.Name, fn.Line,
				describeFunction(fn))) + "\n")
		}
	}

	b.WriteString(rule())
	b.WriteString(cognitiveFooter(res))
	_, err := io.WriteString(w, b.String())
	return err
}

func cognitiveFooter(res *cognitive.Result) string {
	return styleDim.Render(
		"This index is defined by three structural measures and a documented "+
			"weighting. It is not a reading time: there is no validated mapping "+
			"from these properties to the minutes a person will spend on a file, "+
			"so no such number is printed. Method: Go files are parsed with "+
			"go/parser and are exact; other languages are measured line by line "+
			"and are approximate, and are listed with their method so the two "+
			"are never compared as if they were the same measurement.") + "\n"
}

// describeFunction renders one function's contribution.
func describeFunction(fn cognitive.Function) string {
	parts := []string{
		fmt.Sprintf("lifetime %d", fn.MedianLifetime),
		fmt.Sprintf("density %s", trimFloat(fn.Density)),
		fmt.Sprintf("depth %d", fn.ScopeDepth),
	}
	return strings.Join(parts, "  ")
}

// methodLabel states how a file was measured, and is never omitted: a file
// measured two ways must not look like a file measured one way.
func methodLabel(m cognitive.Method) string {
	switch m {
	case cognitive.MethodAST:
		return "[parsed]"
	default:
		return "[lexical: approximate]"
	}
}

func trimFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.1f", v)
}

func trimInt(v int) string { return fmt.Sprintf("%d", v) }
