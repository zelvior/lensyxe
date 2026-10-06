// Package golden implements golden-file comparison for Lensyxe' test suite.
//
// A golden file is the exact expected output of a deterministic function. The
// point is regression detection with a readable diff: when a report's layout
// changes, a reviewer sees a line-by-line diff rather than having to infer what
// moved from a pile of string assertions.
//
// # Regenerating
//
//	go test ./... -update
//
// The `-update` flag is registered here in an init function, which means every
// test binary in the module has to import this package for the flag to be
// recognized. Go passes an unrecognized flag to every test binary it builds, so
// without that import `go test ./... -update` fails outright in the packages
// that lack it. Each test package therefore carries a one-line blank import.
//
// # Determinism
//
// A golden file is only useful if the output is reproducible. The report
// renderers embed a timestamp, a scan duration, and an absolute path, all of
// which change on every run, so callers are expected to normalize those before
// calling Check. Normalization is the caller's job rather than this package's
// because only the caller knows which fields are incidental for its fixture.
//
// This package deliberately does not import "testing": it returns errors so it
// stays a plain helper and cannot be reached from production code by accident.
package golden

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// update records the state of the -update flag.
var update bool

func init() {
	flag.BoolVar(&update, "update", false,
		"rewrite golden files from the current output instead of comparing against them")
}

// UpdateEnabled reports whether -update was passed.
//
// Tests use it to skip assertions that only make sense when comparing, such as
// checking that a rendered report actually contains a particular finding.
func UpdateEnabled() bool { return update }

// Result describes what Check did.
type Result struct {
	// Wrote is true when -update rewrote the file.
	Wrote bool
	// Path is the golden file that was compared or written.
	Path string
}

// Check compares got against the golden file at path.
//
// With -update set it writes got and reports Wrote. Without it, a missing file
// is a failure rather than an implicit creation: a golden file that appears
// without anyone running -update has not been reviewed, and accepting one would
// let a broken renderer define its own correct output.
//
// The returned error carries a unified diff, which `go test` prints verbatim.
func Check(path string, got []byte) (Result, error) {
	res := Result{Path: path}

	if update {
		if err := write(path, got); err != nil {
			return res, err
		}
		res.Wrote = true
		return res, nil
	}

	want, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return res, fmt.Errorf(
				"golden file %s does not exist.\n"+
					"This is a failure rather than an automatic pass: a golden file\n"+
					"that appears without review has not been shown to be correct.\n"+
					"Run: go test ./... -update", path)
		}
		return res, err
	}

	// Compare bytes first. A byte mismatch is reported without a diff, because
	// the likely cause (line endings, a trailing newline) is not something a
	// text diff would make clearer.
	if bytes.Equal(got, want) {
		return res, nil
	}

	return res, fmt.Errorf("output does not match %s\n\n%s", path, Diff(want, got))
}

// write creates the golden file and its directory.
func write(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("golden: create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("golden: write %s: %w", path, err)
	}
	return nil
}

// Diff renders a minimal unified diff of want against got.
//
// This is not a full implementation of the unified diff format. It emits the
// first differing line and a short window around it, which is what a reviewer
// needs from a 400-line report and costs a fraction as much as a real diff.
func Diff(want, got []byte) string {
	wantLines := splitLines(want)
	gotLines := splitLines(got)

	firstDiff := -1
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		w, okW := lineAt(wantLines, i)
		g, okG := lineAt(gotLines, i)
		if !okW || !okG || w != g {
			firstDiff = i
			break
		}
	}
	if firstDiff < 0 {
		return "  (files differ only in trailing bytes)"
	}

	const context = 3
	start := firstDiff - context
	if start < 0 {
		start = 0
	}

	var b strings.Builder
	totalLines := len(wantLines)
	if len(gotLines) > totalLines {
		totalLines = len(gotLines)
	}
	fmt.Fprintf(&b, "first difference at line %d of %d\n", firstDiff+1, totalLines)

	for i := start; i < firstDiff+context+1 && i < totalLines+context; i++ {
		w, okW := lineAt(wantLines, i)
		g, okG := lineAt(gotLines, i)
		switch {
		case okW && okG:
			if w == g {
				fmt.Fprintf(&b, "  %s\n", w)
				continue
			}
			// Both sides present and different: this is a replacement, and a
			// diff that showed only the removed line would hide what the new
			// content actually is.
			fmt.Fprintf(&b, "- %s\n", w)
			fmt.Fprintf(&b, "+ %s\n", g)
		case okW:
			fmt.Fprintf(&b, "- %s\n", w)
		case okG:
			fmt.Fprintf(&b, "+ %s\n", g)
		default:
			return b.String() // both exhausted; nothing further to show
		}
	}
	return b.String()
}

// splitLines splits into lines with the terminators removed, so a trailing
// newline does not produce a phantom empty final line.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func lineAt(lines []string, i int) (string, bool) {
	if i < 0 || i >= len(lines) {
		return "", false
	}
	return lines[i], true
}
