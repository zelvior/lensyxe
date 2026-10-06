// Package watch re-analyzes a repository when its files change.
//
// The watcher is strictly local: it uses the OS filesystem notification API and
// never polls. Events are debounced because editors write files in several
// operations, so a single save would otherwise trigger a burst of redundant
// scans.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/zelvior/lensyxe/pkg/models"
)

// Event kinds reported to the caller.
type EventKind string

// Watch event kinds.
const (
	// EventAnalyze reports that a re-analysis completed.
	EventAnalyze EventKind = "analyze"
	// EventError reports a failed analysis or watch setup.
	EventError EventKind = "error"
	// EventReady reports that watching has started.
	EventReady EventKind = "ready"
	// EventClosed reports clean shutdown.
	EventClosed EventKind = "closed"
)

// Event is a single notification from the watcher.
type Event struct {
	Kind EventKind
	// Changed holds the repo-relative paths that triggered the analysis.
	Changed []string
	// Snapshot is the new analysis result, set for EventAnalyze.
	Snapshot *models.Snapshot
	// Previous is the prior result, used to describe the movement.
	Previous *models.Snapshot
	// Err is set for EventError.
	Err error
	// At is when the event was produced.
	At time.Time
}

// Config controls the watcher.
type Config struct {
	// Root is the directory to watch.
	Root string
	// IntervalSeconds additionally re-analyzes on a fixed cadence even when no
	// file changed. Zero, the default, means react to changes only.
	IntervalSeconds int
	// Analyze runs a full analysis of root and returns the snapshot. Injected
	// so this package does not import internal/analyzer, which would create an
	// import cycle, and so tests can supply a stub.
	Analyze func(ctx context.Context) (*models.Snapshot, error)
	// OnSnapshot, when set, is called after every successful analysis. Used
	// for persistence; the watcher itself does not write anywhere.
	OnSnapshot func(*models.Snapshot) error
	// Debounce coalesces event bursts. Defaults to 250ms.
	Debounce time.Duration
	// IgnoreDirs are directory names pruned from change handling.
	IgnoreDirs []string
	// Extensions limits which file types trigger a re-analysis. Empty means
	// every file.
	Extensions []string
	// Log receives human-readable status lines. Defaults to io.Discard.
	Log io.Writer
}

// Watcher observes a directory tree and re-analyzes on change.
type Watcher struct {
	cfg  Config
	fsw  *fsnotify.Watcher
	log  io.Writer
	prev *models.Snapshot

	// mu guards prev and closed, which the event loop and Close both touch.
	mu     sync.Mutex
	closed bool
}

// ErrClosed is returned by Close on an already-closed watcher.
var ErrClosed = errors.New("watch: already closed")

// New creates a watcher and registers the directory tree.
//
// Directories are watched recursively. A watcher owns an OS handle, so a
// caller that leaks one exhausts the notification budget on Linux; use Close.
func New(cfg Config) (*Watcher, error) {
	if cfg.Root == "" {
		return nil, errors.New("watch: empty root")
	}
	if cfg.Analyze == nil {
		return nil, errors.New("watch: no analyze function configured")
	}
	if cfg.Debounce <= 0 {
		cfg.Debounce = 250 * time.Millisecond
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch: create watcher: %w", err)
	}

	w := &Watcher{cfg: cfg, fsw: fsw, log: cfg.Log}
	if err := w.addTree(cfg.Root); err != nil {
		fsw.Close()
		return nil, err
	}
	return w, nil
}

// addTree registers root and every subdirectory, skipping ignored ones.
//
// Recursive registration is required: fsnotify does not watch subdirectories
// implicitly on any supported platform.
func (w *Watcher) addTree(root string) error {
	ignore := toSet(w.cfg.IgnoreDirs)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory is skipped, not fatal.
			return nil //nolint:nilerr // deliberate: one bad dir must not stop the watch
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != root && ignore[name] {
			return filepath.SkipDir
		}
		// Never descend into VCS metadata: it churns constantly and none of
		// it is source.
		if path != root && name == ".git" {
			return filepath.SkipDir
		}
		if addErr := w.fsw.Add(path); addErr != nil {
			return fmt.Errorf("watch: register %s: %w", path, addErr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("watch: scan %s: %w", root, err)
	}
	return nil
}

// Run blocks until ctx is cancelled, emitting events on out.
//
// The channel is buffered so a slow consumer cannot stall the event loop, which
// would back up the OS notification queue. When the buffer fills, the oldest
// event is dropped in favor of the newest: for a live status feed, the latest
// state is the one that matters.
func (w *Watcher) Run(ctx context.Context, out chan Event) error {
	defer w.fsw.Close()

	out <- Event{Kind: EventReady, At: time.Now().UTC()}
	w.logf("[WATCH] watching %s (debounce %s)", w.cfg.Root, w.cfg.Debounce)

	// An initial analysis establishes the baseline, so the first real change
	// can report a delta rather than just a score. It goes through the same
	// analyze path as every later pass, which keeps the event stream uniform:
	// a consumer sees one EventAnalyze per analysis regardless of cause.
	w.analyze(out, nil, true)

	var (
		pending   = map[string]bool{}
		timer     *time.Timer
		timerC    <-chan time.Time
		intervals = w.intervalC(ctx)
	)

	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			w.logf("[WATCH] stopped")
			w.emit(out, Event{Kind: EventClosed, At: time.Now().UTC()})
			return nil

		case <-intervals:
			// Periodic re-analysis, independent of file events.
			w.analyze(out, nil, false)

		case ev, ok := <-w.fsw.Events:
			if !ok {
				return nil
			}
			if !w.relevant(ev) {
				continue
			}
			rel := w.relPath(ev.Name)
			if rel == "" {
				continue
			}
			pending[rel] = true

			// Reset the debounce window on every event so a continuous stream
			// of writes collapses into one analysis once writing stops.
			if timer != nil {
				timer.Stop()
			}
			timer = time.NewTimer(w.cfg.Debounce)
			timerC = timer.C

		case <-timerC:
			timerC = nil
			changed := sortedKeys(pending)
			pending = map[string]bool{}
			w.analyze(out, changed, false)

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return nil
			}
			// An overflow means events were lost. Recover by re-analyzing so
			// the reported state is never knowingly stale.
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				w.logf("[WATCH] event queue overflowed; forcing re-analysis")
				w.analyze(out, nil, false)
				continue
			}
			w.emit(out, Event{Kind: EventError, Err: err, At: time.Now().UTC()})
		}
	}
}

// intervalC returns a periodic trigger channel, or nil when disabled.
func (w *Watcher) intervalC(ctx context.Context) <-chan time.Time {
	if w.cfg.Interval() <= 0 {
		return nil
	}
	t := time.NewTicker(w.cfg.Interval())
	out := make(chan time.Time, 1)
	go func() {
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case tick := <-t.C:
				select {
				case out <- tick:
				default: // never block the ticker
				}
			}
		}
	}()
	return out
}

// Interval returns the configured periodic re-analysis interval.
func (c Config) Interval() time.Duration {
	if c.IntervalSeconds <= 0 {
		return 0
	}
	return time.Duration(c.IntervalSeconds) * time.Second
}

// SourceExtensions is the default set of file types worth re-analyzing.
//
// The list is source and config only. A change to a README, a lockfile, or a
// build artifact does not alter the metrics, so reacting to those would burn
// CPU on scans that cannot change the score. Note that lockfile changes DO
// matter for dependency drift, which is why the manifest and lockfile names are
// included even though Lensyxe does not parse lockfile contents.
func SourceExtensions() []string {
	return []string{
		// Go
		".go", ".mod", ".sum",
		// JavaScript / TypeScript
		".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts",
		".json", ".vue", ".svelte",
		// Python
		".py", ".pyi", ".toml", ".cfg",
		// Systems and JVM
		".c", ".h", ".cc", ".cpp", ".hpp", ".rs", ".zig", ".swift",
		".kt", ".java", ".scala", ".cs",
		// Scripting and web
		".rb", ".php", ".sh", ".bash", ".zsh", ".lua", ".sql",
		// Markup and data that can affect LOC counts
		".yml", ".yaml", ".html", ".css", ".scss",
	}
}

// analyze runs one analysis pass and emits the result.
//
// first marks the baseline pass, which is logged differently because it has
// nothing to compare against.
func (w *Watcher) analyze(out chan Event, changed []string, first bool) {
	snap, err := w.cfg.Analyze(context.Background())
	if err != nil {
		w.emit(out, Event{Kind: EventError, Changed: changed, Err: err, At: time.Now().UTC()})
		w.logf("[WATCH] analysis failed: %v", err)
		return
	}
	prev := w.store(snap)
	if w.cfg.OnSnapshot != nil {
		if err := w.cfg.OnSnapshot(snap); err != nil {
			// Persistence failed but the analysis itself is valid, so the
			// snapshot is still reported.
			w.emit(out, Event{Kind: EventError, Changed: changed, Err: err, At: time.Now().UTC()})
			w.logf("[WATCH] could not record snapshot: %v", err)
		}
	}
	w.emit(out, Event{
		Kind: EventAnalyze, Changed: changed,
		Snapshot: snap, Previous: prev, At: time.Now().UTC(),
	})
	if first {
		w.logf("[WATCH] initial analysis: health %s", scoreOf(snap))
		return
	}
	w.logf("%s", StatusLine(changed, prev, snap))
}

// store records snap as the new baseline and returns the prior one.
func (w *Watcher) store(snap *models.Snapshot) *models.Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	prev := w.prev
	w.prev = snap
	return prev
}

// relevant reports whether an event should trigger a re-analysis.
//
// Write and create only: a read or a chmod does not change the analyzed
// content, and reacting to them would burn CPU on no-op scans.
func (w *Watcher) relevant(ev fsnotify.Event) bool {
	if !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Rename) {
		return false
	}
	if len(w.cfg.Extensions) == 0 {
		return true
	}
	ext := strings.ToLower(filepath.Ext(ev.Name))
	for _, want := range w.cfg.Extensions {
		if strings.EqualFold(ext, want) {
			return true
		}
	}
	return false
}

// relPath converts an absolute event path to a repo-relative slash path.
func (w *Watcher) relPath(path string) string {
	rel, err := filepath.Rel(w.cfg.Root, path)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return rel
}

// emit sends an event without blocking, dropping the oldest pending event when
// the consumer falls behind.
//
// For a live status feed the newest state is the one that matters, so a full
// buffer discards the oldest event rather than the newest.
func (w *Watcher) emit(out chan Event, ev Event) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	select {
	case out <- ev:
	default:
		select {
		case <-out: // drop the stale event to make room
		default:
		}
		select {
		case out <- ev:
		default:
		}
	}
}

func (w *Watcher) logf(format string, args ...any) {
	fmt.Fprintf(w.log, format+"\n", args...)
}

// Close releases the watcher. Safe to call once; a second call is a no-op.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()
	return w.fsw.Close()
}

// StatusLine renders the one-line status log for a completed analysis, in the
// documented form:
//
//	[WATCH] Detected change in src/engine.go -> Re-analyzing... Health: 87 -> 86 (⚠ Complexity increased)
func StatusLine(changed []string, prev, cur *models.Snapshot) string {
	var b strings.Builder
	b.WriteString("[WATCH] ")
	if len(changed) == 0 {
		b.WriteString("Re-analyzing...")
	} else if len(changed) == 1 {
		b.WriteString("Detected change in " + changed[0] + " -> Re-analyzing...")
	} else {
		b.WriteString(fmt.Sprintf("Detected %d file changes (%s) -> Re-analyzing...",
			len(changed), summarizePaths(changed)))
	}

	if prev == nil || cur == nil {
		b.WriteString(fmt.Sprintf(" Health: %s", scoreOf(cur)))
		return b.String()
	}

	delta := cur.Health.Score - prev.Health.Score
	b.WriteString(fmt.Sprintf(" Health: %.1f -> %.1f (%s)",
		prev.Health.Score, cur.Health.Score, signedScore(delta)))

	if note := movement(prev, cur); note != "" {
		b.WriteString(" (" + note + ")")
	}
	return b.String()
}

// movement summarizes the most significant change between two snapshots.
//
// Only regressions are called out. Improvements are reported too, but strictly
// as confirmations: a watch line that shouts about good news on every save
// trains the reader to ignore it, which is how a real regression gets missed.
func movement(prev, cur *models.Snapshot) string {
	// The watcher can race an absent baseline, so a nil side is not an error.
	if prev == nil || cur == nil {
		return ""
	}
	var notes []string

	// Complexity is the primary concern: it rises silently and blocks refactors.
	if d := complexityDelta(prev, cur); d > 0.05 {
		notes = append(notes, "⚠ Complexity increased")
	} else if d < -0.05 {
		notes = append(notes, "✓ Complexity decreased")
	}

	if d := cur.Code.AverageLines - prev.Code.AverageLines; d > 5 {
		notes = append(notes, fmt.Sprintf("⚠ Avg file size +%.0f LOC", d))
	}

	confirmed := countConfirmed(cur) - countConfirmed(prev)
	if confirmed > 0 {
		notes = append(notes, fmt.Sprintf("⚠ %d new confirmed hotspot(s)", confirmed))
	} else if confirmed < 0 {
		notes = append(notes, fmt.Sprintf("✓ %d hotspot(s) resolved", -confirmed))
	}

	if d := cur.Code.SourceFiles - prev.Code.SourceFiles; d > 0 && cur.Code.TestFiles == prev.Code.TestFiles {
		notes = append(notes, fmt.Sprintf("⚠ %d new file(s) with no tests", d))
	}

	if len(cur.Risks) > len(prev.Risks) {
		notes = append(notes, fmt.Sprintf("⚠ %d new risk(s)", len(cur.Risks)-len(prev.Risks)))
	}

	if cur.Dependencies.Drift && !prev.Dependencies.Drift {
		notes = append(notes, "⚠ Dependency drift introduced")
	}
	if !cur.Dependencies.Drift && prev.Dependencies.Drift {
		notes = append(notes, "✓ Dependency drift resolved")
	}

	if len(notes) == 0 {
		return "no notable change"
	}
	return strings.Join(notes, ", ")
}

// complexityDelta returns the change in average estimated complexity.
func complexityDelta(prev, cur *models.Snapshot) float64 {
	return cur.Code.Complexity.AverageComplexity - prev.Code.Complexity.AverageComplexity
}

func countConfirmed(s *models.Snapshot) int {
	if s == nil {
		return 0
	}
	n := 0
	for _, h := range s.Code.Hotspots {
		if h.Confirmed {
			n++
		}
	}
	return n
}

func scoreOf(s *models.Snapshot) string {
	if s == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.1f (%s)", s.Health.Score, s.Health.Grade)
}

func signedScore(d float64) string {
	switch {
	case d > 0.05:
		return fmt.Sprintf("▲ +%.1f", d)
	case d < -0.05:
		return fmt.Sprintf("▼ %.1f", d)
	default:
		return "▬ unchanged"
	}
}

// summarizePaths lists the first few changed paths for a compact status line.
func summarizePaths(paths []string) string {
	const max = 3
	if len(paths) <= max {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s +%d more", strings.Join(paths[:max], ", "), len(paths)-max)
}

func toSet(values []string) map[string]bool {
	m := make(map[string]bool, len(values))
	for _, v := range values {
		if v != "" {
			m[v] = true
		}
	}
	return m
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Sorted for deterministic log output.
	sort.Strings(out)
	return out
}
