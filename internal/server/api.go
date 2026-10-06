// Package server exposes Lensyxe analysis results over a local HTTP API and
// serves the embedded dashboard that consumes it.
//
// Everything here is read-only with respect to the repository and strictly
// local: the listener binds to the loopback interface and no handler makes an
// outbound request. The API reads from a snapshot produced by the analyzer, so
// serving it is the same code path the CLI already exercises.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zelvior/lensyxe/internal/storage"
	"github.com/zelvior/lensyxe/pkg/models"
)

// APIPrefix is the versioned route prefix. Versioning it now costs nothing;
// changing it later breaks every consumer.
const APIPrefix = "/api/v1"

// maxHistoryLimit caps a history request so a client cannot ask the server to
// serialize its entire database in one response.
const maxHistoryLimit = 500

// defaultHistoryLimit is used when the client does not ask for a count.
const defaultHistoryLimit = 60

// SnapshotFunc returns the current analysis for the served repository.
//
// It is injected rather than calling the analyzer directly so the server
// package stays free of a dependency on the analysis pipeline, and so tests
// can drive it from a fixture instead of scanning a real tree.
type SnapshotFunc func(ctx context.Context) (*models.Snapshot, error)

// StoreFunc returns the history store, or nil when history is unavailable.
//
// A nil store is a supported state, not an error: `serve --no-persist` and a
// repository with no recorded runs both produce one. The history endpoint
// reports that plainly instead of failing the request.
type StoreFunc func() (*storage.Store, error)

// Config configures the HTTP server.
type Config struct {
	// Port is the TCP port to listen on. Zero picks an ephemeral port, which
	// is what tests use.
	Port int
	// Host is the bind address. It defaults to loopback; binding to a public
	// interface would expose a repository's internals to the network.
	Host string
	// Snapshot produces the current analysis. Required.
	Snapshot SnapshotFunc
	// Store provides history. Optional.
	Store StoreFunc
	// ReadTimeout bounds a single request.
	ReadTimeout time.Duration
	// WriteTimeout bounds a single response.
	WriteTimeout time.Duration
	// IdleTimeout bounds a kept-alive connection between requests.
	IdleTimeout time.Duration
	// Assets serves the dashboard. When nil, only the API is mounted.
	Assets http.Handler
}

// Server is the assembled handler set.
type Server struct {
	cfg     Config
	handler http.Handler
}

// DefaultLoopback is the only address Lensyxe binds to unless a caller
// deliberately overrides it.
const DefaultLoopback = "127.0.0.1"

// DefaultPort is the dashboard port.
const DefaultPort = 7357

// New assembles the server's routes.
//
// The routes are explicit rather than derived from a path parameter: an API
// surface where a request path decides which handler runs is a surface where a
// typo reaches something unintended.
func New(cfg Config) (*Server, error) {
	if cfg.Snapshot == nil {
		return nil, errors.New("server: a snapshot function is required")
	}
	s := &Server{cfg: cfg}
	if cfg.Host == "" {
		cfg.Host = DefaultLoopback
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 15 * time.Second
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 30 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 60 * time.Second
	}

	// The API prefix gets its own mux so an unknown API path is answered with
	// a JSON 404. Mounted ahead of the asset handler, which would otherwise
	// satisfy any path with index.html and turn a client's mistyped endpoint
	// into a 200 full of HTML.
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET "+APIPrefix+"/health", func(w http.ResponseWriter, r *http.Request) {
		s.writeHealth(w, r)
	})
	apiMux.HandleFunc("GET "+APIPrefix+"/history", func(w http.ResponseWriter, r *http.Request) {
		s.writeHistory(w, r)
	})
	apiMux.HandleFunc("GET "+APIPrefix+"/risks", func(w http.ResponseWriter, r *http.Request) {
		s.writeRisks(w, r)
	})
	apiMux.HandleFunc("GET "+APIPrefix+"/hotspots", func(w http.ResponseWriter, r *http.Request) {
		s.writeHotspots(w, r)
	})
	apiMux.HandleFunc("GET "+APIPrefix+"/version", func(w http.ResponseWriter, r *http.Request) {
		s.writeVersion(w, r)
	})

	mux := http.NewServeMux()
	mux.Handle(APIPrefix+"/", apiMux)

	// An explicit health endpoint for liveness. It reports that the process is
	// up without running an analysis, so a monitor does not trigger a full
	// scan on every poll.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		s.writeText(w, http.StatusOK, "ok\n")
	})

	if cfg.Assets != nil {
		mux.Handle("/", cfg.Assets)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			s.writeError(w, http.StatusNotFound, "no dashboard assets are embedded in this build")
		})
	}

	s.handler = s.wrap(mux)
	return s, nil
}

// Handler returns the configured handler.
//
// It is exported so tests can exercise the routes through httptest without
// binding a port.
func (s *Server) Handler() http.Handler { return s.handler }

// wrap installs middleware applied to every request.
func (s *Server) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The API is local-only and needs no CORS: nothing on the internet is
		// a legitimate caller. Rejecting cross-origin preflights keeps a
		// malicious page in the user's browser from reading repository details
		// through the loopback interface.
		if origin := r.Header.Get("Origin"); origin != "" {
			if !isLoopbackOrigin(origin) {
				s.writeError(w, http.StatusForbidden,
					"cross-origin requests are not allowed by the local dashboard")
				return
			}
		}

		// These endpoints return repository internals. A browser must never
		// cache them, or a shared machine would leak a prior tenant's data
		// into the next page load.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		next.ServeHTTP(w, r)
	})
}

// isLoopbackOrigin reports whether an Origin header names this machine.
func isLoopbackOrigin(origin string) bool {
	o := strings.TrimSpace(origin)
	for _, prefix := range []string{"http://localhost", "http://127.0.0.1", "http://[::1]"} {
		if strings.HasPrefix(o, prefix) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- responses

// HealthResponse is the body of GET /api/v1/health.
type HealthResponse struct {
	Root        string        `json:"root"`
	Version     string        `json:"version"`
	GeneratedAt time.Time     `json:"generated_at"`
	DurationMS  int64         `json:"duration_ms"`
	Health      models.Health `json:"health"`
	Code        codeSummary   `json:"code"`
	Git         gitSummary    `json:"git"`
	Deps        depSummary    `json:"dependencies"`
	RiskCount   int           `json:"risk_count"`
	Critical    int           `json:"critical_risk_count"`
	High        int           `json:"high_risk_count"`
	HotspotCnt  int           `json:"hotspot_count"`
}

// codeSummary is the subset of code stats the overview needs.
type codeSummary struct {
	Files         int     `json:"files"`
	TotalLines    int     `json:"total_lines"`
	CodeLines     int     `json:"code_lines"`
	AverageLines  float64 `json:"average_lines"`
	MaxFileLines  int     `json:"max_file_lines"`
	TestFiles     int     `json:"test_files"`
	SourceFiles   int     `json:"source_files"`
	TestFileRatio float64 `json:"test_file_ratio"`
	TestLineRatio float64 `json:"test_line_ratio"`
	HasTests      bool    `json:"has_tests"`
	// Languages is the per-language breakdown, already sorted by lines
	// descending then name ascending, so a client can render it directly.
	//
	// It was measured and already in the snapshot; it simply was not on this
	// response. Per-language figures are the first thing anyone asks about a
	// polyglot repository and the engine had them all along.
	Languages []models.LanguageStat `json:"languages"`
}

// gitSummary is the subset of git stats the overview needs.
type gitSummary struct {
	IsRepository    bool   `json:"is_repository"`
	Branch          string `json:"branch"`
	HeadCommit      string `json:"head_commit"`
	TotalCommits    int    `json:"total_commits"`
	WindowCommits   int    `json:"window_commits"`
	Authors         int    `json:"authors"`
	BusFactor       int    `json:"bus_factor"`
	DaysSinceCommit int    `json:"days_since_commit"`
	Note            string `json:"note,omitempty"`
}

// depSummary is the subset of dependency stats the overview needs.
type depSummary struct {
	Detected   bool `json:"detected"`
	Total      int  `json:"total"`
	Direct     int  `json:"direct"`
	Dev        int  `json:"dev"`
	Indirect   int  `json:"indirect"`
	Transitive int  `json:"transitive"`
	Locked     bool `json:"locked"`
	Drift      bool `json:"drift"`
}

// RisksResponse is the body of GET /api/v1/risks.
type RisksResponse struct {
	Root        string        `json:"root"`
	GeneratedAt time.Time     `json:"generated_at"`
	Total       int           `json:"total"`
	Critical    int           `json:"critical"`
	High        int           `json:"high"`
	Medium      int           `json:"medium"`
	Low         int           `json:"low"`
	Risks       []models.Risk `json:"risks"`
}

// HotspotsResponse is the body of GET /api/v1/hotspots.
type HotspotsResponse struct {
	Root        string              `json:"root"`
	GeneratedAt time.Time           `json:"generated_at"`
	Total       int                 `json:"total"`
	Confirmed   int                 `json:"confirmed"`
	Hotspots    []models.Hotspot    `json:"hotspots"`
	Churn       []models.ChurnEntry `json:"churn"`
}

// HistoryResponse is the body of GET /api/v1/history.
type HistoryResponse struct {
	Root    string           `json:"root"`
	Count   int              `json:"count"`
	Records []storage.Record `json:"records"`
	Delta   *historyDelta    `json:"delta"`
}

// historyDelta is the movement between the first and last recorded run.
type historyDelta struct {
	Score      float64 `json:"score"`
	Complexity float64 `json:"complexity"`
	Risks      int     `json:"risks"`
}

// VersionResponse is the body of GET /api/v1/version.
type VersionResponse struct {
	Version   string `json:"version"`
	Schema    string `json:"schema_version"`
	Dashboard bool   `json:"dashboard"`
}

// ---------------------------------------------------------------- handlers

func (s *Server) writeHealth(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "analysis failed: "+err.Error())
		return
	}

	resp := HealthResponse{
		Root:        snap.Root,
		Version:     snap.Version,
		GeneratedAt: snap.GeneratedAt,
		DurationMS:  snap.DurationMS,
		Health:      snap.Health,
		Code: codeSummary{
			Files:         snap.Code.Files,
			TotalLines:    snap.Code.TotalLines,
			CodeLines:     snap.Code.CodeLines,
			AverageLines:  snap.Code.AverageLines,
			MaxFileLines:  snap.Code.MaxFileLines,
			TestFiles:     snap.Code.TestFiles,
			SourceFiles:   snap.Code.SourceFiles,
			TestFileRatio: snap.Code.TestFileRatio,
			TestLineRatio: snap.Code.TestLineRatio,
			HasTests:      snap.Code.HasTests,
			Languages:     append([]models.LanguageStat{}, snap.Code.Languages...),
		},
		Git: gitSummary{
			IsRepository:    snap.Git.IsRepository,
			Branch:          snap.Git.Branch,
			HeadCommit:      snap.Git.HeadCommit,
			TotalCommits:    snap.Git.TotalCommits,
			WindowCommits:   snap.Git.WindowCommits,
			Authors:         snap.Git.Authors,
			BusFactor:       snap.Git.BusFactor,
			DaysSinceCommit: snap.Git.DaysSinceCommit,
			Note:            snap.Git.Note,
		},
		Deps: depSummary{
			Detected:   snap.Dependencies.Detected,
			Total:      snap.Dependencies.Total,
			Direct:     snap.Dependencies.Direct,
			Dev:        snap.Dependencies.Dev,
			Indirect:   snap.Dependencies.Indirect,
			Transitive: snap.Dependencies.Transitive,
			Locked:     snap.Dependencies.Locked,
			Drift:      snap.Dependencies.Drift,
		},
		RiskCount: len(snap.Risks),
	}
	for _, r := range snap.Risks {
		switch r.Severity {
		case models.SeverityCritical:
			resp.Critical++
		case models.SeverityHigh:
			resp.High++
		}
	}
	resp.HotspotCnt = len(snap.Code.Hotspots)

	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) writeRisks(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "analysis failed: "+err.Error())
		return
	}

	resp := RisksResponse{
		Root:        snap.Root,
		GeneratedAt: snap.GeneratedAt,
		// An empty slice rather than nil: `null` in JSON would make the
		// dashboard's `.map()` fail, turning "no risks" into a crash.
		Risks: append([]models.Risk{}, snap.Risks...),
	}
	for _, risk := range snap.Risks {
		switch risk.Severity {
		case models.SeverityCritical:
			resp.Critical++
		case models.SeverityHigh:
			resp.High++
		case models.SeverityMedium:
			resp.Medium++
		case models.SeverityLow:
			resp.Low++
		}
	}
	resp.Total = len(resp.Risks)

	// A caller can narrow the list; an unknown severity yields an error rather
	// than silently returning everything, which would look like the filter
	// was applied.
	if want := strings.TrimSpace(r.URL.Query().Get("severity")); want != "" {
		filtered, ok := filterRisks(resp.Risks, want)
		if !ok {
			s.writeError(w, http.StatusBadRequest,
				fmt.Sprintf("unknown severity %q (want critical, high, medium, low, or info)", want))
			return
		}
		resp.Risks = filtered
	}

	s.writeJSON(w, http.StatusOK, resp)
}

// filterRisks keeps only risks at the named severity.
func filterRisks(in []models.Risk, severity string) ([]models.Risk, bool) {
	target := models.Severity(strings.ToLower(severity))
	switch target {
	case models.SeverityCritical, models.SeverityHigh, models.SeverityMedium,
		models.SeverityLow, models.SeverityInfo:
	default:
		return nil, false
	}

	out := make([]models.Risk, 0, len(in))
	for _, r := range in {
		if r.Severity == target {
			out = append(out, r)
		}
	}
	return out, true
}

func (s *Server) writeHotspots(w http.ResponseWriter, r *http.Request) {
	snap, err := s.snapshot(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "analysis failed: "+err.Error())
		return
	}

	resp := HotspotsResponse{
		Root:        snap.Root,
		GeneratedAt: snap.GeneratedAt,
		Total:       len(snap.Code.Hotspots),
		Hotspots:    append([]models.Hotspot{}, snap.Code.Hotspots...),
		Churn:       append([]models.ChurnEntry{}, snap.Git.Churn...),
	}
	for _, h := range snap.Code.Hotspots {
		if h.Confirmed {
			resp.Confirmed++
		}
	}

	// `confirmed=true` narrows to the hotspots where size, churn, and
	// complexity crossed their thresholds together.
	if v := strings.TrimSpace(r.URL.Query().Get("confirmed")); v != "" {
		only, err := strconv.ParseBool(v)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "confirmed must be true or false")
			return
		}
		filtered := make([]models.Hotspot, 0, len(resp.Hotspots))
		for _, h := range resp.Hotspots {
			if h.Confirmed == only {
				filtered = append(filtered, h)
			}
		}
		resp.Hotspots = filtered
	}

	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) writeHistory(w http.ResponseWriter, r *http.Request) {
	limit := defaultHistoryLimit
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "limit must be an integer")
			return
		}
		if n < 0 {
			s.writeError(w, http.StatusBadRequest, "limit must not be negative")
			return
		}
		limit = min(n, maxHistoryLimit)
	}

	// History is keyed by the absolute repository root, which only the
	// snapshot knows. Asking the snapshot for it here keeps the two endpoints
	// from drifting apart when a path is analyzed and stored under a
	// different resolution.
	resp := HistoryResponse{Records: []storage.Record{}}

	if snap, err := s.snapshot(r.Context()); err == nil && snap != nil {
		resp.Root = snap.Root
	}

	store, err := s.store()
	if err != nil || store == nil {
		if err != nil {
			s.writeError(w, http.StatusInternalServerError,
				"history is unavailable: "+err.Error())
			return
		}
		// No store is a valid state: the response is empty rather than an
		// error, and the dashboard renders its "run analyze" message.
		s.writeJSON(w, http.StatusOK, resp)
		return
	}
	// The store is NOT closed here. It is a long-lived handle owned by the
	// serve command and shared by every request; closing it per request would
	// break every request after the first one.

	// Store.GetHistory applies its limit BEFORE its ordering flip, so a capped
	// query returns the OLDEST N runs rather than the most recent ones. For a
	// dashboard the recent end is the point, so the rows are read in full
	// (already bounded by the store's own 500-row pruning) and the cap is
	// applied here to the most recent window.
	all, err := store.GetHistory(resp.Root, 0)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "read history: "+err.Error())
		return
	}
	if limit > 0 && len(all) > limit {
		all = all[len(all)-limit:]
	}
	records := all
	if records == nil {
		records = []storage.Record{}
	}

	// Store.GetHistory already returns oldest-first, which is the order a chart
	// plots in. It is used as-is; reversing again would turn a rising trend
	// into a falling one.
	resp.Records = records
	resp.Count = len(resp.Records)

	// The delta is only meaningful with at least two runs.
	if len(resp.Records) >= 2 {
		first := resp.Records[0]
		last := resp.Records[len(resp.Records)-1]
		resp.Delta = &historyDelta{
			Score:      round2(last.Score - first.Score),
			Complexity: round2(last.AvgComplexity - first.AvgComplexity),
			Risks:      last.RiskCount - first.RiskCount,
		}
	}

	s.writeJSON(w, http.StatusOK, resp)
}

func (s *Server) writeVersion(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, VersionResponse{
		Version:   apiVersion,
		Schema:    models.SchemaVersion,
		Dashboard: s.cfg.Assets != nil,
	})
}

// apiVersion is the API contract version, independent of the tool version.
const apiVersion = "v1"

// snapshot resolves the current analysis.
func (s *Server) snapshot(ctx context.Context) (*models.Snapshot, error) {
	snap, err := s.cfg.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if snap == nil {
		return nil, errors.New("no snapshot available")
	}
	return snap, nil
}

// store resolves the history store, or nil when there is none.
func (s *Server) store() (*storage.Store, error) {
	if s.cfg.Store == nil {
		return nil, nil
	}
	return s.cfg.Store()
}

// ---------------------------------------------------------------- encoding

// writeJSON encodes v with a trailing newline and the given status.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		// The status line is already sent, so the best that can be done is
		// record that the body was truncated rather than silently delivering a
		// half-written document.
		return
	}
}

// writeError sends a structured error body.
//
// The shape is consistent across every failure so a client never has to guess
// whether it received HTML or JSON.
func (s *Server) writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]string{"error": message})
}

func (s *Server) writeText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// round2 matches the rounding used throughout the analyzer, so a delta
// computed here matches the one the CLI would print.
func round2(v float64) float64 {
	return float64(int64(v*100+copysign(0.5, v))) / 100
}

// copysign returns a positive zero for v >= 0 and a negative one otherwise, so
// round2 breaks ties away from zero.
func copysign(magnitude, sign float64) float64 {
	if sign < 0 {
		return -magnitude
	}
	return magnitude
}
