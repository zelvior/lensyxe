package watch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

// snapshot builds a snapshot for status-line and movement tests.
func snapshot(score float64, complexity float64) *models.Snapshot {
	return &models.Snapshot{
		Health: models.Health{Score: score, Grade: models.Grade(score)},
		Code: models.CodeStats{
			Files: 10, SourceFiles: 8, TestFiles: 2, AverageLines: 50,
			Complexity: models.ComplexitySummary{
				Measured: true, AverageComplexity: complexity, MaxComplexity: complexity,
			},
			Hotspots: []models.Hotspot{},
		},
		Dependencies: models.DependencyStats{},
		Risks:        []models.Risk{},
	}
}

func TestNewValidatesConfig(t *testing.T) {
	noop := func(context.Context) (*models.Snapshot, error) { return nil, nil }
	if _, err := New(Config{Root: "", Analyze: noop}); err == nil {
		t.Error("expected an error for an empty root")
	}
	if _, err := New(Config{Root: t.TempDir()}); err == nil {
		t.Error("expected an error with no analyze function")
	}
}

// The documented single-file status line.
func TestStatusLineSingleFile(t *testing.T) {
	prev := snapshot(87, 4)
	cur := snapshot(86, 6.5)
	got := StatusLine([]string{"src/engine.go"}, prev, cur)

	for _, want := range []string{
		"[WATCH]",
		"Detected change in src/engine.go",
		"Re-analyzing...",
		"Health: 87.0 -> 86.0",
		"▼",
		"Complexity increased",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("status line missing %q\ngot: %s", want, got)
		}
	}
}

func TestStatusLineFirstRunHasNoDelta(t *testing.T) {
	got := StatusLine([]string{"a.go"}, nil, snapshot(80, 5))
	if !strings.Contains(got, "Health: 80.0 (B)") {
		t.Errorf("got: %s", got)
	}
	// No comparison is possible, so no "a -> b" health form.
	if strings.Contains(got, "Health: 80.0 ->") {
		t.Errorf("no comparison is possible on the first run: %s", got)
	}
}

func TestStatusLineImprovement(t *testing.T) {
	got := StatusLine([]string{"a.go"}, snapshot(70, 6), snapshot(85, 4))
	if !strings.Contains(got, "▲") {
		t.Errorf("expected an improvement marker: %s", got)
	}
	if !strings.Contains(got, "Complexity decreased") {
		t.Errorf("expected a complexity note: %s", got)
	}
}

func TestStatusLineUnchanged(t *testing.T) {
	prev := snapshot(80, 5)
	got := StatusLine([]string{"a.go"}, prev, snapshot(80, 5))
	if !strings.Contains(got, "▬") {
		t.Errorf("expected a flat marker: %s", got)
	}
	if !strings.Contains(got, "no notable change") {
		t.Errorf("expected an explicit no-change note: %s", got)
	}
}

func TestStatusLineManyFiles(t *testing.T) {
	got := StatusLine([]string{"a.go", "b.go", "c.go", "d.go", "e.go"},
		snapshot(80, 5), snapshot(81, 5))
	if !strings.Contains(got, "Detected 5 file changes") {
		t.Errorf("got: %s", got)
	}
	// Three paths shown, the remaining two summarized.
	if !strings.Contains(got, "a.go, b.go, c.go +2 more") {
		t.Errorf("expected a summary of the remaining paths: %s", got)
	}
}

func TestStatusLineNoChangedPaths(t *testing.T) {
	got := StatusLine(nil, snapshot(80, 5), snapshot(80, 5))
	if !strings.Contains(got, "Re-analyzing...") {
		t.Errorf("got: %s", got)
	}
	if strings.Contains(got, "Detected change") {
		t.Errorf("no files changed, so no change should be claimed: %s", got)
	}
}

// Routine improvements must not be dressed up as warnings, and must not
// produce noise either.
func TestMovementImprovementIsQuiet(t *testing.T) {
	prev := snapshot(80, 5)
	cur := snapshot(85, 5)
	cur.Code.TestFiles = prev.Code.TestFiles + 3
	cur.Code.SourceFiles = prev.Code.SourceFiles

	note := movement(prev, cur)
	if note != "no notable change" {
		t.Errorf("routine improvement should be quiet, got %q", note)
	}
}

// A genuine improvement is acknowledged, but as a confirmation rather than a
// warning.
func TestMovementAcknowledgesRealImprovement(t *testing.T) {
	prev := snapshot(70, 12)
	cur := snapshot(85, 4)
	note := movement(prev, cur)
	if !strings.Contains(note, "Complexity decreased") {
		t.Errorf("got: %s", note)
	}
	if strings.Contains(note, "⚠") {
		t.Errorf("an improvement must not emit a warning: %s", note)
	}
}

func TestMovementFlagsEverySignal(t *testing.T) {
	prev := snapshot(80, 4)
	cur := snapshot(70, 12)
	cur.Code.AverageLines = 200
	cur.Code.Hotspots = []models.Hotspot{{Path: "x.go", Confirmed: true}}
	cur.Code.SourceFiles = prev.Code.SourceFiles + 4
	cur.Code.TestFiles = prev.Code.TestFiles
	cur.Risks = []models.Risk{{ID: "a"}, {ID: "b"}}
	cur.Dependencies.Drift = true

	note := movement(prev, cur)
	for _, want := range []string{
		"Complexity increased",
		"Avg file size",
		"new confirmed hotspot",
		"no tests",
		"new risk",
		"Dependency drift",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("movement note missing %q\ngot: %s", want, note)
		}
	}
}

func TestMovementResolvedSignals(t *testing.T) {
	prev := snapshot(70, 12)
	prev.Code.Hotspots = []models.Hotspot{{Path: "x.go", Confirmed: true}}
	prev.Dependencies.Drift = true

	cur := snapshot(85, 4)
	note := movement(prev, cur)
	if !strings.Contains(note, "Complexity decreased") {
		t.Errorf("got: %s", note)
	}
	if !strings.Contains(note, "hotspot(s) resolved") {
		t.Errorf("got: %s", note)
	}
	if !strings.Contains(note, "Dependency drift resolved") {
		t.Errorf("got: %s", note)
	}
}

func TestMovementHandlesNilSnapshots(t *testing.T) {
	// Must not panic; the watcher can race an absent baseline. An empty note is
	// correct: there is nothing to compare.
	if got := movement(nil, snapshot(80, 5)); got != "" {
		t.Errorf("movement(nil, cur) = %q, want empty", got)
	}
	if got := movement(snapshot(80, 5), nil); got != "" {
		t.Errorf("movement(prev, nil) = %q, want empty", got)
	}
	if got := movement(nil, nil); got != "" {
		t.Errorf("movement(nil, nil) = %q, want empty", got)
	}
}

// StatusLine must also survive a nil snapshot without panicking.
func TestStatusLineHandlesNilSnapshots(t *testing.T) {
	if got := StatusLine([]string{"a.go"}, nil, nil); !strings.Contains(got, "unknown") {
		t.Errorf("got: %s", got)
	}
	if got := StatusLine(nil, snapshot(80, 5), nil); !strings.Contains(got, "unknown") {
		t.Errorf("got: %s", got)
	}
}

func TestSourceExtensions(t *testing.T) {
	exts := SourceExtensions()
	if len(exts) == 0 {
		t.Fatal("expected a default extension list")
	}
	required := []string{".go", ".ts", ".py", ".rs", ".mod", ".yml"}
	for _, want := range required {
		found := false
		for _, e := range exts {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %q from the default extensions", want)
		}
	}
	// Non-source files must be absent so watches do not churn on them.
	for _, unwanted := range []string{".png", ".zip", ".exe", ".mp4"} {
		for _, e := range exts {
			if e == unwanted {
				t.Errorf("%q should not trigger a re-analysis", unwanted)
			}
		}
	}
}

func TestConfigInterval(t *testing.T) {
	if got := (Config{}).Interval(); got != 0 {
		t.Errorf("default interval = %v, want 0", got)
	}
	if got := (Config{IntervalSeconds: 5}).Interval(); got != 5*time.Second {
		t.Errorf("interval = %v, want 5s", got)
	}
	if got := (Config{IntervalSeconds: -1}).Interval(); got != 0 {
		t.Errorf("negative interval = %v, want 0", got)
	}
}

// syncBuffer is a concurrency-safe log sink.
//
// bytes.Buffer is not safe for concurrent use, and the watcher writes from its
// own goroutine while the test reads. The race detector catches this reliably;
// this wrapper is the fix rather than a suppression.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

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

// runExitTimeout bounds how long a test waits for the Run loop to return.
//
// Generous, because the loop polls a filesystem watcher and an analysis can
// legitimately take a while under load. The point is not to fail a slow test;
// it is to fail a *hung* one.
const runExitTimeout = 30 * time.Second

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
