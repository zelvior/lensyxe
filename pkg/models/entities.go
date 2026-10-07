// Package models contains the data entities produced by Lensyxe.
//
// These structs are the single source of truth for the analysis pipeline:
// every analyzer fills one of them, the metrics scorer derives a Health from
// them, the risk engine explains it, and the reporters serialize them. Keeping
// them in a leaf package (no internal imports) keeps the JSON contract stable
// and easy to consume from other languages.
package models

import "time"

// SchemaVersion is the version of the JSON contract emitted by
// `lensyxe analyze --format json` and `lensyxe compare --format json`.
// Bump the minor part when adding fields, the major part when removing or
// repurposing them.
const SchemaVersion = "0.2"

// Severity classifies a Finding or Risk. Values are ordered strings so they
// sort predictably: critical > high > medium > low > info.
type Severity string

// Severity levels used across findings and risks.
const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityInfo     Severity = "info"
)

// severityRank maps a Severity to a numeric rank used for sorting. Unknown
// severities rank last so malformed input never crashes the reporter.
func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 4
	case SeverityHigh:
		return 3
	case SeverityMedium:
		return 2
	case SeverityLow:
		return 1
	case SeverityInfo:
		return 0
	default:
		return -1
	}
}

// Rank exposes the numeric weight of a severity (higher is worse).
func (s Severity) Rank() int { return severityRank(s) }

// Emoji renders the severity as the single glyph used in human-facing reports.
// Keeping the mapping in the model layer means every renderer agrees.
func (s Severity) Emoji() string {
	switch s {
	case SeverityCritical:
		return "🔴"
	case SeverityHigh:
		return "🟠"
	case SeverityMedium:
		return "🟡"
	case SeverityLow:
		return "🔵"
	case SeverityInfo:
		return "⚪"
	default:
		return "⚪"
	}
}

// Category identifies which analyzer produced a Finding or Risk.
type Category string

// Finding and risk categories.
const (
	CategoryCode        Category = "code"
	CategoryGit         Category = "git"
	CategoryDependency  Category = "dependency"
	CategoryConcurrency Category = "scan"
	CategoryComplexity  Category = "complexity"
	CategoryTesting     Category = "testing"
)

// Evidence is a single measurable fact backing a Risk.
//
// Risks must be evidence-first: a reader should be able to check every claim
// against a number and a path without re-running any tool. Evidence entries
// are therefore structured data, not prose.
type Evidence struct {
	// Kind categorizes the evidence, for example "metric", "file", or
	// "lockfile". Consumers can filter on it without parsing Detail.
	Kind string `json:"kind"`
	// Label is a short human name for the fact, for example "LOC".
	Label string `json:"label"`
	// Detail is the rendered measurement, for example "742 code lines".
	Detail string `json:"detail"`
	// Subject is the related path or identifier when applicable.
	Subject string `json:"subject,omitempty"`
	// Value is the raw measurement when the evidence is numeric. It is
	// omitted (NaN is not representable in JSON) when not applicable.
	Value *float64 `json:"value,omitempty"`
}

// NewEvidence builds an Evidence with a numeric value attached.
func NewEvidence(kind, label, detail string, value float64, subject string) Evidence {
	v := value
	return Evidence{Kind: kind, Label: label, Detail: detail, Subject: subject, Value: &v}
}

// Finding is a single observation about the repository. Findings are factual
// statements about the code, not opinions, and never change the health score
// on their own.
type Finding struct {
	Severity Severity `json:"severity"`
	Category Category `json:"category"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Subject  string   `json:"subject,omitempty"` // file path, module name, ...
}

// Risk is a derived, evidence-backed consequence of one or more findings.
// Unlike a Finding, a Risk contributes to the overall score reduction.
type Risk struct {
	// ID is a stable, deterministic identifier such as
	// "code.hotspot.internal/engine.go". It is derived only from the rule and
	// the subject, never from iteration order, so `lensyxe compare` can
	// reliably tell a new risk from a resolved one.
	ID       string   `json:"id"`
	Severity Severity `json:"severity"`
	Category Category `json:"category"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Subject  string   `json:"subject,omitempty"`
	// Impact is the deterministic score penalty (0..100 scale) this risk
	// explains. It is derived from the data, never hand-assigned.
	Impact float64 `json:"impact"`
	// Evidence lists the measurements backing this risk, most decisive first.
	Evidence []Evidence `json:"evidence"`
	// Recommendation is the concrete next step. It never names a specific
	// tool or refactoring technique, only the measurable outcome to reach.
	Recommendation string `json:"recommendation,omitempty"`
}

// Metric is a single scored dimension of the Engineering Health Score.
type Metric struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	// Value is the raw measurement (e.g. average lines per file).
	Value float64 `json:"value"`
	// Display is a pre-formatted, deterministic rendering of Value for humans.
	Display string `json:"display"`
	// Score is the normalized 0..100 sub-score for this metric.
	Score float64 `json:"score"`
	// Weight is this metric's share of the overall score. The weights of all
	// Applicable metrics are renormalized to sum to 1 before scoring.
	Weight float64 `json:"weight"`
	// Detail explains, in one line, why the score landed where it did.
	Detail string `json:"detail"`
	// Applicable is false when the underlying data was unavailable (for
	// example a non-git directory). Non-applicable metrics are excluded from
	// the weighted sum instead of being silently scored as zero.
	Applicable bool `json:"applicable"`
}

// Health is the aggregated 0..100 Engineering Health Score.
type Health struct {
	Score      float64  `json:"score"`      // 0..100, rounded to 2 decimals
	Grade      string   `json:"grade"`      // A..F
	Summary    string   `json:"summary"`    // one-line verdict
	Metrics    []Metric `json:"metrics"`    // sorted by descending weight
	Components int      `json:"components"` // number of applicable metrics
}

// LanguageStat is the file/line breakdown for a single language.
type LanguageStat struct {
	Name  string `json:"name"`
	Files int    `json:"files"`
	Lines int    `json:"lines"` // code lines (blank lines and comments excluded)
	// TestFiles counts files recognized as tests for this language.
	TestFiles int `json:"test_files"`
}

// ComplexityLevel buckets an estimated cyclomatic complexity. The thresholds
// follow the long-established McCabe bands (10 / 20 / 50) so the labels mean
// what an engineer expects.
type ComplexityLevel string

// Complexity bands, ordered from best to worst.
const (
	ComplexityLow      ComplexityLevel = "low"       // <= 10
	ComplexityModerate ComplexityLevel = "moderate"  // <= 20
	ComplexityHigh     ComplexityLevel = "high"      // <= 50
	ComplexityVeryHigh ComplexityLevel = "very_high" // > 50
)

// Rank orders complexity levels so comparisons are total.
func (l ComplexityLevel) Rank() int {
	switch l {
	case ComplexityLow:
		return 0
	case ComplexityModerate:
		return 1
	case ComplexityHigh:
		return 2
	case ComplexityVeryHigh:
		return 3
	default:
		return -1
	}
}

// FileComplexity is the lexical complexity estimate for a single file.
//
// Every number here is a heuristic derived from token counts, not an AST. The
// doc comments on each field state how it is computed so a reader can judge
// how much to trust it.
type FileComplexity struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	// Lines is the code-line count used as the denominator for Density.
	Lines int `json:"lines"`
	// Functions is the number of function or method definitions detected.
	Functions int `json:"functions"`
	// BranchPoints counts if / else-if / for / while / case / catch / &&
	// / || tokens outside comments and strings.
	BranchPoints int `json:"branch_points"`
	// MaxNesting is the deepest block nesting observed.
	MaxNesting int `json:"max_nesting"`
	// EstimatedComplexity is the mean estimated cyclomatic complexity per
	// function: (BranchPoints + Functions) / Functions, or 1 when no
	// function could be identified.
	EstimatedComplexity float64         `json:"estimated_complexity"`
	MaxFunctionComplex  float64         `json:"max_function_complexity"`
	Level               ComplexityLevel `json:"level"`
	// Density is EstimatedComplexity per 100 code lines.
	Density float64 `json:"density"`
}

// ComplexitySummary aggregates file-level complexity for a repository.
type ComplexitySummary struct {
	// Measured is false when no source file was complex enough to analyze.
	Measured          bool             `json:"measured"`
	Files             int              `json:"files"`
	Functions         int              `json:"functions"`
	BranchPoints      int              `json:"branch_points"`
	MaxNesting        int              `json:"max_nesting"`
	AverageComplexity float64          `json:"average_complexity"`
	MaxComplexity     float64          `json:"max_complexity"`
	MaxComplexityFile string           `json:"max_complexity_file"`
	VeryHighFunctions int              `json:"very_high_functions"`
	HighFunctions     int              `json:"high_functions"`
	WorstFiles        []FileComplexity `json:"worst_files"` // sorted worst first, capped
}

// Hotspot is a file that combines size, churn, and complexity risk.
type Hotspot struct {
	Path     string `json:"path"`
	Lines    int    `json:"lines"`
	Language string `json:"language"`

	// Churn is added+deleted lines for this file inside the git window. It is
	// zero when the target is not a git repository, which is why
	// Classification can report "size only".
	Churn int `json:"churn"`
	// Complexity is the file's estimated cyclomatic complexity.
	Complexity float64         `json:"complexity"`
	Level      ComplexityLevel `json:"level,omitempty"`

	// Classification states which factors put this file on the list.
	// Values are sorted, comma-separated tokens: "size", "churn",
	// "complexity", "confirmed".
	// "confirmed" means every factor of the full hotspot rule was met.
	Classification string `json:"classification"`
	// Confirmed is true when size, churn, and complexity all crossed their
	// thresholds together. This is the only classification that justifies a
	// high-severity risk.
	Confirmed bool `json:"confirmed"`
	// Rationale explains the classification in one sentence with numbers.
	Rationale string `json:"rationale"`
}

// CodeStats is the output of the code analyzer.
type CodeStats struct {
	Files          int            `json:"files"`
	TotalLines     int            `json:"total_lines"`      // physical lines of source
	CodeLines      int            `json:"code_lines"`       // blank lines and comments excluded
	BlankOrComment int            `json:"blank_or_comment"` // TotalLines - CodeLines
	MaxFileLines   int            `json:"max_file_lines"`
	AverageLines   float64        `json:"average_lines"` // CodeLines / Files, 2 decimals
	Languages      []LanguageStat `json:"languages"`     // sorted by Lines desc, then name asc
	Hotspots       []Hotspot      `json:"hotspots"`      // sorted by severity, then Lines desc, then path asc
	Bytes          int64          `json:"bytes"`
	Truncated      bool           `json:"truncated"` // true when the file cap was hit

	// TestFiles counts files matching a recognized test naming convention.
	TestFiles int `json:"test_files"`
	// SourceFiles counts non-test source files.
	SourceFiles int `json:"source_files"`
	// TestFileRatio is TestFiles / (TestFiles + SourceFiles), 0..1.
	TestFileRatio float64 `json:"test_file_ratio"`
	// TestLineRatio is the share of code lines living in test files, 0..1.
	TestLineRatio float64 `json:"test_line_ratio"`
	// HasTests is false when no test file was found at all.
	HasTests bool `json:"has_tests"`

	// Complexity aggregates the per-file complexity estimates.
	Complexity ComplexitySummary `json:"complexity"`
}

// ChurnEntry aggregates how often and how violently a file changed.
type ChurnEntry struct {
	Path    string `json:"path"`
	Commits int    `json:"commits"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	// Score is Commits + Added + Deleted; the ranking key for churn hotspots.
	Score int `json:"score"`
}

// GitStats is the output of the git analyzer. It is the zero value (with
// IsRepository=false) for directories that are not under version control.
type GitStats struct {
	IsRepository    bool      `json:"is_repository"`
	Branch          string    `json:"branch"`
	HeadCommit      string    `json:"head_commit"`
	TotalCommits    int       `json:"total_commits"`
	WindowDays      int       `json:"window_days"`
	WindowCommits   int       `json:"window_commits"`
	Authors         int       `json:"authors"`
	BusFactor       int       `json:"bus_factor"` // authors with >=20% of window commits
	FirstCommitAt   time.Time `json:"first_commit_at"`
	LastCommitAt    time.Time `json:"last_commit_at"`
	DaysSinceCommit int       `json:"days_since_commit"`
	CommitsPerWeek  float64   `json:"commits_per_week"` // window commits over the history that exists
	// CadenceSpanDays is the denominator used for CommitsPerWeek: the shorter of
	// WindowDays and the repository's own age in days.
	//
	// It is reported so a reader can see what the rate was measured over.
	// CommitsPerWeek without it is an unauditable number -- the same value means
	// different things depending on whether the repository is nine months old or
	// nine hours old.
	CadenceSpanDays  int          `json:"cadence_span_days"`
	LinesAdded       int          `json:"lines_added"`
	LinesDeleted     int          `json:"lines_deleted"`
	TopAuthorShare   float64      `json:"top_author_share"`   // 0..1 share of the busiest author
	Churn            []ChurnEntry `json:"churn"`              // sorted by Score desc, then path asc
	ChurnHotspotRate float64      `json:"churn_hotspot_rate"` // share of window churn in the top 3 files
	ChurnFiles       int          `json:"churn_files"`        // distinct files touched in the window
	// ChurnConcentration is a normalized 0..1 measure of how unevenly churn
	// is distributed across files: 0 means perfectly even, 1 means a single
	// file absorbed everything. Computed as
	// (sum(p_i^2) - 1/N) / (1 - 1/N) over the window's churn shares, which
	// stays meaningful for repositories with only a handful of files.
	ChurnConcentration float64 `json:"churn_concentration"`
	Note               string  `json:"note,omitempty"` // human-readable degradation reason
}

// EcosystemStats describes one detected dependency ecosystem.
type EcosystemStats struct {
	// Name is the ecosystem identifier: "npm", "gomod", or "python".
	Name string `json:"name"`
	// Manifest is the repo-relative manifest path.
	Manifest string `json:"manifest"`
	// Lockfile is the repo-relative lockfile, empty when absent.
	Lockfile string `json:"lockfile,omitempty"`
	// LockfileFormat names the detected lockfile syntax, for example
	// "npm-v2" or "yarn-v1". Empty when no lockfile is present.
	LockfileFormat string `json:"lockfile_format,omitempty"`
	Total          int    `json:"total"`
	Direct         int    `json:"direct"`
	Dev            int    `json:"dev"`
	Indirect       int    `json:"indirect"`
	// Transitive is the count resolved from the lockfile, which is the only
	// reliable source for it. Zero when no lockfile is present.
	Transitive int `json:"transitive"`
	// Packages is a sorted, capped list of declared direct dependencies.
	Packages []string `json:"packages"`
	// Detected is true when a manifest for this ecosystem was found.
	Detected bool `json:"detected"`
	// Drift is true when a manifest exists with no lockfile, so resolved
	// versions are not reproducible.
	Drift bool `json:"drift"`
	// DriftReason explains the drift in one sentence.
	DriftReason string `json:"drift_reason,omitempty"`
	// LockfileStale is true when the manifest is newer than the lockfile by
	// modification time. It is a hint, not proof: it never touches the
	// network to check whether versions actually resolve.
	LockfileStale bool `json:"lockfile_stale"`
}

// DependencyStats is the output of the dependency analyzer.
type DependencyStats struct {
	Detected   bool             `json:"detected"`
	Ecosystems []EcosystemStats `json:"ecosystems"` // sorted by Name asc
	Total      int              `json:"total"`
	Direct     int              `json:"direct"`
	Dev        int              `json:"dev"`
	Indirect   int              `json:"indirect"`
	Transitive int              `json:"transitive"`
	Locked     bool             `json:"locked"` // every detected ecosystem has a lockfile
	// Drift is true when any detected ecosystem lacks a lockfile.
	Drift       bool     `json:"drift"`
	DriftReason []string `json:"drift_reason,omitempty"` // one entry per drifting ecosystem
	Note        string   `json:"note,omitempty"`
}

// WorkspaceKind names the workspace layout that was detected.
type WorkspaceKind string

// Detected workspace layouts.
const (
	WorkspaceNone   WorkspaceKind = ""
	WorkspaceNPM    WorkspaceKind = "npm"     // root package.json with "workspaces"
	WorkspacePNPM   WorkspaceKind = "pnpm"    // pnpm-workspace.yaml
	WorkspaceLerna  WorkspaceKind = "lerna"   // lerna.json
	WorkspaceYarn   WorkspaceKind = "yarn"    // root package.json with "workspaces" plus yarn.lock
	WorkspaceGoWork WorkspaceKind = "go-work" // go.work
	WorkspaceCargo  WorkspaceKind = "cargo"   // root Cargo.toml with [workspace]
)

// Workspace describes a detected monorepo root.
type Workspace struct {
	// Kind is the layout that identified this repository as a workspace.
	Kind WorkspaceKind `json:"kind"`
	// Manifest is the repo-relative path to the file that proved it, for
	// example "pnpm-workspace.yaml". Empty when Kind is empty.
	Manifest string `json:"manifest,omitempty"`
	// Packages is one entry per detected package, sorted by path ascending so
	// output is byte-stable.
	Packages []PackageHealth `json:"packages"`
}

// Detected reports whether a workspace layout was found.
func (w *Workspace) Detected() bool {
	return w != nil && w.Kind != WorkspaceNone && len(w.Packages) > 0
}

// PackageHealth is the deterministic score for one workspace package.
//
// A package score is computed from that package's own files and manifests. It
// is not a share of the repository score: a small package with a clean
// structure can legitimately score higher than a large one that does not.
//
// Git-derived components are deliberately absent. Commit cadence, authorship,
// and bus factor are properties of the repository, not of a directory, and
// attributing them to a package would mean inventing numbers. The consequence
// is stated on the PackageHealth itself via GitApplicable.
type PackageHealth struct {
	// Path is the package directory, repo-relative and slash-separated, for
	// example "apps/web".
	Path string `json:"path"`
	// Manifest is the repo-relative manifest that identified this package,
	// for example "apps/web/package.json".
	Manifest string `json:"manifest"`
	// Name is the package name declared in the manifest, when it has one. A
	// Go module or a Cargo member may be unnamed.
	Name string `json:"name,omitempty"`
	// Ecosystem is the dependency manager: "npm", "gomod", "cargo", or
	// "python". Empty when the package declares no dependencies.
	Ecosystem string `json:"ecosystem,omitempty"`
	// Health is the package's own 0..100 score and component breakdown.
	Health Health `json:"health"`
	// Files, SourceFiles, TestFiles, and CodeLines are the package's counts.
	Files       int `json:"files"`
	SourceFiles int `json:"source_files"`
	TestFiles   int `json:"test_files"`
	CodeLines   int `json:"code_lines"`
	// TestFileRatio is TestFiles / (TestFiles + SourceFiles), 0..1.
	TestFileRatio float64 `json:"test_file_ratio"`
	// Hotspots are the package's own hotspot candidates, ranked worst first.
	Hotspots []Hotspot `json:"hotspots"`
	// Risks are the package's own risks, ranked by severity then impact.
	Risks []Risk `json:"risks"`
	// Dependencies is the package's own dependency surface.
	Dependencies DependencyStats `json:"dependencies"`
	// GitApplicable is false because git history cannot be attributed to a
	// package. It is stated explicitly rather than left implicit so a consumer
	// does not read the package score as covering all three dimensions.
	GitApplicable bool `json:"git_applicable"`
	// Attribution risks are the repository-level risks whose subject lies
	// inside this package. They are the risks a maintainer fixing this
	// package should look at first.
	Attribution []string `json:"attribution,omitempty"`
}

// Snapshot is the complete, serializable result of one `lensyxe analyze` run.
type Snapshot struct {
	SchemaVersion string          `json:"schema_version"`
	Tool          string          `json:"tool"`
	Version       string          `json:"version"`
	Root          string          `json:"root"`
	GeneratedAt   time.Time       `json:"generated_at"`
	DurationMS    int64           `json:"duration_ms"`
	Code          CodeStats       `json:"code"`
	Git           GitStats        `json:"git"`
	Dependencies  DependencyStats `json:"dependencies"`
	Health        Health          `json:"health"`
	Findings      []Finding       `json:"findings"` // sorted by severity desc, then title asc
	Risks         []Risk          `json:"risks"`    // sorted by severity, then impact desc, then id asc
	// Workspace is non-nil only when a monorepo layout was detected and the
	// per-package breakdown was requested. It is omitted rather than emitted
	// empty so a single-repository consumer can test for its presence.
	Workspace *Workspace `json:"workspace,omitempty"`
}

// Grade maps a 0..100 score to a letter grade using fixed, inclusive
// thresholds. Deterministic by construction.
func Grade(score float64) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 80:
		return "B"
	case score >= 70:
		return "C"
	case score >= 60:
		return "D"
	default:
		return "F"
	}
}
