package watch

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

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
