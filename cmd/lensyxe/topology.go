package main

import (
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/report"
	"github.com/zelvior/lensyxe/internal/topology"
)

// newTopologyCmd builds `lensyxe topology [path]`.
func newTopologyCmd(a *app) *cobra.Command {
	var (
		busFactor  bool
		temporal   bool
		boundaries bool
		format     string
		limit      int
		siloShare  float64
		staleDays  int
		halfLife   float64
		coupleMin  float64
	)

	cmd := &cobra.Command{
		Use:   "topology [path]",
		Short: "Bus factor, temporal co-change coupling, and package boundary integrity",
		Long: strings.TrimSpace(`
Three independent analyses of repository structure. They are reported separately
because they answer different questions, and combining them would hide which one
produced a finding.

  --bus-factor     Recency-weighted ownership per file and directory. A file is
                   at risk only when one author both holds the majority and has
                   stopped committing; single-author is not the same as
                   unmaintained.
  --temporal       Pairs of files that co-change in commits without importing
                   each other. No import means no compiler enforces the
                   relationship.
  --boundaries     Package import rules: nothing below cmd/ may import it, no
                   package may import the module root, and no local import cycle.

Run with no selector to get all three.

Two analyses refuse to produce a number when the history cannot support it.
Ownership decay needs commits spread over time to mean anything, and co-change
needs enough commits before a ratio is distinguishable from a coincidence. When
either gate fires the output says so and names the threshold instead of printing
a figure.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := a.withTarget(args)

			// `now` is read once, here, and threaded through, so every section of
			// one run is measured against the same instant. Reading the clock
			// inside each analysis would let a slow run straddle a boundary and
			// disagree with itself.
			now := time.Now()

			graph, err := topology.BuildGraph(cfg.Target)
			if err != nil {
				return err
			}

			bfCfg := topology.DefaultBusFactorConfig()
			if halfLife > 0 {
				bfCfg.HalfLifeDays = halfLife
			}
			if staleDays > 0 {
				bfCfg.StaleDays = staleDays
			}
			if siloShare > 0 {
				bfCfg.RiskShare = siloShare
			}
			if limit > 0 {
				bfCfg.Limit = limit
			}

			cpCfg := topology.DefaultCouplingConfig()
			if coupleMin > 0 {
				cpCfg.Threshold = coupleMin
			}
			if limit > 0 {
				cpCfg.Limit = limit
			}

			commits, err := readHistory(cmd, cfg.Target)
			if err != nil {
				return err
			}

			res := &report.TopologyResult{
				Root: cfg.Target,
				Now:  now.UTC().Format(time.RFC3339),
				Bus:  topology.AggregateBusFactor(cfg.Target, bfCfg, commits, now),
				// The graph is what makes "no import between them" a claim rather
				// than an assumption.
				Coupling: topology.AggregateCoupling(cfg.Target, cpCfg, commits, graph),
				Graph:    *graph,
			}

			var only []string
			if busFactor {
				only = append(only, "bus")
			}
			if temporal {
				only = append(only, "temporal")
			}
			if boundaries {
				only = append(only, "boundary")
			}
			return report.RenderTopology(cmd.OutOrStdout(), res, format, only)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&busFactor, "bus-factor", false, "report recency-weighted ownership and bus-factor risk")
	f.BoolVar(&temporal, "temporal", false, "report file pairs that co-change without importing each other")
	f.BoolVar(&boundaries, "boundaries", false, "report package boundary violations")
	f.StringVar(&format, "format", "table", "output format: table or json")
	f.IntVar(&limit, "limit", 25, "maximum entries per section")
	f.Float64Var(&siloShare, "risk-share", 0,
		"ownership share above which one author controls a file (0 uses the default of 0.7)")
	f.IntVar(&staleDays, "stale-days", 0,
		"days without a commit before an owner is considered absent (0 uses the default of 90)")
	f.Float64Var(&halfLife, "half-life", 0,
		"recency decay half-life in days (0 uses the default of 90)")
	f.Float64Var(&coupleMin, "coupling", 0,
		"minimum co-change fraction for a pair to be reported (0 uses the default of 0.6)")

	return cmd
}
