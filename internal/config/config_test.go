package config

import (
	"os"
	"path/filepath"
	"testing"
)

// write writes a config file into dir and returns its path.
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestDefaultValues(t *testing.T) {
	cfg := Default(t.TempDir())
	if cfg.GitWindowDays != 90 {
		t.Errorf("GitWindowDays = %d", cfg.GitWindowDays)
	}
	if cfg.HotspotThreshold != 400 {
		t.Errorf("HotspotThreshold = %d", cfg.HotspotThreshold)
	}
	if !cfg.EnableComplexity {
		t.Error("complexity should be on by default")
	}
	if cfg.HistoryLimit != 10 {
		t.Errorf("HistoryLimit = %d", cfg.HistoryLimit)
	}
	if cfg.Watch.DebounceMS != 250 {
		t.Errorf("Watch.DebounceMS = %d", cfg.Watch.DebounceMS)
	}
	if cfg.Watch.IntervalSeconds != 0 {
		t.Errorf("Watch.IntervalSeconds = %d, want 0 (change-only)", cfg.Watch.IntervalSeconds)
	}
	if filepath.Base(cfg.DatabasePath) != "history.db" {
		t.Errorf("DatabasePath = %q", cfg.DatabasePath)
	}
	// No thresholds means the gate stage is skipped entirely.
	if cfg.Thresholds != NewThresholds() {
		t.Errorf("Thresholds = %+v, want all unset", cfg.Thresholds)
	}
	if cfg.Thresholds.IsSet(float64(cfg.Thresholds.MaxRiskCount)) {
		t.Error("MaxRiskCount must start unset so an explicit 0 stays meaningful")
	}
}

// An explicit zero in the config must be honored as a real bound, not mistaken
// for "not configured".
func TestLoadHonorsZeroThresholds(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".lensyxe.yml", "max_risk_count: 0\nmax_hotspots: 0\n")
	cfg, _, err := Load("", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Thresholds.IsSet(float64(cfg.Thresholds.MaxRiskCount)) {
		t.Error("max_risk_count: 0 must be recognized as configured")
	}
	if !cfg.Thresholds.IsSet(float64(cfg.Thresholds.MaxHotspots)) {
		t.Error("max_hotspots: 0 must be recognized as configured")
	}
	// An omitted key stays unset.
	if cfg.Thresholds.IsSet(cfg.Thresholds.MinHealthScore) {
		t.Error("an omitted threshold must remain unset")
	}
}

// Both extensions must be discovered, since YAML permits either.
func TestLoadDiscoversBothExtensions(t *testing.T) {
	for _, name := range []string{".lensyxe.yml", ".lensyxe.yaml"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, name, "hotspot_threshold: 123\n")
			cfg, warnings, err := Load("", dir)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if len(warnings) != 0 {
				t.Fatalf("warnings = %v", warnings)
			}
			if cfg.HotspotThreshold != 123 {
				t.Errorf("HotspotThreshold = %d, want 123", cfg.HotspotThreshold)
			}
		})
	}
}

func TestLoadMergesOverDefaults(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".lensyxe.yml", `
git_window_days: 30
hotspot_threshold: 250
ignore_dirs:
  - generated
max_file_bytes: 1024
enable_complexity: false
history_limit: 25
database_path: custom.db
watch_debounce_ms: 500
watch_interval_seconds: 15
`)
	cfg, _, err := Load("", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitWindowDays != 30 {
		t.Errorf("GitWindowDays = %d", cfg.GitWindowDays)
	}
	if cfg.HotspotThreshold != 250 {
		t.Errorf("HotspotThreshold = %d", cfg.HotspotThreshold)
	}
	if len(cfg.IgnoreDirs) != 1 || cfg.IgnoreDirs[0] != "generated" {
		t.Errorf("IgnoreDirs = %v", cfg.IgnoreDirs)
	}
	if cfg.MaxFileBytes != 1024 {
		t.Errorf("MaxFileBytes = %d", cfg.MaxFileBytes)
	}
	if cfg.EnableComplexity {
		t.Error("enable_complexity: false must be honored")
	}
	if cfg.HistoryLimit != 25 {
		t.Errorf("HistoryLimit = %d", cfg.HistoryLimit)
	}
	if cfg.DatabasePath != "custom.db" {
		t.Errorf("DatabasePath = %q", cfg.DatabasePath)
	}
	if cfg.Watch.DebounceMS != 500 {
		t.Errorf("Watch.DebounceMS = %d", cfg.Watch.DebounceMS)
	}
	if cfg.Watch.IntervalSeconds != 15 {
		t.Errorf("Watch.IntervalSeconds = %d", cfg.Watch.IntervalSeconds)
	}
}

func TestLoadThresholds(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".lensyxe.yml", `
min_health_score: 70
max_health_drop: 5
max_complexity_increase: 1.5
max_risk_count: 20
max_hotspots: 3
fail_on_drift: true
require_tests: true
`)
	cfg, _, err := Load("", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	th := cfg.Thresholds
	if th.MinHealthScore != 70 {
		t.Errorf("MinHealthScore = %v", th.MinHealthScore)
	}
	if th.MaxHealthDrop != 5 {
		t.Errorf("MaxHealthDrop = %v", th.MaxHealthDrop)
	}
	if th.MaxComplexityIncrease != 1.5 {
		t.Errorf("MaxComplexityIncrease = %v", th.MaxComplexityIncrease)
	}
	if th.MaxRiskCount != 20 {
		t.Errorf("MaxRiskCount = %d", th.MaxRiskCount)
	}
	if th.MaxHotspots != 3 {
		t.Errorf("MaxHotspots = %d", th.MaxHotspots)
	}
	if !th.FailOnDrift {
		t.Error("FailOnDrift must be honored")
	}
	if !th.RequireTests {
		t.Error("RequireTests must be honored")
	}
}

// A threshold written as a YAML integer must coerce to a float, which is the
// common way anyone writes min_health_score: 70
func TestLoadCoercesIntegerThresholds(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".lensyxe.yml", "min_health_score: 70\nmax_health_drop: 3\n")
	cfg, _, err := Load("", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Thresholds.MinHealthScore != 70 {
		t.Errorf("MinHealthScore = %v, want 70", cfg.Thresholds.MinHealthScore)
	}
	if cfg.Thresholds.MaxHealthDrop != 3 {
		t.Errorf("MaxHealthDrop = %v, want 3", cfg.Thresholds.MaxHealthDrop)
	}
}

func TestLoadExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "custom.yml", "hotspot_threshold: 99\n")
	cfg, _, err := Load(path, t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HotspotThreshold != 99 {
		t.Errorf("HotspotThreshold = %d, want 99", cfg.HotspotThreshold)
	}
}

// An explicit path that does not exist warns but never fails: Lensyxe stays
// usable with built-in defaults, which matters in CI and scripts.
func TestLoadExplicitMissingPathWarns(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yml")
	cfg, warnings, err := Load(missing, t.TempDir())
	if err != nil {
		t.Fatalf("a missing config must not fail the run: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if cfg.HotspotThreshold != 400 {
		t.Errorf("defaults must survive, got %d", cfg.HotspotThreshold)
	}
}

func TestLoadExplicitMalformedPathWarns(t *testing.T) {
	path := write(t, t.TempDir(), "broken.yml", "hotspot_threshold: [unclosed\n")
	cfg, warnings, err := Load(path, t.TempDir())
	if err != nil {
		t.Fatalf("a malformed config must not fail the run: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if cfg.HotspotThreshold != 400 {
		t.Errorf("defaults must survive, got %d", cfg.HotspotThreshold)
	}
}

func TestLoadMalformedFileWarnsButSucceeds(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".lensyxe.yml", "hotspot_threshold: [unclosed\n")
	cfg, warnings, err := Load("", dir)
	if err != nil {
		t.Fatalf("a malformed config must not be fatal: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("expected a warning about the malformed config")
	}
	// Defaults must survive.
	if cfg.HotspotThreshold != 400 {
		t.Errorf("HotspotThreshold = %d, want the default 400", cfg.HotspotThreshold)
	}
}

// Absence is normal during auto-discovery and must produce no warning.
func TestLoadNoFileIsSilent(t *testing.T) {
	cfg, warnings, err := Load("", t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if cfg.HotspotThreshold != 400 {
		t.Errorf("HotspotThreshold = %d, want the default", cfg.HotspotThreshold)
	}
}

// Discovery walks up from the working directory.
func TestLoadSearchesParentDirectory(t *testing.T) {
	parent := t.TempDir()
	write(t, parent, ".lensyxe.yml", "hotspot_threshold: 321\n")
	child := filepath.Join(parent, "sub", "deeper")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg, _, err := Load("", child)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HotspotThreshold != 321 {
		t.Errorf("HotspotThreshold = %d, want 321 from the parent", cfg.HotspotThreshold)
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".lensyxe.yml", "git_window_days: 30\n")
	t.Setenv("LENSYXE_GIT_WINDOW_DAYS", "365")

	cfg, _, err := Load("", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitWindowDays != 365 {
		t.Errorf("GitWindowDays = %d, want 365 from the environment", cfg.GitWindowDays)
	}
}

// An environment override must work when there is no config file at all.
//
// This is the case that was broken. Load returned early on
// viper.ConfigFileNotFoundError, before apply ran, so the environment was never
// consulted. The pre-existing TestEnvironmentOverridesFile always wrote a
// config file first, so it passed and the bug survived.
//
// The failure mode was silent and consequential: a threshold set through the
// environment looked configured, produced no error, and gated nothing. For a CI
// gate that is the worst of the three outcomes.
func TestEnvironmentOverridesWithoutAnyConfigFile(t *testing.T) {
	dir := t.TempDir()
	// Sanity check the fixture: there must be no config file here, or this
	// test would silently degrade into the one above.
	cfg, _, err := Load("", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitWindowDays != Default(dir).GitWindowDays {
		t.Fatalf("the fixture already has config; the test would prove nothing")
	}

	t.Setenv("LENSYXE_GIT_WINDOW_DAYS", "365")
	t.Setenv("LENSYXE_MIN_HEALTH_SCORE", "88")
	t.Setenv("LENSYXE_HOTSPOT_THRESHOLD", "123")

	cfg, _, err = Load("", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GitWindowDays != 365 {
		t.Errorf("GitWindowDays = %d, want 365", cfg.GitWindowDays)
	}
	if cfg.Thresholds.MinHealthScore != 88 {
		t.Errorf("MinHealthScore = %v, want 88", cfg.Thresholds.MinHealthScore)
	}
	if cfg.HotspotThreshold != 123 {
		t.Errorf("HotspotThreshold = %d, want 123", cfg.HotspotThreshold)
	}
}

// A missing config file is not a warning. It is the normal state of a
// repository that has not been configured yet, and warning about it would train
// users to ignore warnings.
func TestMissingConfigFileIsNotWarned(t *testing.T) {
	_, warnings, err := Load("", t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("a missing config produced warnings: %v", warnings)
	}
}

// Every documented key must resolve from the environment.
//
// configKeys is a literal list because it is a public contract, and a literal
// list can silently drift from the keys the loader actually reads. This test
// closes that gap by requiring every key to be observable through its
// environment variable.
func TestEveryConfigKeyIsEnvironmentBound(t *testing.T) {
	dir := t.TempDir()

	// One representative per type, since the point is that each key is bound,
	// not that a particular value round-trips.
	t.Setenv("LENSYXE_GIT_WINDOW_DAYS", "7")
	t.Setenv("LENSYXE_HOTSPOT_THRESHOLD", "11")
	t.Setenv("LENSYXE_ENABLE_COMPLEXITY", "false")
	t.Setenv("LENSYXE_DETECT_WORKSPACE", "true")
	t.Setenv("LENSYXE_HISTORY_LIMIT", "3")
	t.Setenv("LENSYXE_WATCH_DEBOUNCE_MS", "77")
	t.Setenv("LENSYXE_AI_PROVIDER", "openai")
	t.Setenv("LENSYXE_AI_KEY_ENV", "MY_KEY_VAR")
	t.Setenv("LENSYXE_DATABASE_PATH", "/tmp/x.db")
	t.Setenv("LENSYXE_COMPARE_ROOT", "/tmp/cmp")
	t.Setenv("LENSYXE_MAX_HOTSPOTS", "1")
	t.Setenv("LENSYXE_MAX_RISK_COUNT", "2")
	t.Setenv("LENSYXE_MIN_HEALTH_SCORE", "70")
	t.Setenv("LENSYXE_MAX_HEALTH_DROP", "3")
	t.Setenv("LENSYXE_MAX_COMPLEXITY_INCREASE", "4")
	t.Setenv("LENSYXE_REQUIRE_TESTS", "true")
	t.Setenv("LENSYXE_FAIL_ON_DRIFT", "true")
	t.Setenv("LENSYXE_EXPLAIN", "true")

	cfg, _, err := Load("", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	checks := []struct {
		name string
		got  interface{}
		want interface{}
	}{
		{"GitWindowDays", cfg.GitWindowDays, 7},
		{"HotspotThreshold", cfg.HotspotThreshold, 11},
		{"EnableComplexity", cfg.EnableComplexity, false},
		{"DetectWorkspace", cfg.DetectWorkspace, true},
		{"HistoryLimit", cfg.HistoryLimit, 3},
		{"WatchDebounceMS", cfg.Watch.DebounceMS, 77},
		{"AIProvider", cfg.AIProvider, "openai"},
		{"AIKeyEnv", cfg.AIKeyEnv, "MY_KEY_VAR"},
		{"DatabasePath", cfg.DatabasePath, "/tmp/x.db"},
		{"CompareRoot", cfg.CompareRoot, "/tmp/cmp"},
		{"Thresholds.MinHealthScore", cfg.Thresholds.MinHealthScore, float64(70)},
		{"Thresholds.MaxHealthDrop", cfg.Thresholds.MaxHealthDrop, float64(3)},
		{"Thresholds.MaxComplexityIncrease", cfg.Thresholds.MaxComplexityIncrease, float64(4)},
		{"Thresholds.MaxRiskCount", cfg.Thresholds.MaxRiskCount, 2},
		{"Thresholds.MaxHotspots", cfg.Thresholds.MaxHotspots, 1},
		{"Thresholds.RequireTests", cfg.Thresholds.RequireTests, true},
		{"Thresholds.FailOnDrift", cfg.Thresholds.FailOnDrift, true},
		{"Explain", cfg.Explain, true},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// An explicit 0 in the environment must be honored as a real bound, not
// mistaken for "unset".
func TestEnvironmentZeroIsNotUnset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("LENSYXE_MAX_RISK_COUNT", "0")

	cfg, _, err := Load("", dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Thresholds.MaxRiskCount != 0 {
		t.Errorf("MaxRiskCount = %d, want 0: an explicit zero is a real bound",
			cfg.Thresholds.MaxRiskCount)
	}
	if !cfg.Thresholds.IsSet(float64(cfg.Thresholds.MaxRiskCount)) {
		t.Error("MaxRiskCount=0 must report as configured")
	}
}

// configKeys must match the envVarName derivation, or the documented variable
// names are wrong.
func TestEnvVarNameDerivation(t *testing.T) {
	cases := map[string]string{
		"min_health_score":  "LENSYXE_MIN_HEALTH_SCORE",
		"git_window_days":   "LENSYXE_GIT_WINDOW_DAYS",
		"watch_debounce_ms": "LENSYXE_WATCH_DEBOUNCE_MS",
		"database_path":     "LENSYXE_DATABASE_PATH",
	}
	for key, want := range cases {
		if got := envVarName(key); got != want {
			t.Errorf("envVarName(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero window", func(c *Config) { c.GitWindowDays = 0 }},
		{"negative window", func(c *Config) { c.GitWindowDays = -1 }},
		{"zero hotspot", func(c *Config) { c.HotspotThreshold = 0 }},
		{"zero history limit", func(c *Config) { c.HistoryLimit = 0 }},
		{"negative debounce", func(c *Config) { c.Watch.DebounceMS = -1 }},
		{"negative interval", func(c *Config) { c.Watch.IntervalSeconds = -1 }},
		{"min score above 100", func(c *Config) { c.Thresholds.MinHealthScore = 101 }},
		{"min score below zero", func(c *Config) { c.Thresholds.MinHealthScore = -2 }},
		{"drop below Unset", func(c *Config) { c.Thresholds.MaxHealthDrop = -2 }},
		{"complexity below Unset", func(c *Config) { c.Thresholds.MaxComplexityIncrease = -2 }},
		{"risk count below Unset", func(c *Config) { c.Thresholds.MaxRiskCount = -2 }},
		{"hotspots below Unset", func(c *Config) { c.Thresholds.MaxHotspots = -2 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default(t.TempDir())
			tc.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestValidateAcceptsDefaults(t *testing.T) {
	if err := Default(t.TempDir()).Validate(); err != nil {
		t.Errorf("defaults must validate: %v", err)
	}
}

// A zero MinHealthScore is a real bound ("must be at least 0") and must validate.
func TestValidateAllowsBoundaryThresholds(t *testing.T) {
	cfg := Default(t.TempDir())
	cfg.Thresholds.MinHealthScore = 100
	cfg.Thresholds.MaxRiskCount = 0
	cfg.Thresholds.MaxHotspots = 0
	if err := cfg.Validate(); err != nil {
		t.Errorf("boundary thresholds must validate: %v", err)
	}
}
