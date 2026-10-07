package ai

import (
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

func TestBuildPromptContainsOnlyMeasurements(t *testing.T) {
	prompt, facts := BuildPrompt(snapshot(), nil, 8)

	for _, want := range []string{
		"82.5/100",    // the score
		"Code health", // a component
		"Risk",        // risk section
		"Repository:", //
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt is missing %q:\n%s", want, prompt)
		}
	}

	// Every fact must have been registered, or the fabrication guard would
	// reject the model's own restatement of a supplied number.
	for _, n := range []string{"82.5", "80", "86.5", "9000", "120", "100", "20", "40", "6", "4", "30", "5", "2", "3", "90"} {
		if !facts[n] {
			t.Errorf("fact %q was supplied to the prompt but not recorded: %v", n, keysOf(facts))
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The prompt must never carry source code. Sending a repository to a third
// party to produce an executive summary would defeat the local-first promise.
func TestPromptNeverCarriesSource(t *testing.T) {
	snap := snapshot()
	// Even if a risk subject looks like content, only the identifier travels.
	snap.Risks[0].Subject = "internal/a.go"

	prompt, _ := BuildPrompt(snap, nil, 8)
	for _, forbidden := range []string{"func ", "package main", "import ", "<html", "<script"} {
		if strings.Contains(prompt, forbidden) {
			t.Errorf("the prompt must not contain source, found %q", forbidden)
		}
	}
}

func TestBuildPromptWithBaselineReportsTheDelta(t *testing.T) {
	base := snapshot()
	base.Health.Score = 85

	prompt, facts := BuildPrompt(snapshot(), base, 8)
	if !strings.Contains(prompt, "Change since the previous recorded run") {
		t.Errorf("a baseline must produce a delta line:\n%s", prompt)
	}
	if !strings.Contains(prompt, "declined") {
		t.Errorf("a falling score must be reported as declined:\n%s", prompt)
	}
	if !facts["2.5"] {
		t.Errorf("the delta must be a recorded fact: %v", keysOf(facts))
	}
}

func TestBuildPromptRespectsMaxRisks(t *testing.T) {
	snap := snapshot()
	for i := 0; i < 30; i++ {
		snap.Risks = append(snap.Risks, models.Risk{
			ID:       "r" + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			Severity: models.SeverityLow,
			Title:    "Filler risk " + string(rune('a'+i%26)),
			Detail:   "detail",
			Impact:   1,
		})
	}
	prompt, _ := BuildPrompt(snap, nil, 3)

	// The total is still reported in full; only the detail list is capped.
	if !strings.Contains(prompt, "Risks detected: 32 total") {
		t.Errorf("the total must be reported even when the list is capped:\n%s", prompt)
	}
	if strings.Count(prompt, "- [") != 3 {
		t.Errorf("expected three risk lines, got %d", strings.Count(prompt, "- ["))
	}
}

// A monorepo breakdown belongs in the prompt when it exists.
func TestBuildPromptIncludesWorkspace(t *testing.T) {
	snap := snapshot()
	snap.Workspace = &models.Workspace{
		Kind: models.WorkspaceNPM,
		Packages: []models.PackageHealth{
			{Path: "apps/web", Health: models.Health{Score: 91}, Files: 30},
			{Path: "packages/ui", Health: models.Health{Score: 94}, Files: 12},
		},
	}
	prompt, facts := BuildPrompt(snap, nil, 8)
	if !strings.Contains(prompt, "Workspace with 2 packages") {
		t.Errorf("workspace summary missing:\n%s", prompt)
	}
	if !strings.Contains(prompt, "apps/web: 91.0/100") {
		t.Errorf("package score missing:\n%s", prompt)
	}
	if !facts["91"] || !facts["94"] {
		t.Errorf("package scores must be recorded facts: %v", keysOf(facts))
	}
}
