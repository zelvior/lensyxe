// Package dependencies implements manifest and lockfile analysis.
//
// Supported ecosystems in this phase:
//
//	JavaScript/TypeScript  package.json  + package-lock.json | yarn.lock | pnpm-lock.yaml
//	Go                     go.mod        + go.sum
//	Python                 pyproject.toml | requirements.txt + poetry.lock | uv.lock | Pipfile.lock
//
// Everything is read from disk with the standard library. No package manager
// is executed, no registry is contacted, and nothing is installed or resolved,
// so results are fast, offline, and byte-for-byte reproducible.
//
// The distinction that matters throughout: a manifest states what a project
// *declares* it needs, a lockfile records what it *resolved* to. Direct counts
// come from the manifest; transitive counts can only come from the lockfile.
package dependencies

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zelvior/lensyxe/pkg/models"
)

// Config tunes dependency detection.
type Config struct {
	// PackageLimit caps how many package names are retained per ecosystem.
	PackageLimit int
	// MaxManifestBytes guards against absurdly large manifests.
	MaxManifestBytes int64
	// MaxLockfileBytes guards against enormous lockfiles, which can reach
	// tens of megabytes in a large monorepo.
	MaxLockfileBytes int64
}

// DefaultConfig returns the baseline dependency analyzer configuration.
func DefaultConfig() Config {
	return Config{
		PackageLimit:     50,
		MaxManifestBytes: 8 << 20,
		MaxLockfileBytes: 64 << 20,
	}
}

// Result pairs the computed stats with any non-fatal observations.
type Result struct {
	Stats    models.DependencyStats
	Findings []models.Finding
}

// ecosystem describes how to detect and parse one dependency manager.
type ecosystem struct {
	// name is the identifier reported in the output.
	name string
	// manifests are the manifest filenames, in probe order.
	manifests []string
	// lockfiles are the recognized lockfiles with their format labels, in
	// probe order. The first match wins.
	lockfiles []lockfileSpec
	// parse reads the manifest and fills direct/dev counts.
	parse func(root, manifest string, cfg Config) (parsedManifest, error)
}

type lockfileSpec struct {
	file   string
	format string
	// count resolves the transitive dependency count from the lockfile.
	// A nil count means the format does not encode a resolvable graph.
	count func(path string, cfg Config) (int, error)
}

// parsedManifest is the normalized result of reading one manifest.
type parsedManifest struct {
	direct    int
	dev       int
	indirect  int
	packages  []string
	ecosystem string
}

var ecosystems = []ecosystem{
	{
		name:      "npm",
		manifests: []string{"package.json"},
		lockfiles: []lockfileSpec{
			{file: "package-lock.json", format: "npm-lock-v2", count: countNPMLock},
			{file: "npm-shrinkwrap.json", format: "npm-lock-v2", count: countNPMLock},
			{file: "pnpm-lock.yaml", format: "pnpm-lock-v6", count: countPnpmLock},
			{file: "yarn.lock", format: "yarn-lock-v1", count: countYarnLock},
		},
		parse: parseNPMManifest,
	},
	{
		name:      "gomod",
		manifests: []string{"go.mod"},
		lockfiles: []lockfileSpec{
			{file: "go.sum", format: "go-sum", count: countGoSum},
		},
		parse: parseGoMod,
	},
	{
		name:      "python",
		manifests: []string{"pyproject.toml", "requirements.txt"},
		lockfiles: []lockfileSpec{
			{file: "poetry.lock", format: "poetry-lock-v2", count: countPoetryLock},
			{file: "uv.lock", format: "uv-lock-v1", count: countPoetryLock},
			{file: "Pipfile.lock", format: "pipenv-lock-v1", count: countPipenvLock},
		},
		parse: parsePythonManifest,
	},
}

// Analyze inspects root for known dependency manifests.
//
// Detection is deterministic: ecosystems are probed in a fixed order and the
// result is sorted by ecosystem name. A malformed manifest degrades to a
// finding instead of failing the scan.
func Analyze(root string, cfg Config) (Result, error) {
	info, err := os.Stat(root)
	if err != nil {
		return Result{}, fmt.Errorf("dependency analyzer: stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return Result{}, fmt.Errorf("dependency analyzer: %s is not a directory", root)
	}

	var (
		ecosystemsFound []models.EcosystemStats
		findings        []models.Finding
	)

	for _, eco := range ecosystems {
		manifestPath, ok := firstExisting(root, eco.manifests)
		if !ok {
			continue
		}

		parsed, err := eco.parse(root, manifestPath, cfg)
		if err != nil {
			findings = append(findings, models.Finding{
				Severity: models.SeverityMedium,
				Category: models.CategoryDependency,
				Title:    fmt.Sprintf("Unreadable %s", filepath.Base(manifestPath)),
				Detail:   err.Error(),
				Subject:  relOrBase(root, manifestPath),
			})
			continue
		}

		stat := models.EcosystemStats{
			Name:     eco.name,
			Manifest: relOrBase(root, manifestPath),
			Total:    parsed.direct + parsed.dev + parsed.indirect,
			Direct:   parsed.direct,
			Dev:      parsed.dev,
			Indirect: parsed.indirect,
			Packages: parsed.packages,
			Detected: true,
		}

		// Join the lockfile, if any.
		if spec, found := findLockfile(root, eco.lockfiles); found {
			stat.Lockfile = spec.file
			stat.LockfileFormat = spec.format
			if spec.count != nil {
				n, err := spec.count(filepath.Join(root, spec.file), cfg)
				switch {
				case err != nil:
					findings = append(findings, models.Finding{
						Severity: models.SeverityLow,
						Category: models.CategoryDependency,
						Title:    "Lockfile could not be parsed",
						Detail:   err.Error(),
						Subject:  spec.file,
					})
				default:
					// The lockfile's own entry count is the resolved graph
					// size. Report it as transitive, but never let it make
					// Total smaller than the declared count.
					stat.Transitive = n
				}
			}
			stat.LockfileStale = manifestNewerThan(manifestPath, filepath.Join(root, spec.file))
		}

		// Drift: a manifest with no lockfile means installs are not
		// reproducible. This is the highest-signal dependency finding.
		if stat.Lockfile == "" {
			stat.Drift = true
			stat.DriftReason = fmt.Sprintf(
				"%s declares %d direct dependencies with no lockfile, so resolved versions are not reproducible",
				stat.Manifest, stat.Direct)
		}

		ecosystemsFound = append(ecosystemsFound, stat)
		findings = append(findings, ecosystemFindings(stat)...)
	}

	sort.Slice(ecosystemsFound, func(i, j int) bool {
		return ecosystemsFound[i].Name < ecosystemsFound[j].Name
	})

	stats := models.DependencyStats{
		Ecosystems: ecosystemsFound,
		Detected:   len(ecosystemsFound) > 0,
		Locked:     len(ecosystemsFound) > 0,
	}
	if stats.Ecosystems == nil {
		stats.Ecosystems = []models.EcosystemStats{}
	}
	for _, eco := range ecosystemsFound {
		stats.Total += eco.Total
		stats.Direct += eco.Direct
		stats.Dev += eco.Dev
		stats.Indirect += eco.Indirect
		// Transitive is a resolved-graph size; the sum across ecosystems is
		// meaningful, and each ecosystem's own transitive count already
		// includes its direct requirements.
		stats.Transitive += eco.Transitive
		if eco.Lockfile == "" {
			stats.Locked = false
			if eco.DriftReason != "" {
				stats.Drift = true
				stats.DriftReason = append(stats.DriftReason, eco.DriftReason)
			}
		}
	}
	sort.Strings(stats.DriftReason)

	if !stats.Detected {
		stats.Note = "no supported dependency manifest found (looked for package.json, go.mod, pyproject.toml, requirements.txt)"
		findings = append(findings, models.Finding{
			Severity: models.SeverityInfo,
			Category: models.CategoryDependency,
			Title:    "No dependency manifest detected",
			Detail:   stats.Note,
		})
	}

	return Result{Stats: stats, Findings: findings}, nil
}

// ecosystemFindings derives observations from one ecosystem's numbers.
func ecosystemFindings(eco models.EcosystemStats) []models.Finding {
	f := make([]models.Finding, 0, 3)
	if eco.Drift {
		f = append(f, models.Finding{
			Severity: models.SeverityHigh,
			Category: models.CategoryDependency,
			Title:    fmt.Sprintf("Missing lockfile for %s", eco.Manifest),
			Detail:   eco.DriftReason,
			Subject:  eco.Manifest,
		})
	}
	if eco.LockfileStale {
		f = append(f, models.Finding{
			Severity: models.SeverityLow,
			Category: models.CategoryDependency,
			Title:    "Lockfile older than manifest",
			Detail: fmt.Sprintf("%s was modified more recently than %s; the lockfile may not reflect the declared set.",
				eco.Manifest, eco.Lockfile),
			Subject: eco.Lockfile,
		})
	}
	if eco.Direct > directWarnThreshold(eco.Name) {
		f = append(f, models.Finding{
			Severity: models.SeverityMedium,
			Category: models.CategoryDependency,
			Title:    "Large direct dependency surface",
			Detail:   fmt.Sprintf("%d direct %s dependencies declared.", eco.Direct, eco.Name),
			Subject:  eco.Manifest,
		})
	}
	return f
}

// directWarnThreshold is the direct-dependency count above which an ecosystem
// gets a medium finding. It matches the dependency health score's full-penalty
// point so the finding and the score never disagree.
func directWarnThreshold(ecosystem string) int {
	switch ecosystem {
	case "python":
		// Python's direct dependency counts are structurally larger than
		// npm's for the same project size, so the bar is higher.
		return 40
	default:
		return 50
	}
}

// npmManifest mirrors the subset of package.json that Lensyxe reads.
// Unknown fields are ignored by encoding/json, so extra keys are harmless.
type npmManifest struct {
	Name                 string            `json:"name"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
}

// parseNPMManifest counts npm dependencies from package.json.
func parseNPMManifest(root, manifestPath string, cfg Config) (parsedManifest, error) {
	out := parsedManifest{ecosystem: "npm", packages: []string{}}

	f, err := openCapped(manifestPath, cfg.MaxManifestBytes)
	if err != nil {
		return out, err
	}
	defer f.Close()

	var m npmManifest
	if err := json.NewDecoder(bufio.NewReader(f)).Decode(&m); err != nil {
		return out, fmt.Errorf("parse package.json: %w", err)
	}

	// A package listed in both dependencies and devDependencies is counted
	// once, in the runtime bucket: that is the bucket where it actually
	// resolves at build time, so it must not be subtracted from Direct.
	// The duplicate is only removed from the dev count.
	dupes := overlap(m.Dependencies, m.DevDependencies)
	out.direct = len(m.Dependencies) + len(m.PeerDependencies) +
		len(m.OptionalDependencies)
	out.dev = len(m.DevDependencies) - dupes
	out.indirect = 0 // only a lockfile can establish this
	out.packages = sortedKeys(m.Dependencies, cfg.PackageLimit)
	return out, nil
}

// parseGoMod counts module requirements from go.mod using a line scanner
// rather than golang.org/x/mod, keeping the dependency surface minimal.
func parseGoMod(root, manifestPath string, cfg Config) (parsedManifest, error) {
	out := parsedManifest{ecosystem: "gomod", packages: []string{}}

	f, err := openCapped(manifestPath, cfg.MaxManifestBytes)
	if err != nil {
		return out, err
	}
	defer f.Close()

	var (
		inBlock bool
		names   []string
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "require ("):
			inBlock = true
			continue
		case line == ")":
			inBlock = false
			continue
		case strings.HasPrefix(line, "require "):
			line = strings.TrimSpace(strings.TrimPrefix(line, "require "))
		case !inBlock:
			continue
		}
		// Strip any trailing comment so `// indirect` can be detected.
		indirect := false
		if idx := strings.Index(line, "//"); idx >= 0 {
			comment := strings.TrimSpace(line[idx:])
			indirect = strings.Contains(comment, "indirect")
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}
		name, _, ok := strings.Cut(line, " ") // module path, version
		if !ok || name == "" {
			continue
		}
		names = append(names, name)
		if indirect {
			out.indirect++
		} else {
			out.direct++
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("read go.mod: %w", err)
	}

	sort.Strings(names)
	out.packages = capStrings(names, cfg.PackageLimit)
	return out, nil
}

// parsePythonManifest reads pyproject.toml or requirements.txt.
//
// pyproject.toml is parsed with a deliberately small TOML reader limited to
// the two array forms that matter: `key = ["a", "b"]` and the table headers
// that precede them. Pulling in a full TOML library for that would be a poor
// trade for a scanner whose job is counting names.
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

// countNPMLock counts resolved packages in a package-lock.json.
//
// Lockfile v2 and v3 store the graph under "packages" keyed by path, which is
// authoritative. v1 files only have "dependencies", so both are attempted.
func countNPMLock(path string, cfg Config) (int, error) {
	f, err := openCapped(path, cfg.MaxLockfileBytes)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var doc struct {
		LockfileVersion int                        `json:"lockfileVersion"`
		Packages        map[string]json.RawMessage `json:"packages"`
		Dependencies    map[string]json.RawMessage `json:"dependencies"`
	}
	if err := json.NewDecoder(bufio.NewReader(f)).Decode(&doc); err != nil {
		return 0, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	if len(doc.Packages) > 0 {
		// The root project entry is keyed "" and is not a dependency.
		n := len(doc.Packages)
		if _, ok := doc.Packages[""]; ok {
			n--
		}
		return n, nil
	}
	return len(doc.Dependencies), nil
}

// countPnpmLock counts resolved package entries in a pnpm-lock.yaml.
//
// Entries appear under `packages:` as keys shaped `/name@version` or
// `/name/version` depending on lockfile version; either way a line whose
// indentation marks it a key is one resolved package.
func countPnpmLock(path string, cfg Config) (int, error) {
	n, err := countYAMLKeys(path, cfg, []string{"packages:", "snapshots:"}, true)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// countPoetryLock counts `[[package]]` blocks in a poetry.lock or uv.lock.
func countPoetryLock(path string, cfg Config) (int, error) {
	return countYAMLKeys(path, cfg, []string{"[[package]]"}, false)
}

// countYarnLock counts top-level descriptor blocks in a yarn.lock.
//
// A yarn.lock v1 entry starts at column zero as `name@range, name@range:`,
// followed by indented lines. Counting column-zero keys that end with a colon
// gives the resolved package count.
func countYarnLock(path string, cfg Config) (int, error) {
	f, err := openCapped(path, cfg.MaxLockfileBytes)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Only column-zero keys are entries; everything else is indented.
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if strings.HasSuffix(strings.TrimSpace(line), ":") {
			n++
		}
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return n, nil
}

// countPipenvLock counts top-level keys in a Pipfile.lock's "default" and
// "develop" tables.
func countPipenvLock(path string, cfg Config) (int, error) {
	f, err := openCapped(path, cfg.MaxLockfileBytes)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	var doc struct {
		Default map[string]json.RawMessage `json:"default"`
		Develop map[string]json.RawMessage `json:"develop"`
	}
	if err := json.NewDecoder(bufio.NewReader(f)).Decode(&doc); err != nil {
		return 0, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return len(doc.Default) + len(doc.Develop), nil
}

// countGoSum counts distinct modules recorded in a go.sum.
//
// Each module appears once per version, so entries are deduplicated by module
// path: that is the resolved graph size.
func countGoSum(path string, cfg Config) (int, error) {
	f, err := openCapped(path, cfg.MaxLockfileBytes)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		seen[fields[0]] = true
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("read go.sum: %w", err)
	}
	return len(seen), nil
}

// countYAMLKeys counts keys under any of the given headers.
//
// When blockMode is true (pnpm style) a nested key is any indented line ending
// in a colon. When false (poetry style) a block is counted per `[[package]]`
// header instead, and headerList entries are exact matches.
func countYAMLKeys(path string, cfg Config, headers []string, nested bool) (int, error) {
	f, err := openCapped(path, cfg.MaxLockfileBytes)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	exact := map[string]bool{}
	for _, h := range headers {
		exact[h] = true
	}

	n := 0
	inSection := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		indented := len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t')

		if !nested {
			if exact[line] {
				n++
			}
			continue
		}

		if !indented {
			// A column-zero key is either a section header or a plain value.
			inSection = strings.HasSuffix(line, ":") && sectionMatches(line, headers)
			continue
		}
		if inSection && strings.HasSuffix(line, ":") {
			n++
		}
	}
	if err := sc.Err(); err != nil {
		return 0, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return n, nil
}

// sectionMatches reports whether a column-zero YAML key is one of headers.
func sectionMatches(line string, headers []string) bool {
	for _, h := range headers {
		if line == h {
			return true
		}
	}
	return false
}

// manifestNewerThan reports whether manifestPath was modified after
// lockfilePath.
//
// This is a filesystem-mtime hint, not proof of drift: a tool can touch a
// manifest without changing its dependency set. It is reported at low
// severity precisely because it can be wrong.
//
// Both arguments are full paths. Comparison uses a one-second tolerance
// because several filesystems quantize modification times coarsely, and a
// sub-second skew must not be reported as staleness.
func manifestNewerThan(manifestPath, lockfilePath string) bool {
	mi, err := os.Stat(manifestPath)
	if err != nil {
		return false
	}
	li, err := os.Stat(lockfilePath)
	if err != nil {
		return false
	}
	return mi.ModTime().After(li.ModTime().Add(time.Second))
}

// firstExisting returns the first filename in names present in root.
func firstExisting(root string, names []string) (string, bool) {
	for _, name := range names {
		p := filepath.Join(root, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return p, true
		}
	}
	return "", false
}

// findLockfile returns the first recognized lockfile present in root.
func findLockfile(root string, specs []lockfileSpec) (lockfileSpec, bool) {
	for _, spec := range specs {
		p := filepath.Join(root, spec.file)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
			return spec, true
		}
	}
	return lockfileSpec{}, false
}

// openCapped opens a file and rejects it when it exceeds maxBytes.
func openCapped(path string, maxBytes int64) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	if maxBytes > 0 {
		if st, serr := f.Stat(); serr == nil && st.Size() > maxBytes {
			f.Close()
			return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), maxBytes)
		}
	}
	return f, nil
}

// overlap counts keys present in both a and b.
func overlap(a, b map[string]string) int {
	n := 0
	for k := range a {
		if _, ok := b[k]; ok {
			n++
		}
	}
	return n
}

func sortedKeys(m map[string]string, limit int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return capStrings(keys, limit)
}

// capStrings truncates a sorted slice to limit entries when limit > 0.
func capStrings(values []string, limit int) []string {
	if limit > 0 && len(values) > limit {
		return values[:limit]
	}
	return values
}

func relOrBase(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(path)
}
