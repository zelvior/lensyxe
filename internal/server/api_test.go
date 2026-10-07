package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
