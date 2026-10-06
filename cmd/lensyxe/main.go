// Command lensyxe is a local-first engineering intelligence CLI.
//
// It inspects a repository's source code, git history, and dependency
// manifests to produce a deterministic Engineering Health Score. All analysis
// runs on the local machine; Lensyxe never uploads source or contacts a
// remote service.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/zelvior/lensyxe/internal/analyzer"
	"github.com/zelvior/lensyxe/internal/compare"
	"github.com/zelvior/lensyxe/internal/config"
	"github.com/zelvior/lensyxe/internal/gates"
	"github.com/zelvior/lensyxe/internal/history"
	"github.com/zelvior/lensyxe/internal/report"
	"github.com/zelvior/lensyxe/internal/storage"
	"github.com/zelvior/lensyxe/internal/watch"
	"github.com/zelvior/lensyxe/pkg/models"
)

// exitGateFailure is returned when a configured threshold is breached. It maps
// to a dedicated exit code so CI can distinguish a policy failure from a crash.
const exitGateFailure = 2

// app holds the state shared between the root command and its subcommands.
//
// Configuration is carried on an explicit *viper.Viper instance rather than the
// global singleton: two commands in the same process (as in tests) must not be
// able to see each other's settings.
type app struct {
	// configFile is the value of the persistent --config flag.
	configFile string
	// cfg is the loaded configuration, refreshed by loadConfig.
	cfg config.Config
	// viper holds the raw loaded values so RunE can read single keys.
	v *viper.Viper
	// baseline is the pre-insert history state for the current run, captured
	// before persisting so max_health_drop has something real to compare to.
	baseline gates.Baseline
	// explain holds the resolved explanation-layer configuration, populated by
	// addExplainFlags when a command registers those flags.
	explain *explainConfig
}

func main() {
	a := &app{}
	if err := newRootCmd(a).Execute(); err != nil {
		os.Exit(exitCodeFor(err))
	}
}

// exitCodeFor maps an error to a process exit code.
//
// A threshold breach is a policy failure, not a malfunction, so it gets its own
// code: CI can treat 1 as "something broke" and 2 as "the gate rejected this".
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, errGateFailed) {
		return exitGateFailure
	}
	return 1
}

// errGateFailed wraps the breach list so the exit code can carry it.
var errGateFailed = errors.New("threshold gate failed")

func newRootCmd(a *app) *cobra.Command {
	if a.v == nil {
		a.v = viper.New()
	}
	if a.cfg.Target == "" {
		wd, _ := os.Getwd()
		a.cfg = config.Default(wd)
	}

	root := &cobra.Command{
		Use:   "lensyxe",
		Short: "Engineering intelligence for your codebase",
		Long: strings.TrimSpace(`
Lensyxe analyzes a repository and produces a deterministic Engineering Health
Score from three dimensions: code structure, dependency surface, and git
maintainability signals.

Everything runs locally. No source code leaves your machine.`),
		Version:      Version,
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.loadConfig(cmd)
		},
	}

	// Shared flags. Registered per command rather than globally so each shows only
	// the options that affect it.
	root.PersistentFlags().StringVar(&a.configFile, "config", "", "path to an Lensyxe config file")

	root.AddCommand(newAnalyzeCmd(a))
	root.AddCommand(newStatusCmd(a))
	root.AddCommand(newSetupCmd(a))
	root.AddCommand(newBlastCmd(a))
	root.AddCommand(newCognitiveCmd(a))
	root.AddCommand(newDecayCmd(a))
	root.AddCommand(newTopologyCmd(a))
	root.AddCommand(newCompareCmd(a))
	root.AddCommand(newHistoryCmd(a))
	root.AddCommand(newWatchCmd(a))
	root.AddCommand(newServeCmd(a))
	root.AddCommand(newVersionCmd())

	// Git-style spellings, so the commands someone already has in their fingers
	// work here too. These are aliases rather than separate commands on purpose:
	// one implementation, one set of flags, one set of documented behaviour, and
	// no way for the two names to drift apart.
	//
	// They are not a claim to be a version control system. `status` reads health,
	// `log` reads recorded health history, and neither can stage or commit.
	// Aliases are listed by `lensyxe <cmd> --help` and in CLI_REFERENCE.md.
	for _, c := range root.Commands() {
		for _, alias := range gitStyleAliases(c.Name()) {
			c.Aliases = append(c.Aliases, alias)
		}
	}
	return root
}

// gitStyleAliases maps a command name to the short and idiomatic spellings it
// answers to.
//
// `log` and `diff` are the interesting ones: both name an operation the tool
// genuinely performs, so a reader arriving from git reaches for them and they
// work. `lg` and `df` exist for muscle memory, and neither is discoverable
// without the documentation.
func gitStyleAliases(name string) []string {
	switch name {
	case "analyze":
		return []string{"an"}
	case "status":
		return []string{"st"}
	case "history":
		return []string{"log", "lg"}
	case "compare":
		return []string{"diff", "df"}
	case "watch":
		return []string{"w"}
	case "serve":
		return []string{"ui"}
	default:
		return nil
	}
}

// loadConfig resolves the configuration for this invocation.
//
// Precedence is flag > config file (or LENSYXE_* env) > built-in default.
func (a *app) loadConfig(cmd *cobra.Command) error {
	wd, err := os.Getwd()
	if err != nil {
		wd = "."
	}
	cfg, warnings, err := config.Load(a.configFile, wd)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		cmd.PrintErrf("lensyxe: %s\n", w)
	}

	// Flags override the loaded file.
	if f := cmd.Flags(); f != nil {
		if f.Changed("git-window") {
			if v, _ := f.GetInt("git-window"); v > 0 {
				cfg.GitWindowDays = v
			}
		}
		if f.Changed("hotspot-threshold") {
			if v, _ := f.GetInt("hotspot-threshold"); v > 0 {
				cfg.HotspotThreshold = v
			}
		}
		if f.Changed("timeout") {
			if v, _ := f.GetInt("timeout"); v >= 0 {
				cfg.TimeoutSeconds = v
			}
		}
	}

	// The target path is NOT taken from positional args here. `compare` takes
	// two positional revisions, and inferring a directory from those would be
	// wrong. Each command that accepts a path resolves it in its own RunE.

	a.cfg = cfg
	if err := cfg.Validate(); err != nil {
		return err
	}
	return nil
}

// withTarget returns a copy of the resolved config whose Target is the first
// positional argument, when the command accepts one. Every path-taking command
// routes through here so target resolution stays in one place.
func (a *app) withTarget(args []string) config.Config {
	cfg := a.cfg
	if len(args) == 1 && strings.TrimSpace(args[0]) != "" {
		cfg.Target = args[0]
	}
	return cfg
}

// scanConfig projects the resolved config onto the analyzer's own config type.
//
// The conversion lives here rather than in internal/config so the analyzer
// package stays free of any dependency on viper or on the CLI's config shape.
func scanConfig(cfg config.Config) analyzer.Config {
	out := analyzer.DefaultConfig(cfg.Target)
	out.GitWindowDays = cfg.GitWindowDays
	out.HotspotThreshold = cfg.HotspotThreshold
	if len(cfg.IgnoreDirs) > 0 {
		out.IgnoreDirs = cfg.IgnoreDirs
	}
	if cfg.MaxFileBytes > 0 {
		out.MaxFileBytes = cfg.MaxFileBytes
	}
	out.EnableComplexity = cfg.EnableComplexity
	out.DetectWorkspace = cfg.DetectWorkspace
	if cfg.TimeoutSeconds > 0 {
		out.Timeout = time.Duration(cfg.TimeoutSeconds) * time.Second
	}
	return out
}

// openStore opens the history database for root.
//
// root is passed explicitly rather than read from the config because the
// positional path argument may differ from the configured target: `analyze
// /some/repo` must record against /some/repo, not against the working
// directory. Callers pass the absolute path the analyzer actually used.
func (a *app) openStore(root string) (*storage.Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", root, err)
	}
	return storage.Open(storage.ResolvePath(abs, a.cfg.DatabasePath))
}

// newAnalyzeCmd builds `lensyxe analyze [path]`.
func newAnalyzeCmd(a *app) *cobra.Command {
	var (
		format          string
		includeFindings bool
		noPersist       bool
		noGate          bool
		monorepo        bool
		ciCfg           ciFlags
	)

	cmd := &cobra.Command{
		Use:   "analyze [path]",
		Short: "Analyze a repository and score its engineering health",
		Long: strings.TrimSpace(`
Analyze a directory and report its Engineering Health Score.

The code, git, and dependency analyzers run concurrently, so wall-clock time is
bounded by the slowest analyzer rather than their sum.

Every run is recorded to the local history database, so the "lensyxe history"
command can show the trend over time. Pass --no-persist to skip that.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved := a.withTarget(args)

			scanCfg := scanConfig(resolved)
			// The flag is additive to the config: either can request the
			// breakdown, so a repository that always wants it does not need a
			// flag on every invocation.
			scanCfg.DetectWorkspace = resolved.DetectWorkspace || monorepo

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			snap, err := analyzer.Scan(ctx, scanCfg, Version)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return fmt.Errorf("analysis cancelled")
				}
				return err
			}

			out := cmd.OutOrStdout()
			switch strings.ToLower(format) {
			case "json":
				if err := report.RenderJSON(out, snap); err != nil {
					return err
				}
			case "markdown", "md":
				if err := report.RenderMarkdown(out, snap, report.MarkdownOptions{
					IncludeFindings: includeFindings,
				}); err != nil {
					return err
				}
			case "terminal", "":
				if err := report.Render(out, snap); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unsupported format %q (want terminal, markdown, or json)", format)
			}

			// Persist before gating so the recorded history includes this run
			// even when it fails a threshold. The captured baseline also feeds
			// the gate, so a failed run still advances the comparison point.
			if !noPersist {
				base, err := a.persist(snap)
				a.baseline = base
				if err != nil {
					// Persistence is best-effort: a read-only checkout must
					// not stop the analysis from being reported.
					cmd.PrintErrf("lensyxe: could not record history: %v\n", err)
				}
			}

			// The explanation layer runs last. It is an addition to a finished
			// analysis, not an input to it: by this point every number it
			// could summarize has already been computed and printed, and a
			// failure here cannot change any of them.
			explainCfg, err := resolveExplain(cmd, a)
			if err != nil {
				return err
			}
			if err := runExplain(ctx, cmd.ErrOrStderr(), explainCfg, snap, nil); err != nil {
				return err
			}

			// CI gates run after the report and after persistence, so a
			// rejected build still leaves a stored snapshot and a published
			// comment explaining what regressed.
			if ciCfg.any() {
				return runCIFlags(cmd, snap, ciOptions{
					Flags: &ciCfg,
					// NoGate is threaded through rather than handled here.
					// `--no-gate --fail-under-health 70` is a
					// contradiction whose resolution must be explicit:
					// --no-gate wins, because its only purpose is to get the
					// report without the verdict. The comment and the
					// workflow report are still produced, since those are
					// outputs rather than gates.
					NoGate: noGate,
					// resolved.Target, not a.cfg.Target: the latter is the
					// process-level default and would point at the
					// working directory whenever a path was given.
					Target:  resolved.Target,
					Version: Version,
				})
			}

			if noGate || !gates.Any(a.cfg.Thresholds) {
				return nil
			}
			breaches := a.evaluateGates(snap, a.baseline)
			if len(breaches) == 0 {
				return nil
			}
			if err := report.RenderGateFailures(cmd.ErrOrStderr(), breaches); err != nil {
				return err
			}
			return fmt.Errorf("%w: %d threshold(s) breached", errGateFailed, len(breaches))
		},
	}

	cmd.Flags().StringVar(&format, "format", "terminal", "output format: terminal, markdown, or json")
	cmd.Flags().BoolVar(&includeFindings, "findings", false, "include raw findings in markdown output")
	cmd.Flags().BoolVar(&noPersist, "no-persist", false, "do not record this snapshot to the history database")
	cmd.Flags().BoolVar(&noGate, "no-gate", false, "skip threshold checks for this run")
	cmd.Flags().BoolVar(&monorepo, "monorepo", false,
		"detect a workspace layout and score each package individually (implies detect_workspace)")
	addExplainFlags(cmd, a)
	// Overrides read by loadConfig; see its precedence comment.
	cmd.Flags().Int("git-window", 90, "days of git history to analyze")
	cmd.Flags().Int("hotspot-threshold", 400, "code lines above which a file is a hotspot candidate")
	cmd.Flags().Int("timeout", 60, "overall scan timeout in seconds (0 disables)")
	addCIFlags(cmd, &ciCfg)

	return cmd
}

// persist records a snapshot, capturing the pre-insert baseline for gating.
//
// The snapshot's own Root is authoritative: the analyzer resolved it to an
// absolute path, so history lands beside the analyzed tree no matter which
// directory the command was invoked from.
func (a *app) persist(snap *models.Snapshot) (gates.Baseline, error) {
	var base gates.Baseline
	store, err := a.openStore(snap.Root)
	if err != nil {
		return base, err
	}
	defer store.Close()

	// Read the previous rows BEFORE inserting, otherwise the baseline would be
	// this run itself and every max_health_drop check would be zero.
	if recs, err := store.GetHistory(snap.Root, 2); err == nil && len(recs) >= 2 {
		base = gates.Baseline{
			HasPrevious:       true,
			Score:             recs[len(recs)-2].Score,
			AverageComplexity: recs[len(recs)-2].AvgComplexity,
		}
	}

	if _, err := store.SaveSnapshot(snap); err != nil {
		return base, err
	}
	// Bound the database so a long-lived repository cannot grow without limit.
	_, _ = store.Prune(snap.Root, defaultHistoryRetention)
	return base, nil
}

// defaultHistoryRetention caps the stored rows per root.
const defaultHistoryRetention = 500

// evaluateGates runs the threshold rules against the current snapshot.
func (a *app) evaluateGates(snap *models.Snapshot, base gates.Baseline) []gates.Breach {
	return gates.Evaluate(snap, a.cfg.Thresholds, base)
}

// newHistoryCmd builds `lensyxe history`.
func newHistoryCmd(a *app) *cobra.Command {
	var (
		limit int
		title string
		grid  bool
	)

	cmd := &cobra.Command{
		Use:   "history [path]",
		Short: "Show the engineering health timeline across recorded runs",
		Long: strings.TrimSpace(`
Render the stored health trend as an ASCII timeline.

Snapshots are recorded by ` + "`lensyxe analyze`" + ` into .lensyxe/history.db in the
analyzed repository. Nothing is uploaded; the database is a local file you can
delete or gitignore.

The optional path selects which repository's history to read, matching the
target ` + "`lensyxe analyze`" + ` recorded against.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// History is keyed by the absolute target, which is what analyze
			// records against.
			target, err := filepath.Abs(a.withTarget(args).Target)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", a.cfg.Target, err)
			}

			store, err := a.openStore(target)
			if err != nil {
				return err
			}
			defer store.Close()

			n := limit
			if !cmd.Flags().Changed("limit") && a.cfg.HistoryLimit > 0 {
				n = a.cfg.HistoryLimit
			}
			if n <= 0 {
				n = 10
			}

			records, err := store.GetHistory(target, n)
			if err != nil {
				return err
			}
			delta, err := store.GetScoreDelta(target)
			if err != nil && !errors.Is(err, storage.ErrNoHistory) {
				return err
			}

			points := history.BuildPoints(records, time.Now().UTC())
			if err := history.Render(cmd.OutOrStdout(), points, delta, history.Options{
				Title:    title,
				ShowGrid: grid,
			}); err != nil {
				return err
			}
			cmd.PrintErrf("\n  %d snapshot(s) in %s\n", len(records), store.Path())
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 10, "number of runs to display")
	cmd.Flags().StringVar(&title, "title", "", "override the timeline heading")
	cmd.Flags().BoolVar(&grid, "grid", false, "draw vertical rules between axis labels")

	return cmd
}

// newWatchCmd builds `lensyxe watch [path]`.
func newWatchCmd(a *app) *cobra.Command {
	var (
		debounceMS  int
		intervalSec int
		extensions  []string
	)

	cmd := &cobra.Command{
		Use:   "watch [path]",
		Short: "Re-analyze the repository whenever files change",
		Long: strings.TrimSpace(`
Watch the repository and re-run the analysis on every relevant change.

Uses the OS filesystem notification API, so an idle repository costs nothing.
Events are debounced because editors write files in several operations.

Each completed analysis is recorded to the history database, so ` + "`lensyxe history`" + `
shows the live trend as you work.`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := a.withTarget(args)
			abs, err := filepath.Abs(cfg.Target)
			if err != nil {
				return fmt.Errorf("resolve %s: %w", cfg.Target, err)
			}

			scanCfg := scanConfig(cfg)
			scanCfg.Target = abs

			store, err := storage.Open(storage.ResolvePath(abs, a.cfg.DatabasePath))
			if err != nil {
				return err
			}
			defer store.Close()

			exts := extensions
			if len(exts) == 0 {
				exts = watch.SourceExtensions()
			}

			wcfg := watch.Config{
				Root:            abs,
				Analyze:         func(ctx context.Context) (*models.Snapshot, error) { return analyzer.Scan(ctx, scanCfg, Version) },
				OnSnapshot:      func(snap *models.Snapshot) error { _, err := store.SaveSnapshot(snap); return err },
				Debounce:        time.Duration(debounceMS) * time.Millisecond,
				IgnoreDirs:      a.cfg.IgnoreDirs,
				Extensions:      exts,
				Log:             cmd.ErrOrStderr(),
				IntervalSeconds: intervalSec,
			}

			w, err := watch.New(wcfg)
			if err != nil {
				return err
			}
			defer w.Close()

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			events := make(chan watch.Event, 64)
			done := make(chan error, 1)
			go func() { done <- w.Run(ctx, events) }()

			// Drain events so the channel never fills, and surface analysis
			// failures as they happen.
			drained := make(chan struct{})
			go func() {
				defer close(drained)
				for ev := range events {
					if ev.Kind == watch.EventError {
						fmt.Fprintf(cmd.ErrOrStderr(), "[WATCH] error: %v\n", ev.Err)
					}
				}
			}()

			err = <-done
			close(events)
			<-drained
			return err
		},
	}

	cmd.Flags().IntVar(&debounceMS, "debounce", 250, "milliseconds to coalesce file events")
	cmd.Flags().IntVar(&intervalSec, "interval", 0, "also re-analyze every N seconds (0 disables)")
	cmd.Flags().StringSliceVar(&extensions, "ext", nil,
		"file extensions to react to (default: common source types)")

	return cmd
}

// newCompareCmd builds `lensyxe compare <revA> <revB>`.
//
// Each revision is exported with `git archive` into a temporary directory and
// analyzed there. The user's working tree, index, and HEAD are never modified,
// so the command is safe to run on a dirty checkout.
func newCompareCmd(a *app) *cobra.Command {
	var (
		format  string
		timeout int
	)

	cmd := &cobra.Command{
		Use:   "compare <revA> <revB>",
		Short: "Compare engineering health between two commits or tags",
		Long: strings.TrimSpace(`
Compare two revisions of a repository and report the metric deltas between them.

Both revisions are exported with git archive into temporary directories and
analyzed there, so your working tree and index are left untouched. Anything git
can resolve works: a SHA, a tag, a branch, or a relative ref like HEAD~5.

Examples:
  lensyxe compare v1.0.0 v1.1.0
  lensyxe compare HEAD~10 HEAD
  lensyxe compare main feature/new-parser --format markdown`),
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := compare.Config{
				Root:    ".",
				A:       args[0],
				B:       args[1],
				Timeout: timeout,
			}
			if cmd.Flags().Changed("compare-root") {
				if root, _ := cmd.Flags().GetString("compare-root"); root != "" {
					cfg.Root = root
				}
			} else if root := strings.TrimSpace(a.cfg.CompareRoot); root != "" {
				cfg.Root = root
			}
			if cmd.Flags().Changed("timeout") {
				if v, _ := cmd.Flags().GetInt("timeout"); v > 0 {
					cfg.Timeout = v
				}
			}
			// The analyzer is injected so internal/compare does not import
			// internal/analyzer, which would create an import cycle.
			cfg.Analyzer = func(ctx context.Context, treePath string) (*models.Snapshot, error) {
				scanCfg := scanConfig(a.cfg)
				scanCfg.Target = treePath
				return analyzer.Scan(ctx, scanCfg, Version)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			res, err := compare.Run(ctx, cfg, Version)
			if err != nil {
				if errors.Is(err, compare.ErrNotRepository) {
					return fmt.Errorf("compare requires a git repository: %w", err)
				}
				return err
			}

			out := cmd.OutOrStdout()
			switch strings.ToLower(format) {
			case "json":
				return report.RenderCompareJSON(out, res)
			case "markdown", "md":
				return report.RenderCompareMarkdown(out, res)
			case "terminal", "":
				return report.RenderCompare(out, res)
			default:
				return fmt.Errorf("unsupported format %q (want terminal, markdown, or json)", format)
			}
		},
	}

	cmd.Flags().StringVar(&format, "format", "terminal", "output format: terminal, markdown, or json")
	cmd.Flags().IntVar(&timeout, "timeout", 120, "per-revision timeout in seconds")
	// The repository is normally the working directory; this flag exists so a
	// comparison can be run from elsewhere.
	cmd.Flags().String("compare-root", "", "repository to compare within (default: current directory)")

	return cmd
}

// newVersionCmd is defined in version.go, alongside the variables the linker
// writes to.
