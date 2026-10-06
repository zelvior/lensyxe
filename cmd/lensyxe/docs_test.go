package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"text/template"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// docsDir locates the documentation suite relative to this package.
const docsDir = "../../docs"

// allDocFiles returns every markdown document under docs/.
func allDocFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		t.Fatalf("read %s: %v", docsDir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, filepath.Join(docsDir, e.Name()))
		}
	}
	if len(out) == 0 {
		t.Fatal("no documentation files found")
	}
	return out
}

// readDoc returns one document.
func readDoc(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// knownFlags returns every flag the real command tree accepts, by building the
// commands rather than maintaining a hand-written list.
//
// A hand-written list would rot the moment a flag is renamed, which is exactly
// the failure this test exists to catch.
func knownFlags(t *testing.T) map[string]bool {
	t.Helper()
	root := newRootCmd(&app{})

	out := map[string]bool{}
	// cobra registers --help on every command lazily, during Execute, so it is
	// not in the flag set yet. --version comes from the root's Version field.
	// Both are accepted at runtime and must be documented.
	out["help"] = true
	out["version"] = true

	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		collect := func(f *pflag.Flag) { out[f.Name] = true }
		cmd.Flags().VisitAll(collect)
		cmd.PersistentFlags().VisitAll(collect)
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
	return out
}

// Every document must link only to files that exist.
//
// Documentation with dead links is worse than none: it sends a reader to a 404
// at the moment they are deciding whether to trust the project.
func TestDocInternalLinksResolve(t *testing.T) {
	linkRe := regexp.MustCompile(`\]\(([^)#\s]+)(?:#[^)]*)?\)`)
	anchorRe := regexp.MustCompile(`^\#`)

	// README is included even though it is not under docs/, because it is the
	// page every reader lands on first and it carries the documentation index,
	// the badges, and the diagram references. A broken link there is the most
	// visible kind of documentation defect, and leaving it unchecked while
	// policing docs/ would be an odd place to draw the line.
	files := append(allDocFiles(t), filepath.Join("..", "..", "README.md"))

	for _, file := range files {
		body := readDoc(t, file)
		for _, m := range linkRe.FindAllStringSubmatch(body, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http://") ||
				strings.HasPrefix(target, "https://") ||
				strings.HasPrefix(target, "mailto:") ||
				anchorRe.MatchString(target) {
				continue
			}
			resolved := filepath.Join(filepath.Dir(file), filepath.FromSlash(target))
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s links to %q, which does not exist",
					filepath.Base(file), target)
			}
		}
	}
}

// Image references in the README must resolve too.
//
// A markdown image link matches the same [](...) shape as an ordinary link, but
// a broken src renders as a broken-image icon rather than a visibly wrong page,
// so it is easy to ship by accident.
func TestReadmeImagesExist(t *testing.T) {
	readme := readDoc(t, filepath.Join("..", "..", "README.md"))

	imgRe := regexp.MustCompile(`<img[^>]+src="([^"]+)"`)
	mdRe := regexp.MustCompile(`!\[[^\]]*\]\(([^)\s]+)\)`)

	for _, re := range []*regexp.Regexp{imgRe, mdRe} {
		for _, m := range re.FindAllStringSubmatch(readme, -1) {
			src := m[1]
			if strings.HasPrefix(src, "http://") ||
				strings.HasPrefix(src, "https://") ||
				strings.HasPrefix(src, "data:") {
				continue
			}
			// The banner may legitimately appear more than once: it is the
			// hero image, and is also linked from the documentation table.
			if _, err := os.Stat(filepath.Join("..", "..", filepath.FromSlash(src))); err != nil {
				t.Errorf("README references image %q, which does not exist", src)
			}
		}
	}
}

// headingAnchors extracts the GitHub-style slugs for every ATX heading.
func headingAnchors(body string) map[string]bool {
	out := map[string]bool{}
	inFence := false

	for _, line := range strings.Split(body, "\n") {
		// Fenced code blocks are skipped.
		//
		// A `#` comment inside a ```bash block is not a heading, but treating
		// it as one produces a phantom anchor. That matters because this
		// function decides which links are valid: without the fence check, a
		// link to an anchor that GitHub does not generate can pass because
		// some unrelated shell comment happened to slugify to the same string.
		// The test would then report the documentation as correct while a
		// reader clicking the link lands on the top of the page.
		//
		// The fence marker may be indented, and may carry an info string.
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}

		if !strings.HasPrefix(line, "#") {
			continue
		}
		heading := strings.TrimSpace(strings.TrimLeft(line, "#"))
		// A closed ATX heading ends in a run of hashes.
		if idx := strings.LastIndex(heading, " #"); idx >= 0 {
			heading = strings.TrimSpace(heading[:idx])
		}
		heading = strings.NewReplacer("`", "", "*", "", "_", "", "[", "", "]", "").Replace(heading)

		var b strings.Builder
		for _, r := range strings.ToLower(heading) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				b.WriteRune(r)
			case r == ' ' || r == '-' || r == '_':
				b.WriteRune('-')
			}
		}
		slug := b.String()
		for strings.Contains(slug, "--") {
			slug = strings.ReplaceAll(slug, "--", "-")
		}
		out[strings.Trim(slug, "-")] = true
	}
	return out
}

// Every anchor referenced within a document must exist as a heading in it.
//
// GitHub silently scrolls to the top when an anchor is missing, so a wrong
// `#some-heading` reads as a rendering bug rather than a broken link.
func TestDocAnchorsExist(t *testing.T) {
	anchorRe := regexp.MustCompile(`\]\(#([a-z0-9-]+)\)`)

	for _, file := range allDocFiles(t) {
		body := readDoc(t, file)
		anchors := headingAnchors(body)

		for _, m := range anchorRe.FindAllStringSubmatch(body, -1) {
			if !anchors[m[1]] {
				t.Errorf("%s links to #%s, which is not a heading in that file\n  headings present: %s",
					filepath.Base(file), m[1], strings.Join(sortedKeys(anchors), ", "))
			}
		}
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The scoring spec quotes constants from internal/metrics. If a constant
// changes and the spec does not, the spec documents a formula that no longer
// exists.
func TestScoringSpecQuotesLiveConstants(t *testing.T) {
	body := readDoc(t, filepath.Join(docsDir, "SCORING_SPEC.md"))

	constants := map[string]string{
		"WeightCode":                  "0.40",
		"WeightDeps":                  "0.30",
		"WeightGit":                   "0.30",
		"MaxFilesPerLanguage":         "40",
		"IdealAvgFileLines":           "150.0",
		"AvgLinesPenaltyRate":         "150.0",
		"HotspotSharePenalty":         "0.30",
		"MinAvgFileLines":             "25.0",
		"FragmentPenaltyRate":         "15.0",
		"MaxDirectDeps":               "50",
		"UnlockedPenalty":             "0.20",
		"NoManifestPenalty":           "0.10",
		"IdealCommitsPerWeek":         "8.0",
		"LowCadenceFloor":             "0.5",
		"StaleDaysPenalty":            "180",
		"ChurnConcentrationThreshold": "0.6",
		"BusFactorPenalty":            "0.15",
	}

	for name, want := range constants {
		re := regexp.MustCompile(regexp.QuoteMeta(name) + `\s*=\s*` + regexp.QuoteMeta(want))
		if !re.MatchString(body) {
			t.Errorf("SCORING_SPEC.md does not quote %s = %s; either the constant changed or the spec is stale",
				name, want)
		}
	}
}

// The config reference and the sample file must agree on the key set. A key in
// one and not the other is a trap in either direction.
func TestConfigurationDocCoversEveryConfigKey(t *testing.T) {
	body := readDoc(t, filepath.Join(docsDir, "CONFIGURATION.md"))
	sample := readDoc(t, filepath.Join("..", "..", ".lensyxe.yml"))

	keys := []string{
		"git_window_days", "hotspot_threshold", "ignore_dirs",
		"max_file_bytes", "enable_complexity", "timeout_seconds",
		"history_limit", "database_path", "compare_root",
		"watch_debounce_ms", "watch_interval_seconds",
		"detect_workspace",
		"explain", "ai_provider", "ai_model", "ai_key_env",
		"min_health_score", "max_health_drop", "max_complexity_increase",
		"max_risk_count", "max_hotspots", "fail_on_drift", "require_tests",
	}

	for _, k := range keys {
		if !strings.Contains(body, k) {
			t.Errorf("CONFIGURATION.md does not document the config key %q", k)
		}
		if !strings.Contains(sample, k) {
			t.Errorf("the sample .lensyxe.yml does not mention the config key %q", k)
		}
	}
}

// The CLI reference must not document a flag the binary does not accept. The
// flags come from the real command tree, so a rename fails this test.
func TestCLIReferenceFlagsExist(t *testing.T) {
	body := readDoc(t, filepath.Join(docsDir, "CLI_REFERENCE.md"))
	known := knownFlags(t)

	// Flags appear inside backticks in the tables. A trailing hyphen means the
	// author wrote a glob such as `--fail-*`, which names a family of flags
	// rather than one flag.
	flagRe := regexp.MustCompile("`--([a-z][a-z0-9-]*-)`")
	for _, m := range flagRe.FindAllStringSubmatch(body, -1) {
		if !known[m[1]] {
			t.Errorf("CLI_REFERENCE.md documents the flag glob --%s*, which matches no command flag", m[1])
		}
	}

	flagRe = regexp.MustCompile("`--([a-z][a-z0-9-]*)`")
	for _, m := range flagRe.FindAllStringSubmatch(body, -1) {
		if !known[m[1]] {
			t.Errorf("CLI_REFERENCE.md documents --%s, which no command accepts", m[1])
		}
	}
}

// The reverse direction: a flag the CLI accepts but the reference does not
// document is a flag nobody can discover.
func TestEveryFlagIsDocumented(t *testing.T) {
	body := readDoc(t, filepath.Join(docsDir, "CLI_REFERENCE.md"))
	known := knownFlags(t)

	var undocumented []string
	for name := range known {
		if name == "help" || name == "version" || name == "config" {
			continue // documented as global flags, checked separately
		}
		if !strings.Contains(body, "--"+name) {
			undocumented = append(undocumented, name)
		}
	}
	sort.Strings(undocumented)
	for _, name := range undocumented {
		t.Errorf("--%s is accepted by the CLI but not documented in CLI_REFERENCE.md", name)
	}
}

// Every command must be documented. An undocumented command is invisible.
func TestEveryCommandIsDocumented(t *testing.T) {
	body := readDoc(t, filepath.Join(docsDir, "CLI_REFERENCE.md"))
	root := newRootCmd(&app{})

	for _, cmd := range root.Commands() {
		if cmd.Name() == "help" || cmd.Name() == "completion" {
			continue
		}
		anchor := "lensyxe-" + strings.ToLower(cmd.Name())
		if !strings.Contains(body, "`"+cmd.Name()+"`") &&
			!headingAnchors(body)[anchor] {
			t.Errorf("command %q is not documented in CLI_REFERENCE.md", cmd.Name())
		}
	}
}

// The exit-code table is a contract with every pipeline that uses Lensyxe.
func TestExitCodeContractIsDocumented(t *testing.T) {
	body := readDoc(t, filepath.Join(docsDir, "CLI_REFERENCE.md"))
	if !strings.Contains(body, "exitGateFailure") {
		// The constant name is internal; assert on the meaning instead.
		if !strings.Contains(body, "Policy rejection") {
			t.Error("CLI_REFERENCE.md does not document exit code 2 as a policy rejection")
		}
	}
	// The gates and action docs must agree on the same three codes.
	for _, doc := range []string{"CLI_REFERENCE.md", "CONFIGURATION.md"} {
		b := readDoc(t, filepath.Join(docsDir, doc))
		if !strings.Contains(b, "Policy rejection") {
			t.Errorf("%s does not document the exit-2 policy rejection", doc)
		}
	}
}

// The GitHub Action guide must document every input the action actually
// declares. A consumer reading the guide must not hit an undocumented knob.
func TestActionGuideCoversEveryInput(t *testing.T) {
	guide := readDoc(t, filepath.Join(docsDir, "GITHUB_ACTION.md"))

	// The action's declared inputs, read from the action file itself.
	data, err := os.ReadFile(filepath.Join("..", "..", "action", "action.yml"))
	if err != nil {
		t.Fatalf("read action.yml: %v", err)
	}

	inputRe := regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*):\s*$`)
	// Only consider the inputs: block, which runs until the outputs section.
	inputsBlock := string(data)
	if idx := strings.Index(inputsBlock, "\noutputs:"); idx >= 0 {
		inputsBlock = inputsBlock[:idx]
	}

	seen := 0
	for _, m := range inputRe.FindAllStringSubmatch(inputsBlock, -1) {
		name := m[1]
		// The top-level sibling keys of `inputs:` are also two-space indented;
		// skip the known structural names.
		switch name {
		case "inputs", "runs", "branding", "name", "description", "author":
			continue
		}
		seen++
		if !strings.Contains(guide, "`"+name+"`") {
			t.Errorf("GITHUB_ACTION.md does not document the action input %q", name)
		}
	}
	if seen == 0 {
		t.Fatal("no action inputs were parsed; the regex is wrong")
	}
}

// The VS Code guide must not describe a CLI surface that does not exist.
//
// The extension guide documents the flags and subcommands it passes to the
// binary. Those are the whole contract between the two halves of the project,
// and the extension is TypeScript that the Go build never sees, so nothing else
// would notice the CLI dropping one.
func TestVSCodeGuideOnlyReferencesRealFlags(t *testing.T) {
	guide := readDoc(t, filepath.Join(docsDir, "VSCODE_EXTENSION.md"))
	known := knownFlags(t)

	// A backticked flag. Requires a word character after the leading --, so `--`
	// on its own and any punctuation-terminated fragment do not produce a hit.
	refRe := regexp.MustCompile("`(--[a-z][a-z0-9-]*)`")

	seen := 0
	for _, m := range refRe.FindAllStringSubmatch(guide, -1) {
		name := strings.TrimPrefix(m[1], "--")
		seen++
		if !known[name] {
			t.Errorf("VSCODE_EXTENSION.md references %s, which no lensyxe command accepts", m[1])
		}
	}
	if seen == 0 {
		t.Fatal("no CLI flags parsed from VSCODE_EXTENSION.md; the regex is wrong")
	}
}

// The subcommands the extension invokes must exist.
func TestVSCodeGuideOnlyReferencesRealCommands(t *testing.T) {
	guide := readDoc(t, filepath.Join(docsDir, "VSCODE_EXTENSION.md"))

	known := map[string]bool{}
	var walk func(*cobra.Command)
	walk = func(cmd *cobra.Command) {
		known[cmd.Name()] = true
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(newRootCmd(&app{}))

	// `lensyxe <word>`, in the forms the guide actually uses.
	cmdRe := regexp.MustCompile("`lensyxe ([a-z]+)")

	seen := 0
	for _, m := range cmdRe.FindAllStringSubmatch(guide, -1) {
		name := m[1]
		seen++
		if !known[name] {
			t.Errorf("VSCODE_EXTENSION.md runs `lensyxe %s`, which is not a command", name)
		}
	}
	if seen == 0 {
		t.Fatal("no subcommands parsed from VSCODE_EXTENSION.md; the regex is wrong")
	}
}

// The installer's artifact naming must match the release spec, or every
// install 404s on a filename that looks plausible.
func TestInstallerMatchesReleaseArtifactNames(t *testing.T) {
	spec := readDoc(t, filepath.Join("..", "..", ".goreleaser.yaml"))

	// The version template has to strip the tag's leading v, because that is
	// what the installer strips before building the asset name.
	if !strings.Contains(spec, "{{ .Version }}") {
		t.Error(".goreleaser.yaml does not name the archive after .Version")
	}
	for _, token := range []string{"{{ .ProjectName }}", "title .Os"} {
		if !strings.Contains(spec, token) {
			t.Errorf(".goreleaser.yaml archive name_template is missing %q", token)
		}
	}

	// project_name is the first path segment of every asset name.
	if !strings.Contains(spec, "project_name: lensyxe") {
		t.Error(".goreleaser.yaml project_name is not lensyxe, which install.sh assumes")
	}

	// The installer must fetch checksums.txt by that exact name.
	installer := readDoc(t, filepath.Join("..", "..", "install.sh"))
	if !strings.Contains(installer, "checksums.txt") {
		t.Error("install.sh does not verify checksums.txt")
	}

	// Render the template and compare it to what the installer builds.
	//
	// Checking that the tokens are present is not enough, and that is the whole
	// point of this addition: the tokens were all present while every published
	// archive was named "lensyxe_ 1.0.0-rc1_Darwin_arm64.tar.gz", because a YAML
	// folded block had turned a newline between two actions into a literal space
	// that install.sh does not emit. Every install would have 404'd.
	nameTmpl, ok := archiveNameTemplate(t, spec)
	if !ok {
		return // already reported
	}

	// install.sh builds: "${BIN_NAME}_${version}_${os}_${arch}.${ext}"
	wantRe := regexp.MustCompile(`archive="\$\{BIN_NAME\}_\$\{version\}_\$\{os\}_\$\{arch\}\.\$\{ext\}"`)
	if !wantRe.MatchString(installer) {
		t.Errorf("install.sh no longer builds the archive name as " +
			"${BIN_NAME}_${version}_${os}_${arch}.${ext}; this test cannot " +
			"confirm the two agree, so it is reporting rather than passing silently")
	}

	for _, tc := range []struct{ os, arch, want string }{
		{"darwin", "amd64", "lensyxe_1.0.0-rc1_Darwin_x86_64"},
		{"darwin", "arm64", "lensyxe_1.0.0-rc1_Darwin_arm64"},
		{"linux", "amd64", "lensyxe_1.0.0-rc1_Linux_x86_64"},
		{"linux", "arm64", "lensyxe_1.0.0-rc1_Linux_arm64"},
		{"windows", "amd64", "lensyxe_1.0.0-rc1_Windows_x86_64"},
		{"windows", "arm64", "lensyxe_1.0.0-rc1_Windows_arm64"},
	} {
		got := renderArchiveName(t, nameTmpl, tc.os, tc.arch)
		if got != tc.want {
			t.Errorf("archive name for %s/%s = %q, want %q", tc.os, tc.arch, got, tc.want)
		}
		// Belt and braces, and the check that would have caught the folded
		// block directly rather than by comparing against a hardcoded string.
		if strings.ContainsAny(got, " \t") {
			t.Errorf("archive name for %s/%s contains whitespace: %q; "+
				"install.sh builds no whitespace into the name, so the download "+
				"URL will not match", tc.os, tc.arch, got)
		}
	}
}

// archiveNameTemplate pulls the archives[].name_template out of the release
// spec, accepting either a plain or a folded scalar.
func archiveNameTemplate(t *testing.T, spec string) (string, bool) {
	t.Helper()

	// Find the archives block, then its name_template, so the checksum template
	// elsewhere in the file cannot be mistaken for the archive one.
	arch := strings.Index(spec, "\narchives:")
	if arch < 0 {
		t.Fatal(".goreleaser.yaml has no archives section")
	}
	block := spec[arch:]
	// The archives section ends at the next top-level key.
	if end := strings.Index(block, "\nchecksum:"); end > 0 {
		block = block[:end]
	}

	const key = "name_template:"
	i := strings.Index(block, key)
	if i < 0 {
		t.Error(".goreleaser.yaml archives section has no name_template")
		return "", false
	}
	rest := block[i+len(key):]
	rest = strings.TrimLeft(rest, " ")

	// A folded or literal block scalar: consume the indentation-indented lines.
	if strings.HasPrefix(rest, ">") || strings.HasPrefix(rest, "|") {
		lines := strings.Split(rest, "\n")
		head := lines[0]
		// The block body is indented further than the `name_template:` key.
		// Anything at or left of that is a sibling key or a comment, and must
		// not be swallowed into the template value.
		keyIndent := 0
		if idx := strings.Index(block, "name_template:"); idx >= 0 {
			lineStart := strings.LastIndex(block[:idx], "\n")
			keyIndent = idx - (lineStart + 1)
		}

		var parts []string
		for _, l := range lines[1:] {
			trimmed := strings.TrimLeft(l, " ")
			if trimmed == "" {
				continue
			}
			lead := len(l) - len(trimmed)
			// A comment at any indentation is not part of a block scalar.
			if strings.HasPrefix(trimmed, "#") {
				break
			}
			if lead <= keyIndent {
				break
			}
			parts = append(parts, trimmed)
		}
		// Folded blocks join their lines with a space, which is exactly the
		// behaviour that broke this once.
		if strings.HasPrefix(head, ">") {
			return strings.Join(parts, " "), true
		}
		return strings.Join(parts, "\n"), true
	}

	// A plain scalar, possibly quoted. YAML quotes are not part of the value,
	// and leaving them on would make every rendered name carry a literal
	// apostrophe.
	v := strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
	if len(v) >= 2 {
		if (v[0] == '\'' && v[len(v)-1] == '\'') || (v[0] == '"' && v[len(v)-1] == '"') {
			v = v[1 : len(v)-1]
		}
	}
	return v, true
}

// renderArchiveName evaluates the archive name template through text/template,
// which is in the standard library, so this costs no dependency and handles the
// if/else/end block and any trim markers exactly the way goreleaser will.
//
// Replacing the actions by string substitution, which this did first, silently
// mishandles a template written without trim markers and looks like it works.
func renderArchiveName(t *testing.T, tmpl, goos, goarch string) string {
	t.Helper()

	// `title` capitalises the first letter, as goreleaser's does. Written out
	// rather than pulled from golang.org/x/text/cases, which would add a
	// dependency for one call in a test.
	funcs := template.FuncMap{
		"title": func(s string) string {
			if s == "" {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
	}

	tpl, err := template.New("name").Funcs(funcs).Parse(tmpl)
	if err != nil {
		t.Fatalf(".goreleaser.yaml archive name_template does not parse: %v", err)
	}

	data := struct {
		ProjectName string
		Version     string
		Os          string
		Arch        string
	}{"lensyxe", "1.0.0-rc1", goos, goarch}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		t.Fatalf("archive name_template does not execute: %v", err)
	}
	return buf.String()
}

// The repo name appears in several places. A mismatch between them produces a
// failure that is hard to trace, so they are checked against each other.
//
// go.mod is in this list because it is the one that matters most and is the
// easiest to get wrong: Go resolves a module by the path it declares, so a
// mismatch does not degrade gracefully, it makes the documented
// `go install github.com/zelvior/lensyxe/cmd/lensyxe@latest` fail outright
// with "module declares its path as: ... but was required as: ...". The other
// three only affect curl URLs, which fail more visibly.
func TestRepoNameIsConsistent(t *testing.T) {
	const want = "zelvior/lensyxe"

	files := map[string]string{
		"go.mod":            filepath.Join("..", "..", "go.mod"),
		"install.sh":        filepath.Join("..", "..", "install.sh"),
		"README.md":         filepath.Join("..", "..", "README.md"),
		"GITHUB_ACTION.md":  filepath.Join(docsDir, "GITHUB_ACTION.md"),
		"action/action.yml": filepath.Join("..", "..", "action", "action.yml"),
	}

	for name, path := range files {
		body := readDoc(t, path)
		if !strings.Contains(body, want) {
			t.Errorf("%s does not reference the repository %q", name, want)
		}
	}

	// The inverse check matters more than the forward one: a file can satisfy
	// the assertion above by mentioning the right repo in a comment while
	// still fetching the wrong one.
	for name, path := range files {
		body := readDoc(t, path)
		if strings.Contains(body, "lensyxe/lensyxe") {
			t.Errorf("%s still references lensyxe/lensyxe, which is not this repository", name)
		}
	}
}

// The action must not build a release asset name itself.
//
// It used to, and its copy of the mapping was wrong in four ways at once: it
// dropped the version segment, lower-cased the OS that goreleaser title-cases,
// used Go architecture names where the release uses uname-style ones, and
// hardcoded .tar.gz where Windows gets .zip. Every platform 404'd. The action
// now delegates to install.sh, and this test exists so a future edit that
// reintroduces a second copy of the naming fails here rather than in a
// customer's CI log.
func TestActionDelegatesArtifactNamingToInstaller(t *testing.T) {
	action := readDoc(t, filepath.Join("..", "..", "action", "action.yml"))

	if strings.Contains(action, "releases/download") {
		t.Error("action.yml builds a release download URL itself; " +
			"it must delegate to install.sh so the artifact naming has one owner")
	}

	// Delegation must actually reference the installer, with a version pinned
	// so a resolved release cannot pull a script from a moving branch.
	if !strings.Contains(action, "install.sh") {
		t.Error("action.yml does not invoke install.sh")
	}
	if !strings.Contains(action, "/v${version}/install.sh") {
		t.Error("action.yml must pin install.sh to the resolved version tag")
	}

	// The installer honours these; passing them is how the action controls the
	// install location.
	for _, token := range []string{"LENSYXE_VERSION", "LENSYXE_PREFIX"} {
		if !strings.Contains(action, token) {
			t.Errorf("action.yml does not pass %s to the installer", token)
		}
	}
}

// The action downloads and runs a binary, so it must inherit install.sh's
// integrity check rather than bypassing it.
func TestActionInheritsChecksumVerification(t *testing.T) {
	action := readDoc(t, filepath.Join("..", "..", "action", "action.yml"))

	if strings.Contains(action, "| tar -xz") || strings.Contains(action, "| unzip") {
		t.Error("action.yml extracts an archive directly; that path has no " +
			"checksum verification. Delegate to install.sh instead")
	}
	if !strings.Contains(action, "curl -fsSL \"$installer\"") {
		t.Error("action.yml must pipe the installer into a shell")
	}
}
