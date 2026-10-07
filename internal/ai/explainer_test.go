package ai

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

// snapshot builds a realistic snapshot for prompt tests.
func snapshot() *models.Snapshot {
	return &models.Snapshot{
		SchemaVersion: models.SchemaVersion,
		Tool:          "lensyxe",
		Version:       "0.3.0",
		Root:          "/repo",
		GeneratedAt:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Health: models.Health{
			Score: 82.5, Grade: "B", Summary: "fine",
			Metrics: []models.Metric{
				{Key: "code", Label: "Code health", Score: 80, Weight: 0.5714, Applicable: true, Detail: "avg 120 lines"},
				{Key: "dependency", Label: "Dependency health", Score: 86.5, Weight: 0.4286, Applicable: true, Detail: "locked"},
				{Key: "git", Label: "Maintainability (Git)", Score: 0, Weight: 0, Applicable: false},
			},
		},
		Code: models.CodeStats{
			Files: 120, SourceFiles: 100, TestFiles: 20, CodeLines: 9000,
			TestFileRatio: 0.1667,
			Hotspots: []models.Hotspot{
				{Path: "a.go", Confirmed: true},
				{Path: "b.go", Confirmed: false},
			},
		},
		Git: models.GitStats{
			IsRepository: true, WindowDays: 90, WindowCommits: 40, Authors: 5,
			BusFactor: 2, DaysSinceCommit: 3,
		},
		Dependencies: models.DependencyStats{
			Detected: true, Total: 40, Direct: 6, Dev: 4, Indirect: 30, Locked: true,
		},
		Risks: []models.Risk{
			{ID: "r1", Severity: models.SeverityCritical, Title: "Code health below expectations",
				Detail: "code sub-score is low", Subject: "internal/a.go", Impact: 15},
			{ID: "r2", Severity: models.SeverityHigh, Title: "Complex file",
				Detail: "above the band", Subject: "internal/b.go", Impact: 7.5},
		},
	}
}

func TestResolveMissingConfiguration(t *testing.T) {
	t.Setenv(DefaultProviderEnv, "")
	t.Setenv(DefaultKeyEnv, "")

	if _, err := Resolve("", "", ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no provider must report ErrNotConfigured, got %v", err)
	}
}

// A provider without a key is not usable. Calling out anyway would turn a local
// message into a remote 401.
func TestResolveProviderWithoutKey(t *testing.T) {
	t.Setenv(DefaultProviderEnv, "openai")
	t.Setenv(DefaultKeyEnv, "")

	_, err := Resolve("", "", "")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	if !strings.Contains(err.Error(), DefaultKeyEnv) {
		t.Errorf("the error must name the missing variable, got %q", err)
	}
}

func TestResolveUnknownProvider(t *testing.T) {
	t.Setenv(DefaultKeyEnv, "sk-test")
	t.Setenv(DefaultProviderEnv, "")

	_, err := Resolve("not-a-provider", "", "")
	if err == nil || !strings.Contains(err.Error(), "not-a-provider") {
		t.Fatalf("err = %v, want an unknown-provider error", err)
	}
	if errors.Is(err, ErrNotConfigured) {
		t.Error("an unknown provider is a mistake, not an unconfigured state")
	}
}

func TestResolveFromEnvironment(t *testing.T) {
	t.Setenv(DefaultKeyEnv, "sk-test")
	t.Setenv(DefaultProviderEnv, "")
	t.Setenv(DefaultModelEnv, "")

	cfg, err := Resolve("OpenRouter", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.Provider != ProviderOpenRouter {
		t.Errorf("Provider = %q", cfg.Provider)
	}
	if cfg.APIKey != "sk-test" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
	// With no model configured, the provider default applies.
	if cfg.Model != providers[ProviderOpenRouter].defaultModel {
		t.Errorf("Model = %q, want the provider default", cfg.Model)
	}
}

func TestResolveModelPrecedence(t *testing.T) {
	t.Setenv(DefaultKeyEnv, "sk-test")
	t.Setenv(DefaultProviderEnv, "")
	t.Setenv(DefaultModelEnv, "env-model")

	// Config (or flag) beats the environment default, and the environment
	// beats the provider's own default. This is the same precedence every
	// other setting uses: most specific wins.
	cfg, err := Resolve("openai", "config-model", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "config-model" {
		t.Errorf("Model = %q, want the configured model to win", cfg.Model)
	}

	t.Setenv(DefaultProviderEnv, "gemini")
	cfg, err = Resolve("openai", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != ProviderOpenAI {
		t.Errorf("Provider = %q, want the explicit provider to win over the environment", cfg.Provider)
	}

	t.Setenv(DefaultModelEnv, "")
	cfg, err = Resolve("openai", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != providers[ProviderOpenAI].defaultModel {
		t.Errorf("Model = %q, want the provider default", cfg.Model)
	}
}

// A custom key variable name is honored, so a shared machine need not use the
// default name.
func TestResolveCustomKeyEnv(t *testing.T) {
	t.Setenv(DefaultKeyEnv, "")
	t.Setenv("ACME_TOKEN", "custom-key")
	t.Setenv(DefaultProviderEnv, "")

	if _, err := Resolve("openai", "", DefaultKeyEnv); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("the default variable must not be consulted when empty, got %v", err)
	}
	cfg, err := Resolve("openai", "", "ACME_TOKEN")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cfg.APIKey != "custom-key" {
		t.Errorf("APIKey = %q", cfg.APIKey)
	}
}
