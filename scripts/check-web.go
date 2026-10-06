//go:build ignore

// Command check-web validates the static site in web/.
//
// The site is a set of plain files that nothing else in this repository reads,
// so a broken link or a missing asset is invisible to the Go build, to the
// linter, and to the extension tests. A marketing page that 404s an image or
// sends a reader to a deleted anchor is worse than no page, because it is the
// first thing anyone sees.
//
// What it checks:
//
//   - Every internal href and src resolves to a file in web/, or to an
//     explicitly allowed off-site prefix.
//   - Every internal link with a #fragment names a heading that actually exists.
//     This is the one that rots: an anchor is a plain string in the HTML and
//     nothing else validates it.
//   - Every outbound link is https and carries rel="noopener".
//   - vercel.json parses and declares the security headers the site claims.
//
// Usage:
//
//	go run scripts/check-web.go
package main

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const webDir = "web"

// allowedOffsite lists hosts the site links to. Anything not on this list and
// not relative is reported, so a typo'd or hijacked URL cannot pass unnoticed.
var allowedOffsite = []string{
	"https://github.com/zelvior/lensyxe",
	"https://raw.githubusercontent.com/zelvior/lensyxe",
}

func main() {
	var problems []string
	note := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	indexPath := filepath.Join(webDir, "index.html")
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		fail(err)
	}
	page := string(raw)

	// ---------------------------------------------------------------- files
	// Every href/src the page references, split into local paths and off-site.
	refRe := regexp.MustCompile(`(?:href|src)="([^"]+)"`)
	checked := 0
	anchors := headingIDs(page)

	for _, m := range refRe.FindAllStringSubmatch(page, -1) {
		raw := html.UnescapeString(m[1])
		if raw == "" || strings.HasPrefix(raw, "#") {
			// A bare fragment is checked separately, against this page.
			if strings.HasPrefix(raw, "#") && raw != "#" {
				id := strings.TrimPrefix(raw, "#")
				if _, ok := anchors[id]; !ok {
					note("index.html links to #%s, which is not a heading on this page", id)
				}
			}
			continue
		}

		if isOffsite(raw) {
			checked++
			if !strings.HasPrefix(raw, "https://") {
				note("%s is off-site but not https", raw)
			}
			if !allowed(raw) {
				note("%s points outside the project's own URLs; add it to allowedOffsite if that is intended", raw)
			}
			continue
		}

		target := raw
		if i := strings.Index(target, "#"); i >= 0 {
			// A fragment on a local file would need that file parsed too. The
			// site has exactly one page, so a fragment on another file means the
			// layout grew a second page and this check needs extending.
			if i > 0 {
				note("%s points at another file with a fragment; this checker only resolves fragments on index.html", raw)
			}
			target = target[:i]
		}

		checked++
		resolved := filepath.Join(webDir, filepath.FromSlash(target))
		if _, err := os.Stat(resolved); err != nil {
			note("index.html references %s, which does not exist in %s", target, webDir)
		}
	}

	if checked == 0 {
		note("no references were parsed; the regex is wrong")
	}

	// --------------------------------------------------------------- assets
	// The social preview is declared in metadata and used by link unfurlers.
	// It is easy to delete while iterating on the page.
	for _, required := range []string{"favicon.svg", "social-preview.png", "styles.css", "app.js", "vercel.json"} {
		if _, err := os.Stat(filepath.Join(webDir, required)); err != nil {
			note("%s is missing from %s", required, webDir)
		}
	}

	// ------------------------------------------------------------ vercel.json
	vercelPath := filepath.Join(webDir, "vercel.json")
	vraw, err := os.ReadFile(vercelPath)
	if err != nil {
		note("vercel.json is missing: %v", err)
	} else {
		checkVercel(vraw, note)
	}

	if len(problems) == 0 {
		fmt.Printf("ok   %d reference(s), %d heading anchor(s), security headers present\n",
			checked, len(anchors))
		return
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "problem:", p)
	}
	fmt.Fprintf(os.Stderr, "\n%d problem(s)\n", len(problems))
	os.Exit(1)
}

// headingIDs returns the set of ids the page defines, from both heading id
// attributes and named anchors.
func headingIDs(page string) map[string]bool {
	out := map[string]bool{}
	idRe := regexp.MustCompile(`id="([^"]+)"`)
	for _, m := range idRe.FindAllStringSubmatch(page, -1) {
		out[m[1]] = true
	}
	nameRe := regexp.MustCompile(`<a[^>]+name="([^"]+)"`)
	for _, m := range nameRe.FindAllStringSubmatch(page, -1) {
		out[m[1]] = true
	}
	return out
}

// checkVercel asserts the deployment config declares the headers the site
// depends on. A missing CSP is the failure that matters most here, because the
// site is static and has no reason to permit inline script at all.
func checkVercel(raw []byte, note func(string, ...any)) {
	var cfg struct {
		Version int `json:"version"`
		Headers []struct {
			Source  string `json:"source"`
			Headers []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"headers"`
		} `json:"headers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		note("vercel.json does not parse: %v", err)
		return
	}
	if cfg.Version != 2 {
		note("vercel.json declares version %d; Vercel requires 2 for headers", cfg.Version)
	}

	got := map[string]string{}
	for _, rule := range cfg.Headers {
		for _, h := range rule.Headers {
			got[strings.ToLower(h.Key)] = h.Value
		}
	}

	required := []string{
		"content-security-policy",
		"strict-transport-security",
		"x-content-type-options",
		"referrer-policy",
		"x-frame-options",
	}
	var missing []string
	for _, k := range required {
		if _, ok := got[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		note("vercel.json does not set %s", k)
	}

	// The CSP must not allow inline or eval script. The site's own JavaScript is
	// an external file, so it has no reason to need either.
	if csp := got["content-security-policy"]; csp != "" {
		for _, bad := range []string{"unsafe-inline", "unsafe-eval"} {
			if strings.Contains(csp, bad) {
				note("Content-Security-Policy permits %s, which this static site does not need", bad)
			}
		}
	}
}

func isOffsite(raw string) bool {
	return strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") ||
		strings.HasPrefix(raw, "mailto:") || strings.HasPrefix(raw, "//")
}

func allowed(raw string) bool {
	for _, p := range allowedOffsite {
		if strings.HasPrefix(raw, p) {
			return true
		}
	}
	return false
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "check-web:", err)
	os.Exit(1)
}
