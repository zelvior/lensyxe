package compare

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SafeJoin must refuse archive entries that escape the destination directory.
func TestSafeJoinRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	good := []string{
		"a.go",
		"internal/b.go",
		"deep/nested/c.txt",
	}
	for _, name := range good {
		if _, err := safeJoin(base, name); err != nil {
			t.Errorf("safeJoin(%q) errored: %v", name, err)
		}
	}
	bad := []string{
		"../escape.txt",
		"../../escape.txt",
		"/absolute.txt",
		"internal/../../escape.txt",
	}
	for _, name := range bad {
		if _, err := safeJoin(base, name); err == nil {
			t.Errorf("safeJoin(%q) should have been rejected", name)
		}
	}
}

func TestShorten(t *testing.T) {
	if got := shorten("0123456789abcdef"); got != "0123456789ab" {
		t.Errorf("shorten = %q", got)
	}
	if got := shorten("abc"); got != "abc" {
		t.Errorf("shorten = %q, want unchanged", got)
	}
}

func TestAbs(t *testing.T) {
	if abs(-3.5) != 3.5 || abs(2.5) != 2.5 {
		t.Error("abs is broken")
	}
}

func TestVerdictBands(t *testing.T) {
	cases := []struct {
		scoreA, scoreB float64
		wantContains   string
	}{
		{90, 80, "Significant regression"},
		{80, 78, "Slight regression"},
		{80, 90, "Significant improvement"},
		{80, 81, "Slight improvement"},
		{80, 80, "No net change"},
	}
	for _, tc := range cases {
		a, b := snapshotA(), snapshotA()
		a.Health.Score = tc.scoreA
		b.Health.Score = tc.scoreB
		res := build(Config{}, "test", "/repo", RevisionInfo{}, RevisionInfo{}, a, b)
		if !strings.Contains(res.Verdict, tc.wantContains) {
			t.Errorf("verdict for %v->%v = %q, want it to contain %q",
				tc.scoreA, tc.scoreB, res.Verdict, tc.wantContains)
		}
	}
}

// Temp extraction dirs must be cleaned up so repeated runs do not fill the
// system temp directory.
func TestExportRevisionCreatesAndCleansTree(t *testing.T) {
	if !hasGit(t) {
		t.Skip("git not installed")
	}
	repo := initRepo(t)
	writeIn(t, repo, "a.go", "package a\n")
	gitIn(t, repo, "add", ".")
	gitIn(t, repo, "commit", "-q", "-m", "one")

	tree, cleanup, err := exportRevision(context.Background(), repo, "HEAD")
	if err != nil {
		t.Fatalf("exportRevision: %v", err)
	}
	if _, err := statFile(filepath.Join(tree, "a.go")); err != nil {
		t.Errorf("expected a.go in the exported tree: %v", err)
	}
	// The tar itself must not remain in the tree.
	if _, err := statFile(filepath.Join(tree, "tree.tar")); err == nil {
		t.Error("the intermediate tar should be removed after extraction")
	}

	cleanup()
	if _, err := statFile(tree); err == nil {
		t.Error("cleanup should have removed the temp tree")
	}
}

// A large archive entry is refused rather than exhausting the temp directory.
func TestExtractTarRejectsOversizedEntry(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "tree.tar")
	writeTar(t, tarPath, tarEntry{name: "big.bin", size: maxArchiveFileBytes + 1})

	err := extractTar(tarPath, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("expected an error for an oversized archive entry")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error = %v, want it to mention the size cap", err)
	}
}

func TestExtractTarSkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "tree.tar")
	writeTar(t, tarPath,
		tarEntry{name: "ok.go", body: "package p\n", mode: 0o644},
		tarEntry{name: "link", linkname: "ok.go", mode: 0o777, symlink: true},
	)
	out := filepath.Join(dir, "out")
	if err := extractTar(tarPath, out); err != nil {
		t.Fatalf("extractTar: %v", err)
	}
	if _, err := statFile(filepath.Join(out, "ok.go")); err != nil {
		t.Errorf("regular file should be extracted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(out, "link")); err == nil {
		t.Error("symlinks must be skipped, not created")
	}
}

func TestExtractTarRejectsTraversalEntry(t *testing.T) {
	dir := t.TempDir()
	tarPath := filepath.Join(dir, "tree.tar")
	writeTar(t, tarPath, tarEntry{name: "../escaped.go", body: "package p\n", mode: 0o644})

	err := extractTar(tarPath, filepath.Join(dir, "out"))
	if err == nil {
		t.Fatal("expected a traversal entry to be rejected")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Errorf("error = %v, want it to mention the escape", err)
	}
	if _, err := statFile(filepath.Join(dir, "escaped.go")); err == nil {
		t.Error("a file escaped the extraction directory")
	}
}
