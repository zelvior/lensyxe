package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zelvior/lensyxe/internal/ai"
	"github.com/zelvior/lensyxe/pkg/models"
)

// explainConfig holds the resolved explanation-layer settings.
type explainConfig struct {
	// enabled is true when --explain was passed or the config turned it on.
	enabled bool
	// provider, model, and keyEnv name the configuration.
	provider string
	model    string
	keyEnv   string
	// timeout bounds the request.
	timeout time.Duration
}

// addExplainFlags registers the explanation flags on a command.
func addExplainFlags(cmd *cobra.Command, a *app) {
	f := &explainConfig{}
	// The flags are bound to the app so RunE can read them, matching how the
	// existing per-command flags are threaded.
	a.explain = f

	cmd.Flags().Bool("explain", false,
		"append a natural-language summary from the configured AI provider; the score is unaffected")
	cmd.Flags().String("ai-provider", "",
		"AI provider for --explain: openrouter, openai, or gemini (default $LENSYXE_AI_PROVIDER)")
	cmd.Flags().String("ai-model", "",
		"model identifier for --explain (default $LENSYXE_AI_MODEL, then the provider default)")
	cmd.Flags().String("ai-key-env", "",
		"environment variable holding the API key (default LENSYXE_AI_KEY)")
	cmd.Flags().Duration("ai-timeout", ai.DefaultTimeout, "timeout for the AI request")
}

// resolveExplain reads the flags and configuration into an explainConfig.
//
// enabled is the OR of the flag and the config file, so a repository that
// always wants a summary does not need the flag on every invocation, and a
// config that turns it on is not silently overridden by an absent flag.
func resolveExplain(cmd *cobra.Command, a *app) (explainConfig, error) {
	var cfg explainConfig
	cfg.provider = a.cfg.AIProvider
	cfg.model = a.cfg.AIModel
	cfg.keyEnv = a.cfg.AIKeyEnv
	cfg.timeout = ai.DefaultTimeout

	flags := cmd.Flags()
	if flags.Changed("explain") {
		cfg.enabled, _ = flags.GetBool("explain")
	} else {
		cfg.enabled = a.cfg.Explain
	}
	if v, _ := flags.GetString("ai-provider"); flags.Changed("ai-provider") {
		cfg.provider = v
	}
	if v, _ := flags.GetString("ai-model"); flags.Changed("ai-model") {
		cfg.model = v
	}
	if v, _ := flags.GetString("ai-key-env"); flags.Changed("ai-key-env") {
		cfg.keyEnv = v
	}
	if flags.Changed("ai-timeout") {
		if d, _ := flags.GetDuration("ai-timeout"); d > 0 {
			cfg.timeout = d
		}
	}

	if cfg.provider != "" {
		if err := validateProvider(cfg.provider); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

// validateProvider rejects an unknown provider before any network call.
func validateProvider(name string) error {
	switch ai.Provider(strings.ToLower(strings.TrimSpace(name))) {
	case ai.ProviderOpenRouter, ai.ProviderOpenAI, ai.ProviderGemini:
		return nil
	}
	return fmt.Errorf("unknown --ai-provider %q (want openrouter, openai, or gemini)", name)
}

// runExplain requests a summary and writes it, labelling it as generated.
//
// The explanation is written to w, which for `analyze` is stderr. It goes there
// rather than stdout for one reason: stdout may carry a JSON report that a
// pipeline is piping into another step, and a paragraph of prose in the middle
// of that would break the parse. The deterministic report is never modified.
func runExplain(
	ctx context.Context,
	w io.Writer,
	cfg explainConfig,
	snap *models.Snapshot,
	baseline *models.Snapshot,
) error {
	if !cfg.enabled {
		return nil
	}

	providerCfg, err := ai.Resolve(cfg.provider, cfg.model, cfg.keyEnv)
	if err != nil {
		if errors.Is(err, ai.ErrNotConfigured) {
			// Not an error. The user asked for a summary and there is no way
			// to produce one; the deterministic analysis is complete and
			// unaffected, so this is a note rather than a failure.
			fmt.Fprintf(w, "\nlensyxe: --explain skipped: %v\n", err)
			return nil
		}
		return err
	}
	providerCfg.Timeout = cfg.timeout

	exp, err := ai.Explain(ctx, providerCfg, snap, baseline)
	if err != nil {
		if errors.Is(err, ai.ErrNotConfigured) {
			fmt.Fprintf(w, "\nlensyxe: --explain skipped: %v\n", err)
			return nil
		}
		// A provider failure is reported and the run continues. Failing the
		// whole analysis because a summary could not be fetched would make the
		// deterministic score depend on a third-party service's uptime, which
		// is precisely what this layer is designed not to do.
		fmt.Fprintf(w, "\nlensyxe: --explain failed: %v\n", err)
		return nil
	}

	if _, err := io.WriteString(w, renderExplanation(exp)); err != nil {
		return err
	}
	return nil
}

// renderExplanation formats the summary with the labelling it requires.
//
// The label is not decoration. A reader who sees a paragraph of confident prose
// about their repository has no way to tell a deterministic measurement from a
// generated sentence, so the provenance, the model, and the non-authoritative
// status all appear next to the text itself.
func renderExplanation(exp *ai.Explanation) string {
	// The width matches the rule the terminal reporter draws, so the section
	// reads as part of the same report rather than something bolted on.
	sep := strings.Repeat("-", 62) + "\n"

	var b strings.Builder
	b.WriteString("\n" + sep)
	b.WriteString("EXECUTIVE SUMMARY (generated, not a measurement)\n")
	b.WriteString(sep)
	b.WriteString(exp.Text)
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "-- AI-generated summary from %s (%s). The Engineering Health Score and "+
		"every figure above it are computed locally and are unaffected by this "+
		"text. Read the numbers, not the prose.\n",
		exp.Provider, exp.Model)
	return b.String()
}
