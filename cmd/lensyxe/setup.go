package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/analyzer"
	"github.com/zelvior/lensyxe/internal/report"
	"github.com/zelvior/lensyxe/internal/setup"
)

// configFileName is the name setup writes, and the name the loader looks for.
const configFileName = ".lensyxe.yml"

// newSetupCmd builds `lensyxe setup [path]`.
//
// The command is a proposal, not an installation. It measures the repository and
// prints a configuration derived from that measurement; writing it requires an
// explicit --write, and overwriting an existing file requires --force as well.
// A tool that rewrites your config on first sight of it is a tool you cannot
// run twice without checking.
func newSetupCmd(a *app) *cobra.Command {
	var (
		write    bool
		force    bool
		headless bool
	)

	cmd := &cobra.Command{
		Use:   "setup [path]",
		Short: "Propose a .lensyxe.yml derived from this repository's own measurements",
		Long: strings.TrimSpace(`
Analyze the directory and propose a configuration file built from what was
actually measured: the build directories that exist, how deep the git history
goes, and how long the scan itself takes.

Prints the proposal. Nothing is written without --write, and an existing
file is never replaced without --force as well.

Two things it deliberately will not do. It does not propose a
hotspot_threshold, because that value changes the code health score and a
wizard should not move your score without being asked. And it does not put the
CI thresholds in the config file, because they are flags on analyze, not
configuration keys; they are printed as a command line instead.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved := a.withTarget(args)
			target, err := filepath.Abs(resolved.Target)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", resolved.Target, err)
			}

			// --headless exists for a server or container with no terminal to
			// draw on. It takes every default and refuses to prompt, so a
			// non-interactive run cannot hang waiting for a keypress. Requiring
			// --write alongside it is deliberate: a proposal nobody reads, on a
			// machine nobody is watching, is not setup.
			if headless {
				if !write {
					return fmt.Errorf(
						"--headless requires --write: with nobody to read a proposal, " +
							"setup must write the file or do nothing")
				}
				// Anything still goes to stdout, which a container log captures.
				// The proposal is printed as well as written so the run is
				// auditable after the fact.
				fmt.Fprintln(cmd.ErrOrStderr(),
					"lensyxe: headless setup: writing derived configuration")
			}

			snap, err := analyzer.Scan(cmd.Context(), scanConfig(resolved), Version)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return fmt.Errorf("analysis cancelled")
				}
				return err
			}

			proposal := setup.Propose(snap, rootEntries(target), snap.GeneratedAt)

			out := cmd.OutOrStdout()
			if err := report.RenderSetup(out, target, proposal); err != nil {
				return err
			}

			path := filepath.Join(target, configFileName)
			if !write {
				return report.RenderSetupFooter(out, false, path)
			}

			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf(
					"%s already exists. Re-run with --force to replace it, or edit it in place",
					path)
			} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("check %s: %w", path, err)
			}

			if err := os.WriteFile(path, []byte(configHeader+proposal.Render()), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", path, err)
			}
			return report.RenderSetupFooter(out, true, path)
		},
	}

	cmd.Flags().BoolVar(&headless, "headless", false,
		"non-interactive mode for servers and containers: take every default and "+
			"never prompt. Requires --write, because there is nobody to read a proposal")
	cmd.Flags().BoolVar(&write, "write", false, "write the proposal to "+configFileName)
	cmd.Flags().BoolVar(&force, "force", false,
		"replace an existing "+configFileName+" (the old file is overwritten, not merged)")

	return cmd
}

// configHeader precedes the generated keys. It states the precedence rules and
// the one thing a generated file cannot know, so the file explains itself to
// whoever reads it six months from now.
const configHeader = `# Lensyxe configuration
#
# Proposed by ` + "`lensyxe setup`" + ` from a measurement of this repository, and
# generated rather than written by hand. The values below are what was measured
# here, not the built-in defaults.
#
# Every key is optional. Keys not listed keep their built-in defaults, so
# deleting a line reverts that one setting. Precedence: flag > this file (or
# LENSYXE_* env) > built-in default.
#
# A missing or malformed config never aborts a run: Lensyxe warns on stderr and
# falls back to the defaults.
#
# Nothing here affects scoring. hotspot_threshold is the one key that would, and
# it is deliberately not in this file.
#
# This file is local. Nothing in it is read over the network and nothing in it
# causes an outbound request.
#
# Re-run ` + "`lensyxe setup`" + ` to see the reasoning again. It will not
# overwrite this file without --force.

`

// rootEntries returns the set of directory names present at the repository root.
//
// One level only. The wizard proposes what to prune, so it must not itself walk
// the tree looking for build directories: a generated .lensyxe.yml that ignored
// a nested output directory would be pruning the wrong thing.
func rootEntries(target string) map[string]bool {
	out := map[string]bool{}
	entries, err := os.ReadDir(target)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			out[e.Name()] = true
		}
	}
	return out
}
