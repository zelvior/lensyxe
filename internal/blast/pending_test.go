package blast

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Pending changes must include untracked files: a brand new file is the case
// with no history at all, and dropping it would drop the file most likely to
// need a second pair of eyes.
func TestPendingChangesIncludesUntrackedFiles(t *testing.T) {
	dir := newRepoWithHistory(t, 3)

	// Modify a tracked file and create a new one.
	write(t, dir, "a.go", "package a\n\nfunc A999() {}\n")
	write(t, dir, "brand-new.go", "package a\n\nfunc New() {}\n")

	got, err := PendingChanges(context.Background(), dir, 30*time.Second)
	if err != nil {
		t.Fatalf("PendingChanges: %v", err)
	}
	want := map[string]bool{"a.go": false, "brand-new.go": false}
	for _, f := range got {
		if _, ok := want[f]; ok {
			want[f] = true
		}
	}
	for f, found := range want {
		if !found {
			t.Errorf("%q missing from the pending changeset: %v", f, got)
		}
	}
}

// Staged and unstaged tracked changes must both appear. Comparing against the
// index alone would miss unstaged work, which is most of what is in progress.
func TestPendingChangesIncludesUnstagedChanges(t *testing.T) {
	dir := newRepoWithHistory(t, 3)

	write(t, dir, "a.go", "package a\n\nfunc Changed() {}\n")
	got, err := PendingChanges(context.Background(), dir, 30*time.Second)
	if err != nil {
		t.Fatalf("PendingChanges: %v", err)
	}
	found := false
	for _, f := range got {
		if f == "a.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("an unstaged change was not detected: %v", got)
	}
}

// A clean tree is empty, not an error.
func TestPendingChangesOnACleanTree(t *testing.T) {
	dir := newRepoWithHistory(t, 3)
	got, err := PendingChanges(context.Background(), dir, 30*time.Second)
	if err != nil {
		t.Fatalf("PendingChanges on a clean tree: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a clean tree reported changes: %v", got)
	}
}

// Outside a repository there is no changeset to report, and that is not an
// error worth failing on.
func TestPendingChangesOutsideARepository(t *testing.T) {
	got, err := PendingChanges(context.Background(), t.TempDir(), 30*time.Second)
	if err != nil {
		t.Fatalf("PendingChanges outside a repository: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a non-repository reported changes: %v", got)
	}
}

// Ignored files must not be reported; they are not part of a changeset.
func TestPendingChangesRespectsGitignore(t *testing.T) {
	dir := newRepoWithHistory(t, 3)
	write(t, dir, ".gitignore", "*.log\n")
	commit(t, dir, "ignore")

	write(t, dir, "a.go", "package a\n\nfunc Changed() {}\n")
	write(t, dir, "noise.log", "should not appear\n")

	got, err := PendingChanges(context.Background(), dir, 30*time.Second)
	if err != nil {
		t.Fatalf("PendingChanges: %v", err)
	}
	for _, f := range got {
		if strings.HasSuffix(f, ".log") {
			t.Errorf("a gitignored file was reported as a change: %v", got)
		}
	}
}
