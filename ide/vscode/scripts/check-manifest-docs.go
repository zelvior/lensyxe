// Cross-checks the extension manifest against its documentation.
//
// A settings table in a README drifts from package.json the moment either one is
// edited alone, and nothing in the build notices. This asserts they agree.
//
// Run from the repository root:
//
//	go run ide/vscode/scripts/check-manifest-docs.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// manifestDoc is the subset of the extension manifest this needs. Decoding the
// whole thing into a map and reaching in would avoid a struct that has to track
// upstream additions; decoding only what is read keeps this from becoming a
// second, stale copy of the manifest.
type manifestDoc struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Publisher string `json:"publisher"`
	Main      string `json:"main"`
	Icon      string `json:"icon"`
	Engines   struct {
		VSCode string `json:"vscode"`
	} `json:"engines"`
	Scripts     map[string]string `json:"scripts"`
	Contributes struct {
		Commands []struct {
			Command string `json:"command"`
			Title   string `json:"title"`
		} `json:"commands"`
		Configuration struct {
			Properties map[string]struct {
				Type         string `json:"type"`
				Default      any    `json:"default"`
				MarkdownDesc string `json:"markdownDescription"`
			} `json:"properties"`
		} `json:"configuration"`
	} `json:"contributes"`
}

func main() {
	manifestPath := filepath.Join("ide", "vscode", "package.json")
	readmePath := filepath.Join("ide", "vscode", "README.md")
	docPath := filepath.Join("docs", "VSCODE_EXTENSION.md")

	var problems []string
	note := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	// Whether the extension has been compiled in this working tree. It has not
	// been on a fresh clone, and the checks below that depend on generated output
	// are skipped until it has. See the main-entrypoint check for why.
	builtDirExists := false
	if entries, err := os.ReadDir(filepath.Join("ide", "vscode", "out")); err == nil {
		builtDirExists = len(entries) > 0
	}

	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		fail(err)
	}

	var m manifestDoc
	// DisallowUnknownFields is deliberately off: a published manifest carries
	// fields this check has no opinion about, and failing on them would make the
	// check a nuisance rather than a guard. What it verifies is agreement between
	// two documents, not manifest validity, which vsce and tsc already do.
	if err := json.Unmarshal(raw, &m); err != nil {
		fail(err)
	}

	readme := read(readmePath)
	doc := read(docPath)

	// ------------------------------------------------- settings agreement
	settings := sortedKeys(m.Contributes.Configuration.Properties)

	if len(settings) == 0 {
		note("manifest declares no configuration settings")
	}

	// Every setting must be documented, and nothing may be documented that does
	// not exist. The second direction is the one that matters: a removed setting
	// leaves a row in the README that tells a user to change something that no
	// longer has any effect.
	for _, key := range settings {
		for _, doc := range []struct {
			name string
			text string
		}{{"README.md", readme}, {"docs/VSCODE_EXTENSION.md", doc}} {
			if !strings.Contains(doc.text, "`"+key+"`") {
				note("%s does not document the %s setting", doc.name, key)
			}
		}
	}

	// Settings named in a documentation table row, and only there.
	//
	// The first column of a settings row is the setting id. Matching the whole
	// document would also catch command ids and file names, because a command and
	// a setting can share an id (`lensyxe.analyze` is a command, not a setting),
	// and those are checked separately.
	for _, d := range []struct {
		name string
		text string
	}{{"README.md", readme}, {"docs/VSCODE_EXTENSION.md", doc}} {
		for _, key := range settingsInTables(d.text) {
			if !slices.Contains(settings, key) {
				note("%s documents %s as a setting, which the manifest does not declare", d.name, key)
			}
		}
	}

	// ------------------------------------------- defaults agree with the text
	for _, key := range settings {
		want := defaultText(m.Contributes.Configuration.Properties[key].Default)
		if want == "" {
			continue
		}
		// Only the long-form guide states defaults in a table; the README points
		// at it. Checking the guide alone avoids demanding the number twice.
		if !strings.Contains(doc, "`"+key+"` | `"+want+"`") &&
			!strings.Contains(doc, "| `"+key+"` | "+want+" |") {
			note("docs/VSCODE_EXTENSION.md does not state the default %s for %s", want, key)
		}
	}

	// --------------------------------------------------- commands agreement
	for _, cmd := range m.Contributes.Commands {
		if !strings.Contains(doc, cmd.Title) && !strings.Contains(readme, cmd.Title) {
			note("command %s (%s) is documented under neither name", cmd.Title, cmd.Command)
		}
	}

	// ------------------------------------------------- packaging references
	if m.Icon == "" {
		note("manifest declares no icon, so the marketplace listing will use a placeholder")
	} else {
		p := filepath.Join("ide", "vscode", filepath.FromSlash(m.Icon))
		if _, err := os.Stat(p); err != nil {
			note("manifest icon %s does not exist", m.Icon)
		}
	}

	if m.Main == "" {
		note("manifest declares no main entrypoint")
	} else if _, err := os.Stat(filepath.Join("ide", "vscode", filepath.FromSlash(m.Main))); err != nil {
		// Report a missing entrypoint only once the extension has been built.
		//
		// out/ is generated and gitignored, so the entrypoint is absent from a
		// fresh clone. Reporting that unconditionally made this validator fail
		// on a clean tree -- exactly the state it exists to pass -- because CI's
		// matrix jobs run `go test ./...` before the dedicated extension job that
		// does `npm ci && npm run compile`.
		//
		// Gating on the existence of out/ keeps the check meaningful instead of
		// dropping it: the dedicated job builds the extension and then re-runs
		// this validator, at which point out/ exists and a genuinely missing or
		// misnamed entrypoint is still reported. Before that point the file
		// cannot be built yet, so its absence says nothing about the manifest.
		if builtDirExists {
			note("manifest main %s is not built yet (run npm run compile)", m.Main)
		}
	}

	for _, license := range []string{"LICENSE"} {
		if _, err := os.Stat(filepath.Join("ide", "vscode", license)); err != nil {
			note("%s is missing from the extension root; vsce warns and the package ships unlicensed", license)
		}
	}

	// The version in the filename documented in the README must be the real one.
	vsix := fmt.Sprintf("%s-%s.vsix", m.Name, m.Version)
	if !strings.Contains(readme, vsix) {
		note("README.md does not reference the packaged artifact name %s", vsix)
	}

	// ---------------------------------------- the guide must not lie about it
	// The install command in the docs has to name the extension id that vsce
	// will actually publish, which is publisher.name.
	id := m.Publisher + "." + m.Name
	if strings.Contains(doc, "code --uninstall-extension") &&
		!strings.Contains(doc, id) {
		note("docs/VSCODE_EXTENSION.md shows an uninstall command that does not name %s", id)
	}

	if len(problems) == 0 {
		fmt.Printf("ok   %d setting(s), %d command(s), extension id %s\n",
			len(settings), len(m.Contributes.Commands), id)
		return
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "problem:", p)
	}
	fmt.Fprintf(os.Stderr, "\n%d problem(s)\n", len(problems))
	os.Exit(1)
}

// settingsTable finds the settings table and returns the ids in its first column.
//
// The table is located by its header rather than by scanning every row of the
// document. The commands table has the same shape and the same `lensyxe.*` ids in
// the same column position, so a document-wide row scan cannot tell the two apart
// and reports every command as a phantom setting.
var settingsHeader = regexp.MustCompile(`(?m)^\|[^|]*\bSetting\b[^|]*\|`)

// rowFirstColumnPattern captures the first cell of a Markdown table row.
var rowFirstColumnPattern = regexp.MustCompile("(?m)^\\|\\s*`([^`]+)`\\s*\\|")

// settingsInTables returns every id appearing in the first column of the settings
// table. The table extends from its header to the first line that is neither a
// table row nor blank.
func settingsInTables(text string) []string {
	loc := settingsHeader.FindStringIndex(text)
	if loc == nil {
		return nil
	}

	var out []string
	rest := text[loc[0]:]
	for i, line := range strings.Split(rest, "\n") {
		trimmed := strings.TrimSpace(line)
		// A blank line is tolerated inside the table because Markdown renderers
		// and formatters both insert them; anything non-tabular ends it.
		if trimmed == "" {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			// Stop at the first prose line, but only after collecting rows.
			if i > 0 {
				break
			}
			continue
		}
		if m := rowFirstColumnPattern.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// defaultText renders a JSON default the way the documentation table writes it.
func defaultText(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		// JSON numbers arrive as float64. Format without a trailing ".0" so 750
		// reads as 750 rather than 750.000000.
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	default:
		return fmt.Sprintf("%v", t)
	}
}

func read(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		fail(err)
	}
	return string(b)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "check-manifest-docs:", err)
	os.Exit(1)
}
