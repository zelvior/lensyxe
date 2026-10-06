package gitlog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The stream layout is the thing most easily got wrong, so it is written out
// here rather than described: git emits
//
//	\x00<date>\x00<author>\n<path>\n<path>\n...
//
// per commit, which means splitting on NUL puts the date and the author in
// *separate* pieces. A split-based parser looks for a separator inside a piece
// that no longer has one and returns zero commits -- from every real repository,
// while passing every test built on a wrong mental model.
const twoCommits = "\x002026-01-01T00:00:00Z\x00ann\n" +
	"a.go\nb.go\n" +
	"\x002026-01-02T00:00:00Z\x00bob\n" +
	"a.go\n"

func TestParseReadsTwoCommits(t *testing.T) {
	got := Parse([]byte(twoCommits), Options{})
	if len(got) != 2 {
		t.Fatalf("got %d commits, want 2: %+v", len(got), got)
	}
	if got[0].Author != "ann" || got[1].Author != "bob" {
		t.Errorf("authors = %q, %q; want ann, bob", got[0].Author, got[1].Author)
	}
	if got[0].Files[0] != "a.go" || got[0].Files[1] != "b.go" {
		t.Errorf("files = %v, want [a.go b.go]", got[0].Files)
	}
	if got[1].date().Day() != 2 {
		t.Errorf("second date = %v, want 2 January", got[1].When)
	}
}

// date is a tiny helper so the test reads clearly.
func (c Commit) date() time.Time { return c.When }

func TestParseHandlesPathsWithSpaces(t *testing.T) {
	raw := "\x002026-01-01T00:00:00Z\x00ann\nmy file.go\nother file.go\n"
	got := Parse([]byte(raw), Options{})
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1", len(got))
	}
	if len(got[0].Files) != 2 || got[0].Files[0] != "my file.go" {
		t.Errorf("a path with a space was split or lost: %+v", got[0].Files)
	}
}

func TestParseDeduplicatesFiles(t *testing.T) {
	raw := "\x002026-01-01T00:00:00Z\x00ann\n" +
		"a.go\na.go\nb.go\n"
	got := Parse([]byte(raw), Options{})
	if len(got[0].Files) != 2 {
		t.Errorf("files = %v, want 2 after dedup", got[0].Files)
	}
}

func TestParseSortsFilesForStableOutput(t *testing.T) {
	raw := "\x002026-01-01T00:00:00Z\x00ann\nz.go\na.go\nm.go\n"
	got := Parse([]byte(raw), Options{})
	want := []string{"a.go", "m.go", "z.go"}
	for i := range want {
		if got[0].Files[i] != want[i] {
			t.Fatalf("files = %v, want %v", got[0].Files, want)
		}
	}
}

// A commit with no files carries no evidence and must not become a record.
func TestParseSkipsFileLessCommits(t *testing.T) {
	raw := "\x002026-01-01T00:00:00Z\x00ann\n" +
		"\x002026-01-02T00:00:00Z\x00bob\nreal.go\n"
	got := Parse([]byte(raw), Options{})
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1: %+v", len(got), got)
	}
	if got[0].Author != "bob" {
		t.Errorf("kept %q, want bob", got[0].Author)
	}
}

func TestParseOptionalFields(t *testing.T) {
	// With SHA and email requested, the header carries three NUL-separated
	// fields before the paths.
	raw := "\x00abc123\x002026-01-01T00:00:00Z\x00ann@example.com\x00Ann\nf.go\n"
	got := Parse([]byte(raw), Options{WantSHA: true, WantEmail: true})
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1", len(got))
	}
	if got[0].SHA != "abc123" {
		t.Errorf("sha = %q, want abc123", got[0].SHA)
	}
	if got[0].AuthorEmail != "ann@example.com" {
		t.Errorf("email = %q, want ann@example.com", got[0].AuthorEmail)
	}
	if got[0].Author != "Ann" {
		t.Errorf("author = %q, want Ann", got[0].Author)
	}
	if got[0].When.IsZero() {
		t.Error("the date was not parsed when other fields were requested")
	}
}

func TestParseNumstat(t *testing.T) {
	raw := "\x002026-01-01T00:00:00Z\x00ann\n" +
		"10\t5\ta.go\n" +
		"3\t1\tb.go\n"
	got := Parse([]byte(raw), Options{WantNumstat: true})
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1", len(got))
	}
	if got[0].Insertions != 13 || got[0].Deletions != 6 {
		t.Errorf("counts = +%d/-%d, want +13/-6", got[0].Insertions, got[0].Deletions)
	}
	if len(got[0].Files) != 2 {
		t.Errorf("files = %v, want 2", got[0].Files)
	}
}

// A binary file reports "-\t-\tpath". The counts are unknown, not zero, so the
// commit must survive with the counts left absent.
func TestParseNumstatBinaryFileKeepsThePath(t *testing.T) {
	raw := "\x002026-01-01T00:00:00Z\x00ann\n" +
		"-\t-\tlogo.png\n" +
		"4\t2\ta.go\n"
	got := Parse([]byte(raw), Options{WantNumstat: true})
	if len(got) != 1 {
		t.Fatalf("got %d commits, want 1", len(got))
	}
	if len(got[0].Files) != 2 {
		t.Errorf("files = %v, want both: a binary file still changed", got[0].Files)
	}
	if got[0].Insertions != 4 || got[0].Deletions != 2 {
		t.Errorf("counts = +%d/-%d, want +4/-2 from the only text file",
			got[0].Insertions, got[0].Deletions)
	}
}

func TestParseSurvivesGarbage(t *testing.T) {
	for _, raw := range []string{
		"", "\x00", "\x00\x00", "\x00\x00\x00", "no separators",
		"\x00no-newline", "\x00\x00\x00\n", "\x00\x00\x002026-01-01T00:00:00Z\x00ann\n",
	} {
		for _, c := range Parse([]byte(raw), Options{}) {
			if len(c.Files) == 0 {
				t.Errorf("input %q produced a commit with no files", raw)
			}
		}
	}
}

func TestFailureTextReadsStderr(t *testing.T) {
	// err.Error() alone is only "exit status 128", so matching git's actual
	// message requires ExitError.Stderr. A real git failure is used rather than a
	// shell script so the assertion does not depend on a POSIX shell.
	// Output() rather than Run(): only Output populates ExitError.Stderr when no
	// Stderr was set, and that field is the whole point of this test.
	cmd := exec.Command("git", "log")
	cmd.Dir = t.TempDir()
	_, err := cmd.Output()
	if err == nil {
		t.Fatal("git log outside a repository was expected to fail")
	}
	if !IsUnavailable(err) {
		t.Errorf("a non-repository error was not recognised: %q", FailureText(err))
	}
	if !strings.Contains(strings.ToLower(FailureText(err)), "not a git repository") {
		t.Errorf("FailureText lost git's message: %q", FailureText(err))
	}
}

func TestIsUnavailableToleratesCapitalisation(t *testing.T) {
	// git log says "not a git repository"; git diff says "Not a git repository".
	for _, msg := range []string{
		"not a git repository", "Not a git repository. Use --no-index",
		"fatal: ambiguous argument 'HEAD': unknown revision",
		"fatal: bad revision 'HEAD'",
		"error: this operation must be run in a work tree",
	} {
		if !strings.Contains(msg, "unknown revision") &&
			strings.Contains(msg, "work tree") {
			continue // genuinely not one of the four shapes
		}
		if !IsUnavailable(fakeExitError(msg)) {
			t.Errorf("message %q was not recognised as 'no history'", msg)
		}
	}
}

func TestFailureTextOnNil(t *testing.T) {
	if FailureText(nil) != "" {
		t.Error("FailureText(nil) should be empty")
	}
}

// ---- against a real repository ----

func TestReadFromARealRepository(t *testing.T) {
	dir := newRepo(t, 3)
	got, err := Reader{
		Root:    dir,
		Timeout: 30 * time.Second,
		Options: Options{WantSHA: true, WantEmail: true, SkipMerges: true},
	}.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("read %d commits, want 3", len(got))
	}
	for _, c := range got {
		if c.SHA == "" {
			t.Error("a commit came back with no SHA")
		}
		if c.Author == "" || c.AuthorEmail == "" {
			t.Errorf("author details missing: %+v", c)
		}
		if c.When.IsZero() {
			t.Errorf("no date: %+v", c)
		}
		if len(c.Files) != 1 || c.Files[0] != "f.go" {
			t.Errorf("files = %v, want [f.go]", c.Files)
		}
	}
}

func TestReadOutsideARepositoryIsNotAnError(t *testing.T) {
	got, err := Reader{Root: t.TempDir(), Timeout: 30 * time.Second}.Read(context.Background())
	if err != nil {
		t.Fatalf("Read outside a repository: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a non-repository produced commits: %+v", got)
	}
}

func TestReadRespectsLimit(t *testing.T) {
	dir := newRepo(t, 5)
	got, err := Reader{
		Root: dir, Timeout: 30 * time.Second,
		Options: Options{Limit: 2, SkipMerges: true},
	}.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("read %d commits, want 2", len(got))
	}
}

func TestReadOnAnEmptyRepository(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@example.com"},
		{"config", "user.name", "T"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	got, err := Reader{Root: dir, Timeout: 30 * time.Second}.Read(context.Background())
	if err != nil {
		t.Fatalf("Read on an empty repository: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("an empty repository produced commits: %+v", got)
	}
}

// ---- helpers ----

func newRepo(t *testing.T, commits int) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@example.com")
	run("config", "user.name", "T")
	run("config", "commit.gpgsign", "false")
	run("config", "core.autocrlf", "false")
	for i := 0; i < commits; i++ {
		body := "package f\n\nfunc F" + itoa(i) + "() {}\n"
		if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("commit", "-q", "-m", "c"+itoa(i))
	}
	return dir
}

// fakeExitError builds an ExitError-like error carrying only a message, which is
// what the matching paths have to cope with when Stderr is empty.
func fakeExitError(msg string) error {
	return &plainError{msg: msg}
}

type plainError struct{ msg string }

func (e *plainError) Error() string { return e.msg }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
