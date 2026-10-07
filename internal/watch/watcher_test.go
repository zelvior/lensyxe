package watch

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// newTestWatcher wires a watcher over a temp tree with a stub analyzer.
func newTestWatcher(t *testing.T, analyze func(context.Context) (*models.Snapshot, error)) (*Watcher, string, *syncBuffer) {
	t.Helper()
	return newTestWatcherDebounced(t, analyze, 30*time.Millisecond)
}

// newTestWatcherDebounced builds a watcher with an explicit debounce window.
//
// The window is a parameter because tests that assert on coalescing need it to
// be wide enough to absorb whatever the machine is doing. A 30ms window is fine
// for "does a change trigger an analysis" and hopeless for "do five changes
// trigger exactly one": under parallel load, five filesystem writes can easily
// take longer than 30ms, the debouncer fires twice, and the test fails for a
// reason that has nothing to do with the code under test.
func newTestWatcherDebounced(
	t *testing.T,
	analyze func(context.Context) (*models.Snapshot, error),
	debounce time.Duration,
) (*Watcher, string, *syncBuffer) {
	t.Helper()
	dir := t.TempDir()
	log := &syncBuffer{}
	w, err := New(Config{
		Root:            dir,
		Analyze:         analyze,
		Debounce:        debounce,
		Log:             log,
		IntervalSeconds: 0,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, dir, log
}

// waitRunExit waits for Run to return, and fails the test if it does not.
//
// Every call site used to be a bare `<-done`, which is an unbounded wait. If
// Run failed to return, the test hung until the package timeout ten minutes
// later and the output named no test at all — the failure looked like a slow
// suite rather than a stuck goroutine.
//
// This package is the most exposed to that of anything in the repository: it
// drives a real filesystem watcher with real debounce windows, so it is exactly
// where contention shows up. A contended run during development produced a
// 600-second package timeout here with every test up to that point passing.
func waitRunExit(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		// Cancellation is how every one of these tests ends, so a
		// context error is the expected result rather than a failure.
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("%s: Run returned %v", what, err)
		}
	case <-time.After(runExitTimeout):
		t.Fatalf("%s: Run did not return within %s", what, runExitTimeout)
	}
}

// waitFor polls until cond holds or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// runExitTimeout bounds how long a test waits for the Run loop to return.
//
// Generous, because the loop polls a filesystem watcher and an analysis can
// legitimately take a while under load. The point is not to fail a slow test;
// it is to fail a *hung* one.
const runExitTimeout = 30 * time.Second

func TestRunEmitsReadyAndInitialAnalysis(t *testing.T) {
	calls := 0
	var mu sync.Mutex
	w, _, log := newTestWatcher(t, func(context.Context) (*models.Snapshot, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return snapshot(80, 5), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan Event, 16)
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, events) }()

	if ev := <-events; ev.Kind != EventReady {
		t.Errorf("first event = %v, want ready", ev.Kind)
	}

	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == EventAnalyze {
				// The first analysis establishes the baseline; it must not
				// claim a comparison.
				if ev.Previous != nil {
					t.Error("the initial analysis must have no previous snapshot")
				}
				if !waitFor(t, time.Second, func() bool {
					return strings.Contains(log.String(), "initial analysis")
				}) {
					t.Errorf("missing initial log line, got: %s", log.String())
				}
				cancel()
				waitRunExit(t, done, "test")
				return
			}
		case <-deadline:
			t.Fatal("no analyze event received")
		}
	}
}
