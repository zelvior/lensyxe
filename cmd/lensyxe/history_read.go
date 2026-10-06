package main

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/gitlog"
)

// readHistory reads a repository's commits with the fields the topology
// analyses need.
//
// One read serves all three sections. Reading history separately per section
// would mean three `git log` invocations over the same commits, and three
// different views of the repository if anything changed underneath -- which on a
// live working tree it often does.
func readHistory(cmd *cobra.Command, root string) ([]gitlog.Commit, error) {
	return gitlog.Reader{
		Root:    root,
		Timeout: 30 * time.Second,
		Options: gitlog.Options{
			WantSHA:    true,
			WantEmail:  true,
			SkipMerges: true,
		},
	}.Read(cmd.Context())
}
