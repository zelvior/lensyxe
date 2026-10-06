package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/analyzer"
	"github.com/zelvior/lensyxe/internal/report"
)

// newStatusCmd builds `lensyxe status [path]`.
//
// The command exists because `analyze` is a report and a report is the wrong
// thing to read when you only want to know where things stand. The split is
// deliberate: status runs the same analysis and renders a summary, so it cannot
// disagree with the full report about any number.
func newStatusCmd(a *app) *cobra.Command {
	var (
		format    string
		noPersist bool
	)

	cmd := &cobra.Command{
		Use:   "status [path]",
		Short: "Show the current health of a repository at a glance",
		Long: strings.TrimSpace(`
Show where a repository stands right now: the score, the three dimensions, a
count of risks, and which dimension is weakest.

This is the same analysis that ` + "`lensyxe analyze`" + ` performs, rendered in a few
lines rather than a full report. It is not a cheaper measurement -- the numbers
are identical -- only a shorter rendering, so the score here and the score in
` + "`lensyxe analyze`" + ` always agree.

Unlike ` + "`lensyxe analyze`" + `, status does not record a run to the history
database. Checking whether you are healthy should not itself become an entry in
your health history.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved := a.withTarget(args)

			// The same scan analyze performs. status differs only in what it does
			// with the result, which is what keeps the two from ever disagreeing
			// about a number.
			snap, err := analyzer.Scan(cmd.Context(), scanConfig(resolved), Version)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return fmt.Errorf("analysis cancelled")
				}
				return err
			}

			out := cmd.OutOrStdout()
			switch strings.ToLower(format) {
			case "json":
				return report.RenderJSON(out, snap)
			case "markdown", "md":
				return report.RenderMarkdown(out, snap, report.MarkdownOptions{})
			case "terminal", "":
				return report.RenderStatus(out, snap)
			default:
				return fmt.Errorf("unsupported format %q (want terminal, markdown, or json)", format)
			}
		},
	}

	cmd.Flags().StringVar(&format, "format", "terminal",
		"output format: terminal, json, or markdown")
	cmd.Flags().BoolVar(&noPersist, "no-persist", false,
		"accepted for symmetry with analyze; status never records a run")

	return cmd
}
