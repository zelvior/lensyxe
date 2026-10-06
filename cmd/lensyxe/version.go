// This file holds the build metadata injected at link time and the `version`
// command that reports it.
//
// The variables are capitalized because the linker writes to them by symbol
// name: `-X main.Version=1.2.3` cannot reach an unexported `version`. Keeping
// them all in one file means the full set of `-X` flags the release pipeline
// must pass is visible in a single place, which is the part that is easy to get
// wrong when they are scattered across the package.
package main

import (
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// Build metadata, overridden at link time:
//
//	go build -ldflags "\
//	  -X main.Version=1.2.3 \
//	  -X main.Commit=8f3a9d2 \
//	  -X main.Date=2026-10-05T06:00:00Z \
//	  -X main.BuiltBy=goreleaser" ./cmd/lensyxe
//
// The defaults are deliberately obvious rather than empty: a binary built
// without the flags must be identifiable as such, and an empty Commit field
// reads like a bug rather than like "not injected".
var (
	// Version is the semantic version, without a leading "v".
	Version = "dev"
	// Commit is the abbreviated git commit the binary was built from.
	Commit = "none"
	// Date is the RFC 3339 build timestamp.
	Date = "unknown"
	// BuiltBy names the build tool, for example "goreleaser" or "go build".
	BuiltBy = "unknown"
)

// versionString renders Version with a leading "v".
//
// goreleaser's {{.Version}} strips the tag's leading "v", so the prefix is
// added here rather than in the template. A version that already carries one is
// left alone, because "vv1.0.0" in a release banner is the kind of small
// embarrassing thing that ships.
func versionString() string {
	v := strings.TrimSpace(Version)
	if v == "" {
		v = "dev"
	}
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

// isDevelopmentBuild reports whether the binary was built without injected
// metadata.
//
// A "dev" version is not an error, but it must not be presented as a release:
// a user who ran a locally built binary needs to know the score came from
// unreleased code.
func isDevelopmentBuild() bool {
	switch strings.ToLower(strings.TrimSpace(Version)) {
	case "", "dev", "devel", "unknown":
		return true
	}
	return false
}

// BuildInfo is the full set of facts `version` reports.
type BuildInfo struct {
	Version   string
	Commit    string
	Date      string
	BuiltBy   string
	GoVersion string
	OS        string
	Arch      string
}

// CurrentBuildInfo collects the build metadata for this binary.
func CurrentBuildInfo() BuildInfo {
	return BuildInfo{
		Version:   versionString(),
		Commit:    Commit,
		Date:      Date,
		BuiltBy:   BuiltBy,
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
}

// String renders the metadata as the aligned block `version` prints.
//
// The field order is fixed and the values are padded by the caller so the
// output stays aligned regardless of how long a commit hash or path is.
func (b BuildInfo) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Lensyxe Core %s\n", b.Version)

	rows := [][2]string{
		{"Commit", b.Commit},
		{"Build Date", b.Date},
		{"Built By", b.BuiltBy},
		{"Go Version", b.GoVersion},
		{"OS/Arch", b.OS + "/" + b.Arch},
	}
	// Padding is computed from the longest label so adding a row later cannot
	// leave the block ragged.
	width := 0
	for _, r := range rows {
		if len(r[0]) > width {
			width = len(r[0])
		}
	}
	for _, r := range rows {
		fmt.Fprintf(&sb, "%-*s %s\n", width+1, r[0]+":", r[1])
	}
	return sb.String()
}

// newVersionCmd prints the build metadata.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the Lensyxe version and build metadata",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return writeVersion(cmd.OutOrStdout(), CurrentBuildInfo(), isDevelopmentBuild())
		},
	}
}

// writeVersion prints the metadata block.
//
// An unversioned build says so explicitly. A `dev` binary that renders exactly
// like a release binary is how a bug report ends up filed against a tag that
// never shipped.
func writeVersion(w io.Writer, info BuildInfo, dev bool) error {
	if _, err := io.WriteString(w, info.String()); err != nil {
		return fmt.Errorf("version: %w", err)
	}
	if dev {
		if _, err := io.WriteString(w,
			"\nDevelopment build: this binary was compiled without injected "+
				"version metadata, so it does not correspond to any release.\n"); err != nil {
			return fmt.Errorf("version: %w", err)
		}
	}
	return nil
}
