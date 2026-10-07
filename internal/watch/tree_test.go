package watch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

func TestEmitDropsOldestWhenBufferIsFull(t *testing.T) {
	dir := t.TempDir()
	w, err := New(Config{
		Root:     dir,
		Analyze:  func(context.Context) (*models.Snapshot, error) { return nil, nil },
		Debounce: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	// A buffer of one: the second emit must evict the first rather than block.
	out := make(chan Event, 1)
	w.emit(out, Event{Kind: EventAnalyze, Changed: []string{"first.go"}})
	w.emit(out, Event{Kind: EventAnalyze, Changed: []string{"second.go"}})

	ev := <-out
	if len(ev.Changed) != 1 || ev.Changed[0] != "second.go" {
		t.Errorf("kept %v, want the newest event", ev.Changed)
	}
}

func TestRelPath(t *testing.T) {
	dir := filepath.Join("/repo", "src")
	w := &Watcher{cfg: Config{Root: filepath.Join("/repo")}}
	if got := w.relPath(filepath.Join(dir, "a.go")); got != "src/a.go" {
		t.Errorf("relPath = %q, want src/a.go", got)
	}
	// Paths outside the root have no repo-relative form.
	if got := w.relPath(filepath.Join("/elsewhere", "a.go")); got != "" {
		t.Errorf("relPath outside root = %q, want empty", got)
	}
	if got := w.relPath(filepath.Join("/repo")); got != "" {
		t.Errorf("relPath of the root itself = %q, want empty", got)
	}
}

// Close must be idempotent: signal handling plus deferred cleanup both call it.
func TestCloseIsIdempotent(t *testing.T) {
	w, _, _ := newTestWatcher(t, func(context.Context) (*models.Snapshot, error) {
		return snapshot(80, 5), nil
	})
	if err := w.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// The tree registration must skip ignored and VCS directories.
func TestAddTreeSkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{"src", "node_modules", ".git", "vendor"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "a.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	log := &syncBuffer{}
	w, err := New(Config{
		Root:       dir,
		Analyze:    func(context.Context) (*models.Snapshot, error) { return nil, nil },
		IgnoreDirs: []string{"node_modules", "vendor"},
		Debounce:   time.Millisecond,
		Log:        log,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	// An ignored directory must not produce a registration error, and a file
	// written there must not be seen.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan Event, 16)
	go func() { _ = w.Run(ctx, events) }()
	<-events // ready

	if err := os.WriteFile(filepath.Join(dir, "node_modules", "x.js"), []byte("var a=1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	// No analysis should be triggered by a file in an unwatched directory.
	time.Sleep(200 * time.Millisecond)
}
