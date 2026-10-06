package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/zelvior/lensyxe/internal/gates"
)

// RenderGateFailures writes the threshold breach report.
//
// A non-empty result means the run failed a configured gate. Output goes to
// stderr in normal use so a JSON consumer on stdout is not corrupted.
func RenderGateFailures(w io.Writer, breaches []gates.Breach) error {
	if len(breaches) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "THRESHOLD FAILURES (%d)\n", len(breaches))
	for _, br := range breaches {
		fmt.Fprintf(&b, "  ✗ %-24s %s\n", br.Rule, br.Message)
	}
	b.WriteString("\n  configure these under `thresholds:` in .lensyxe.yml, " +
		"or relax them if the change is intended\n")

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("render gate failures: %w", err)
	}
	return nil
}
