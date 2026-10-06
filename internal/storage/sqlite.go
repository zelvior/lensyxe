// Package storage persists analysis snapshots in a local SQLite database.
//
// The database lives at .lensyxe/history.db inside the analyzed repository and
// is created on demand. Nothing is ever uploaded: the history is a local file
// the user can delete, inspect, or add to .gitignore.
//
// The SQLite driver is modernc.org/sqlite, a pure-Go translation, so Lensyxe
// keeps building on machines with no C toolchain. The tradeoff is binary size:
// the driver adds roughly 10 MB, which is a deliberate trade for a tool whose
// selling point is "go install and it just works".
package storage

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"

	// Pure-Go SQLite driver: registers itself as "sqlite" and requires no cgo.
	_ "modernc.org/sqlite"
)

// DefaultDir is the per-repository data directory, relative to the repo root.
const DefaultDir = ".lensyxe"

// DefaultFile is the database filename inside DefaultDir.
const DefaultFile = "history.db"

// ErrNoHistory indicates the store holds no snapshots.
var ErrNoHistory = errors.New("no history recorded")

// schemaVersion is bumped when the table layout changes. Stored in a
// metadata table so a future migration can detect an old file.
const schemaVersion = "1"

// Record is one row of stored history: the scalar metrics used for trending,
// plus the full snapshot JSON for fidelity.
//
// The scalar columns exist so trend queries never have to parse JSON, and so
// the table remains queryable from the sqlite3 CLI by hand.
type Record struct {
	// ID is the insertion order. Ordering by ID is stable even when two
	// snapshots share a timestamp.
	ID int64 `json:"id"`
	// RecordedAt is when the analysis ran, in UTC.
	RecordedAt time.Time `json:"recorded_at"`
	// Root is the analyzed directory, so one database can hold several repos.
	Root string `json:"root"`
	// Commit is the git HEAD at analysis time, empty outside a repository.
	Commit string `json:"commit"`
	// Version is the Lensyxe build that produced the row.
	Version string `json:"version"`

	Score float64 `json:"score"`
	Grade string  `json:"grade"`

	CodeScore        float64 `json:"code_score"`
	DependencyScore  float64 `json:"dependency_score"`
	GitScore         float64 `json:"git_score"`
	CodeScoreApplied bool    `json:"code_applicable"`
	DepScoreApplied  bool    `json:"dependency_applicable"`
	GitScoreApplied  bool    `json:"git_applicable"`

	RiskCount         int `json:"risk_count"`
	CriticalRiskCount int `json:"critical_risk_count"`
	HighRiskCount     int `json:"high_risk_count"`
	HotspotCount      int `json:"hotspot_count"`
	ConfirmedHotspots int `json:"confirmed_hotspots"`

	Files           int     `json:"files"`
	SourceFiles     int     `json:"source_files"`
	TestFiles       int     `json:"test_files"`
	TestFileRatio   float64 `json:"test_file_ratio"`
	CodeLines       int     `json:"code_lines"`
	AvgComplexity   float64 `json:"avg_complexity"`
	MaxComplexity   float64 `json:"max_complexity"`
	DependencyDrift bool    `json:"dependency_drift"`

	// Snapshot is the full serialized snapshot, used for detail views.
	Snapshot []byte `json:"-"`
}

// Store is a handle on the history database.
//
// It is safe for concurrent use: database/sql manages connection pooling and
// SQLite serializes writes internally.
type Store struct {
	db   *sql.DB
	path string
}

// Open creates or opens the history database at path.
//
// The parent directory is created if missing, so a first run on a fresh
// repository works with no setup.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("storage: empty database path")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("storage: create %s: %w", dir, err)
		}
	}

	// WAL keeps readers from blocking the writer, which matters for `watch`
	// saving snapshots while a `history` render may be reading.
	// Busy timeout avoids spurious "database is locked" under concurrent use.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}
	// A single writer avoids SQLITE_BUSY entirely under our access pattern.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("storage: ping %s: %w", path, err)
	}

	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// migrate creates the schema if absent.
func (s *Store) migrate() error {
	const ddl = `
CREATE TABLE IF NOT EXISTS lensyxe_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS snapshots (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    recorded_at          TEXT    NOT NULL,
    root                 TEXT    NOT NULL DEFAULT '',
    -- "commit" is a reserved word in SQLite's parser (COMMIT is the
    -- transaction statement), so the column is named git_commit.
    git_commit           TEXT    NOT NULL DEFAULT '',
    version              TEXT    NOT NULL DEFAULT '',

    score                REAL    NOT NULL DEFAULT 0,
    grade                TEXT    NOT NULL DEFAULT '',

    code_score           REAL    NOT NULL DEFAULT 0,
    dependency_score     REAL    NOT NULL DEFAULT 0,
    git_score            REAL    NOT NULL DEFAULT 0,
    code_applicable      INTEGER NOT NULL DEFAULT 0,
    dependency_applicable INTEGER NOT NULL DEFAULT 0,
    git_applicable       INTEGER NOT NULL DEFAULT 0,

    risk_count           INTEGER NOT NULL DEFAULT 0,
    critical_risk_count  INTEGER NOT NULL DEFAULT 0,
    high_risk_count      INTEGER NOT NULL DEFAULT 0,
    hotspot_count        INTEGER NOT NULL DEFAULT 0,
    confirmed_hotspots   INTEGER NOT NULL DEFAULT 0,

    files                INTEGER NOT NULL DEFAULT 0,
    source_files         INTEGER NOT NULL DEFAULT 0,
    test_files           INTEGER NOT NULL DEFAULT 0,
    test_file_ratio      REAL    NOT NULL DEFAULT 0,
    code_lines           INTEGER NOT NULL DEFAULT 0,
    avg_complexity       REAL    NOT NULL DEFAULT 0,
    max_complexity       REAL    NOT NULL DEFAULT 0,
    dependency_drift     INTEGER NOT NULL DEFAULT 0,

    snapshot             BLOB
);

-- Trending queries read newest-first for a root and filter on score.
CREATE INDEX IF NOT EXISTS idx_snapshots_root_id ON snapshots (root, id);
CREATE INDEX IF NOT EXISTS idx_snapshots_root_time ON snapshots (root, recorded_at);
`
	if _, err := s.db.Exec(ddl); err != nil {
		return fmt.Errorf("storage: create schema: %w", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO lensyxe_meta (key, value) VALUES ('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		schemaVersion,
	); err != nil {
		return fmt.Errorf("storage: record schema version: %w", err)
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// SaveSnapshot persists a snapshot and returns the new row ID.
//
// The snapshot is stored twice: as scalar columns for fast trending, and as
// the full JSON blob so detail views never need to re-analyze.
func (s *Store) SaveSnapshot(snap *models.Snapshot) (int64, error) {
	if snap == nil {
		return 0, errors.New("storage: nil snapshot")
	}
	rec := NewRecord(snap)

	blob, err := json.Marshal(snap)
	if err != nil {
		return 0, fmt.Errorf("storage: marshal snapshot: %w", err)
	}

	const q = `
INSERT INTO snapshots (
    recorded_at, root, git_commit, version, score, grade,
    code_score, dependency_score, git_score,
    code_applicable, dependency_applicable, git_applicable,
    risk_count, critical_risk_count, high_risk_count,
    hotspot_count, confirmed_hotspots,
    files, source_files, test_files, test_file_ratio, code_lines,
    avg_complexity, max_complexity, dependency_drift, snapshot
) VALUES (?,?,?,?,?,?, ?,?,?, ?,?,?, ?,?,?, ?,?, ?,?,?,?,?, ?,?,?,?)`

	res, err := s.db.Exec(q,
		rec.RecordedAt.UTC().Format(time.RFC3339Nano),
		rec.Root, rec.Commit, rec.Version,
		rec.Score, rec.Grade,
		rec.CodeScore, rec.DependencyScore, rec.GitScore,
		rec.CodeScoreApplied, rec.DepScoreApplied, rec.GitScoreApplied,
		rec.RiskCount, rec.CriticalRiskCount, rec.HighRiskCount,
		rec.HotspotCount, rec.ConfirmedHotspots,
		rec.Files, rec.SourceFiles, rec.TestFiles, rec.TestFileRatio, rec.CodeLines,
		rec.AvgComplexity, rec.MaxComplexity, rec.DependencyDrift,
		blob,
	)
	if err != nil {
		return 0, fmt.Errorf("storage: insert snapshot: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("storage: last insert id: %w", err)
	}
	return id, nil
}

// NewRecord projects a snapshot into a storage row.
func NewRecord(snap *models.Snapshot) Record {
	rec := Record{
		RecordedAt: snap.GeneratedAt,
		Root:       snap.Root,
		Commit:     snap.Git.HeadCommit,
		Version:    snap.Version,
		Score:      snap.Health.Score,
		Grade:      snap.Health.Grade,

		RiskCount:    len(snap.Risks),
		HotspotCount: len(snap.Code.Hotspots),

		Files:         snap.Code.Files,
		SourceFiles:   snap.Code.SourceFiles,
		TestFiles:     snap.Code.TestFiles,
		TestFileRatio: snap.Code.TestFileRatio,
		CodeLines:     snap.Code.CodeLines,
		AvgComplexity: snap.Code.Complexity.AverageComplexity,
		MaxComplexity: snap.Code.Complexity.MaxComplexity,

		DependencyDrift: snap.Dependencies.Drift,
	}
	if rec.RecordedAt.IsZero() {
		rec.RecordedAt = time.Now().UTC()
	}
	for _, m := range snap.Health.Metrics {
		switch m.Key {
		case "code":
			rec.CodeScore, rec.CodeScoreApplied = m.Score, m.Applicable
		case "dependency":
			rec.DependencyScore, rec.DepScoreApplied = m.Score, m.Applicable
		case "git":
			rec.GitScore, rec.GitScoreApplied = m.Score, m.Applicable
		}
	}
	for _, r := range snap.Risks {
		switch r.Severity {
		case models.SeverityCritical:
			rec.CriticalRiskCount++
		case models.SeverityHigh:
			rec.HighRiskCount++
		}
	}
	for _, h := range snap.Code.Hotspots {
		if h.Confirmed {
			rec.ConfirmedHotspots++
		}
	}
	return rec
}

// columns is the shared projection, used by every read query so the field
// order in Record and in the SQL can never drift apart.
const columns = `id, recorded_at, root, git_commit, version, score, grade,
    code_score, dependency_score, git_score,
    code_applicable, dependency_applicable, git_applicable,
    risk_count, critical_risk_count, high_risk_count,
    hotspot_count, confirmed_hotspots,
    files, source_files, test_files, test_file_ratio, code_lines,
    avg_complexity, max_complexity, dependency_drift, snapshot`

// GetHistory returns up to limit snapshots for root, oldest first.
//
// limit <= 0 returns every stored row. Rows are selected newest-first and then
// reversed, so the result reads chronologically while still applying the limit
// to the most recent entries.
func (s *Store) GetHistory(root string, limit int) ([]Record, error) {
	q := `SELECT ` + columns + ` FROM snapshots WHERE root = ? ORDER BY id DESC`
	args := []any{root}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query history: %w", err)
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: read history: %w", err)
	}
	// Query returned newest-first; flip for chronological order.
	reverse(out)
	return out, nil
}

// GetHistoryAll returns history across every stored root, oldest first.
func (s *Store) GetHistoryAll(limit int) ([]Record, error) {
	q := `SELECT ` + columns + ` FROM snapshots ORDER BY id DESC`
	var args []any
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: query history: %w", err)
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: read history: %w", err)
	}
	reverse(out)
	return out, nil
}

// scanRecord reads one row via a rowScanner.
func scanRecord(rows *sql.Rows) (Record, error) {
	var (
		rec    Record
		at     string
		codeOK int
		depOK  int
		gitOK  int
		drift  int
	)
	err := rows.Scan(
		&rec.ID, &at, &rec.Root, &rec.Commit, &rec.Version, &rec.Score, &rec.Grade,
		&rec.CodeScore, &rec.DependencyScore, &rec.GitScore,
		&codeOK, &depOK, &gitOK,
		&rec.RiskCount, &rec.CriticalRiskCount, &rec.HighRiskCount,
		&rec.HotspotCount, &rec.ConfirmedHotspots,
		&rec.Files, &rec.SourceFiles, &rec.TestFiles, &rec.TestFileRatio, &rec.CodeLines,
		&rec.AvgComplexity, &rec.MaxComplexity, &drift, &rec.Snapshot,
	)
	if err != nil {
		return Record{}, fmt.Errorf("storage: scan row: %w", err)
	}
	if t, perr := time.Parse(time.RFC3339Nano, at); perr == nil {
		rec.RecordedAt = t.UTC()
	}
	rec.CodeScoreApplied = codeOK != 0
	rec.DepScoreApplied = depOK != 0
	rec.GitScoreApplied = gitOK != 0
	rec.DependencyDrift = drift != 0
	return rec, nil
}

// ScoreDelta is the change between the two most recent snapshots for a root.
type ScoreDelta struct {
	// HasPrevious is false when only one snapshot exists, in which case the
	// Delta fields are meaningless and rendered as "n/a".
	HasPrevious bool
	// Latest and Previous are the two rows, oldest first.
	Latest   Record
	Previous Record
	// Delta is Latest.Score - Previous.Score, rounded to two decimals.
	Delta float64
	// Trend is "improving", "declining", or "flat".
	Trend string
}

// GetScoreDelta returns the change between the last two stored snapshots.
//
// Returns ErrNoHistory when nothing is stored and a zero ScoreDelta with
// HasPrevious=false when only one snapshot exists.
func (s *Store) GetScoreDelta(root string) (ScoreDelta, error) {
	recs, err := s.GetHistory(root, 2)
	if err != nil {
		return ScoreDelta{}, err
	}
	switch len(recs) {
	case 0:
		return ScoreDelta{}, fmt.Errorf("%w for %s", ErrNoHistory, root)
	case 1:
		return ScoreDelta{HasPrevious: false, Latest: recs[0]}, nil
	}

	prev, latest := recs[0], recs[1]
	delta := round2(latest.Score - prev.Score)
	trend := "flat"
	switch {
	case delta > 0.05:
		trend = "improving"
	case delta < -0.05:
		trend = "declining"
	}
	return ScoreDelta{
		HasPrevious: true,
		Latest:      latest,
		Previous:    prev,
		Delta:       delta,
		Trend:       trend,
	}, nil
}

// Count returns the number of snapshots stored for root.
func (s *Store) Count(root string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE root = ?`, root).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("storage: count: %w", err)
	}
	return n, nil
}

// Latest returns the most recent snapshot for root, or ErrNoHistory.
func (s *Store) Latest(root string) (Record, error) {
	const q = `SELECT ` + columns + ` FROM snapshots WHERE root = ? ORDER BY id DESC LIMIT 1`
	row := s.db.QueryRow(q, root)
	var (
		rec           Record
		at            string
		codeOK, depOK int
		gitOK, drift  int
	)
	err := row.Scan(
		&rec.ID, &at, &rec.Root, &rec.Commit, &rec.Version, &rec.Score, &rec.Grade,
		&rec.CodeScore, &rec.DependencyScore, &rec.GitScore,
		&codeOK, &depOK, &gitOK,
		&rec.RiskCount, &rec.CriticalRiskCount, &rec.HighRiskCount,
		&rec.HotspotCount, &rec.ConfirmedHotspots,
		&rec.Files, &rec.SourceFiles, &rec.TestFiles, &rec.TestFileRatio, &rec.CodeLines,
		&rec.AvgComplexity, &rec.MaxComplexity, &drift, &rec.Snapshot,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("%w for %s", ErrNoHistory, root)
	}
	if err != nil {
		return Record{}, fmt.Errorf("storage: latest: %w", err)
	}
	if t, perr := time.Parse(time.RFC3339Nano, at); perr == nil {
		rec.RecordedAt = t.UTC()
	}
	rec.CodeScoreApplied = codeOK != 0
	rec.DepScoreApplied = depOK != 0
	rec.GitScoreApplied = gitOK != 0
	rec.DependencyDrift = drift != 0
	return rec, nil
}

// Prune keeps at most keep rows per root, deleting the oldest beyond that.
//
// Called automatically so a long-lived repository cannot grow the database
// without bound. keep <= 0 disables pruning.
func (s *Store) Prune(root string, keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}
	const q = `
DELETE FROM snapshots
WHERE root = ?
  AND id NOT IN (
      SELECT id FROM snapshots WHERE root = ? ORDER BY id DESC LIMIT ?
  )`
	res, err := s.db.Exec(q, root, root, keep)
	if err != nil {
		return 0, fmt.Errorf("storage: prune: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("storage: prune rows affected: %w", err)
	}
	return n, nil
}

// ResolvePath returns the database path for a repository root, honoring an
// explicit override and otherwise using .lensyxe/history.db under the root.
//
// An override may be absolute (used as-is) or relative (joined onto root).
// filepath.IsAbs is deliberately not used for the test: on Windows it is false
// for "\abs\h.db", which is rooted but drive-relative, and such a path must
// not be silently appended to the repository root.
func ResolvePath(root, override string) string {
	override = strings.TrimSpace(override)
	if override == "" {
		return filepath.Join(root, DefaultDir, DefaultFile)
	}
	if hasVolumeOrRoot(override) {
		return override
	}
	return filepath.Join(root, override)
}

// hasVolumeOrRoot reports whether path is absolute on any supported platform.
//
// On Windows this must accept both "C:\dir" and "\dir"; on Unix only the
// latter form applies, and a leading "/" is already absolute there.
func hasVolumeOrRoot(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	if len(p) >= 2 && p[1] == ':' {
		// Drive-qualified, e.g. C:\ or C:/
		c := p[0]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') {
			return true
		}
	}
	// Rooted but drive-relative, which only occurs on Windows.
	return strings.HasPrefix(p, string(filepath.Separator))
}

func reverse(r []Record) {
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
}

func round2(v float64) float64 {
	scaled := v * 100
	if scaled >= 0 {
		scaled = float64(int64(scaled + 0.5))
	} else {
		scaled = float64(int64(scaled - 0.5))
	}
	return scaled / 100
}
