// Package ai implements Lensyxe's optional, bring-your-own-key explanation
// layer.
//
// # What this package may and may not do
//
// It may rewrite numbers the deterministic engine already produced into prose.
// It may not compute a score, derive a metric, rank a risk, or fill in a figure
// that the Go analyzers did not measure.
//
// That boundary is the entire design constraint. Everything below exists to
// keep it: the prompt is assembled from a snapshot (never from source code),
// every figure in it is interpolated from a struct field, the system message
// forbids the model from introducing numbers, the response is checked for
// numbers it did not receive, and the result is labelled as machine-generated
// wherever it is displayed.
//
// # Why it is off by default
//
// No provider is configured unless the user supplies a key. A tool whose core
// promise is "deterministic, local-first" cannot quietly start calling a remote
// service; the request is opt-in, and `--explain` with no key performs no
// network I/O at all.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

// DefaultTimeout bounds the request. A local CLI should not appear to hang
// because a third-party endpoint is slow.
const DefaultTimeout = 45 * time.Second

// maxResponseBytes bounds how much of a provider reply is read. A runaway or
// hostile endpoint cannot exhaust memory.
const maxResponseBytes = 1 << 20

// Provider names the supported upstream services.
type Provider string

// Supported providers.
const (
	ProviderOpenRouter Provider = "openrouter"
	ProviderOpenAI     Provider = "openai"
	ProviderGemini     Provider = "gemini"
)

// ErrNotConfigured is returned when no provider or key is available.
//
// It is a normal outcome, not a failure: `--explain` without a key is a no-op
// and the analysis it accompanies is unaffected.
var ErrNotConfigured = errors.New(
	"no AI provider configured; set " + DefaultKeyEnv +
		" and " + DefaultProviderEnv + " (openrouter, openai, or gemini)")

// Default environment variable names. The key is read from the environment
// rather than from the config file so it never lands in a file that gets
// committed, printed, or serialized into a snapshot.
const (
	DefaultKeyEnv      = "LENSYXE_AI_KEY"
	DefaultProviderEnv = "LENSYXE_AI_PROVIDER"
	DefaultModelEnv    = "LENSYXE_AI_MODEL"
)

// endpoint describes one provider's HTTP shape.
//
// The three services disagree on almost everything except the existence of a
// chat endpoint, so the differences are modelled explicitly here rather than
// papered over with a lowest-common-denominator shape that would silently
// produce wrong requests for one of them.
type endpoint struct {
	// baseURL is the service root; the request path is appended.
	baseURL string
	// defaultModel is used when no model is configured.
	defaultModel string
	// buildRequest renders the JSON body and reports the auth header value.
	buildRequest func(model, system, user string) (body []byte, header string)
	// parseResponse extracts the assistant text from a reply.
	parseResponse func(data []byte) (string, error)
}

// chatCompletionsBody renders an OpenAI-compatible request. OpenRouter and
// OpenAI share this shape exactly.
func chatCompletionsBody(model, system, user string) ([]byte, string) {
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		// A temperature of zero and a low token ceiling keep the reply a
		// summary rather than an essay, and reduce run-to-run drift.
		"temperature": 0,
		"max_tokens":  maxTokens,
	}
	// The JSON encoder escapes HTML by default, which would mangle code
	// snippets in the summary; a summary is prose, so leave the characters
	// alone.
	data, err := marshalNoEscape(body)
	if err != nil {
		// The body is built from strings and floats, so encoding cannot fail.
		// Returning valid JSON here keeps the request function total.
		return []byte(`{}`), "Bearer "
	}
	return data, "Bearer "
}

// geminiBody renders a Gemini request, which nests content differently and
// authenticates with a query parameter rather than a header.
func geminiBody(model, system, user string) ([]byte, string) {
	body := map[string]any{
		// Gemini names the model in the path, so it is absent from the body.
		"systemInstruction": map[string]any{
			"parts": []map[string]string{{"text": system}},
		},
		"contents": []map[string]any{
			{
				"role":  "user",
				"parts": []map[string]string{{"text": user}},
			},
		},
		"generationConfig": map[string]any{
			"temperature":     0,
			"maxOutputTokens": maxTokens,
		},
	}
	data, err := marshalNoEscape(body)
	if err != nil {
		return []byte(`{}`), ""
	}
	return data, ""
}

// maxTokens caps the reply. A summary of a score delta does not need more, and
// an unbounded reply is an unbounded bill.
const maxTokens = 600

// providers maps each supported service to its endpoint description.
var providers = map[Provider]endpoint{
	ProviderOpenRouter: {
		baseURL:       "https://openrouter.ai/api/v1",
		defaultModel:  "anthropic/claude-3.5-sonnet",
		buildRequest:  chatCompletionsBody,
		parseResponse: parseChatCompletions,
	},
	ProviderOpenAI: {
		baseURL:       "https://api.openai.com/v1",
		defaultModel:  "gpt-4o-mini",
		buildRequest:  chatCompletionsBody,
		parseResponse: parseChatCompletions,
	},
	ProviderGemini: {
		baseURL:       "https://generativelanguage.googleapis.com/v1beta",
		defaultModel:  "gemini-1.5-flash",
		buildRequest:  geminiBody,
		parseResponse: parseGemini,
	},
}

// Config describes how to reach a provider.
type Config struct {
	// Provider names the service. Required.
	Provider Provider
	// APIKey authenticates the request. Never logged.
	APIKey string
	// Model overrides the provider's default.
	Model string
	// Timeout bounds the request. Zero uses DefaultTimeout.
	Timeout time.Duration
	// MaxRisks caps how many risks appear in the prompt. Zero uses 8.
	MaxRisks int
	// BaseURL overrides the provider's endpoint. Intended for a local proxy
	// or a test; empty uses the provider default.
	BaseURL string
	// HTTPClient overrides the transport. Nil uses a client with Timeout.
	HTTPClient *http.Client
}

// Resolve reads the provider configuration from the environment.
//
// It returns ErrNotConfigured when either the provider or the key is missing,
// because a provider with no key is not a usable configuration and calling out
// anyway would produce an authentication error from the remote service instead
// of a clear local message.
func Resolve(provider, model, keyEnv string) (Config, error) {
	if keyEnv == "" {
		keyEnv = DefaultKeyEnv
	}
	// The argument carries the flag or the config file. The environment
	// variable is the fallback for a user who never writes a config file.
	providerName := firstNonEmpty(provider, os.Getenv(DefaultProviderEnv))
	if providerName == "" {
		return Config{}, ErrNotConfigured
	}

	key := strings.TrimSpace(os.Getenv(keyEnv))
	if key == "" {
		return Config{}, fmt.Errorf(
			"%w: %s is set but %s is empty", ErrNotConfigured, DefaultProviderEnv, keyEnv)
	}

	p := Provider(strings.ToLower(strings.TrimSpace(providerName)))
	ep, ok := providers[p]
	if !ok {
		return Config{}, fmt.Errorf(
			"unknown AI provider %q (want openrouter, openai, or gemini)", providerName)
	}

	return Config{
		Provider: p,
		APIKey:   key,
		// The argument carries the flag or the config file, both of which are
		// more specific than a bare environment variable, so the argument
		// wins. This matches the precedence every other setting uses.
		Model: firstNonEmpty(model, os.Getenv(DefaultModelEnv), ep.defaultModel),
	}, nil
}

// firstNonEmpty returns the first non-empty trimmed value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// Explanation is a provider's summary plus the metadata needed to label it
// honestly in output.
type Explanation struct {
	// Text is the provider's prose.
	Text string
	// Provider and Model identify what produced it.
	Provider Provider
	Model    string
	// GeneratedAt is when the reply was received.
	GeneratedAt time.Time
	// Facts is the set of numeric figures the prompt supplied.
	//
	// It is retained so the caller can verify the response introduced
	// nothing new. It is not part of any serialization.
	Facts map[string]bool
}

// Explain asks the configured provider for a summary of snap.
//
// The returned Explanation carries the numbers the prompt contained so the
// caller can check the reply against them. A reply containing a figure that was
// not supplied is reported through ErrUnsupportedNumber rather than being
// passed on, because a summary that invents a measurement is exactly the
// failure this layer exists to avoid.
func Explain(ctx context.Context, cfg Config, snap *models.Snapshot, baseline *models.Snapshot) (*Explanation, error) {
	if snap == nil {
		return nil, errors.New("ai: nil snapshot")
	}
	ep, ok := providers[cfg.Provider]
	if !ok {
		return nil, fmt.Errorf("ai: unknown provider %q", cfg.Provider)
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("%w: the API key is empty", ErrNotConfigured)
	}

	system := systemMessage
	user, facts := BuildPrompt(snap, baseline, cfg.MaxRisks)

	model := firstNonEmpty(cfg.Model, ep.defaultModel)
	body, authPrefix := ep.buildRequest(model, system, user)

	url := firstNonEmpty(cfg.BaseURL, ep.baseURL)
	if cfg.Provider == ProviderGemini {
		url = fmt.Sprintf("%s/models/%s:generateContent", url, model)
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ai: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	switch cfg.Provider {
	case ProviderGemini:
		req.Header.Set("x-goog-api-key", cfg.APIKey)
	default:
		req.Header.Set("Authorization", authPrefix+cfg.APIKey)
		if cfg.Provider == ProviderOpenRouter {
			// OpenRouter attributes requests by project; the values here are
			// what identifies this tool, not who is using it.
			req.Header.Set("HTTP-Referer", "https://github.com/zelvior/lensyxe")
			req.Header.Set("X-Title", "Lensyxe")
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ai: request to %s: %w", cfg.Provider, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("ai: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ai: %s returned HTTP %d: %s",
			cfg.Provider, resp.StatusCode, providerErrorMessage(data))
	}

	text, err := ep.parseResponse(data)
	if err != nil {
		return nil, fmt.Errorf("ai: parse %s response: %w", cfg.Provider, err)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("ai: %s returned an empty summary", cfg.Provider)
	}

	if extra := unsupportedNumbers(text, facts); len(extra) > 0 {
		return nil, fmt.Errorf(
			"ai: the summary contains figures that were not measured (%s); "+
				"it is discarded rather than shown",
			strings.Join(extra, ", "))
	}

	return &Explanation{
		Text:        text,
		Provider:    cfg.Provider,
		Model:       model,
		GeneratedAt: time.Now().UTC(),
		Facts:       facts,
	}, nil
}

// systemMessage instructs the model on the one rule that matters.
//
// It is deliberately blunt and includes the prohibition explicitly, because a
// model asked to "analyze" code will happily produce analysis, and the whole
// value of this layer is that it does not.
const systemMessage = `You are summarizing a deterministic static-analysis report produced by Lensyxe.

STRICT RULES:
1. Use ONLY the numbers present in the input. Never compute, derive, estimate, round, or invent any figure.
2. Never introduce a metric that is not in the input, even if it seems relevant.
3. Do not speculate about causes. State only what the evidence shows.
4. Do not recommend specific tools, vendors, or refactoring techniques.
5. If the input lacks the information for a question, say the report does not cover it.

Write 2-4 sentences of plain prose for an engineering manager. Be specific and
concrete. Do not use headings, bullet points, or markdown.`

// BuildPrompt renders the deterministic facts and reports the numeric tokens it
// supplied.
//
// The prompt contains aggregate statistics and risk titles. It never contains
// source code, file contents, or any identifier that would let a provider
// reconstruct the repository: the summary is an executive narrative, and
// shipping the codebase to make one would defeat the local-first guarantee.
func BuildPrompt(snap *models.Snapshot, baseline *models.Snapshot, maxRisks int) (string, map[string]bool) {
	if maxRisks <= 0 {
		maxRisks = 8
	}
	facts := map[string]bool{}
	record := func(format string, args ...any) string {
		s := fmt.Sprintf(format, args...)
		for _, n := range numbersIn(s) {
			facts[n] = true
		}
		return s
	}

	var b strings.Builder
	b.WriteString("Repository: " + snap.Root + "\n")
	b.WriteString(record("Overall health score: %.1f/100 (grade %s)\n",
		snap.Health.Score, snap.Health.Grade))

	if baseline != nil {
		delta := snap.Health.Score - baseline.Health.Score
		dir := "unchanged"
		if delta > 0.05 {
			dir = "improved"
		} else if delta < -0.05 {
			dir = "declined"
		}
		b.WriteString(record(
			"Change since the previous recorded run: %.1f points (%s)\n", delta, dir))
	}

	// The component breakdown is the part a manager can actually act on.
	for _, m := range snap.Health.Metrics {
		if !m.Applicable {
			continue
		}
		b.WriteString(record("Component %s: %.1f/100 (weight %.0f%%) - %s\n",
			m.Label, m.Score, m.Weight*100, m.Detail))
	}

	b.WriteString(record("Files: %d scanned, %d source, %d test; %d code lines\n",
		snap.Code.Files, snap.Code.SourceFiles, snap.Code.TestFiles, snap.Code.CodeLines))
	b.WriteString(record("Test-to-code file ratio: %.0f%%\n", snap.Code.TestFileRatio*100))

	if snap.Git.IsRepository {
		b.WriteString(record(
			"Git window: %d commits by %d authors over %d days, bus factor %d, %d day(s) since the last commit\n",
			snap.Git.WindowCommits, snap.Git.Authors, snap.Git.WindowDays,
			snap.Git.BusFactor, snap.Git.DaysSinceCommit))
	}

	if snap.Dependencies.Detected {
		b.WriteString(record("Dependencies: %d declared (%d direct, %d dev, %d indirect)\n",
			snap.Dependencies.Total, snap.Dependencies.Direct,
			snap.Dependencies.Dev, snap.Dependencies.Indirect))
		if snap.Dependencies.Drift {
			b.WriteString("Some ecosystems have no lockfile, so versions are not reproducible.\n")
		}
	}

	confirmed := 0
	for _, h := range snap.Code.Hotspots {
		if h.Confirmed {
			confirmed++
		}
	}
	b.WriteString(record("Hotspots: %d candidates, %d confirmed on size, churn and complexity together\n",
		len(snap.Code.Hotspots), confirmed))

	// Risks are supplied worst-first and capped, so the model summarizes the
	// most consequential findings rather than an arbitrary slice.
	risks := make([]models.Risk, len(snap.Risks))
	copy(risks, snap.Risks)
	sortRisks(risks)

	counts := map[models.Severity]int{}
	for _, r := range risks {
		counts[r.Severity]++
	}
	b.WriteString(record("Risks detected: %d total (%d critical, %d high, %d medium, %d low)\n",
		len(risks), counts[models.SeverityCritical], counts[models.SeverityHigh],
		counts[models.SeverityMedium], counts[models.SeverityLow]))

	limit := len(risks)
	if limit > maxRisks {
		limit = maxRisks
	}
	for i := 0; i < limit; i++ {
		r := risks[i]
		line := fmt.Sprintf("- [%s] %s (score impact %.1f points): %s",
			r.Severity, r.Title, r.Impact, r.Detail)
		if r.Subject != "" {
			line += " [subject: " + r.Subject + "]"
		}
		b.WriteString(record("%s\n", line))
	}

	// A monorepo breakdown belongs in the prompt when it exists, otherwise a
	// summary of a workspace would describe only the aggregate.
	if snap.Workspace.Detected() {
		b.WriteString(record("\nWorkspace with %d packages (%s):\n",
			len(snap.Workspace.Packages), snap.Workspace.Kind))
		for _, p := range snap.Workspace.Packages {
			b.WriteString(record("- %s: %.1f/100, %d files, %d risks\n",
				p.Path, p.Health.Score, p.Files, len(p.Risks)))
		}
	}

	b.WriteString("\nWrite the summary using only these figures.")
	return b.String(), facts
}

// sortRisks orders risks worst-first by severity, then impact, then ID.
func sortRisks(risks []models.Risk) {
	for i := 1; i < len(risks); i++ {
		for j := i; j > 0 && riskLess(risks[j], risks[j-1]); j-- {
			risks[j], risks[j-1] = risks[j-1], risks[j]
		}
	}
}

func riskLess(a, b models.Risk) bool {
	if a.Severity != b.Severity {
		return a.Severity.Rank() > b.Severity.Rank()
	}
	if a.Impact != b.Impact {
		return a.Impact > b.Impact
	}
	return a.ID < b.ID
}

// numbersIn extracts the numeric tokens from a string.
//
// A decimal point followed by a digit is treated as part of the number, so
// "82.5" is one token rather than "82" and "5". Every token is normalized so
// the comparison against the supplied facts is not defeated by formatting: the
// fact "2" must match a reply that wrote "2.0".
func numbersIn(s string) []string {
	var (
		out   []string
		runes = []rune(s)
	)
	for i := 0; i < len(runes); {
		if !isDigit(runes[i]) {
			i++
			continue
		}
		start := i
		for i < len(runes) && isDigit(runes[i]) {
			i++
		}
		// A dot joins the number only when a digit follows it, so "v1.2.3"
		// reads as 1.2 and 3 rather than as one malformed token.
		if i+1 < len(runes) && runes[i] == '.' && isDigit(runes[i+1]) {
			i++ // step over the dot
			for i < len(runes) && isDigit(runes[i]) {
				i++
			}
		}
		// i now sits on the first rune past the token, so the slice is
		// half-open at i.
		out = append(out, normalizeNumber(string(runes[start:i])))
	}
	return out
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// normalizeNumber renders a numeric token so equal values compare equal,
// regardless of trailing zeros or a leading plus.
func normalizeNumber(tok string) string {
	tok = strings.TrimPrefix(tok, "+")
	if !strings.Contains(tok, ".") {
		return tok
	}
	trimmed := strings.TrimRight(tok, "0")
	trimmed = strings.TrimSuffix(trimmed, ".")
	if trimmed == "" || trimmed == "-" {
		return "0"
	}
	return trimmed
}

// unsupportedNumbers returns the numeric tokens in text that were not among the
// supplied facts.
//
// Small integers are exempt because they appear legitimately in ordinary prose
// ("three packages", "one week") and rejecting them would make the check
// useless. A figure large enough to be mistaken for a measurement is not.
func unsupportedNumbers(text string, facts map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, tok := range numbersIn(text) {
		if facts[tok] || seen[tok] {
			continue
		}
		// Ordinals and list positions in prose are not measurements.
		if isSmallOrdinal(tok) {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	sort.Strings(out)
	return out
}

// isSmallOrdinal reports whether a token is an integer below 20.
//
// Twenty is the cutoff because the prompt's own figures are all larger than
// that in practice (scores, counts, percentages, line counts), while genuine
// prose numbers ("two risks", "one week") sit below it.
func isSmallOrdinal(tok string) bool {
	if strings.Contains(tok, ".") {
		return false
	}
	v := 0
	for _, r := range tok {
		if !isDigit(r) {
			return false
		}
		v = v*10 + int(r-'0')
		if v > 20 {
			return false
		}
	}
	return true
}

// parseChatCompletions extracts the assistant message from an
// OpenAI-compatible reply.
func parseChatCompletions(data []byte) (string, error) {
	var reply struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			// Some providers return the text under `text` instead.
			Text string `json:"text"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return "", err
	}
	if reply.Error != nil && reply.Error.Message != "" {
		return "", fmt.Errorf("provider error: %s", reply.Error.Message)
	}
	if len(reply.Choices) == 0 {
		return "", fmt.Errorf("response contained no choices")
	}
	if content := reply.Choices[0].Message.Content; content != "" {
		return content, nil
	}
	return reply.Choices[0].Text, nil
}

// parseGemini extracts the first text part from a Gemini reply.
func parseGemini(data []byte) (string, error) {
	var reply struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		PromptFeedback *struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
	}
	if err := json.Unmarshal(data, &reply); err != nil {
		return "", err
	}
	if reply.Error != nil && reply.Error.Message != "" {
		return "", fmt.Errorf("provider error: %s", reply.Error.Message)
	}
	if reply.PromptFeedback != nil && reply.PromptFeedback.BlockReason != "" {
		return "", fmt.Errorf("request blocked: %s", reply.PromptFeedback.BlockReason)
	}
	if len(reply.Candidates) == 0 {
		return "", fmt.Errorf("response contained no candidates")
	}
	var b strings.Builder
	for _, p := range reply.Candidates[0].Content.Parts {
		b.WriteString(p.Text)
	}
	return b.String(), nil
}

// providerErrorMessage extracts the most useful line from an error body.
//
// A provider's raw error body is often an HTML error page from a proxy, in
// which case the whole body would be noise in a CLI. It is truncated either
// way.
func providerErrorMessage(data []byte) string {
	msg := strings.TrimSpace(string(data))
	if msg == "" {
		return "(empty response)"
	}
	if idx := strings.Index(msg, "\"message\""); idx >= 0 {
		var parsed struct {
			Message string `json:"message"`
			Error   struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &parsed); err == nil {
			if parsed.Error.Message != "" {
				return parsed.Error.Message
			}
			if parsed.Message != "" {
				return parsed.Message
			}
		}
	}
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

// marshalNoEscape encodes v without HTML escaping.
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
