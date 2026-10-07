package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

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
