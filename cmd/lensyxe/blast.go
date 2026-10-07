package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/blast"
	"github.com/zelvior/lensyxe/internal/report"
)

// newBlastCmd builds `lensyxe blast [path] [file...]`.
//
// With no file arguments it reviews the pending changeset: everything modified
// or added but not yet committed. Naming files overrides that, which is what
// makes the command usable on a branch that is already committed.
func newBlastCmd(a *app) *cobra.Command {
	var (
		coupling  float64
		minShared int
		distance  int
		limit     int
	)

	cmd := &cobra.Command{
		Use:   "blast [path] [file...]",
		Short: "Find files that historically change together with the ones you are changing",
		Long: strings.TrimSpace(`
Report which files have historically changed in the same commit as the files in
this changeset.

With no file arguments it reviews the pending changeset: every tracked file
modified since HEAD plus every untracked file that is not gitignored.

Coupling is the share of the rarer file's commits that the other file was also
in. It is an association observed in commits, not a dependency analysis. Two
files that were always bumped together score exactly like two files that were
always refactored together, and this command cannot tell those apart.

Nothing is predicted below ten commits of history, because over fewer commits a
coupling ratio is dominated by coincidence. When that applies the command says
so rather than printing numbers it cannot support.`),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// A leading path is a target; the rest are files within it. Splitting
			// on whether an argument exists keeps `blast` and `blast <file>`
			// working without a flag between them.
			target, files := splitTargetAndFiles(a, args)
			if err := requireDir(target); err != nil {
				return err
			}

			cfg := blast.DefaultConfig()
			cfg.CouplingThreshold = coupling
			cfg.MinSharedCommits = minShared
			cfg.MaxDistance = distance
			cfg.Limit = limit

			if len(files) == 0 {
				pending, err := blast.PendingChanges(
					cmd.Context(), target, cfg.Timeout)
				if err != nil {
					return err
				}
				if len(pending) == 0 {
					return errors.New(
						"nothing to review: the working tree has no uncommitted " +
							"changes. Name the files explicitly, or commit the " +
							"work first")
				}
				files = pending
			}

			res, err := blast.Analyze(cmd.Context(), target, files, cfg)
			if err != nil {
				return err
			}
			if res.Note != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "lensyxe: %s\n", res.Note)
			}
			return report.RenderBlast(cmd.OutOrStdout(), &res)
		},
	}

	f := cmd.Flags()
	f.Float64Var(&coupling, "coupling", 0.5,
		"minimum shared-commit fraction for a pair to be reported")
	f.IntVar(&minShared, "min-shared", 3,
		"commits two files must co-occur in before the pair counts as evidence")
	f.IntVar(&distance, "distance", 3,
		"how many co-change hops from the changeset to search")
	f.IntVar(&limit, "limit", 50, "maximum predicted files to report")

	return cmd
}

// splitTargetAndFiles separates an optional leading directory from file names.
//
// A single argument is treated as the target, so `blast` on its own path behaves
// like every other command in this tool -- unless it names an existing regular
// file, in which case it is the file to review and the configured target stands.
// The filesystem settles an ambiguity the arguments cannot: one argument is a
// path only when it is one.
//
// This mattered. `blast internal/gap/profiler.go` was read as a *directory*,
// which set git's working directory to a file, and the command failed with
//
//	fork/exec ...git.exe: The directory name is invalid
//
// An opaque OS error for what is plainly a file review. Two arguments worked,
// so the documented way to name a file required a redundant "." first.
func splitTargetAndFiles(a *app, args []string) (string, []string) {
	if len(args) == 0 {
		return a.cfg.Target, nil
	}
	if len(args) == 1 {
		if info, err := os.Stat(args[0]); err == nil && !info.IsDir() {
			return a.cfg.Target, args[:1]
		}
		return a.withTarget(args[:1]).Target, nil
	}
	cfg := a.withTarget(args[:1])
	return cfg.Target, args[1:]
}

// requireDir rejects a target that is not a directory, before any command runs
// against it.
//
// Without this the failure surfaces later as an OS error from whatever the
// command happened to exec first, naming that tool rather than the path the user
// typed. The message has to name the path and say what was expected.
func requireDir(target string) error {
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("target %s: %w", target, err)
	}
	if !info.IsDir() {
		return fmt.Errorf(
			"target %s is a file, not a directory; name the directory to "+
				"analyze, or pass the file after it", target)
	}
	return nil
}
