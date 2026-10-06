package tests

import (
	"io"
	"net/http"
	"testing"
	"time"
)

// httpGet fetches a URL and returns the body and status.
//
// The client carries its own timeout rather than relying on a default, because
// a hung server in a test must fail the test rather than hang the suite until
// the package timeout.
func httpGet(t testing.TB, url string) (string, int) {
	t.Helper()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err.Error(), 0
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err.Error(), resp.StatusCode
	}
	return string(body), resp.StatusCode
}

// waitForHTTP polls a URL until it answers or the budget runs out.
//
// Polling rather than sleeping a fixed interval: the server usually comes up in
// well under a second, so a fixed sleep would add dead time to every run while
// still being flaky on a loaded machine.
func waitForHTTP(url string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	client := &http.Client{Timeout: 2 * time.Second}

	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			// Anything below 500 means the server is serving. A 404 is a
			// correct answer for a probe hitting an unknown path; what matters
			// is that the process is up and routing.
			if resp.StatusCode < 500 {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
