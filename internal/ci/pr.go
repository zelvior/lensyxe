// This file implements the pull-request comment generator.
//
// A note on what this file will and will not print. Lensyxe measures code
// structure, dependency surface, git history, and a test-to-code *file ratio*.
// It does not execute the build and it does not instrument tests, so it has no
// build duration and no line-coverage figure. Those rows are rendered as
// "not measured" rather than filled with a plausible-looking number. A CI
// comment that invents a metric is worse than no comment, because someone will
// act on it.
package ci

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/zelvior/lensyxe/pkg/models"
)

// CommentMarker identifies Lensyxe's own comment so a later run can update it
// instead of posting a new one.
const CommentMarker = "<!-- lensyxe:engineering-impact -->"

// CommentTitle is the heading of the generated comment.
const CommentTitle = "🔍 Lensyxe Engineering Impact"

// NotMeasured is the placeholder for a metric Lensyxe does not collect.
const NotMeasured = "—"

// Environment variables Lensyxe reads from GitHub Actions.
//
// GITHUB_TOKEN and GITHUB_REF are recognized but deliberately not read. The
// token is only meaningful to a client that calls the API, and Lensyxe does
// not: posting the comment is the Action's job. Loading a credential into
// memory that nothing consumes buys nothing and widens the blast radius if the
// process is compromised. The Action holds the token instead.
const (
	EnvActions   = "GITHUB_ACTIONS"
	EnvEventPath = "GITHUB_EVENT_PATH"
	EnvToken     = "GITHUB_TOKEN" // recognized, never read; see above
	EnvRef       = "GITHUB_REF"   // recognized, never read; see above
)

// EventContext is the subset of a GitHub event payload Lensyxe reads.
type EventContext struct {
	// IsPullRequest is true for pull_request and pull_request_target events.
	IsPullRequest bool
	// Number is the pull request number.
	Number int
	// Action is the event action, for example "opened" or "synchronize".
	Action string
	// BaseRef, BaseSHA, HeadRef, HeadSHA identify the two revisions.
	BaseRef string
	BaseSHA string
	HeadRef string
	HeadSHA string
	// Repository is "owner/name".
	Repository string
}

// Environment is the CI context read from process environment variables.
//
// It is a plain struct rather than bare os.Getenv calls so every code path is
// testable without mutating the process environment.
type Environment struct {
	// IsCI is true when GITHUB_ACTIONS says so.
	IsCI bool
	// EventPath points at the event payload JSON, empty outside Actions.
	EventPath string
}

// LoadEnvironment reads the CI context from the process environment.
//
// It never returns an error: an absent GitHub environment is simply "not CI",
// which is the correct outcome for local use.
func LoadEnvironment() Environment {
	return Environment{
		IsCI:      strings.EqualFold(os.Getenv(EnvActions), "true"),
		EventPath: os.Getenv(EnvEventPath),
	}
}

// ReadEvent parses the GitHub event payload at path.
//
// A missing file is not an error: Lensyxe can still render a comment from the
// snapshot alone, it just cannot know the pull request number.
func ReadEvent(path string) (EventContext, error) {
	ctx := EventContext{}
	if path == "" {
		return ctx, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ctx, nil
		}
		return ctx, fmt.Errorf("ci: open event %s: %w", path, err)
	}
	defer f.Close()

	var payload struct {
		Action     string `json:"action"`
		Number     int    `json:"number"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		PullRequest *struct {
			Number int `json:"number"`
			Base   struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"base"`
			Head struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
		} `json:"pull_request"`
	}
	if err := json.NewDecoder(f).Decode(&payload); err != nil {
		return ctx, fmt.Errorf("ci: parse event %s: %w", path, err)
	}

	ctx.Action = payload.Action
	ctx.Repository = payload.Repository.FullName
	if payload.PullRequest != nil {
		ctx.IsPullRequest = true
		ctx.Number = payload.PullRequest.Number
		if ctx.Number == 0 {
			ctx.Number = payload.Number
		}
		ctx.BaseRef = payload.PullRequest.Base.Ref
		ctx.BaseSHA = payload.PullRequest.Base.SHA
		ctx.HeadRef = payload.PullRequest.Head.Ref
		ctx.HeadSHA = payload.PullRequest.Head.SHA
	}
	return ctx, nil
}

// Category is one row of the impact table.
type Category struct {
	// Label is the row heading.
	Label string
	// Value is the measured figure, on a 0..100 scale where applicable.
	Value float64
	// HasValue is false for a metric Lensyxe does not collect.
	HasValue bool
	// Unit describes Value, for example "" or "%".
	Unit string
	// HigherIsBetter inverts the status calculation for metrics where lower is
	// better, such as a test-to-code ratio drop.
	HigherIsBetter bool
	// Footnote explains the row when its meaning is narrower than the label.
	Footnote string
}

// Status classifies a movement for display.
type Status struct {
	Glyph string
	Label string
	// Worse ranks true when the movement is a regression, used for gating.
	Worse bool
}

// statusThreshold is the smallest absolute movement reported as a change.
// Below it, a metric is "stable": reporting noise as movement trains readers to
// ignore the table.
const statusThreshold = 0.5

// Classify turns a delta into a display status.
func Classify(delta float64, higherIsBetter bool) Status {
	if abs(delta) < statusThreshold {
		return Status{Glyph: "🟢", Label: "Stable"}
	}
	improved := delta > 0
	if !higherIsBetter {
		improved = !improved
	}
	if improved {
		return Status{Glyph: "🟢", Label: "Improved"}
	}
	// A large regression is called out separately from a small one so the
	// reader can triage.
	if abs(delta) >= 10 {
		return Status{Glyph: "🔴", Label: "Regression", Worse: true}
	}
	return Status{Glyph: "🟡", Label: "Dropped", Worse: true}
}

// Categories builds the impact table rows from a snapshot.
//
// Only measurements Lensyxe actually performs appear as rows with values.
// Build duration is listed explicitly as not measured, because a CI comment
// that omits it silently is as misleading as one that invents it.
func Categories(snap *models.Snapshot) []Category {
	cats := []Category{
		{Label: "Code", HigherIsBetter: true},
		{Label: "Dependencies", HigherIsBetter: true},
		{
			Label: "Testing", HigherIsBetter: true, Unit: "%",
			Footnote: "test-to-code file ratio, not line coverage",
		},
		{Label: "Maintainability", HigherIsBetter: true},
		{
			Label: "Build", HasValue: false,
			Footnote: "Lensyxe does not execute the build, so duration is not measured",
		},
	}

	if snap == nil {
		return cats
	}

	for _, m := range snap.Health.Metrics {
		if !m.Applicable {
			continue
		}
		switch m.Key {
		case "code":
			setValue(cats, "Code", m.Score, "")
		case "dependency":
			setValue(cats, "Dependencies", m.Score, "")
		case "git":
			setValue(cats, "Maintainability", m.Score, "")
		}
	}
	// The ratio is a fraction; the table shows it as a percentage.
	setValue(cats, "Testing", snap.Code.TestFileRatio*100, "%")

	return cats
}

// setValue fills in a row's measured value by label.
func setValue(cats []Category, label string, value float64, unit string) {
	for i := range cats {
		if cats[i].Label == label {
			cats[i].Value = value
			cats[i].HasValue = true
			cats[i].Unit = unit
		}
	}
}

// CommentOptions tune the generated comment.
type CommentOptions struct {
	// Baseline is the snapshot recorded for the base revision. Without it no
	// deltas are shown, because a delta against nothing is not a delta.
	Baseline *models.Snapshot
	// BaselineLabel describes where the baseline came from, for example a tag.
	BaselineLabel string
	// MaxRisks caps the risk list. Zero renders all of them.
	MaxRisks int
	// Event supplies pull request metadata for the provenance footer.
	Event EventContext
}

// RenderComment writes the pull-request comment body.
//
// Every number in the output comes from a snapshot field or a computed delta.
// Nothing is estimated or filled in.
func RenderComment(w io.Writer, snap *models.Snapshot, opts CommentOptions) error {
	if snap == nil {
		return fmt.Errorf("ci: render comment: nil snapshot")
	}

	var b strings.Builder
	b.WriteString(CommentMarker + "\n")
	b.WriteString("## " + CommentTitle + "\n\n")

	renderHeadline(&b, snap, opts)
	renderTable(&b, snap, opts)
	renderRisks(&b, snap, opts)
	renderProvenance(&b, snap, opts)

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("ci: render comment: %w", err)
	}
	return nil
}

// renderHeadline writes the overall health movement.
func renderHeadline(b *strings.Builder, snap *models.Snapshot, opts CommentOptions) {
	score := fmt.Sprintf("%.1f", snap.Health.Score)
	if opts.Baseline == nil {
		fmt.Fprintf(b, "**Overall Health:** `%s` (%s)\n\n", score, snap.Health.Grade)
		return
	}

	before := fmt.Sprintf("%.1f", opts.Baseline.Health.Score)
	delta := snap.Health.Score - opts.Baseline.Health.Score
	arrow := "↑"
	if delta < -statusThreshold {
		arrow = "↓"
	} else if abs(delta) < statusThreshold {
		arrow = "→"
	}
	// The arrow carries the direction, so only the magnitude follows it.
	fmt.Fprintf(b, "**Overall Health:** `%s → %s` (%s%s)\n\n",
		before, score, arrow, magnitude(delta))
}

// renderTable writes the per-category impact table.
func renderTable(b *strings.Builder, snap *models.Snapshot, opts CommentOptions) {
	b.WriteString("| Category | Score | Delta | Status |\n")
	b.WriteString("| :--- | :---: | :---: | :---: |\n")

	for _, cat := range Categories(snap) {
		before, hasBaseline := baselineValue(opts.Baseline, cat.Label)

		if !cat.HasValue {
			fmt.Fprintf(b, "| %s | %s | %s | ⚪ Not measured |\n",
				escapeCell(cat.Label), NotMeasured, NotMeasured)
			continue
		}

		value := fmt.Sprintf("%.1f%s", cat.Value, cat.Unit)
		if !hasBaseline {
			fmt.Fprintf(b, "| %s | %s | %s | ⚪ No baseline |\n",
				escapeCell(cat.Label), value, NotMeasured)
			continue
		}

		// Every measured row is rendered, including stable ones: dropping a
		// row would hide the fact that the metric was checked at all.
		delta := cat.Value - before
		st := Classify(delta, cat.HigherIsBetter)
		fmt.Fprintf(b, "| %s | %s | %s | %s %s |\n",
			escapeCell(cat.Label), value, signed(delta), st.Glyph, st.Label)
	}
	b.WriteByte('\n')

	// Footnotes for the rows whose meaning is narrower than the label.
	seen := map[string]bool{}
	for _, cat := range Categories(snap) {
		if cat.Footnote == "" || seen[cat.Label] {
			continue
		}
		seen[cat.Label] = true
		fmt.Fprintf(b, "- **%s**: %s\n", cat.Label, cat.Footnote)
	}
	if len(seen) > 0 {
		b.WriteByte('\n')
	}
}

// baselineValue reads the matching category value from the baseline snapshot.
func baselineValue(base *models.Snapshot, label string) (float64, bool) {
	if base == nil {
		return 0, false
	}
	switch label {
	case "Code":
		return metricScore(base, "code")
	case "Dependencies":
		return metricScore(base, "dependency")
	case "Maintainability":
		return metricScore(base, "git")
	case "Testing":
		return base.Code.TestFileRatio * 100, true
	}
	return 0, false
}

// metricScore returns an applicable metric's score.
func metricScore(snap *models.Snapshot, key string) (float64, bool) {
	for _, m := range snap.Health.Metrics {
		if m.Key == key && m.Applicable {
			return m.Score, true
		}
	}
	return 0, false
}

// renderRisks writes the detected risks, newest first by impact.
func renderRisks(b *strings.Builder, snap *models.Snapshot, opts CommentOptions) {
	if len(snap.Risks) == 0 {
		b.WriteString("### ✅ Detected Risks\n\nNone.\n\n")
		return
	}

	// Only risks present in the current snapshot are listed. Risks that exist
	// in the baseline but are gone are a success story and belong in the
	// provenance section, not in a "detected risks" list.
	risks := append([]models.Risk(nil), snap.Risks...)
	sort.SliceStable(risks, func(i, j int) bool {
		if risks[i].Severity.Rank() != risks[j].Severity.Rank() {
			return risks[i].Severity.Rank() > risks[j].Severity.Rank()
		}
		if risks[i].Impact != risks[j].Impact {
			return risks[i].Impact > risks[j].Impact
		}
		return risks[i].ID < risks[j].ID
	})

	limit := len(risks)
	if opts.MaxRisks > 0 && limit > opts.MaxRisks {
		limit = opts.MaxRisks
	}

	fmt.Fprintf(b, "### ⚠ Detected Risks (%d)\n\n", len(risks))
	for _, r := range risks[:limit] {
		line := fmt.Sprintf("- %s **%s**", r.Severity.Emoji(), escapeCell(r.Title))
		if r.Subject != "" {
			line += fmt.Sprintf(" — `%s`", escapeCell(r.Subject))
		}
		b.WriteString(line + "\n")
		// The first evidence line is the number that justifies the claim, so
		// the reader can check it without leaving the comment.
		if len(r.Evidence) > 0 {
			e := r.Evidence[0]
			fmt.Fprintf(b, "  - %s\n", escapeCell(e.Detail))
		}
	}
	if limit < len(risks) {
		fmt.Fprintf(b, "- _%d further risk(s) omitted; see the JSON artifact._\n",
			len(risks)-limit)
	}
	b.WriteByte('\n')
}

// renderProvenance writes the footer identifying what produced the comment.
func renderProvenance(b *strings.Builder, snap *models.Snapshot, opts CommentOptions) {
	var parts []string
	if opts.Event.Repository != "" {
		parts = append(parts, opts.Event.Repository)
	}
	if opts.Event.HeadSHA != "" {
		parts = append(parts, "head `"+shortSHA(opts.Event.HeadSHA)+"`")
	}
	if opts.BaselineLabel != "" {
		parts = append(parts, "baseline "+opts.BaselineLabel)
	} else if opts.Baseline != nil {
		parts = append(parts, "baseline "+opts.Baseline.Version)
	}
	parts = append(parts, fmt.Sprintf("lensyxe %s", snap.Version))

	b.WriteString("---\n")
	prefix := "_Generated by Lensyxe from local analysis"
	if detail := joinNonEmpty(parts, ", "); detail != "" {
		prefix += " (" + detail + ")"
	}
	b.WriteString(prefix + ". Figures come from Lensyxe's own measurements; nothing is estimated._\n")
}

// joinNonEmpty joins the non-empty parts with sep.
func joinNonEmpty(parts []string, sep string) string {
	kept := parts[:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// shortSHA abbreviates a commit hash.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// signed renders a delta with an explicit sign.
func signed(v float64) string {
	switch {
	case v > 0:
		return "+" + magnitude(v)
	case v < 0:
		return "-" + magnitude(v)
	default:
		return "0"
	}
}

// magnitude renders the absolute value of a delta without a trailing ".0".
func magnitude(v float64) string {
	// fmt's %.1f rounds halves to even, so 0.25 prints as "0.2" while a reader
	// expecting ordinary rounding reads "0.3". Round half away from zero
	// explicitly so the displayed number matches the one people compute by hand.
	r := math.Round(math.Abs(v)*10) / 10
	if r == math.Trunc(r) {
		return strconv.FormatFloat(r, 'f', 0, 64)
	}
	return strconv.FormatFloat(r, 'f', 1, 64)
}

// escapeCell protects a Markdown table cell from embedded pipes and newlines.
func escapeCell(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// LoadBaseline reads a snapshot from a JSON file, for use as the comparison
// base in CI where no local history exists.
//
// An empty path yields a nil baseline and no error.
func LoadBaseline(path string) (*models.Snapshot, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ci: open baseline %s: %w", path, err)
	}
	defer f.Close()

	var snap models.Snapshot
	if err := json.NewDecoder(f).Decode(&snap); err != nil {
		return nil, fmt.Errorf("ci: parse baseline %s: %w", path, err)
	}
	if snap.SchemaVersion == "" {
		return nil, fmt.Errorf("ci: baseline %s is not an Lensyxe snapshot", path)
	}
	return &snap, nil
}
