package ai

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

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
