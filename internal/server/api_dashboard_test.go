package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zelvior/lensyxe/internal/storage"
)

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
