package watch

import (
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

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
