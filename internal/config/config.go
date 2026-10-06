// Package config loads and resolves Lensyxe configuration.
//
// Configuration comes from three layers, in decreasing precedence:
//
//  1. command-line flags
//  2. this package's loaded file (and LENSYXE_* environment overrides)
//  3. built-in defaults
//
// The package deliberately owns no I/O side effects beyond reading the config
// file, so every consumer can be tested by handing it a resolved Config.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cast"
	"github.com/spf13/viper"
)

// Config is the fully resolved configuration for a run.
type Config struct {
	// Target is the directory to analyze.
	Target string
	// GitWindowDays bounds the git churn window.
	GitWindowDays int
	// HotspotThreshold is the code-line count at which a file becomes a
	// hotspot candidate.
	HotspotThreshold int
	// IgnoreDirs are directory names pruned from the code walk.
	IgnoreDirs []string
	// MaxFileBytes skips files larger than this during the code walk.
	MaxFileBytes int64
	// EnableComplexity toggles the lexical complexity estimator.
	EnableComplexity bool
	// DetectWorkspace makes the monorepo breakdown the default for every
	// command that analyzes a repository.
	//
	// It defaults to false because most repositories are not workspaces, and
	// detection reads several root manifests to find that out. Individual
	// commands can still request it with --monorepo for a one-off.
	DetectWorkspace bool
	// Explain enables the optional AI summary layer by default.
	Explain bool
	// AIProvider names the provider for the explanation layer: "openrouter",
	// "openai", or "gemini". Empty means unset, and the layer stays off.
	AIProvider string
	// AIModel overrides the provider's default model.
	AIModel string
	// AIKeyEnv names the environment variable holding the API key. It names a
	// variable rather than holding a key, so the key itself never reaches the
	// config file, the process arguments, or a snapshot.
	AIKeyEnv string
	// TimeoutSeconds bounds the whole scan; zero disables the deadline.
	TimeoutSeconds int

	// CompareRoot is the repository `lensyxe compare` operates within.
	CompareRoot string
	// HistoryLimit is the default row count for `lensyxe history`.
	HistoryLimit int
	// DatabasePath is the SQLite file backing the history store.
	DatabasePath string

	// Watch controls file-watch behavior.
	Watch WatchConfig

	// Thresholds are the CI-facing gates. All are optional.
	Thresholds Thresholds
}

// WatchConfig controls `lensyxe watch`.
type WatchConfig struct {
	// DebounceMS coalesces bursts of filesystem events into one re-analysis.
	// Editors write files in several operations; without a debounce a single
	// save triggers a dozen redundant scans.
	DebounceMS int
	// IntervalSeconds additionally re-analyzes on a fixed cadence even when
	// no file changed. Zero disables periodic re-analysis, which is the
	// default: an idle repository should consume no CPU.
	IntervalSeconds int
}

// Thresholds are pass/fail gates that turn into a non-zero exit code.
//
// These exist so Lensyxe can be used as a CI gate: "fail the build if health
// drops below 70, or falls more than 5 points from the stored baseline".
//
// Numeric thresholds use Unset (-1) to mean "not configured", NOT zero. Zero is
// a meaningful bound for a maximum ("allow zero risks"), so treating zero as
// unset would make a configured gate silently do nothing -- the worst possible
// failure mode for something a team trusts in CI.
type Thresholds struct {
	// MinHealthScore fails the run when the overall score is below this.
	MinHealthScore float64
	// MaxHealthDrop fails the run when the score falls more than this many
	// points relative to the previous stored snapshot.
	MaxHealthDrop float64
	// MaxComplexityIncrease fails the run when average estimated complexity
	// rises by more than this many points.
	MaxComplexityIncrease float64
	// MaxRiskCount fails the run when the total risk count exceeds this.
	MaxRiskCount int
	// MaxHotspots fails the run when the confirmed hotspot count exceeds this.
	MaxHotspots int
	// FailOnDrift fails the run when dependency drift is detected.
	FailOnDrift bool
	// RequireTests fails the run when no test files are detected.
	RequireTests bool
}

// Unset marks a numeric threshold as not configured.
const Unset = -1

// IsSet reports whether a numeric threshold is configured, i.e. anything other
// than Unset.
func (t Thresholds) IsSet(value float64) bool { return value != Unset }

// Default returns the built-in configuration for target.
func Default(target string) Config {
	return Config{
		Target:           target,
		GitWindowDays:    90,
		HotspotThreshold: 400,
		MaxFileBytes:     2 << 20,
		EnableComplexity: true,
		DetectWorkspace:  false,
		Explain:          false,
		AIProvider:       "",
		AIModel:          "",
		AIKeyEnv:         "LENSYXE_AI_KEY",
		TimeoutSeconds:   60,
		CompareRoot:      "",
		HistoryLimit:     10,
		DatabasePath:     filepath.Join(".lensyxe", "history.db"),
		Watch: WatchConfig{
			DebounceMS:      250,
			IntervalSeconds: 0,
		},
		Thresholds: NewThresholds(),
	}
}

// NewThresholds returns a fully unset threshold set.
//
// Every numeric field starts at Unset so that a zero written in the config file
// is honored as a real bound rather than mistaken for "not configured".
func NewThresholds() Thresholds {
	return Thresholds{
		MinHealthScore:        Unset,
		MaxHealthDrop:         Unset,
		MaxComplexityIncrease: Unset,
		MaxRiskCount:          Unset,
		MaxHotspots:           Unset,
	}
}

// maxParentSearch bounds how far up the tree auto-discovery walks.
const maxParentSearch = 5

// envPrefix is the prefix of every environment override.
//
// It is exported as documentation of the contract: CONTRIBUTING.md and the
// sample config both promise LENSYXE_* overrides, and those promises are only
// true because every key in configKeys is bound to LENSYXE_<KEY>.
const envPrefix = "LENSYXE"

// configKeys is every key the loader reads from viper.
//
// It is a literal list rather than a derived one because the set is a public
// contract: it is the documented environment variable surface. Deriving it from
// struct tags would silently change the contract whenever a field is renamed.
var configKeys = []string{
	"git_window_days",
	"hotspot_threshold",
	"enable_complexity",
	"max_file_bytes",
	"ignore_dirs",
	"timeout_seconds",
	"detect_workspace",
	"explain",
	"ai_provider",
	"ai_model",
	"ai_key_env",
	"history_limit",
	"database_path",
	"compare_root",
	"watch_debounce_ms",
	"watch_interval_seconds",
	"min_health_score",
	"max_health_drop",
	"max_complexity_increase",
	"max_risk_count",
	"max_hotspots",
	"fail_on_drift",
	"require_tests",
}

// envVarName renders a config key as its environment variable name.
func envVarName(key string) string {
	return envPrefix + "_" + strings.ToUpper(key)
}

// Load reads configuration from disk and merges it over the defaults.
//
// Search order for an explicit path:
//   - path, if given (must exist; a missing file is an error)
//   - otherwise .lensyxe.yml then .lensyxe.yaml in the working directory
//   - then the parent directory
//   - then $HOME
//
// A malformed file is reported through the returned warnings but never fatal:
// Lensyxe degrades to defaults rather than becoming unusable. A missing file
// is not even a warning.
func Load(explicitPath, workingDir string) (Config, []string, error) {
	cfg := Default(workingDir)
	var warnings []string

	v := viper.New()
	v.SetEnvPrefix("LENSYXE")
	v.AutomaticEnv()

	// Bind every config key to its environment variable explicitly.
	//
	// AutomaticEnv alone is not enough, and the reason is subtle enough to be
	// worth writing down. Viper resolves $LENSYXE_FOO only when it is asked
	// for "foo" directly, and it does not list environment-only keys in
	// AllKeys(). So an env-only override of a key that appears in no config
	// file was silently dropped: no error, no warning, no effect. A threshold
	// set through the environment looked configured and did nothing, which is
	// the worst failure mode available for a CI gate.
	//
	// Binding makes the keys known up front, so AllKeys includes them, the
	// publish loop below covers them, and a typo'd variable name is still
	// ignored rather than silently matching something.
	for _, key := range configKeys {
		// BindEnv takes the variable name explicitly, so it is independent of
		// SetEnvPrefix and stays correct if the prefix ever changes.
		if err := v.BindEnv(key, envVarName(key)); err != nil {
			warnings = append(warnings,
				fmt.Sprintf("config: could not bind %s: %v", envVarName(key), err))
		}
	}

	if explicitPath != "" {
		v.SetConfigFile(explicitPath)
		if err := v.ReadInConfig(); err != nil {
			// A missing or broken config is reported but never fatal. Lensyxe
			// stays usable with built-in defaults, which matters most in CI
			// and scripts where aborting on a typo is worse than running with
			// documented defaults.
			warnings = append(warnings, describeConfigError(explicitPath, err))
			return cfg, warnings, nil
		}
	} else {
		v.SetConfigName(".lensyxe")
		v.SetConfigType("yaml")
		// Walk up from the working directory so `lensyxe analyze ./cmd/x`
		// still finds the repository-level config. The walk is bounded: past a
		// few levels the user is more likely to be editing an unrelated tree
		// than looking for a repo config, and an unbounded walk could escape
		// the repository entirely.
		for dir, i := workingDir, 0; dir != "" && i < maxParentSearch; i++ {
			v.AddConfigPath(dir)
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		if home, err := os.UserHomeDir(); err == nil {
			v.AddConfigPath(home)
		}
		if err := v.ReadInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) && !os.IsNotExist(err) {
				warnings = append(warnings,
					fmt.Sprintf("ignoring config %s: %v", v.ConfigFileUsed(), err))
			}
			// A missing config file is not an error and must not skip apply.
			//
			// Returning here instead meant that with no file on disk the
			// environment was never consulted at all, so LENSYXE_MIN_HEALTH_SCORE
			// and friends did nothing. The documented precedence is
			// flag > file or env > default, and a repository with no config file
			// is precisely the case where an env-only override is the only option
			// a user has. Falling through keeps that true.
		}
	}

	// Publish onto the instance so lookups are uniform whether the values came
	// from a file or the environment.
	for _, key := range v.AllKeys() {
		v.Set(key, v.Get(key))
	}

	apply(&cfg, v)
	return cfg, warnings, nil
}

// describeConfigError turns a load failure into an actionable warning.
func describeConfigError(path string, err error) string {
	switch {
	case os.IsNotExist(err):
		return fmt.Sprintf("config file %s does not exist; using built-in defaults", path)
	default:
		return fmt.Sprintf("ignoring config %s: %v; using built-in defaults", path, err)
	}
}

// apply merges loaded values over cfg. Every key is optional.
func apply(cfg *Config, v *viper.Viper) {
	if n := v.GetInt("git_window_days"); n > 0 {
		cfg.GitWindowDays = n
	}
	if n := v.GetInt("hotspot_threshold"); n > 0 {
		cfg.HotspotThreshold = n
	}
	if dirs := v.GetStringSlice("ignore_dirs"); len(dirs) > 0 {
		cfg.IgnoreDirs = dirs
	}
	if n := v.GetInt64("max_file_bytes"); n > 0 {
		cfg.MaxFileBytes = n
	}
	if v.IsSet("enable_complexity") {
		cfg.EnableComplexity = v.GetBool("enable_complexity")
	}
	if v.IsSet("detect_workspace") {
		cfg.DetectWorkspace = v.GetBool("detect_workspace")
	}
	if v.IsSet("explain") {
		cfg.Explain = v.GetBool("explain")
	}
	if s := strings.TrimSpace(v.GetString("ai_provider")); s != "" {
		cfg.AIProvider = s
	}
	if s := strings.TrimSpace(v.GetString("ai_model")); s != "" {
		cfg.AIModel = s
	}
	if s := strings.TrimSpace(v.GetString("ai_key_env")); s != "" {
		cfg.AIKeyEnv = s
	}
	if n := v.GetInt("timeout_seconds"); n >= 0 {
		cfg.TimeoutSeconds = n
	}
	if s := strings.TrimSpace(v.GetString("compare_root")); s != "" {
		cfg.CompareRoot = s
	}
	if n := v.GetInt("history_limit"); n > 0 {
		cfg.HistoryLimit = n
	}
	if s := strings.TrimSpace(v.GetString("database_path")); s != "" {
		cfg.DatabasePath = s
	}

	if n := v.GetInt("watch_debounce_ms"); n > 0 {
		cfg.Watch.DebounceMS = n
	}
	if n := v.GetInt("watch_interval_seconds"); n >= 0 {
		cfg.Watch.IntervalSeconds = n
	}

	// viper v1.19 has no GetFloat, so numeric coercion goes through cast.
	// That also handles a YAML value written as an integer where a float is
	// expected, which is the common case for min_health_score: 70.
	//
	// IsSet gates every numeric threshold so an explicit 0 is honored as a
	// real bound. Writing 0 for a maximum means "allow none of this".
	if v.IsSet("min_health_score") {
		cfg.Thresholds.MinHealthScore = cast.ToFloat64(v.Get("min_health_score"))
	}
	if v.IsSet("max_health_drop") {
		cfg.Thresholds.MaxHealthDrop = cast.ToFloat64(v.Get("max_health_drop"))
	}
	if v.IsSet("max_complexity_increase") {
		cfg.Thresholds.MaxComplexityIncrease = cast.ToFloat64(v.Get("max_complexity_increase"))
	}
	if v.IsSet("max_risk_count") {
		cfg.Thresholds.MaxRiskCount = v.GetInt("max_risk_count")
	}
	if v.IsSet("max_hotspots") {
		cfg.Thresholds.MaxHotspots = v.GetInt("max_hotspots")
	}
	if v.IsSet("fail_on_drift") {
		cfg.Thresholds.FailOnDrift = v.GetBool("fail_on_drift")
	}
	if v.IsSet("require_tests") {
		cfg.Thresholds.RequireTests = v.GetBool("require_tests")
	}
}

// Validate checks the resolved configuration.
func (c Config) Validate() error {
	if c.GitWindowDays <= 0 {
		return fmt.Errorf("git_window_days must be positive, got %d", c.GitWindowDays)
	}
	if c.HotspotThreshold <= 0 {
		return fmt.Errorf("hotspot_threshold must be positive, got %d", c.HotspotThreshold)
	}
	if c.HistoryLimit <= 0 {
		return fmt.Errorf("history_limit must be positive, got %d", c.HistoryLimit)
	}
	if c.Watch.DebounceMS < 0 {
		return fmt.Errorf("watch_debounce_ms must not be negative, got %d", c.Watch.DebounceMS)
	}
	if c.Watch.IntervalSeconds < 0 {
		return fmt.Errorf("watch_interval_seconds must not be negative, got %d", c.Watch.IntervalSeconds)
	}
	if t := c.Thresholds; t.MinHealthScore != Unset &&
		(t.MinHealthScore < 0 || t.MinHealthScore > 100) {
		return fmt.Errorf("min_health_score must be between 0 and 100, got %v", t.MinHealthScore)
	}
	for name, v := range map[string]float64{
		"max_health_drop":         c.Thresholds.MaxHealthDrop,
		"max_complexity_increase": c.Thresholds.MaxComplexityIncrease,
	} {
		if v != Unset && v < 0 {
			return fmt.Errorf("%s must not be negative, got %v", name, v)
		}
	}
	for name, v := range map[string]int{
		"max_risk_count": c.Thresholds.MaxRiskCount,
		"max_hotspots":   c.Thresholds.MaxHotspots,
	} {
		if v != Unset && v < 0 {
			return fmt.Errorf("%s must not be negative, got %d", name, v)
		}
	}
	return nil
}
