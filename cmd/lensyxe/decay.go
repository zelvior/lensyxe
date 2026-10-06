package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/decay"
	"github.com/zelvior/lensyxe/internal/report"
)

// newDecayCmd builds `lensyxe decay [path]`.
func newDecayCmd(a *app) *cobra.Command {
	var (
		window int
		limit  int
		silo   float64
	)

	cmd := &cobra.Command{
		Use:   "decay [path]",
		Short: "Find code that has stopped changing, and report how fast activity is falling",
		Long: strings.TrimSpace(`
Report three separate findings from git history:

  - Unchanged files: tracked files with no change inside the activity window.
  - Knowledge silos: files whose history is dominated by one contributor. This
    is the per-file form of a bus factor and overlaps the repository-wide one
    ` + "`lensyxe analyze`" + ` reports.
  - Activity half-life: how long the rate of change takes to halve.

The half-life needs a dated time series to fit, and is not reported below 180
days of history. Over a shorter span any curve fits, and the fitted number would
describe the shape of the available commits rather than the decay of the code.
When that applies the command says so and still reports the other two findings.

Nothing here is a prediction. An untouched file is not dead code, and a
single-author file is not unmaintained code.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := a.withTarget(args)

			settings := decay.DefaultConfig()
			if window > 0 {
				settings.WindowDays = window
			}
			settings.Limit = limit
			if silo > 0 {
				settings.SiloShare = silo
			}

			res, err := decay.Analyze(cmd.Context(), cfg.Target, settings)
			if err != nil {
				return err
			}
			return report.RenderDecay(cmd.OutOrStdout(), &res)
		},
	}

	f := cmd.Flags()
	f.IntVar(&window, "window", 0,
		"activity window in days (0 uses the default of 90)")
	f.IntVar(&limit, "limit", 25, "maximum files per finding")
	f.Float64Var(&silo, "silo-share", 0,
		"single-contributor share that marks a file a silo (0 uses the default of 0.9)")

	return cmd
}
