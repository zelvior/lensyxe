package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/pkg/models"
)

func TestAnalyzeFlagsOverrideConfig(t *testing.T) {
	dir := t.TempDir()
	body := ""
	for i := 0; i < 30; i++ {
		body += "line\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLI(t, "analyze", dir, "--format", "json", "--hotspot-threshold", "10")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	var snap models.Snapshot
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(snap.Code.Hotspots) != 1 {
		t.Errorf("expected one hotspot with --hotspot-threshold 10, got %d", len(snap.Code.Hotspots))
	}

	stdout, _, err = runCLI(t, "analyze", dir, "--format", "json", "--hotspot-threshold", "1000")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	snap = models.Snapshot{}
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(snap.Code.Hotspots) != 0 {
		t.Errorf("expected no hotspots with a high threshold, got %d", len(snap.Code.Hotspots))
	}
}

func TestConfigFileIsHonored(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "custom.yml")
	if err := os.WriteFile(cfg, []byte("hotspot_threshold: 5\ngit_window_days: 30\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	body := ""
	for i := 0; i < 10; i++ {
		body += "line\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLI(t, "analyze", dir, "--format", "json", "--config", cfg)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	var snap models.Snapshot
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("json: %v", err)
	}
	if snap.Git.WindowDays != 30 {
		t.Errorf("WindowDays = %d, want 30 from the config file", snap.Git.WindowDays)
	}
	if len(snap.Code.Hotspots) != 1 {
		t.Errorf("expected the config hotspot_threshold to apply, got %d hotspots", len(snap.Code.Hotspots))
	}
}

func TestFlagBeatsConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "custom.yml")
	if err := os.WriteFile(cfg, []byte("hotspot_threshold: 1000\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("line\nline\nline\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, _, err := runCLI(t, "analyze", dir, "--format", "json",
		"--config", cfg, "--hotspot-threshold", "2")
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	var snap models.Snapshot
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(snap.Code.Hotspots) != 1 {
		t.Error("the flag should override the config file value")
	}
}

func TestMissingConfigFileIsNotFatal(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "absent.yml")
	if _, _, err := runCLI(t, "analyze", dir, "--config", missing); err != nil {
		t.Fatalf("a missing config file must not fail the run: %v", err)
	}
}

func TestMalformedConfigFallsBackToDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "broken.yml")
	if err := os.WriteFile(cfg, []byte("hotspot_threshold: [unclosed\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout, stderr, err := runCLI(t, "analyze", dir, "--config", cfg, "--format", "json")
	if err != nil {
		t.Fatalf("a malformed config must not be fatal: %v", err)
	}
	if !strings.Contains(stderr, "ignoring config") {
		t.Errorf("expected a warning on stderr, got %q", stderr)
	}
	var snap models.Snapshot
	if err := json.Unmarshal([]byte(stdout), &snap); err != nil {
		t.Fatalf("json: %v", err)
	}
	if snap.Health.Score <= 0 {
		t.Error("expected a scored snapshot despite the broken config")
	}
}
