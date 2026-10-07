package dependencies

// The Python manifest readers.
//
// Python is the one ecosystem here with no single canonical file and three
// incompatible declaration styles, so this file is the bulk of the parsing work:
// a small TOML reader, a flat pip reader, and the name normalisation that both
// share.
//
// Two conventions meet in pyproject.toml and must both be handled. PEP 621 puts
// requirement *strings* in an array; Poetry puts a package name as the *key* of
// a table with a version specifier as its value. A reader that understands only
// the first finds no dependencies in a Poetry project, and one that understands
// only the second finds none in a PEP 621 project.

import (
	"bufio"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// parsePythonManifest reads pyproject.toml or requirements.txt.
func parsePythonManifest(root, manifestPath string, cfg Config) (parsedManifest, error) {
	base := filepath.Base(manifestPath)
	switch base {
	case "requirements.txt":
		return parseRequirementsTxt(manifestPath, cfg)
	case "pyproject.toml":
		return parsePyproject(manifestPath, cfg)
	default:
		return parsedManifest{}, fmt.Errorf("unsupported python manifest %q", base)
	}
}

// parsePyproject reads the dependency arrays out of a pyproject.toml.
func parsePyproject(manifestPath string, cfg Config) (parsedManifest, error) {
	out := parsedManifest{ecosystem: "python", packages: []string{}}

	f, err := openCapped(manifestPath, cfg.MaxManifestBytes)
	if err != nil {
		return out, err
	}
	defer f.Close()

	// section tracks the most recent [table] header so entries are attributed
	// to the right bucket.
	section := ""
	var names []string

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	multi := "" // accumulates a multi-line array
	multiKey := ""
	multiBucket := ""
	multiDepth := 0 // unclosed '[' depth, quote-aware

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if multiKey != "" {
			multi += " " + line
			multiDepth += bracketDelta(line)
			if multiDepth > 0 {
				continue
			}
			names = append(names, classifyPythonDeps(multiBucket, extractArrayItems(multi))...)
			multi, multiKey, multiBucket, multiDepth = "", "", "", 0
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.Trim(line, "[]")
			continue
		}
		key, rest, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		rest = strings.TrimSpace(rest)
		bucket := dependencySection(section, key)
		switch {
		case strings.HasPrefix(rest, "["):
			// Single-line array, or the start of one that continues below.
			if delta := bracketDelta(rest); delta <= 0 {
				names = append(names, classifyPythonDeps(bucket, extractArrayItems(rest))...)
			} else {
				multi, multiKey, multiBucket, multiDepth = rest, key, bucket, delta
			}
		case bucket != "" && isKeyNamedTable(section):
			// Poetry style: `mypy = "^1.8"` inside a dependency table, where
			// the key is the package name and the value is a version spec.
			names = append(names, bucket+"\x00"+normalizePythonName(key))
		default:
			// PEP 621 inline scalar: `dependencies = "flask"`.
			names = append(names, classifyPythonDeps(bucket, inlineDependencyValues(rest))...)
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("read pyproject.toml: %w", err)
	}

	direct, dev := dedupeCounts(names)
	out.direct = direct
	out.dev = dev
	sort.Strings(names)
	out.packages = capStrings(dedupeNames(names), cfg.PackageLimit)
	return out, nil
}

// isKeyNamedTable reports whether a table uses the Poetry convention where each
// key is a package name and each value is a version specifier, as opposed to
// the PEP 621 convention where the table holds one array of requirement
// strings.
func isKeyNamedTable(table string) bool {
	switch table {
	case "tool.poetry.dependencies",
		"tool.poetry.dev-dependencies",
		"tool.poetry.group.dev.dependencies":
		return true
	}
	return strings.HasPrefix(table, "tool.poetry.group.") &&
		strings.HasSuffix(table, ".dependencies")
}

// dependencySection maps a (table, key) pair to "direct", "dev", or "".
// Anything unrecognized maps to "" so it is never counted.
func dependencySection(table, key string) string {
	switch table {
	case "project":
		switch key {
		case "dependencies":
			return "direct"
		case "optional-dependencies":
			return "dev"
		}
	case "tool.poetry.dependencies":
		return "direct"
	case "tool.poetry.dev-dependencies", "tool.poetry.group.dev.dependencies":
		return "dev"
	case "dependency-groups":
		return "dev"
	}
	// Extras groups name their own sub-tables, for example
	// [project.optional-dependencies.dev]. Every key under the table, whether
	// it is declared as a dotted header or as a key of the base table, is a
	// development-time requirement.
	if table == "project.optional-dependencies" ||
		strings.HasPrefix(table, "project.optional-dependencies.") {
		return "dev"
	}
	// Poetry groups can be named arbitrarily:
	// [tool.poetry.group.<name>.dependencies].
	if strings.HasPrefix(table, "tool.poetry.group.") &&
		strings.HasSuffix(table, ".dependencies") {
		return "dev"
	}
	return ""
}

// classifyPythonDeps attributes a batch of requirement strings to counts.
//
// Each classified name carries its section in a NUL-delimited prefix. A name
// alone would lose the distinction, and a package required by both the runtime
// and dev sections must count once as direct -- see dedupeCounts.
func classifyPythonDeps(section string, items []string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		name, ok := normalizePythonDep(it)
		if !ok {
			continue
		}
		// The section decides attribution; the name carries it.
		out = append(out, section+"\x00"+name)
	}
	return out
}

// dedupeCounts splits classified names into direct and dev counts.
func dedupeCounts(classified []string) (direct, dev int) {
	seenDirect, seenDev := map[string]bool{}, map[string]bool{}
	for _, c := range classified {
		section, name, ok := strings.Cut(c, "\x00")
		if !ok {
			continue
		}
		switch section {
		case "direct":
			seenDirect[name] = true
		case "dev":
			seenDev[name] = true
		}
	}
	for n := range seenDirect {
		if seenDev[n] {
			continue // counted once, as a direct dependency
		}
		direct++
	}
	dev = len(seenDev)
	return direct, dev
}

// dedupeNames returns the bare dependency names, sorted and deduplicated.
func dedupeNames(classified []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(classified))
	for _, c := range classified {
		_, name, ok := strings.Cut(c, "\x00")
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// normalizePythonDep extracts a package name from a requirement string and
// reports whether it is a real dependency.
//
// Handles "flask>=2.0", "flask[async]==2.0", "flask @ https://...", and skips
// options ("-r other.txt"), comments, and bare URLs.
func normalizePythonDep(spec string) (string, bool) {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.HasPrefix(spec, "#") {
		return "", false
	}
	if idx := strings.Index(spec, " #"); idx >= 0 {
		spec = strings.TrimSpace(spec[:idx])
	}
	spec = strings.TrimSpace(strings.Trim(spec, `"'`))
	if spec == "" {
		return "", false
	}
	if strings.HasPrefix(spec, "-") {
		// Only an editable VCS install names a package, and only through an
		// "#egg=" fragment. Every other flag (-r, --index-url, ...) is
		// configuration rather than a dependency.
		if !strings.HasPrefix(spec, "-e ") && !strings.HasPrefix(spec, "--editable ") {
			return "", false
		}
		spec = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(spec, "--editable"), "-e"))
		idx := strings.Index(spec, "#egg=")
		if idx < 0 {
			return "", false
		}
		egg := strings.TrimSpace(spec[idx+len("#egg="):])
		if egg == "" {
			return "", false
		}
		return normalizePythonName(egg), true
	}

	// Direct URL reference: "name @ url".
	if name, _, ok := strings.Cut(spec, "@"); ok {
		name = strings.TrimSpace(name)
		if name == "" || strings.Contains(name, "://") {
			return "", false // bare URL with no name
		}
		return normalizePythonName(name), true
	}
	// A bare URL or VCS URL is not a named dependency.
	if strings.Contains(spec, "://") {
		return "", false
	}

	// Strip environment markers.
	if idx := strings.IndexByte(spec, ';'); idx >= 0 {
		spec = spec[:idx]
	}
	// Strip extras: flask[async] -> flask.
	if idx := strings.IndexByte(spec, '['); idx >= 0 {
		spec = spec[:idx]
	}
	// Cut at the first version-specifier character.
	if idx := strings.IndexAny(spec, "<>=!~"); idx >= 0 {
		spec = spec[:idx]
	}
	name := strings.TrimSpace(spec)
	if name == "" || strings.ContainsAny(name, " \t") {
		return "", false
	}
	return normalizePythonName(name), true
}

// normalizePythonName lowercases and normalizes separators in a package name.
//
// PEP 503 treats all three of -, _ and . as equivalent, so a project listing
// "Flask_Login" and another listing "flask-login" are the same distribution and
// must not be counted twice.
func normalizePythonName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, ".", "-")
	return strings.Trim(name, "-")
}

// parseRequirementsTxt reads a flat pip requirements file.
func parseRequirementsTxt(manifestPath string, cfg Config) (parsedManifest, error) {
	out := parsedManifest{ecosystem: "python", packages: []string{}}

	f, err := openCapped(manifestPath, cfg.MaxManifestBytes)
	if err != nil {
		return out, err
	}
	defer f.Close()

	var names []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		// Strip an inline comment before parsing.
		line := sc.Text()
		if idx := strings.Index(line, " #"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// "-r other.txt" is an include directive. Counting it would either
		// double-count the included file or report a fake dependency.
		if idx := strings.Index(line, ".txt"); idx >= 0 && strings.HasPrefix(line, "-") &&
			strings.TrimSpace(line[idx+4:]) == "" {
			continue
		}
		name, ok := normalizePythonDep(line)
		if !ok {
			continue
		}
		names = append(names, name)
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("read requirements.txt: %w", err)
	}

	names = dedupeNames(names2classified(names))
	out.direct = len(names)
	out.packages = capStrings(names, cfg.PackageLimit)
	return out, nil
}

// names2classified wraps bare names in the classified form so dedupeNames can
// be reused for requirements.txt.
func names2classified(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "direct\x00"+n)
	}
	return out
}

// extractArrayItems pulls the quoted strings out of a TOML array.
//
// A '#' inside a quoted string is part of the value, so the comment strip and
// the scan must both be quote-aware. Stripping comments first and scanning
// second would silently truncate `"foo # bar"` to `foo`.
func extractArrayItems(arr string) []string {
	items := make([]string, 0, 8)
	var cur strings.Builder
	inQuote := byte(0)

	for i := 0; i < len(arr); i++ {
		c := arr[i]
		switch {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
				continue
			}
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			inQuote = c
		case c == '#':
			// Comment runs to the end of the line.
			flushItem(&items, &cur)
			return items
		case c == ',':
			flushItem(&items, &cur)
		case c == '[' || c == ']':
			// Array delimiters are structure, not content.
			continue
		default:
			cur.WriteByte(c)
		}
	}
	flushItem(&items, &cur)
	return items
}

// bracketDelta returns the net bracket depth change across a line, ignoring
// brackets inside quoted strings.
//
// This is what decides whether a TOML array ends on this line. A naive
// strings.Contains(line, "]") is wrong in both directions: "flask[async]"
// closes early, and a line ending in a quoted "]" never closes at all.
func bracketDelta(line string) int {
	delta := 0
	inQuote := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inQuote != 0 {
			if c == inQuote {
				inQuote = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inQuote = c
		case '[':
			delta++
		case ']':
			delta--
		}
	}
	return delta
}

// flushItem appends the accumulated item when it is non-empty, then resets it.
func flushItem(items *[]string, cur *strings.Builder) {
	if s := strings.TrimSpace(cur.String()); s != "" {
		*items = append(*items, s)
	}
	cur.Reset()
}

// inlineDependencyValues handles `name = "1.0"` and `name = { version = "1" }`.
func inlineDependencyValues(rest string) []string {
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "{") {
		// Table form: take the key before the brace as the name.
		return nil // handled by the key itself in parsePyproject
	}
	name, ok := normalizePythonDep(rest)
	if !ok {
		return nil
	}
	return []string{name}
}
