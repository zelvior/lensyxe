package compare

import (
	"archive/tar"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

// ids renders risk IDs for assertion failure messages.
func ids(risks []models.Risk) string {
	if len(risks) == 0 {
		return "(none)"
	}
	out := make([]string, 0, len(risks))
	for _, r := range risks {
		out = append(out, r.ID)
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// branchyBody returns a function body with n branch points.
func branchyBody(n int) string {
	var b strings.Builder
	b.WriteString("func branchy() {\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\tif c%d && d%d {\n\t\tswitch v {\n\t\tcase 1:\n\t\tdefault:\n\t\t}\n\t}\n", i, i)
	}
	b.WriteString("}\n")
	return b.String()
}

// hasGit reports whether git is available, skipping the test if not.
func hasGit(t *testing.T) bool {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	return true
}

// initRepo creates a git repository with deterministic identity.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main", ".")
	gitIn(t, dir, "config", "user.name", "Tester")
	gitIn(t, dir, "config", "user.email", "tester@example.com")
	gitIn(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// gitIn runs a git command in dir, skipping the test on failure.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(),
		"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git %v failed (%v): %s", args, err, out)
	}
}

// gitOut2 runs git and returns stdout.
func gitOut2(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

// writeIn writes a file inside dir.
func writeIn(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// statFile is a thin os.Stat wrapper for readability in assertions.
func statFile(path string) (os.FileInfo, error) { return os.Stat(path) }

// tarEntry describes one archive member for writeTar.
type tarEntry struct {
	name     string
	body     string
	size     int64
	mode     int64
	linkname string
	symlink  bool
}

// writeTar builds a tar archive at path containing the given entries.
func writeTar(t *testing.T, path string, entries ...tarEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create tar: %v", err)
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: e.mode}
		switch {
		case e.symlink:
			hdr.Typeflag = tar.TypeSymlink
			hdr.Linkname = e.linkname
		case e.size > 0:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = e.size
		default:
			hdr.Typeflag = tar.TypeReg
			hdr.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Size > 0 {
			// Pad with zeroes so the declared size matches the payload.
			payload := []byte(e.body)
			if int64(len(payload)) < hdr.Size {
				payload = append(payload, make([]byte, hdr.Size-int64(len(payload)))...)
			}
			if _, err := tw.Write(payload); err != nil {
				t.Fatalf("write body: %v", err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
}
