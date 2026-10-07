package watch

import (
	"context"
	"strings"
	"testing"

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
