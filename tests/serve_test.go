package tests

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The server must start, answer the API, and shut down. It binds loopback on
// an ephemeral port, so this does not collide with anything.
func TestServeAPI(t *testing.T) {
	if testing.Short() {
		t.Skip("serve starts a process; skipped under -short")
	}

	dir := t.TempDir()
	copyTree(t, filepath.Join("..", "examples", "healthy-go"), dir)

	port := freePort(t)
	cmd := exec.Command(cli(t), "serve", ".", "--port", itoa(port))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LENSYXE_AI_KEY=", "LENSYXE_AI_PROVIDER=")

	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	base := "http://127.0.0.1:" + itoa(port)
	if !waitForHTTP(base+"/api/v1/version", 30*time.Second) {
		t.Fatalf("the server did not become ready\nstderr:\n%s", stderr.String())
	}

	// Every documented endpoint must answer, and each must return valid JSON
	// with the fields the dashboard reads.
	for _, tc := range []struct {
		path   string
		fields []string
	}{
		{"/api/v1/health", []string{"root", "health", "code", "git", "dependencies"}},
		{"/api/v1/risks", []string{"total", "risks"}},
		{"/api/v1/hotspots", []string{"total", "hotspots", "churn"}},
		{"/api/v1/history", []string{"count", "records"}},
		{"/api/v1/version", []string{"version", "schema_version"}},
	} {
		body, code := httpGet(t, base+tc.path)
		if code != 200 {
			t.Errorf("%s returned %d: %s", tc.path, code, truncate(body, 300))
			continue
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &doc); err != nil {
			t.Errorf("%s did not return JSON: %v", tc.path, err)
			continue
		}
		for _, f := range tc.fields {
			if _, ok := doc[f]; !ok {
				t.Errorf("%s is missing the %q field the dashboard reads", tc.path, f)
			}
		}
	}
}

// A dashboard route must not be answered with the API's JSON, and vice versa.
func TestServeDoesNotConfuseJSONWithHTML(t *testing.T) {
	if testing.Short() {
		t.Skip("serve starts a process; skipped under -short")
	}

	dir := t.TempDir()
	copyTree(t, filepath.Join("..", "examples", "healthy-go"), dir)

	port := freePort(t)
	cmd := exec.Command(cli(t), "serve", ".", "--port", itoa(port))
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	base := "http://127.0.0.1:" + itoa(port)
	if !waitForHTTP(base+"/api/v1/version", 30*time.Second) {
		t.Fatalf("the server did not become ready\n%s", stderr.String())
	}

	// An unknown API path must be a JSON error, not the dashboard. Returning
	// index.html with a 200 would break every API client silently.
	body, code := httpGet(t, base+"/api/v1/nonexistent")
	if code == 200 {
		t.Errorf("an unknown API path returned 200: %s", truncate(body, 200))
	}
	if strings.Contains(body, "<!doctype html") {
		t.Error("an API path was answered with the dashboard HTML")
	}
}
