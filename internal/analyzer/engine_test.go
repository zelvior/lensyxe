package analyzer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestScanProducesSnapshot(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, dir, "main_test.go", "package main\n\nfunc TestMain(t *testing.T) {}\n")
	writeFile(t, dir, "go.mod", "module demo\n\ngo 1.22\n")
	writeFile(t, dir, "go.sum", "")

	snap, err := Scan(context.Background(), DefaultConfig(dir), "test")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if snap.Tool != "lensyxe" || snap.Version != "test" {
		t.Errorf("tool/version = %q/%q", snap.Tool, snap.Version)
	}
	if snap.Root != dir {
		t.Errorf("Root = %q, want %q", snap.Root, dir)
	}
	if snap.Code.Files != 2 {
		t.Errorf("Code.Files = %d, want 2", snap.Code.Files)
	}
	if snap.Code.TestFiles != 1 || snap.Code.SourceFiles != 1 {
		t.Errorf("test/source = %d/%d, want 1/1",
			snap.Code.TestFiles, snap.Code.SourceFiles)
	}
	if snap.Health.Score < 0 || snap.Health.Score > 100 {
		t.Errorf("health score %v out of range", snap.Health.Score)
	}
	if !snap.Code.Complexity.Measured {
		t.Error("complexity should be measured by default")
	}
	// Slices must be non-nil for stable JSON output.
	if snap.Code.Languages == nil || snap.Findings == nil || snap.Risks == nil ||
		snap.Code.Hotspots == nil || snap.Code.Complexity.WorstFiles == nil {
		t.Error("slices in the snapshot must be non-nil")
	}
	if snap.SchemaVersion != "0.2" {
		t.Errorf("SchemaVersion = %q, want 0.2", snap.SchemaVersion)
	}
}

// Concurrency must not introduce nondeterminism: repeated scans of an
// unchanged tree must agree on every field except timing.
func TestScanConcurrentAnalyzersAreConsistent(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, dir, "f.go", "package p\n\nfunc f() { if a && b { for x := range y { } } }\n")
	}
	first, err := Scan(context.Background(), DefaultConfig(dir), "test")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for i := 0; i < 8; i++ {
		got, err := Scan(context.Background(), DefaultConfig(dir), "test")
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		// GeneratedAt and DurationMS are timing dependent; everything else
		// must be identical across concurrent runs.
		if !reflect.DeepEqual(got.Code, first.Code) ||
			!reflect.DeepEqual(got.Git, first.Git) ||
			!reflect.DeepEqual(got.Dependencies, first.Dependencies) ||
			got.Health.Score != first.Health.Score ||
			!reflect.DeepEqual(got.Risks, first.Risks) ||
			!reflect.DeepEqual(got.Findings, first.Findings) {
			t.Fatalf("run %d differs from the first run", i)
		}
	}
}

func TestScanFindingsAndRisksAreSorted(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\nfunc main() {}\n")
	writeFile(t, dir, "package.json", `{"dependencies":{"a":"1"}}`)

	snap, err := Scan(context.Background(), DefaultConfig(dir), "test")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for i := 1; i < len(snap.Findings); i++ {
		if snap.Findings[i].Severity.Rank() > snap.Findings[i-1].Severity.Rank() {
			t.Fatalf("findings are not ordered by severity: %+v", snap.Findings)
		}
	}
	for i := 1; i < len(snap.Risks); i++ {
		prev, cur := snap.Risks[i-1], snap.Risks[i]
		if prev.Severity.Rank() < cur.Severity.Rank() {
			t.Fatalf("risks not ordered by severity: %+v", snap.Risks)
		}
		if prev.Severity == cur.Severity && prev.Impact < cur.Impact {
			t.Fatalf("risks not ordered by impact: %+v", snap.Risks)
		}
		if prev.Severity == cur.Severity && prev.Impact == cur.Impact && prev.ID > cur.ID {
			t.Fatalf("risk order is not total (IDs out of order): %q then %q", prev.ID, cur.ID)
		}
	}
}

func TestScanHonorsHotspotThreshold(t *testing.T) {
	dir := t.TempDir()
	body := ""
	for i := 0; i < 60; i++ {
		body += "line\n"
	}
	writeFile(t, dir, "a.go", body)

	strict := DefaultConfig(dir)
	strict.HotspotThreshold = 10
	snap, err := Scan(context.Background(), strict, "test")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(snap.Code.Hotspots) == 0 {
		t.Error("expected a hotspot with threshold=10")
	}
	// A 60-line file is a candidate but can never be confirmed: it is under
	// the rule's 500-line size bar.
	for _, h := range snap.Code.Hotspots {
		if h.Confirmed {
			t.Errorf("%s confirmed despite being under the 500-line rule threshold", h.Path)
		}
	}

	loose := DefaultConfig(dir)
	loose.HotspotThreshold = 1000
	snap2, err := Scan(context.Background(), loose, "test")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(snap2.Code.Hotspots) != 0 {
		t.Errorf("expected no hotspots with a high threshold, got %d", len(snap2.Code.Hotspots))
	}
}

// The churn join must not leave duplicate code findings behind: the pre-join
// size-only findings are replaced by the churn-aware ones.
func TestScanDoesNotDuplicateCodeFindings(t *testing.T) {
	dir := t.TempDir()
	body := ""
	for i := 0; i < 60; i++ {
		body += "line\n"
	}
	writeFile(t, dir, "big.go", body)

	// The default 400-line threshold would leave this file below candidacy,
	// so lower it to get a hotspot at all.
	cfg := DefaultConfig(dir)
	cfg.HotspotThreshold = 10
	snap, err := Scan(context.Background(), cfg, "test")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	seen := map[string]int{}
	for _, f := range snap.Findings {
		seen[f.Title+"|"+f.Subject]++
	}
	for key, n := range seen {
		if n > 1 {
			t.Errorf("finding %q appears %d times", key, n)
		}
	}
	if len(snap.Code.Hotspots) != 1 {
		t.Fatalf("hotspots = %d, want 1", len(snap.Code.Hotspots))
	}
}

// Without git data, churn is unknown, so nothing can be confirmed. The snapshot
// must still be complete rather than partially populated.
func TestScanNonGitDirectoryStillScores(t *testing.T) {
	dir := t.TempDir()
	body := ""
	for i := 0; i < 600; i++ {
		body += "if a && b { for x := range y { } }\n"
	}
	writeFile(t, dir, "big.go", body)

	snap, err := Scan(context.Background(), DefaultConfig(dir), "test")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if snap.Git.IsRepository {
		t.Error("IsRepository should be false")
	}
	for _, h := range snap.Code.Hotspots {
		if h.Confirmed {
			t.Errorf("%s confirmed without git data", h.Path)
		}
		if h.Churn != 0 {
			t.Errorf("%s reports churn %d without a repository", h.Path, h.Churn)
		}
	}
	// Git is not applicable, so the health score is judged on code alone.
	if snap.Health.Components != 2 {
		t.Errorf("Components = %d, want 2 (code + dependency)", snap.Health.Components)
	}
	if snap.Health.Score <= 0 {
		t.Error("expected a positive score for code-only analysis")
	}
}

func TestValidateRejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"zero window", Config{Target: ".", GitWindowDays: 0, HotspotThreshold: 10}},
		{"negative window", Config{Target: ".", GitWindowDays: -1, HotspotThreshold: 10}},
		{"zero hotspot", Config{Target: ".", GitWindowDays: 30, HotspotThreshold: 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.cfg.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestValidateMakesTargetAbsolute(t *testing.T) {
	cfg := Config{Target: ".", GitWindowDays: 30, HotspotThreshold: 100}
	got, err := cfg.Validate()
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !filepath.IsAbs(got.Target) {
		t.Errorf("Target = %q, want an absolute path", got.Target)
	}
}

func TestScanMissingDirectoryFails(t *testing.T) {
	cfg := DefaultConfig(filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := Scan(context.Background(), cfg, "test"); err == nil {
		t.Fatal("expected an error for a nonexistent target")
	}
}

func TestScanRespectsContextCancellation(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Scan(ctx, DefaultConfig(dir), "test"); err == nil {
		t.Fatal("expected an error for a cancelled context")
	}
}

// Every risk in a full snapshot must carry evidence, since that is the
// contract the risk engine advertises.
func TestScanRisksCarryEvidence(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "main.go", "package main\nfunc main() {}\n")
	writeFile(t, dir, "package.json", `{"dependencies":{"a":"1"}}`)

	snap, err := Scan(context.Background(), DefaultConfig(dir), "test")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, r := range snap.Risks {
		if len(r.Evidence) == 0 {
			t.Errorf("risk %q has no evidence", r.ID)
		}
		if r.ID == "" {
			t.Errorf("risk %q has no ID", r.Title)
		}
	}
	// With no tests present, the testing risk must be present and evidence-backed.
	found := false
	for _, r := range snap.Risks {
		if strings.HasPrefix(r.ID, "testing.") {
			found = true
		}
	}
	if !found {
		t.Error("expected a testing risk for a repository with no tests")
	}
}

// Disabling complexity must propagate from the scan config to the code analyzer.
func TestScanPropagatesComplexityToggle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.go", "package p\nfunc f() { if a { if b { } } }\n")

	cfg := DefaultConfig(dir)
	cfg.EnableComplexity = false
	snap, err := Scan(context.Background(), cfg, "test")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if snap.Code.Complexity.Measured {
		t.Error("complexity should be off when disabled on the scan config")
	}
}
