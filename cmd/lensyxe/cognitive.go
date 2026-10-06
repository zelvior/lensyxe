package main

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/cognitive"
	"github.com/zelvior/lensyxe/internal/report"
)

// newCognitiveCmd builds `lensyxe cognitive [path]`.
func newCognitiveCmd(a *app) *cobra.Command {
	var (
		top int
	)

	cmd := &cobra.Command{
		Use:   "cognitive [path]",
		Short: "Measure structural properties that make code hard to follow",
		Long: strings.TrimSpace(`
Measure three structural properties of each source file and combine them into a
0-100 friction index:

  - Variable lifetime span: lines between a local's declaration and its last use.
  - Context-switch density: distinct call targets referenced per hundred lines.
  - Scope depth: the deepest block nesting in a function.

Go files are parsed with go/parser, so every figure for them is exact. Other
languages are measured line by line, which is approximate; those files are
labelled with their method so an exact and an approximate measurement are never
compared as if they were the same thing.

There is no reading-time figure and this command will not invent one. There is no
validated mapping from these structural properties to the minutes a person will
spend reading a file, so a number carrying that unit would be a measurement
nobody took.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := a.withTarget(args)

			res, err := cognitive.Analyze(cfg.Target, cognitive.DefaultConfig())
			if err != nil {
				return err
			}

			if top > 0 && len(res.Files) > top {
				// Trimming here rather than in the package keeps the result
				// complete for any other consumer.
				res.Files = res.Files[:top]
			}
			return report.RenderCognitive(cmd.OutOrStdout(), &res)
		},
	}

	cmd.Flags().IntVar(&top, "top", 0,
		"show only the N highest-friction files (0 shows all)")

	return cmd
}
