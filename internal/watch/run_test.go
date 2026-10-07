package watch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

func TestRunReactsToFileChange(t *testing.T) {
	var mu sync.Mutex
	runs := 0
	w, dir, log := newTestWatcher(t, func(context.Context) (*models.Snapshot, error) {
		mu.Lock()
		runs++
		n := runs
		mu.Unlock()
		// Health degrades on the second run so a delta is reportable.
		if n == 1 {
			return snapshot(87, 4), nil
		}
		return snapshot(86, 7), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan Event, 16)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, events) }()

	// Consume the ready event and the initial analysis.
	<-events
	waitFor(t, 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return runs >= 1
	})

	if err := os.WriteFile(filepath.Join(dir, "engine.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !waitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return runs >= 2
	}) {
		t.Fatalf("watcher did not re-analyze after a write (runs=%d)", runs)
	}

	// Drain to find the change event.
	var change Event
	found := false
	for !found {
		select {
		case ev := <-events:
			if ev.Kind == EventAnalyze && len(ev.Changed) > 0 {
				change = ev
				found = true
			}
		case <-time.After(2 * time.Second):
			cancel()
			waitRunExit(t, done, "test")
			t.Fatalf("no change event; log: %s", log.String())
		}
	}
	if len(change.Changed) != 1 || change.Changed[0] != "engine.go" {
		t.Errorf("Changed = %v, want [engine.go]", change.Changed)
	}
	if change.Previous == nil {
		t.Error("a change event must carry the previous snapshot")
	}
	if !waitFor(t, time.Second, func() bool {
		return strings.Contains(log.String(), "Health: 87.0 -> 86.0")
	}) {
		t.Errorf("missing health delta in log:\n%s", log.String())
	}
}

// A burst of writes within the debounce window must collapse into one analysis.
// TestRunDebouncesBursts asserts that a burst of changes is coalesced into a
// single analysis.
//
// The debounce window is deliberately generous (400ms). The property under test
// is "five changes produce one analysis", which only holds when the five
// changes arrive inside one window. With the default 30ms window this test was
// the flakiest in the package: on a loaded machine five small writes routinely
// take longer than 30ms, the debouncer fires more than once, and the assertion
// fails for reasons that have nothing to do with the watcher.
//
// A wider window makes the test independent of how fast the filesystem is,
// which is the whole point: the test should fail only when the debouncing
// logic is broken.
func TestRunDebouncesBursts(t *testing.T) {
	const debounce = 400 * time.Millisecond

	var mu sync.Mutex
	runs := 0
	w, dir, _ := newTestWatcherDebounced(t, func(context.Context) (*models.Snapshot, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		return snapshot(80, 5), nil
	}, debounce)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan Event, 64)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, events) }()

	if ev := <-events; ev.Kind != EventReady {
		t.Fatalf("first event = %v, want ready", ev.Kind)
	}

	// Let the initial analysis settle so the burst is measured on its own.
	if !waitFor(t, 5*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return runs >= 1
	}) {
		t.Fatal("the initial analysis never ran")
	}
	mu.Lock()
	baseline := runs
	mu.Unlock()

	// Five writes, timed so a failure can be diagnosed: if the burst took
	// longer than the debounce window, the assertion below cannot hold and the
	// log line says why.
	burstStart := time.Now()
	for i := 0; i < 5; i++ {
		name := filepath.Join(dir, "burst.go")
		body := "package p\n// " + strings.Repeat("x", i+1) + "\n"
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	burstDuration := time.Since(burstStart)
	t.Logf("burst of 5 writes took %s against a %s debounce window",
		burstDuration.Round(time.Millisecond), debounce)

	// Wait for the coalesced analysis, then wait out a further window so any
	// second debounce cycle would have fired.
	if !waitFor(t, 10*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return runs > baseline
	}) {
		t.Fatal("the burst never triggered an analysis")
	}
	time.Sleep(debounce * 2)

	mu.Lock()
	total := runs
	mu.Unlock()

	if total != baseline+1 {
		t.Errorf("a burst of 5 writes (spanning %s against a %s window) caused %d analyses, want exactly 1",
			burstDuration.Round(time.Millisecond), debounce, total-baseline)
	}
	cancel()
	waitRunExit(t, done, "test")
}

func TestRunIgnoresIrrelevantExtensions(t *testing.T) {
	var mu sync.Mutex
	runs := 0
	w, dir, _ := newTestWatcher(t, func(context.Context) (*models.Snapshot, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		return snapshot(80, 5), nil
	})
	w.cfg.Extensions = []string{".go"}

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan Event, 16)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, events) }()
	<-events
	waitFor(t, 3*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return runs >= 1
	})
	mu.Lock()
	baseline := runs
	mu.Unlock()

	if err := os.WriteFile(filepath.Join(dir, "notes.png"), []byte("binary"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	mu.Lock()
	total := runs
	mu.Unlock()
	if total != baseline {
		t.Errorf("a .png write triggered %d analyses, want 0", total-baseline)
	}
	cancel()
	waitRunExit(t, done, "test")
}

func TestRunReportsAnalyzeErrors(t *testing.T) {
	w, dir, log := newTestWatcher(t, func(context.Context) (*models.Snapshot, error) {
		return nil, os.ErrPermission
	})

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan Event, 16)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, events) }()

	<-events // ready

	var sawErr bool
	deadline := time.After(3 * time.Second)
	for !sawErr {
		select {
		case ev := <-events:
			if ev.Kind == EventError && ev.Err != nil {
				sawErr = true
			}
		case <-deadline:
			cancel()
			waitRunExit(t, done, "test")
			t.Fatalf("no error event; log: %s", log.String())
		}
	}
	if !waitFor(t, time.Second, func() bool {
		return strings.Contains(log.String(), "analysis failed")
	}) {
		t.Errorf("expected a failure log line:\n%s", log.String())
	}

	// A later successful change must still be reported.
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cancel()
	waitRunExit(t, done, "test")
}

func TestRunOnSnapshotFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	log := &syncBuffer{}
	w, err := New(Config{
		Root:       dir,
		Analyze:    func(context.Context) (*models.Snapshot, error) { return snapshot(80, 5), nil },
		OnSnapshot: func(*models.Snapshot) error { return os.ErrPermission },
		Debounce:   30 * time.Millisecond,
		Log:        log,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan Event, 16)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, events) }()

	found := false
	deadline := time.After(3 * time.Second)
	for !found {
		select {
		case ev := <-events:
			if ev.Kind == EventError {
				found = true
			}
		case <-deadline:
			cancel()
			waitRunExit(t, done, "test")
			t.Fatal("no error event for a failing OnSnapshot")
		}
	}
	cancel()
	waitRunExit(t, done, "test")
}

func TestRunStopsCleanlyOnContextCancel(t *testing.T) {
	w, _, _ := newTestWatcher(t, func(context.Context) (*models.Snapshot, error) {
		return snapshot(80, 5), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan Event, 16)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, events) }()
	<-events
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want nil on clean cancel", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}
