package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
