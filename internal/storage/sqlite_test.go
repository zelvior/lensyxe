package storage

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

// openTestStore opens a store in a temp dir and registers cleanup.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), DefaultDir, DefaultFile)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// snap builds a snapshot with the fields the storage layer reads.
func snap(root string, at time.Time, score float64) *models.Snapshot {
	return &models.Snapshot{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "test",
		Root:          root,
		GeneratedAt:   at,
		Code: models.CodeStats{
			Files: 100, SourceFiles: 80, TestFiles: 20, HasTests: true,
			TestFileRatio: 0.2, CodeLines: 8000,
			Hotspots: []models.Hotspot{
				{Path: "a.go", Lines: 900, Confirmed: true},
				{Path: "b.go", Lines: 700, Confirmed: false},
			},
			Complexity: models.ComplexitySummary{
				Measured: true, AverageComplexity: 5.5, MaxComplexity: 42,
			},
		},
		Dependencies: models.DependencyStats{Detected: true, Drift: true, Direct: 6},
		Git:          models.GitStats{IsRepository: true, HeadCommit: "abc123def456789"},
		Health: models.Health{
			Score: score, Grade: models.Grade(score),
			Metrics: []models.Metric{
				{Key: "code", Score: 70, Weight: 0.4, Applicable: true},
				{Key: "dependency", Score: 90, Weight: 0.3, Applicable: true},
				{Key: "git", Score: 80, Weight: 0.3, Applicable: false},
			},
		},
		Risks: []models.Risk{
			{Severity: models.SeverityCritical, ID: "a", Impact: 9},
			{Severity: models.SeverityHigh, ID: "b", Impact: 6},
			{Severity: models.SeverityMedium, ID: "c", Impact: 3},
		},
	}
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "history.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if s.Path() != path {
		t.Errorf("Path = %q, want %q", s.Path(), path)
	}
}

func TestOpenRejectsEmptyPath(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Error("expected an error for an empty path")
	}
}

// Opening the same path twice must succeed: Open has to be idempotent across
// invocations, which is how `analyze` and `history` coexist.
func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	for i := 0; i < 3; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if _, err := s.SaveSnapshot(snap("/repo", time.Now(), 80)); err != nil {
			t.Fatalf("SaveSnapshot %d: %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close %d: %v", i, err)
		}
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	n, err := s.Count("/repo")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("count = %d, want 3", n)
	}
}

func TestSaveAndGetHistory(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	scores := []float64{80, 85, 78}
	for i, score := range scores {
		if _, err := s.SaveSnapshot(snap("/repo", base.Add(time.Duration(i)*time.Hour), score)); err != nil {
			t.Fatalf("SaveSnapshot %d: %v", i, err)
		}
	}

	got, err := s.GetHistory("/repo", 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(got) != len(scores) {
		t.Fatalf("history = %d rows, want %d", len(got), len(scores))
	}
	// Oldest first, so the ordering is chronological.
	for i, want := range scores {
		if got[i].Score != want {
			t.Errorf("row %d score = %v, want %v", i, got[i].Score, want)
		}
	}
	// IDs must increase monotonically so ordering is stable on equal timestamps.
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Errorf("IDs not increasing: %d then %d", got[i-1].ID, got[i].ID)
		}
	}
}

func TestSaveProjectsEveryField(t *testing.T) {
	s := openTestStore(t)
	at := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if _, err := s.SaveSnapshot(snap("/repo", at, 72.5)); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	rec, err := s.Latest("/repo")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Root", rec.Root, "/repo"},
		{"Commit", rec.Commit, "abc123def456789"},
		{"Score", rec.Score, 72.5},
		{"Grade", rec.Grade, "C"},
		{"CodeScore", rec.CodeScore, 70.0},
		{"DependencyScore", rec.DependencyScore, 90.0},
		{"GitScore", rec.GitScore, 80.0},
		{"CodeScoreApplied", rec.CodeScoreApplied, true},
		{"DepScoreApplied", rec.DepScoreApplied, true},
		// The git metric was not applicable; the value must not be read as a
		// real score.
		{"GitScoreApplied", rec.GitScoreApplied, false},
		{"RiskCount", rec.RiskCount, 3},
		{"CriticalRiskCount", rec.CriticalRiskCount, 1},
		{"HighRiskCount", rec.HighRiskCount, 1},
		{"HotspotCount", rec.HotspotCount, 2},
		{"ConfirmedHotspots", rec.ConfirmedHotspots, 1},
		{"SourceFiles", rec.SourceFiles, 80},
		{"TestFiles", rec.TestFiles, 20},
		{"CodeLines", rec.CodeLines, 8000},
		{"AvgComplexity", rec.AvgComplexity, 5.5},
		{"DependencyDrift", rec.DependencyDrift, true},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if !rec.RecordedAt.Equal(at) {
		t.Errorf("RecordedAt = %v, want %v", rec.RecordedAt, at)
	}
	// The full JSON blob must survive for detail views.
	if len(rec.Snapshot) == 0 {
		t.Error("the snapshot JSON blob must be stored")
	}
}

func TestGetHistoryLimitTakesMostRecent(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, score := range []float64{10, 20, 30, 40, 50} {
		if _, err := s.SaveSnapshot(snap("/repo", base.Add(time.Duration(i)*time.Hour), score)); err != nil {
			t.Fatalf("SaveSnapshot: %v", err)
		}
	}

	got, err := s.GetHistory("/repo", 2)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	// The limit must apply to the newest entries, still returned oldest-first.
	if got[0].Score != 40 || got[1].Score != 50 {
		t.Errorf("scores = %v,%v, want 40,50", got[0].Score, got[1].Score)
	}
}

// Roots are part of the key: one database can hold several repositories.
func TestHistoryIsScopedByRoot(t *testing.T) {
	s := openTestStore(t)
	now := time.Now()
	if _, err := s.SaveSnapshot(snap("/repo-a", now, 90)); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	if _, err := s.SaveSnapshot(snap("/repo-b", now, 50)); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	a, err := s.GetHistory("/repo-a", 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(a) != 1 || a[0].Score != 90 {
		t.Errorf("repo-a history = %+v", a)
	}

	all, err := s.GetHistoryAll(0)
	if err != nil {
		t.Fatalf("GetHistoryAll: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("GetHistoryAll = %d rows, want 2", len(all))
	}
}

func TestGetScoreDelta(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// No history at all.
	if _, err := s.GetScoreDelta("/repo"); err == nil {
		t.Error("expected ErrNoHistory on an empty store")
	}

	if _, err := s.SaveSnapshot(snap("/repo", base, 80)); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	d, err := s.GetScoreDelta("/repo")
	if err != nil {
		t.Fatalf("GetScoreDelta: %v", err)
	}
	if d.HasPrevious {
		t.Error("HasPrevious must be false with a single snapshot")
	}

	if _, err := s.SaveSnapshot(snap("/repo", base.Add(time.Hour), 74)); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	d, err = s.GetScoreDelta("/repo")
	if err != nil {
		t.Fatalf("GetScoreDelta: %v", err)
	}
	if !d.HasPrevious {
		t.Fatal("HasPrevious must be true with two snapshots")
	}
	if d.Delta != -6 {
		t.Errorf("Delta = %v, want -6", d.Delta)
	}
	if d.Trend != "declining" {
		t.Errorf("Trend = %q, want declining", d.Trend)
	}
	if d.Previous.Score != 80 || d.Latest.Score != 74 {
		t.Errorf("scores = %v -> %v", d.Previous.Score, d.Latest.Score)
	}
}

func TestScoreDeltaTrend(t *testing.T) {
	cases := []struct {
		from, to float64
		want     string
	}{
		{80, 90, "improving"},
		{80, 70, "declining"},
		{80, 80, "flat"},
		{80, 80.01, "flat"}, // sub-0.05 movement is noise, not a trend
		{80, 79.99, "flat"},
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, tc := range cases {
		// A distinct root per case: GetScoreDelta reads the two newest rows,
		// so reusing one root would compare against a prior case's leftovers.
		root := filepath.Join("/repo", string(rune('a'+i)))
		t.Run(tc.want, func(t *testing.T) {
			s := openTestStore(t)
			saveAt(t, s, root, base, tc.from)
			saveAt(t, s, root, base.Add(time.Hour), tc.to)

			d, err := s.GetScoreDelta(root)
			if err != nil {
				t.Fatalf("GetScoreDelta: %v", err)
			}
			if !d.HasPrevious {
				t.Fatal("HasPrevious must be true")
			}
			if d.Trend != tc.want {
				t.Errorf("trend for %v->%v = %q, want %q", tc.from, tc.to, d.Trend, tc.want)
			}
		})
	}
}

func save(t *testing.T, s *Store, at time.Time, score float64) {
	t.Helper()
	saveAt(t, s, "/repo", at, score)
}

func saveAt(t *testing.T, s *Store, root string, at time.Time, score float64) {
	t.Helper()
	if _, err := s.SaveSnapshot(snap(root, at, score)); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
}

func TestLatestNoHistory(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.Latest("/nothing"); err == nil {
		t.Error("expected ErrNoHistory")
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	s := openTestStore(t)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 10; i++ {
		save(t, s, base.Add(time.Duration(i)*time.Hour), float64(60+i))
	}

	deleted, err := s.Prune("/repo", 3)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if deleted != 7 {
		t.Errorf("deleted = %d, want 7", deleted)
	}
	got, err := s.GetHistory("/repo", 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3", len(got))
	}
	// The survivors must be the three newest.
	if got[0].Score != 67 || got[2].Score != 69 {
		t.Errorf("survivors = %v..%v, want 67..69", got[0].Score, got[2].Score)
	}

	// A non-positive keep disables pruning.
	before, _ := s.Count("/repo")
	if _, err := s.Prune("/repo", 0); err != nil {
		t.Fatalf("Prune(0): %v", err)
	}
	after, _ := s.Count("/repo")
	if before != after {
		t.Errorf("Prune(0) deleted rows: %d -> %d", before, after)
	}
}

func TestResolvePath(t *testing.T) {
	rel := ResolvePath("/repo", "")
	want := filepath.Join("/repo", DefaultDir, DefaultFile)
	if rel != want {
		t.Errorf("ResolvePath(no override) = %q, want %q", rel, want)
	}
	if got := ResolvePath("/repo", "custom.db"); got != filepath.Join("/repo", "custom.db") {
		t.Errorf("ResolvePath(relative) = %q", got)
	}
	if got := ResolvePath("/repo", filepath.Join(string(filepath.Separator), "abs", "h.db")); got != filepath.Join(string(filepath.Separator), "abs", "h.db") {
		t.Errorf("ResolvePath(absolute) = %q, want it kept absolute", got)
	}
}

func TestSaveNilSnapshot(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.SaveSnapshot(nil); err == nil {
		t.Error("expected an error for a nil snapshot")
	}
}

func TestZeroGeneratedAtIsReplaced(t *testing.T) {
	s := openTestStore(t)
	s0 := snap("/repo", time.Time{}, 80)
	if _, err := s.SaveSnapshot(s0); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	rec, err := s.Latest("/repo")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rec.RecordedAt.IsZero() {
		t.Error("RecordedAt must be filled in when the snapshot has none")
	}
}

func TestCloseIsSafeOnNilStore(t *testing.T) {
	var s *Store
	if err := s.Close(); err != nil {
		t.Errorf("Close on a nil store = %v, want nil", err)
	}
}

// Concurrent writers must not lose rows or surface SQLITE_BUSY. This is the
// scenario `watch` creates: a background save while a render reads.
func TestConcurrentWrites(t *testing.T) {
	s := openTestStore(t)
	const n = 12
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			_, err := s.SaveSnapshot(snap("/repo", time.Now(), float64(70+i%5)))
			done <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent SaveSnapshot: %v", err)
		}
	}
	got, err := s.Count("/repo")
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got != n {
		t.Errorf("count = %d, want %d; concurrent writes lost rows", got, n)
	}
}
