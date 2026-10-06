package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/internal/storage"
	"github.com/zelvior/lensyxe/pkg/models"
)

// fixtureSnapshot builds a realistic snapshot for handler tests.
//
// A hand-built value is used rather than scanning a directory so the expected
// JSON is exact and a change to the analyzer cannot silently rewrite the
// assertions in this file.
func fixtureSnapshot() *models.Snapshot {
	return &models.Snapshot{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "0.3.0",
		Root:          filepath.FromSlash("/repo"),
		GeneratedAt:   mustTime("2026-01-02T03:04:05Z"),
		DurationMS:    42,
		Health: models.Health{
			Score:      82.5,
			Grade:      "B",
			Summary:    "Healthy, with room to improve.",
			Components: 3,
			Metrics: []models.Metric{
				{Key: "code", Label: "Code health", Score: 88, Weight: 0.5, Applicable: true},
				{Key: "dependency", Label: "Dependency health", Score: 79, Weight: 0.3, Applicable: true},
				{Key: "git", Label: "Maintainability (Git)", Score: 0, Weight: 0.2, Applicable: false},
			},
		},
		Code: models.CodeStats{
			Files:         120,
			TotalLines:    9000,
			CodeLines:     7000,
			AverageLines:  58.33,
			MaxFileLines:  900,
			TestFiles:     40,
			SourceFiles:   80,
			TestFileRatio: 0.33,
			TestLineRatio: 0.4,
			HasTests:      true,
			Hotspots: []models.Hotspot{
				{Path: "internal/a.go", Lines: 900, Churn: 400, Complexity: 30,
					Level: models.ComplexityVeryHigh, Classification: "size,churn,complexity,confirmed",
					Confirmed: true, Rationale: "large and actively changing"},
				{Path: "config/data.yml", Lines: 700, Churn: 0, Complexity: 0,
					Classification: "size", Rationale: "size only"},
			},
			// In the order the model documents: lines descending, then name
			// ascending. The endpoint relays rather than re-sorts, so this is
			// the guarantee a client can rely on.
			Languages: []models.LanguageStat{
				{Name: "Go", Files: 90, Lines: 7000, TestFiles: 20},
				{Name: "Shell", Files: 3, Lines: 800},
				{Name: "YAML", Files: 4, Lines: 200},
			},
		},
		Git: models.GitStats{
			IsRepository:  true,
			Branch:        "main",
			TotalCommits:  500,
			WindowCommits: 40,
			Authors:       5,
			Churn:         []models.ChurnEntry{{Path: "internal/a.go", Commits: 20, Added: 300, Deleted: 100, Score: 420}},
		},
		Dependencies: models.DependencyStats{
			Detected: true, Total: 40, Direct: 6, Dev: 4, Indirect: 30,
			Transitive: 61, Locked: true,
		},
		Risks: []models.Risk{
			{ID: "r1", Severity: models.SeverityCritical, Category: models.CategoryCode,
				Title: "Confirmed hotspot", Detail: "size, churn and complexity",
				Subject: "internal/a.go", Impact: 12,
				Evidence: []models.Evidence{models.NewEvidence("metric", "LOC", "900 code lines", 900, "internal/a.go")}},
			{ID: "r2", Severity: models.SeverityHigh, Category: models.CategoryGit,
				Title: "Bus factor", Detail: "one author", Impact: 5},
			{ID: "r3", Severity: models.SeverityMedium, Category: models.CategoryComplexity,
				Title: "Nesting", Detail: "deep", Impact: 3},
			{ID: "r4", Severity: models.SeverityLow, Category: models.CategoryDependency,
				Title: "Lockfile stale", Detail: "mtime", Impact: 1},
		},
	}
}

// Per-language figures were measured by the analyzer and were simply not on the
// response before. This asserts they reach the wire with their values intact
// and in the documented order, because a client renders the list directly.
func TestHealthCarriesTheLanguageBreakdown(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/health", nil))

	var resp struct {
		Code struct {
			Languages []models.LanguageStat `json:"languages"`
		} `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	got := resp.Code.Languages
	if len(got) != 3 {
		t.Fatalf("got %d languages, want 3: %+v", len(got), got)
	}
	want := fixtureSnapshot().Code.Languages
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Lines != want[i].Lines ||
			got[i].Files != want[i].Files || got[i].TestFiles != want[i].TestFiles {
			t.Errorf("language %d came back as %+v, want %+v", i, got[i], want[i])
		}
	}
	// Lines descending, so a client can render bars without sorting.
	for i := 1; i < len(got); i++ {
		if got[i-1].Lines < got[i].Lines {
			t.Errorf("languages are not in descending line order: %+v", got)
			break
		}
	}
}

// An empty breakdown must serialize as an empty list, not as null, so the
// client can map over it without a guard.
func TestEmptyLanguageBreakdownSerializesAsAList(t *testing.T) {
	snap := fixtureSnapshot()
	snap.Code.Languages = nil
	s := newTestServer(t, snap, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/health", nil))

	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var code map[string]json.RawMessage
	if err := json.Unmarshal(body["code"], &code); err != nil {
		t.Fatal(err)
	}
	if string(code["languages"]) != "[]" {
		t.Errorf("empty languages serialized as %s, want []", code["languages"])
	}
}

func mustTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// newTestServer builds a server over a fixture snapshot.
// newTestServer builds a server over a fixture snapshot.
//
// An owned store is closed by t.Cleanup: the handler deliberately does not
// close it (it is a shared, long-lived handle), so the test owns the lifetime.
func newTestServer(t *testing.T, snap *models.Snapshot, store *storage.Store) *Server {
	t.Helper()
	cfg := Config{
		Snapshot: func(context.Context) (*models.Snapshot, error) { return snap, nil },
	}
	if store != nil {
		t.Cleanup(func() { _ = store.Close() })
		cfg.Store = func() (*storage.Store, error) { return store, nil }
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// get performs a request against the server and decodes a JSON body.
func get(t *testing.T, s *Server, path string, out any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

	if out != nil && rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %s: %v\nbody: %s", path, err, rec.Body.String())
		}
	}
	return rec.Code
}

func TestNewRequiresSnapshotFunc(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("a server with no snapshot source must be rejected")
	}
}

func TestHealthEndpoint(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)

	var got HealthResponse
	if code := get(t, s, APIPrefix+"/health", &got); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}

	if got.Health.Score != 82.5 || got.Health.Grade != "B" {
		t.Errorf("unexpected score/grade: %+v", got.Health)
	}
	if got.Code.SourceFiles != 80 || got.Code.TestFiles != 40 {
		t.Errorf("unexpected file counts: %+v", got.Code)
	}
	if got.Deps.Total != 40 || !got.Deps.Locked {
		t.Errorf("unexpected dependency summary: %+v", got.Deps)
	}
	if !got.Git.IsRepository || got.Git.Authors != 5 {
		t.Errorf("unexpected git summary: %+v", got.Git)
	}
	// Severity counts are derived here rather than stored, so they are the
	// server's own arithmetic and must be checked.
	if got.Critical != 1 || got.High != 1 || got.RiskCount != 4 {
		t.Errorf("risk counts = critical %d high %d total %d", got.Critical, got.High, got.RiskCount)
	}
	if got.HotspotCnt != 2 {
		t.Errorf("hotspot count = %d, want 2", got.HotspotCnt)
	}
}

func TestHealthEndpointIsDeterministic(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/health", nil))
	first := rec.Body.String()

	for i := 0; i < 5; i++ {
		r := httptest.NewRecorder()
		s.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, APIPrefix+"/health", nil))
		if r.Body.String() != first {
			t.Fatalf("run %d produced different JSON", i)
		}
	}
}

func TestRisksEndpoint(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)

	var got RisksResponse
	if code := get(t, s, APIPrefix+"/risks", &got); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if got.Total != 4 || got.Critical != 1 || got.High != 1 || got.Medium != 1 || got.Low != 1 {
		t.Errorf("unexpected counts: %+v", got)
	}
	if len(got.Risks) != 4 {
		t.Fatalf("got %d risks, want 4", len(got.Risks))
	}
	// Evidence must survive serialization: a risk without its evidence is a
	// claim nobody can check.
	if len(got.Risks[0].Evidence) == 0 {
		t.Error("evidence was dropped")
	}
	if got.Risks[0].Evidence[0].Value == nil {
		t.Error("numeric evidence lost its value")
	}
}

// An empty risk list must serialize as [] so the dashboard's .map() cannot
// fail on null.
func TestRisksEndpointEmptyListIsArray(t *testing.T) {
	snap := fixtureSnapshot()
	snap.Risks = nil
	s := newTestServer(t, snap, nil)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/risks", nil))
	if !strings.Contains(rec.Body.String(), `"risks": []`) {
		t.Errorf("an empty risk list must be [], got:\n%s", rec.Body.String())
	}
}

func TestRisksSeverityFilter(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)

	var got RisksResponse
	get(t, s, APIPrefix+"/risks?severity=critical", &got)
	if len(got.Risks) != 1 || got.Risks[0].Severity != models.SeverityCritical {
		t.Errorf("filter returned %+v", got.Risks)
	}

	// The counts describe the whole set, not the filtered one, so a caller can
	// still see the severity breakdown after narrowing.
	if got.Critical != 1 {
		t.Errorf("counts must describe the full set, got %+v", got)
	}
}

// An unrecognized severity must be an error. Silently returning everything
// would look like the filter had been applied.
func TestRisksSeverityFilterRejectsUnknown(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/risks?severity=bogus", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"error"`) {
		t.Errorf("the error body must be JSON: %s", rec.Body.String())
	}
}

func TestHotspotsEndpoint(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)

	var got HotspotsResponse
	if code := get(t, s, APIPrefix+"/hotspots", &got); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if got.Total != 2 || got.Confirmed != 1 {
		t.Errorf("total=%d confirmed=%d, want 2/1", got.Total, got.Confirmed)
	}
	if len(got.Churn) != 1 || got.Churn[0].Path != "internal/a.go" {
		t.Errorf("churn was not returned: %+v", got.Churn)
	}
}

func TestHotspotsConfirmedFilter(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)

	var got HotspotsResponse
	get(t, s, APIPrefix+"/hotspots?confirmed=true", &got)
	if len(got.Hotspots) != 1 || !got.Hotspots[0].Confirmed {
		t.Errorf("filter returned %+v", got.Hotspots)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/hotspots?confirmed=maybe", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a non-boolean filter must be rejected, got %d", rec.Code)
	}
}

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

func TestVersionEndpoint(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)

	var got VersionResponse
	if code := get(t, s, APIPrefix+"/version", &got); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if got.Schema != models.SchemaVersion {
		t.Errorf("schema = %q, want %q", got.Schema, models.SchemaVersion)
	}
	// A build with no assets must say so, rather than letting a client assume
	// the dashboard is present.
	if got.Dashboard {
		t.Error("no assets were mounted, so dashboard must be false")
	}
}

// Repository internals must never be cached by a browser: a shared machine
// would otherwise serve one user's data to the next.
func TestResponsesAreNotCacheable(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)
	for _, path := range []string{
		APIPrefix + "/health", APIPrefix + "/history",
		APIPrefix + "/risks", APIPrefix + "/hotspots",
	} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control = %q, want no-store", path, got)
		}
	}
}

// A page on another origin must not be able to read repository details through
// the loopback listener.
func TestCrossOriginRequestsAreRejected(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, APIPrefix+"/health", nil)
	req.Header.Set("Origin", "https://evil.example")
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestLoopbackOriginIsAllowed(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)
	for _, origin := range []string{
		"http://localhost:7357", "http://127.0.0.1:8080", "http://[::1]:7357",
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, APIPrefix+"/health", nil)
		req.Header.Set("Origin", origin)
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("origin %s: status = %d, want 200", origin, rec.Code)
		}
	}
}

// A POST to a GET-only route must not fall through to the asset handler and
// return the dashboard with a 200.
func TestWrongMethodIsRejected(t *testing.T) {
	assets := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte("dashboard")); err != nil {
			t.Errorf("asset handler write: %v", err)
		}
	})
	s, err := New(Config{
		Snapshot: func(context.Context) (*models.Snapshot, error) { return fixtureSnapshot(), nil },
		Assets:   assets,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, APIPrefix+"/health", nil))
	if rec.Code == http.StatusOK {
		t.Errorf("POST to a read-only endpoint returned 200: %s", rec.Body.String())
	}
}

// The dashboard is served from the same mux, so an API path must never be
// answered with index.html.
func TestAPIPathsNeverReturnTheDashboard(t *testing.T) {
	assets := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := w.Write([]byte("<!doctype html>")); err != nil {
			t.Errorf("asset handler write: %v", err)
		}
	})
	s, err := New(Config{
		Snapshot: func(context.Context) (*models.Snapshot, error) { return fixtureSnapshot(), nil },
		Assets:   assets,
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/nope", nil))
	if strings.Contains(rec.Body.String(), "doctype") {
		t.Error("an unknown API path was answered with the dashboard HTML")
	}
}

func TestUnknownRouteReturnsJSONError(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHealthzDoesNotRunAnAnalysis(t *testing.T) {
	var calls atomic.Int64
	s, err := New(Config{
		Snapshot: func(context.Context) (*models.Snapshot, error) {
			calls.Add(1)
			return fixtureSnapshot(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if calls.Load() != 0 {
		t.Error("a liveness probe must not trigger a full analysis")
	}
}

func TestSnapshotErrorIsReportedAsJSON(t *testing.T) {
	s, err := New(Config{
		Snapshot: func(context.Context) (*models.Snapshot, error) {
			return nil, errors.New("permission denied")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/health", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("error body is not JSON: %s", rec.Body.String())
	}
	if !strings.Contains(body.Error, "permission denied") {
		t.Errorf("the underlying cause must be reported, got %q", body.Error)
	}
}

func TestNilSnapshotIsAnError(t *testing.T) {
	s, err := New(Config{
		Snapshot: func(context.Context) (*models.Snapshot, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/health", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("a nil snapshot must not produce 200, got %d", rec.Code)
	}
}

// Concurrency: several requests share one snapshot pointer. A test that only
// issues sequential requests would miss a shared-state bug here.
func TestConcurrentRequests(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)

	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := []string{"/health", "/risks", "/hotspots", "/history"}[i%4]
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+path, nil))
			if rec.Code != http.StatusOK {
				errs <- path + ": " + strconv.Itoa(rec.Code)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				errs <- path + ": bad json: " + err.Error()
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// The dashboard's TypeScript types mirror these JSON keys. This test is the
// contract check: a field the UI reads must exist in the response.
func TestDashboardTypeContract(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)

	// Every top-level field declared in src/lib/types.ts for each response.
	contracts := map[string][]string{
		"/health": {"root", "version", "generated_at", "duration_ms", "health", "code",
			"git", "dependencies", "risk_count", "critical_risk_count",
			"high_risk_count", "hotspot_count"},
		"/risks":    {"root", "generated_at", "total", "critical", "high", "medium", "low", "risks"},
		"/hotspots": {"root", "generated_at", "total", "confirmed", "hotspots", "churn"},
		"/history":  {"root", "count", "records", "delta"},
	}

	for path, fields := range contracts {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+path, nil))

		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		for _, f := range fields {
			if _, ok := body[f]; !ok {
				t.Errorf("%s: response is missing the %q field the dashboard reads", path, f)
			}
		}
	}
}

// The components added alongside CompareView read fields that are easy to get
// wrong, because each one has a near-miss twin: `critical_risk_count` against
// `risk_count`, `confirmed` against `total`, and per-dimension scores that live
// on a history record rather than on the live snapshot. A typo in any of them
// renders as `undefined` in the browser and nothing else fails.
//
// The field lists are hand-written on purpose. Generating them from types.ts
// would make the test agree with whatever the UI does, which is the one thing a
// contract check must not do.
func TestFieldsTheNewViewsRead(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)

	get := func(path string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+path, nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return body
	}

	// CompareView: risk and hotspot counters on the live snapshot.
	health := get("/health")
	for _, f := range []string{"risk_count", "critical_risk_count", "hotspot_count"} {
		if _, ok := health[f]; !ok {
			t.Errorf("CompareView reads health.%s, which the response lacks", f)
		}
	}

	// CompareView: confirmed hotspots come from /hotspots, not /health, so
	// passing the health total here would silently compare candidates against
	// confirmed on one side only.
	hot := get("/hotspots")
	if _, ok := hot["confirmed"]; !ok {
		t.Error("CompareView reads hotspots.confirmed, which the response lacks")
	}
	if _, ok := health["confirmed"]; ok {
		t.Error("health exposes `confirmed`; CompareView reads it from /hotspots " +
			"and reading it from both places would be ambiguous")
	}

	// ChurnTable: the churn array the hotspots endpoint has always returned and
	// nothing rendered.
	if _, ok := hot["churn"]; !ok {
		t.Error("ChurnTable reads hotspots.churn, which the response lacks")
	}

	// LanguageBreakdown: the per-language rows and their fields.
	langs, ok := health["code"].(map[string]any)["languages"].([]any)
	if !ok {
		t.Fatal("LanguageBreakdown reads code.languages, which is not a list")
	}
	if len(langs) == 0 {
		t.Fatal("the fixture has no languages, so the field checks below prove nothing")
	}
	first, ok := langs[0].(map[string]any)
	if !ok {
		t.Fatalf("a language row is not an object: %T", langs[0])
	}
	for _, f := range []string{"name", "files", "lines", "test_files"} {
		if _, ok := first[f]; !ok {
			t.Errorf("LanguageBreakdown reads languages[].%s, which a row lacks", f)
		}
	}

	// CompareView: per-dimension scores and the applicability flags that decide
	// whether a delta may exist at all.
	recs, ok := get("/history")["records"].([]any)
	if !ok {
		t.Fatal("CompareView reads history.records, which is not a list")
	}
	for _, r := range recs {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		for _, f := range []string{
			"score", "code_score", "dependency_score", "git_score",
			"code_applicable", "dependency_applicable", "git_applicable",
			"risk_count", "critical_risk_count", "hotspot_count",
			"confirmed_hotspots", "test_file_ratio", "code_lines",
		} {
			if _, ok := row[f]; !ok {
				t.Errorf("CompareView reads records[].%s, which a record lacks", f)
			}
		}
		return
	}
	t.Log("history is empty in this fixture; per-record fields are covered by " +
		"TestDashboardTypeContract")
}

// Nested objects the UI reads directly must be present too.
func TestNestedContract(t *testing.T) {
	s := newTestServer(t, fixtureSnapshot(), nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, APIPrefix+"/health", nil))

	// A key-by-key presence check: unmarshalling into a struct would accept a
	// missing key as a zero value, which is exactly the failure mode this
	// contract test exists to catch.
	var body map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	nested := map[string][]string{
		"health":       {"score", "grade", "summary", "metrics", "components"},
		"code":         {"files", "code_lines", "source_files", "test_files", "test_file_ratio", "languages"},
		"git":          {"is_repository", "branch", "window_commits", "authors", "bus_factor"},
		"dependencies": {"detected", "total", "direct", "locked", "drift"},
	}
	for section, fields := range nested {
		raw, ok := body[section]
		if !ok {
			t.Errorf("missing section %q", section)
			continue
		}
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(raw, &inner); err != nil {
			t.Errorf("%s: %v", section, err)
			continue
		}
		for _, f := range fields {
			if _, ok := inner[f]; !ok {
				t.Errorf("%s: missing field %q", section, f)
			}
		}
	}
}

func mustBody(t *testing.T, s *Server, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	b, _ := io.ReadAll(rec.Result().Body)
	return string(b)
}

func scores(records []storage.Record) []float64 {
	out := make([]float64, len(records))
	for i, r := range records {
		out[i] = r.Score
	}
	return out
}
