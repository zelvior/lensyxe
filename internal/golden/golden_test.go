package golden

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skipUnderUpdate skips a test that exercises comparison behaviour.
//
// `go test ./... -update` runs every package, and under -update Check writes
// instead of comparing, so assertions about mismatches would all pass vacuously
// and the suite would report coverage it did not have.
func skipUnderUpdate(t *testing.T) {
	t.Helper()
	if UpdateEnabled() {
		t.Skip("comparison behaviour is bypassed by -update")
	}
}

func TestCheckReportsMatch(t *testing.T) {
	skipUnderUpdate(t)

	path := filepath.Join(t.TempDir(), "a.golden")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Check(path, []byte("hello\n"))
	if err != nil {
		t.Fatalf("a matching output must not fail: %v", err)
	}
	if res.Wrote {
		t.Error("without -update nothing should be written")
	}
}

// A golden file that appears without anyone reviewing it has not been shown to
// be correct, so a missing file must fail rather than silently define the
// expectation.
func TestCheckFailsOnMissingGolden(t *testing.T) {
	skipUnderUpdate(t)

	_, err := Check(filepath.Join(t.TempDir(), "absent.golden"), []byte("x"))
	if err == nil {
		t.Fatal("a missing golden file must fail")
	}
	if !strings.Contains(err.Error(), "-update") {
		t.Errorf("the error must say how to create the file, got %q", err)
	}
}

func TestCheckReportsMismatchWithContext(t *testing.T) {
	skipUnderUpdate(t)

	path := filepath.Join(t.TempDir(), "a.golden")
	want := "line 1\nline 2\nline 3\nline 4\nline 5\n"
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Check(path, []byte("line 1\nline 2\nline 3\nCHANGED\nline 5\n"))
	if err == nil {
		t.Fatal("a differing output must fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, "- line 4") {
		t.Errorf("the diff must mark the removed line:\n%s", msg)
	}
	if !strings.Contains(msg, "+ CHANGED") {
		t.Errorf("the diff must mark the added line:\n%s", msg)
	}
	// Context lines make the diff readable rather than a bare assertion.
	if !strings.Contains(msg, "line 3") || !strings.Contains(msg, "line 5") {
		t.Errorf("the diff must include surrounding context:\n%s", msg)
	}
}

func TestCheckHandlesLengthChanges(t *testing.T) {
	skipUnderUpdate(t)

	path := filepath.Join(t.TempDir(), "a.golden")
	if err := os.WriteFile(path, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Got is longer.
	if _, err := Check(path, []byte("a\nb\nc\nd\ne\n")); err == nil {
		t.Error("extra lines must be reported")
	}
	// Got is shorter.
	if _, err := Check(path, []byte("a\n")); err == nil {
		t.Error("missing lines must be reported")
	}
	// Got is empty.
	if _, err := Check(path, nil); err == nil {
		t.Error("empty output must be reported")
	}
}

// A trailing newline difference is a real difference and must be caught. If it
// were not, a renderer that stopped emitting one would pass unnoticed and the
// file would be harder to review in a diff.
func TestCheckCatchesTrailingNewlineOnly(t *testing.T) {
	skipUnderUpdate(t)

	path := filepath.Join(t.TempDir(), "a.golden")
	if err := os.WriteFile(path, []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(path, []byte("a")); err == nil {
		t.Error("a missing trailing newline must be reported")
	}
}

func TestCheckHandlesEmptyGolden(t *testing.T) {
	skipUnderUpdate(t)

	path := filepath.Join(t.TempDir(), "a.golden")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(path, []byte("x")); err == nil {
		t.Error("content against an empty golden must fail")
	}
	if _, err := Check(path, nil); err != nil {
		t.Errorf("empty against empty must match, got %v", err)
	}
}

// Update mode must write the file and create its directory.
func TestCheckWritesUnderUpdate(t *testing.T) {
	// This test asserts the -update path, so it needs the flag on. It cannot
	// set the flag itself without racing every other test in this package, so
	// it only asserts when the suite was invoked with -update.
	if !UpdateEnabled() {
		t.Skip("only meaningful under -update")
	}

	path := filepath.Join(t.TempDir(), "nested", "dir", "a.golden")
	res, err := Check(path, []byte("written\n"))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Wrote {
		t.Error("-update must report that it wrote the file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file was not created: %v", err)
	}
	if string(data) != "written\n" {
		t.Errorf("content = %q", data)
	}
}

func TestSplitLines(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"\n", 0},
		{"a", 1},
		{"a\n", 1},
		{"a\nb", 2},
		{"a\nb\n", 2},
	}
	for _, c := range cases {
		if got := len(splitLines([]byte(c.in))); got != c.want {
			t.Errorf("splitLines(%q) = %d lines, want %d", c.in, got, c.want)
		}
	}
}

func TestDiffOnIdenticalReportsNoDifference(t *testing.T) {
	got := Diff([]byte("a\nb\n"), []byte("a\nb\n"))
	if !strings.Contains(got, "differ only in trailing bytes") {
		t.Errorf("identical inputs must say so, got %q", got)
	}
}

func TestDiffIncludesLineNumber(t *testing.T) {
	got := Diff([]byte("a\nb\nc\n"), []byte("a\nX\nc\n"))
	if !strings.Contains(got, "line 2") {
		t.Errorf("the diff must report where it diverged, got %q", got)
	}
}
