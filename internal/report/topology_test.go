package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/internal/topology"
)

func topoResult() *TopologyResult {
	when := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	return &TopologyResult{
		Root: "/repo",
		Now:  when.Format(time.RFC3339),
		Bus: topology.BusFactorResult{
			Root: "/repo", Commits: 40,
			FirstCommitAt: when.AddDate(0, -8, 0),
			LastCommitAt:  when,
			Authors: []topology.Ownership{
				{Author: "ann", Commits: 25, WeightedShare: 0.8, RawShare: 0.62,
					LastCommit: when.AddDate(0, 0, -200)},
				{Author: "bob", Commits: 7, WeightedShare: 0.2, RawShare: 0.38,
					LastCommit: when.AddDate(0, 0, -10)},
			},
			StaleFiles: []topology.FileOwnership{{
				Path: "internal/old/thing.go", Commits: 4,
				Dominant:      topology.Ownership{Author: "ann", WeightedShare: 1},
				DominantShare: 1, BusFactor: 1, AtRisk: true,
				RiskReason:   "ann holds 100% of the weighted history and has not committed here in 200 days",
				InactiveDays: 200,
			}},
			Directories: []topology.DirectoryOwnership{{
				Path: "internal", Files: 12, Commits: 30,
				Dominant:      topology.Ownership{Author: "ann", WeightedShare: 0.9},
				DominantShare: 0.9, BusFactor: 1,
			}},
		},
		Coupling: topology.CouplingResult{
			Root: "/repo", Commits: 40,
			HiddenPairs: []topology.CouplingPair{{
				A: "pkg/a/a.go", B: "pkg/b/b.go",
				SharedCommits: 9, CommitsA: 12, CommitsB: 11, Coupling: 0.75,
				SameBoundary: true,
			}},
			HiddenFiles: []string{"pkg/a/a.go", "pkg/b/b.go"},
		},
		Graph: topology.Graph{
			Module: "example.com/m", Files: 130,
			Boundaries: []topology.Boundary{
				{Name: "cmd", Files: 16, Packages: 1},
				{Name: "internal", Files: 92, Packages: 24, DependsOn: []string{"pkg"}},
			},
			Violations: []topology.Violation{{
				Kind: topology.ViolationCmdImported, From: "internal", To: "cmd",
				Package: "example.com/m/internal/lib", Imported: "example.com/m/cmd/tool",
				Detail: "a non-command package imports example.com/m/cmd/tool",
			}},
		},
	}
}

// The counts in the header were transposed once -- 130 files printed as
// "package(s)" -- because nothing asserted the labels.
func TestBoundaryHeaderLabelsMatchTheNumbers(t *testing.T) {
	var b bytes.Buffer
	res := topoResult()
	if err := RenderTopology(&b, res, "table", []string{"boundary"}); err != nil {
		t.Fatalf("RenderTopology: %v", err)
	}
	out := b.String()

	if !strings.Contains(out, "25 package(s)") {
		t.Errorf("package count missing or wrong:\n%s", out)
	}
	if !strings.Contains(out, "130 file(s)") {
		t.Errorf("file count missing or wrong:\n%s", out)
	}
	if !strings.Contains(out, "2 region(s)") {
		t.Errorf("region count missing or wrong:\n%s", out)
	}
}

func TestRenderTopologyShowsAllThreeSections(t *testing.T) {
	var b bytes.Buffer
	if err := RenderTopology(&b, topoResult(), "table", nil); err != nil {
		t.Fatalf("RenderTopology: %v", err)
	}
	out := b.String()
	for _, want := range []string{
		"ARCHITECTURE BOUNDARIES",
		"⚠ ARCHITECTURE BOUNDARY VIOLATIONS",
		"🔴 HIGH BUS FACTOR RISK MODULES",
		"🔗 HIDDEN CO-CHANGE COUPLED FILE PAIRS",
		"internal/old/thing.go", // the at-risk file
		"pkg/a/a.go",            // the hidden pair
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

// A selector must restrict the output to its own section.
func TestSelectorsRestrictTheOutput(t *testing.T) {
	cases := map[string]struct {
		only    []string
		absent  string
		present string
	}{
		"bus only":        {[]string{"bus"}, "HIDDEN CO-CHANGE", "HIGH BUS FACTOR"},
		"temporal only":   {[]string{"temporal"}, "HIGH BUS FACTOR", "HIDDEN CO-CHANGE"},
		"boundaries only": {[]string{"boundary"}, "HIGH BUS FACTOR", "ARCHITECTURE BOUNDARIES"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var b bytes.Buffer
			if err := RenderTopology(&b, topoResult(), "table", c.only); err != nil {
				t.Fatalf("RenderTopology: %v", err)
			}
			out := b.String()
			if !strings.Contains(out, c.present) {
				t.Errorf("selected section %q missing:\n%s", c.present, out)
			}
			if strings.Contains(out, c.absent) {
				t.Errorf("unselected section %q was rendered:\n%s", c.absent, out)
			}
		})
	}
}

func TestJSONFormatEmitsOnlySelectedSections(t *testing.T) {
	var b bytes.Buffer
	if err := RenderTopology(&b, topoResult(), "json", []string{"bus"}); err != nil {
		t.Fatalf("RenderTopology: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, b.String())
	}
	if _, ok := got["bus_factor"]; !ok {
		t.Error("bus_factor missing from JSON output")
	}
	for _, absent := range []string{"coupling", "graph"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%q was emitted although not selected", absent)
		}
	}
}

func TestJSONWithNoSelectorEmitsEverything(t *testing.T) {
	var b bytes.Buffer
	if err := RenderTopology(&b, topoResult(), "json", nil); err != nil {
		t.Fatalf("RenderTopology: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	for _, want := range []string{"root", "now", "bus_factor", "coupling", "graph"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%q missing from JSON output", want)
		}
	}
}

func TestUnknownFormatIsRejected(t *testing.T) {
	var b bytes.Buffer
	if err := RenderTopology(&b, topoResult(), "yaml", nil); err == nil {
		t.Error("an unknown format was accepted")
	}
}

func TestRenderTopologyRejectsNil(t *testing.T) {
	var b bytes.Buffer
	if err := RenderTopology(&b, nil, "table", nil); err == nil {
		t.Error("RenderTopology(nil) returned no error")
	}
}

// A gate message must reach the output. A withheld verdict that never says so
// reads as a clean result.
func TestInsufficientHistoryReachesTheOutput(t *testing.T) {
	res := topoResult()
	res.Bus.InsufficientHistory = "history spans 0 day(s); a limit of the data, not a finding."

	var b bytes.Buffer
	if err := RenderTopology(&b, res, "table", []string{"bus"}); err != nil {
		t.Fatalf("RenderTopology: %v", err)
	}
	if !strings.Contains(b.String(), "limit of the data") {
		t.Errorf("the gate message was swallowed:\n%s", b.String())
	}
}

// The weightings must both be shown, since their difference is the finding.
func TestBothWeightedAndRawSharesAreShown(t *testing.T) {
	var b bytes.Buffer
	if err := RenderTopology(&b, topoResult(), "table", []string{"bus"}); err != nil {
		t.Fatalf("RenderTopology: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "80%") || !strings.Contains(out, "62%") {
		t.Errorf("weighted and raw shares are not both visible:\n%s", out)
	}
}
