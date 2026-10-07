package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/internal/storage"
	"github.com/zelvior/lensyxe/pkg/models"
)

func TestHistoryWithNoStore(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)

	var got HistoryResponse
	if code := get(t, s, APIPrefix+"/history", &got); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if got.Count != 0 {
		t.Errorf("count = %d, want 0", got.Count)
	}
	// A JSON null here would break the chart's iteration.
	if !strings.Contains(mustBody(t, s, APIPrefix+"/history"), `"records": []`) {
		t.Error("an empty history must serialize records as []")
	}
}

func TestHistoryOrderingIsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	root := filepath.FromSlash("/repo")
	base := fixtureSnapshot()
	base.Root = root

	// Three runs with rising scores.
	for i, score := range []float64{70, 80, 90} {
		snap := *base
		snap.Health.Score = score
		snap.Health.Grade = models.Grade(score)
		snap.Code.AverageLines = float64(i)
		if _, err := store.SaveSnapshot(&snap); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}

	s := newTestServer(t, base, store)
	var got HistoryResponse
	get(t, s, APIPrefix+"/history", &got)

	if got.Count != 3 {
		t.Fatalf("count = %d, want 3", got.Count)
	}
	// A chart plots oldest-first. Getting this backwards would render a
	// rising trend as a falling one.
	if got.Records[0].Score >= got.Records[len(got.Records)-1].Score {
		t.Errorf("records are not oldest-first: %v", scores(got.Records))
	}
	if got.Records[0].Score != 70 || got.Records[2].Score != 90 {
		t.Errorf("unexpected sequence: %v", scores(got.Records))
	}
	if got.Delta == nil {
		t.Fatal("a delta must be reported when there are two or more runs")
	}
	if got.Delta.Score != 20 {
		t.Errorf("delta = %v, want 20", got.Delta.Score)
	}
}

func TestHistoryDeltaNeedsTwoRuns(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	base := fixtureSnapshot()
	base.Root = filepath.FromSlash("/repo")
	if _, err := store.SaveSnapshot(base); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t, base, store)
	var got HistoryResponse
	get(t, s, APIPrefix+"/history", &got)

	if got.Count != 1 {
		t.Fatalf("count = %d", got.Count)
	}
	if got.Delta != nil {
		t.Errorf("a single run has no delta, got %+v", got.Delta)
	}
}

func TestHistoryLimit(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	base := fixtureSnapshot()
	base.Root = filepath.FromSlash("/repo")
	for i := 0; i < 5; i++ {
		if _, err := store.SaveSnapshot(base); err != nil {
			t.Fatal(err)
		}
	}
	s := newTestServer(t, base, store)

	var got HistoryResponse
	get(t, s, APIPrefix+"/history?limit=2", &got)
	if got.Count != 2 {
		t.Errorf("count = %d, want 2", got.Count)
	}

	for _, bad := range []string{"limit=-1", "limit=abc"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/history?"+bad, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", bad, rec.Code)
		}
	}

	// An oversized limit is capped rather than rejected, so a client asking
	// for everything gets as much as the server is willing to send.
	var capped HistoryResponse
	get(t, s, APIPrefix+"/history?limit=999999", &capped)
	if capped.Count != 5 {
		t.Errorf("capped count = %d, want 5", capped.Count)
	}
}

// The history root must come from the snapshot, so history recorded under the
// analyzed path is actually found.
func TestHistoryUsesSnapshotRoot(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	base := fixtureSnapshot()
	base.Root = filepath.FromSlash("/recorded-root")
	if _, err := store.SaveSnapshot(base); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t, base, store)
	var got HistoryResponse
	get(t, s, APIPrefix+"/history", &got)
	if got.Count != 1 {
		t.Errorf("count = %d, want 1: history was keyed under a different root", got.Count)
	}
}
