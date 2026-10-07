package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/code"
	"github.com/zelvior/lensyxe/internal/gap"
	"github.com/zelvior/lensyxe/internal/report"
)

// newGapCmd builds `lensyxe gap [path]`.
func newGapCmd(a *app) *cobra.Command {
	var (
		profilePath string
		format      string
		minCoverage float64
		limit       int
	)

	cmd := &cobra.Command{
		Use:   "gap [path]",
		Short: "Cross-reference static complexity against real runtime execution",
		Long: strings.TrimSpace(`
Join what the code looks like to what actually ran, and report the difference.

Accepts three profile formats, detected from content rather than extension:

  --profile cpu.pprof     a gzipped Go pprof CPU or heap profile
  --profile spans.json    an OpenTelemetry span export, array or resourceSpans
  --profile access.log    a structured HTTP access log

The profile is not optional. Without runtime evidence this command has nothing to
compare against, and reporting static complexity alone would answer a question
lensyxe analyze already answers better.

Three sections are reported, and they call for opposite actions:

  Critical Path Hotspots   complicated code that actually executes
  Deprioritized Debt       complicated code that does not
  Phantom Code Candidates  code with no runtime evidence at all

Phantom code is the strongest claim in the output and the easiest to get wrong by
accident: an untested path, a disabled feature flag, and a caller in another
process all look identical in a profile. It is therefore only reported when at
least --min-coverage of the profile's observations resolved to a source function.
Below that, files are listed as candidates with the shortfall stated.

An access log records request routes, not functions. Its counts and latency are
reported against routes and are not joined to code, because nothing in a log line
says which handler ran.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(profilePath) == "" {
				return errors.New(
					"--profile is required: this command compares static analysis " +
						"against runtime evidence, and without a profile there is " +
						"nothing to compare. Use lensyxe analyze for static-only metrics")
			}

			cfg := a.withTarget(args)

			prof, err := gap.Load(profilePath)
			if err != nil {
				return err
			}

			// Static measurements come from the same analyzer `analyze` uses, so
			// the two commands cannot disagree about what is complex.
			codeCfg := code.DefaultConfig()
			result, err := code.Analyze(cfg.Target, codeCfg, nil)
			if err != nil {
				return err
			}

			gapCfg := gap.DefaultConfig()
			if minCoverage >= 0 {
				gapCfg.MinCoverage = minCoverage
			}
			if limit > 0 {
				gapCfg.Limit = limit
			}

			res, err := gap.Analyze(cfg.Target, gapCfg, result.PerFile, nil, prof, Version)
			if err != nil {
				return err
			}
			return report.RenderGap(cmd.OutOrStdout(), &res, format)
		},
	}

	f := cmd.Flags()
	f.StringVar(&profilePath, "profile", "",
		"path to a pprof, OpenTelemetry span JSON, or HTTP access log")
	f.StringVar(&format, "format", "table", "output format: table or json")
	f.Float64Var(&minCoverage, "min-coverage", 0.5,
		"profile attribution coverage required before a zero-hit file is called phantom")
	f.IntVar(&limit, "limit", 20, "maximum entries per section")

	return cmd
}

var _ = fmt.Sprintf
