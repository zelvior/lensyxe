package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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

// ---------------------------------------------------------------- Resolve

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

// ---------------------------------------------------------------- Prompt

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

// ---------------------------------------------------------- number handling

func TestNumbersIn(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"no digits here", nil},
		{"score 82.5 of 100", []string{"82.5", "100"}},
		{"v1.2.3 build", []string{"1.2", "3"}},
		{"1,234 lines", []string{"1", "234"}},
		{"a.b.c", nil},
		{"ratio 0.25", []string{"0.25"}},
		{"trailing 5.", []string{"5"}},
		{"-3 delta", []string{"3"}},
	}
	for _, c := range cases {
		got := numbersIn(c.in)
		if len(got) != len(c.want) {
			t.Errorf("numbersIn(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("numbersIn(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

// Equal values must compare equal regardless of formatting, or the fabrication
// guard would reject a model that correctly restated "80" as "80.0".
func TestNumberNormalization(t *testing.T) {
	facts := map[string]bool{"80": true, "2": true, "0": true}

	for _, text := range []string{
		"the score is 80",
		"the score is 80.0",
		"the score is +80",
	} {
		if extra := unsupportedNumbers(text, facts); len(extra) > 0 {
			t.Errorf("%q should be accepted, rejected %v", text, extra)
		}
	}

	if extra := unsupportedNumbers("the score is 81", facts); len(extra) != 1 || extra[0] != "81" {
		t.Errorf("a genuinely different figure must be rejected, got %v", extra)
	}
}

// Small integers appear in ordinary prose. Rejecting them would make the guard
// useless, because every legitimate summary contains some.
func TestSmallIntegersAreNotTreatedAsMeasurements(t *testing.T) {
	facts := map[string]bool{}
	for _, text := range []string{
		"two risks were found in one package",
		"three components, one of which is weakest",
		"a single team owns it",
	} {
		if extra := unsupportedNumbers(text, facts); len(extra) > 0 {
			t.Errorf("%q should be accepted with no facts supplied, got %v", text, extra)
		}
	}
}

func TestUnsupportedNumbersReportsSortedUnique(t *testing.T) {
	got := unsupportedNumbers("about 95 and 42 and 95 and 7", map[string]bool{})
	if len(got) != 2 || got[0] != "42" || got[1] != "95" {
		t.Errorf("got %v, want [42 95]", got)
	}
}

// -------------------------------------------------------------- Explain

// responder is a fake provider endpoint.
type responder struct {
	t        *testing.T
	status   int
	body     string
	seenPath string
	seenBody []byte
	seenHead http.Header
}

func (r *responder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.seenPath = req.URL.Path
	r.seenHead = req.Header.Clone()
	buf := make([]byte, 1<<20)
	n, _ := req.Body.Read(buf)
	r.seenBody = buf[:n]
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(r.status)
	_, _ = w.Write([]byte(r.body))
}

func chatReply(text string) string {
	return `{"choices":[{"message":{"content":` + mustJSON(text) + `}}]}`
}

func geminiReply(text string) string {
	return `{"candidates":[{"content":{"parts":[{"text":` + mustJSON(text) + `}]}}]}`
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func testConfig(t *testing.T, base string) Config {
	t.Helper()
	return Config{
		Provider:   ProviderOpenAI,
		APIKey:     "sk-test",
		BaseURL:    base,
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	}
}

func TestExplainOpenAI(t *testing.T) {
	srv := &responder{t: t, status: 200, body: chatReply(
		"The repository scores 82.5 out of 100, with two risks of high consequence.")}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	exp, err := Explain(context.Background(), testConfig(t, ts.URL), snapshot(), nil)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if !strings.Contains(exp.Text, "82.5") {
		t.Errorf("Text = %q", exp.Text)
	}
	if exp.Provider != ProviderOpenAI {
		t.Errorf("Provider = %q", exp.Provider)
	}
	if exp.GeneratedAt.IsZero() {
		t.Error("GeneratedAt must be set")
	}

	// The request must authenticate and carry both messages.
	if got := srv.seenHead.Get("Authorization"); got != "Bearer sk-test" {
		t.Errorf("Authorization = %q", got)
	}
	if !strings.Contains(string(srv.seenBody), `"role":"system"`) {
		t.Errorf("the system instruction must be sent:\n%s", srv.seenBody)
	}
	if !strings.Contains(string(srv.seenBody), "82.5") {
		t.Errorf("the prompt must carry the measured score:\n%s", srv.seenBody)
	}
}

func TestExplainGemini(t *testing.T) {
	srv := &responder{t: t, status: 200, body: geminiReply("The score is 82.5.")}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	cfg := testConfig(t, ts.URL)
	cfg.Provider = ProviderGemini

	if _, err := Explain(context.Background(), cfg, snapshot(), nil); err != nil {
		t.Fatalf("Explain: %v", err)
	}

	// Gemini names the model in the path and authenticates with a header, not
	// a bearer token. Getting this wrong is the single most likely way to
	// break one provider while the other two work.
	if !strings.Contains(srv.seenPath, "generateContent") {
		t.Errorf("path = %q, want a generateContent URL", srv.seenPath)
	}
	if !strings.Contains(srv.seenPath, cfg.Model) {
		t.Errorf("path = %q, want the model in the path", srv.seenPath)
	}
	if got := srv.seenHead.Get("x-goog-api-key"); got != "sk-test" {
		t.Errorf("x-goog-api-key = %q", got)
	}
	if got := srv.seenHead.Get("Authorization"); got != "" {
		t.Errorf("Gemini must not send a bearer token, got %q", got)
	}
	if !strings.Contains(string(srv.seenBody), "systemInstruction") {
		t.Errorf("Gemini nests the system prompt:\n%s", srv.seenBody)
	}
}

func TestExplainOpenRouterSendsAttribution(t *testing.T) {
	srv := &responder{t: t, status: 200, body: chatReply("Fine.")}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	cfg := testConfig(t, ts.URL)
	cfg.Provider = ProviderOpenRouter
	if _, err := Explain(context.Background(), cfg, snapshot(), nil); err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if srv.seenHead.Get("HTTP-Referer") == "" {
		t.Error("OpenRouter expects a Referer header")
	}
}

// The core guarantee: a reply containing a figure the engine never measured is
// discarded, not displayed.
func TestExplainRejectsFabricatedFigures(t *testing.T) {
	srv := &responder{t: t, status: 200, body: chatReply(
		"The repository scores 82.5 and has 4371 files with 91.2% coverage.")}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	_, err := Explain(context.Background(), testConfig(t, ts.URL), snapshot(), nil)
	if err == nil {
		t.Fatal("a fabricated figure must be rejected")
	}
	if !strings.Contains(err.Error(), "4371") {
		t.Errorf("the error must name the offending figure, got %q", err)
	}
	// The wording must make clear the text was discarded rather than shown.
	if !strings.Contains(err.Error(), "discarded") {
		t.Errorf("the error must say the summary was discarded, got %q", err)
	}
}

// A summary using only supplied figures is accepted even when phrased oddly.
func TestExplainAcceptsRestatedFigures(t *testing.T) {
	srv := &responder{t: t, status: 200, body: chatReply(
		"At 80.0 points on code and 86.5 on dependencies, the repository scores 82.5.")}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	if _, err := Explain(context.Background(), testConfig(t, ts.URL), snapshot(), nil); err != nil {
		t.Fatalf("a summary restating measured figures must be accepted: %v", err)
	}
}

func TestExplainProviderErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `{"error":{"message":"invalid api key"}}`},
		{"rate limited", 429, `{"error":{"message":"slow down"}}`},
		{"server error", 500, `internal error`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := &responder{t: t, status: c.status, body: c.body}
			ts := httptest.NewServer(srv)
			defer ts.Close()

			_, err := Explain(context.Background(), testConfig(t, ts.URL), snapshot(), nil)
			if err == nil {
				t.Fatal("an HTTP error must be reported")
			}
			// The provider's own message is far more useful than the status code.
			if c.status != 500 && !strings.Contains(err.Error(), "slow down") &&
				!strings.Contains(err.Error(), "invalid api key") {
				t.Errorf("the provider message must survive, got %q", err)
			}
		})
	}
}

func TestExplainEmptyResponse(t *testing.T) {
	srv := &responder{t: t, status: 200, body: chatReply("   ")}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	if _, err := Explain(context.Background(), testConfig(t, ts.URL), snapshot(), nil); err == nil {
		t.Error("an empty summary must be reported rather than shown blank")
	}
}

func TestExplainNilSnapshot(t *testing.T) {
	if _, err := Explain(context.Background(), Config{Provider: ProviderOpenAI, APIKey: "k"}, nil, nil); err == nil {
		t.Error("a nil snapshot must be rejected before any request")
	}
}

func TestExplainUnknownProvider(t *testing.T) {
	cfg := Config{Provider: "nope", APIKey: "k"}
	if _, err := Explain(context.Background(), cfg, snapshot(), nil); err == nil {
		t.Error("an unknown provider must be rejected")
	}
}

func TestExplainMissingKey(t *testing.T) {
	cfg := Config{Provider: ProviderOpenAI}
	if _, err := Explain(context.Background(), cfg, snapshot(), nil); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}

// A blocked request is a refusal, not a parse failure, and must say so.
func TestExplainBlockedRequest(t *testing.T) {
	srv := &responder{t: t, status: 200,
		body: `{"promptFeedback":{"blockReason":"SAFETY"}}`}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	cfg := testConfig(t, ts.URL)
	cfg.Provider = ProviderGemini
	_, err := Explain(context.Background(), cfg, snapshot(), nil)
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("err = %v, want a blocked-request error", err)
	}
}

// Cancellation must propagate: the CLI's Ctrl+C has to reach the request.
func TestExplainRespectsContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Explain(ctx, testConfig(t, srv.URL), snapshot(), nil); err == nil {
		t.Error("a cancelled context must abort the request")
	}
}

func TestParseChatCompletionsVariants(t *testing.T) {
	// Some gateways return `text` rather than `message.content`.
	got, err := parseChatCompletions([]byte(`{"choices":[{"text":"hello"}]}`))
	if err != nil || got != "hello" {
		t.Errorf("got %q, %v", got, err)
	}
	if _, err := parseChatCompletions([]byte(`{"choices":[]}`)); err == nil {
		t.Error("an empty choices array must be reported")
	}
	if _, err := parseChatCompletions([]byte(`not json`)); err == nil {
		t.Error("malformed JSON must be reported")
	}
}

func TestProviderErrorMessage(t *testing.T) {
	if got := providerErrorMessage(nil); got == "" {
		t.Error("an empty body must still produce a message")
	}
	if got := providerErrorMessage([]byte(`{"error":{"message":"quota exceeded"}}`)); !strings.Contains(got, "quota exceeded") {
		t.Errorf("got %q", got)
	}
	// A proxy returning HTML must not dump a page into a terminal.
	long := providerErrorMessage([]byte(strings.Repeat("<html>error</html>", 100)))
	if len(long) > 220 {
		t.Errorf("a huge error body must be truncated, got %d bytes", len(long))
	}
}

// The API key must never appear in an error message. An error is the most
// likely string to end up pasted into a bug report.
func TestErrorsNeverLeakTheKey(t *testing.T) {
	srv := &responder{t: t, status: 401, body: `{"error":{"message":"invalid api key"}}`}
	ts := httptest.NewServer(srv)
	defer ts.Close()

	cfg := testConfig(t, ts.URL)
	_, err := Explain(context.Background(), cfg, snapshot(), nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), cfg.APIKey) {
		t.Errorf("the API key leaked into the error: %q", err)
	}
}

// The generated prompt must be stable, so a diff between two runs of the same
// snapshot shows nothing.
func TestBuildPromptIsDeterministic(t *testing.T) {
	snap := snapshot()
	first, factsA := BuildPrompt(snap, nil, 8)
	for i := 0; i < 5; i++ {
		got, factsB := BuildPrompt(snap, nil, 8)
		if got != first {
			t.Fatalf("run %d differs:\n%s\n---\n%s", i, first, got)
		}
		if len(factsA) != len(factsB) {
			t.Fatalf("run %d recorded a different fact set", i)
		}
	}
}

func TestEnvironmentIsNotRequired(t *testing.T) {
	// The package must be importable and testable without touching the process
	// environment beyond the variables under test.
	t.Setenv(DefaultKeyEnv, "")
	if os.Getenv(DefaultKeyEnv) != "" {
		t.Fatal("t.Setenv failed to isolate the environment")
	}
}
