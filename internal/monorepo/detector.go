// Package monorepo detects workspace layouts and computes per-package health.
//
// Detection is by manifest evidence, never by convention: a directory is called
// a package only when a recognized manifest declares it. Guessing from folder
// names ("there is an `apps/` directory, these must be apps") produces a
// workspace report for repositories that have none, and a confidently wrong
// list is worse than no list at all.
//
// Every number this package produces comes from the same deterministic
// analyzers and the same scorer the single-repository path uses. A package
// score is computed from that package's own files and manifests; it is not a
// share of the repository score.
package monorepo

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/zelvior/lensyxe/pkg/models"
)

// Manifest names recognized as declaring a package, in probe order within an
// ecosystem.
const (
	manifestPackageJSON = "package.json"
	manifestGoMod       = "go.mod"
	manifestCargoToml   = "Cargo.toml"
	manifestPyProject   = "pyproject.toml"
)

// workspaceFiles are the root files that can declare a workspace, in detection
// order. The order is fixed so two repositories with several workspace files
// always resolve to the same kind.
var workspaceFiles = []struct {
	file string
	kind models.WorkspaceKind
}{
	{"pnpm-workspace.yaml", models.WorkspacePNPM},
	{"lerna.json", models.WorkspaceLerna},
	{"go.work", models.WorkspaceGoWork},
	{"Cargo.toml", models.WorkspaceCargo},
	{"package.json", models.WorkspaceNPM},
}

// maxManifestBytes guards against a pathological manifest.
const maxManifestBytes = 8 << 20

// maxPackages caps how many packages are reported. A workspace with hundreds
// of members produces a report nobody reads; the cap is applied to the
// sorted list so which packages survive is deterministic.
const maxPackages = 200

// maxDepth bounds the directory search for manifests. A deeply nested tree is
// walked shallowly rather than recursively to the filesystem root, because the
// candidates only come from the workspace's declared globs.
const maxDepth = 8

// Detect inspects root and returns the detected workspace, or nil.
//
// A nil result with a nil error means "not a monorepo", which is the common
// case and is not a failure. A malformed workspace manifest is also not an
// error: the layout is simply not recognized, because guessing around a file
// the user wrote incorrectly would produce a report they cannot trust.
func Detect(root string) (*models.Workspace, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("monorepo: stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("monorepo: %s is not a directory", root)
	}

	for _, candidate := range workspaceFiles {
		patterns, err := readPatterns(root, candidate.file, candidate.kind)
		if err != nil || len(patterns.include) == 0 {
			// An unreadable or patternless file simply does not describe a
			// workspace. Detection continues so a second, valid file still
			// gets a chance.
			continue
		}

		ws := &models.Workspace{Kind: candidate.kind, Manifest: candidate.file}
		ws.Packages = sortPackages(resolvePackages(root, patterns))
		if len(ws.Packages) == 0 {
			continue
		}
		return ws, nil
	}

	return nil, nil
}

// readPatterns extracts the package path patterns a workspace file declares.
//
// An empty include set means the file does not describe a workspace, whether
// because it is absent, malformed, or simply not a workspace marker.
func readPatterns(root, file string, kind models.WorkspaceKind) (patternSet, error) {
	full := filepath.Join(root, filepath.FromSlash(file))
	info, err := os.Stat(full)
	if err != nil {
		return patternSet{}, nil // absent: not a workspace marker
	}
	if info.Size() > maxManifestBytes {
		return patternSet{}, nil
	}

	data, err := os.ReadFile(full)
	if err != nil {
		return patternSet{}, nil
	}

	var set patternSet
	switch kind {
	case models.WorkspacePNPM:
		var doc struct {
			Packages []string `yaml:"packages"`
		}
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return patternSet{}, nil
		}
		set = cleanPatterns(doc.Packages)

	case models.WorkspaceLerna:
		var doc struct {
			Packages []string `json:"packages"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return patternSet{}, nil
		}
		set = cleanPatterns(doc.Packages)

	case models.WorkspaceGoWork:
		patterns, err := parseGoWork(data)
		if err != nil {
			return patternSet{}, nil
		}
		set = cleanPatterns(patterns)

	case models.WorkspaceCargo:
		patterns, err := parseCargoWorkspace(data)
		if err != nil {
			return patternSet{}, nil
		}
		set = cleanPatterns(patterns)

	case models.WorkspaceNPM:
		parsed, err := parseNPMWorkspaces(data)
		if err != nil {
			return patternSet{}, nil
		}
		set = parsed
	}

	return set, nil
}

// patternSet holds the positive and negative globs a workspace declared.
type patternSet struct {
	include []string
	exclude []string
}

// cleanPatterns normalizes declared globs into slash-separated repo-relative
// patterns, separating exclusions.
//
// A leading "!" marks an exclusion (pnpm and Cargo both support it). Exclusions
// are honored rather than ignored: dropping them would report packages the
// workspace explicitly opted out of, which is a wrong answer presented
// confidently.
func cleanPatterns(in []string) patternSet {
	var set patternSet
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		excluded := false
		if strings.HasPrefix(p, "!") {
			p = strings.TrimPrefix(p, "!")
			excluded = true
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
		}

		p = filepath.ToSlash(filepath.Clean(p))
		// A pattern escaping the root is not a package of this repository.
		if p == ".." || strings.HasPrefix(p, "../") {
			continue
		}
		p = strings.TrimPrefix(p, "./")
		p = strings.TrimSuffix(p, "/")
		if p == "" || p == "." {
			continue
		}

		if excluded {
			set.exclude = append(set.exclude, p)
		} else {
			set.include = append(set.include, p)
		}
	}
	return set
}

// excludedBy reports whether dir matches any exclusion pattern.
func (s patternSet) excludedBy(dir string) bool {
	for _, ex := range s.exclude {
		if matchPattern(ex, dir) {
			return true
		}
	}
	return false
}

// matchPattern reports whether a slash-separated path matches a multi-segment
// glob.
//
// Each segment is matched independently, so `apps/*` cannot match
// `apps/web/src/index.ts` and `packages/**` matches every depth. A pattern
// ending in `/**` also matches the directory itself, which is how pnpm treats
// it.
func matchPattern(pattern, dir string) bool {
	pSegs := strings.Split(pattern, "/")
	dSegs := strings.Split(dir, "/")

	pi, di := 0, 0
	for pi < len(pSegs) {
		if pSegs[pi] == "**" {
			// A `**` absorbs this segment and everything after it, including
			// nothing at all. That is what makes `!packages/legacy/**` exclude
			// the package itself as well as its subtree.
			return true
		}
		if di >= len(dSegs) {
			// The pattern is longer than the path, so it matched only a prefix.
			return false
		}
		if !matchSegment(pSegs[pi], dSegs[di]) {
			return false
		}
		pi++
		di++
	}
	// Every pattern segment was consumed; the path must be exhausted too.
	return di == len(dSegs)
}

// parseNPMWorkspaces reads the `workspaces` field, which is either an array or
// an object with a `packages` array. Both forms are in wide use and both are
// declared by npm.
func parseNPMWorkspaces(data []byte) (patternSet, error) {
	var doc struct {
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return patternSet{}, err
	}
	if len(doc.Workspaces) == 0 {
		return patternSet{}, nil
	}

	var asList []string
	if err := json.Unmarshal(doc.Workspaces, &asList); err == nil {
		return cleanPatterns(asList), nil
	}

	var asObject struct {
		Packages []string `json:"packages"`
	}
	if err := json.Unmarshal(doc.Workspaces, &asObject); err == nil {
		return cleanPatterns(asObject.Packages), nil
	}

	// A `workspaces` value that is neither shape means this is not a
	// workspace declaration Lensyxe understands.
	return patternSet{}, nil
}

// parseGoWork reads the `use` directives from a go.work file.
//
// The Go toolchain uses a flat TOML syntax, not nested tables. A line-oriented
// reader is correct here and avoids pulling a TOML parser in for six keywords;
// it is used only for this one file shape.
func parseGoWork(data []byte) ([]string, error) {
	var (
		out      []string
		inUse    bool
		rawLines []string
	)

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}

		// Inside a parenthesized `use` block, a line has no keyword: it is a
		// bare path. Splitting on a space first and discarding lines without
		// one would drop every entry in that block, which is the common form.
		if inUse {
			if trimmed == ")" {
				inUse = false
				continue
			}
			rawLines = append(rawLines, trimmed)
			continue
		}

		keyword, rest, found := strings.Cut(trimmed, " ")
		if !found {
			continue
		}
		if strings.TrimSpace(keyword) != "use" {
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == "(" {
			inUse = true
			continue
		}
		rawLines = append(rawLines, rest)
	}

	for _, p := range rawLines {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if p == "" || strings.HasPrefix(p, "//") {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

// parseCargoWorkspace reads `members` from the `[workspace]` table.
func parseCargoWorkspace(data []byte) ([]string, error) {
	var (
		out      []string
		inTable  bool
		inArray  bool
		members  []string
		tableEnd = "[workspace]"
	)

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if strings.HasPrefix(trimmed, "[") {
			// Any other table ends the workspace table.
			inTable = trimmed == tableEnd
			inArray = false
			continue
		}
		if !inTable {
			continue
		}

		key, rest, found := strings.Cut(trimmed, "=")
		if !found || strings.TrimSpace(key) != "members" {
			// An entry inside a multi-line members array has no "=" at all,
			// so it must be handled before this check rather than skipped by
			// it.
			if inArray {
				rest = strings.TrimSpace(trimmed)
				if rest == "]" {
					inArray = false
					continue
				}
				rest = strings.Trim(strings.TrimSpace(rest), "]")
				rest = strings.Trim(rest, ",")
				if rest != "" && !strings.HasPrefix(rest, "#") {
					members = append(members, unquoteCargo(rest))
				}
			}
			continue
		}
		rest = strings.TrimSpace(rest)
		if rest == "[" {
			inArray = true
			continue
		}
		inArray = false
		rest = strings.Trim(strings.TrimSpace(rest), "]")
		rest = strings.Trim(rest, ",")
		if rest != "" {
			members = append(members, unquoteCargo(rest))
		}
	}

	if inArray {
		// A multiline array that never closed is a malformed file.
		return nil, fmt.Errorf("monorepo: unterminated members array in Cargo.toml")
	}

	out = members
	return out, nil
}

// unquoteCargo strips one layer of TOML quoting.
func unquoteCargo(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// resolvePackages expands the declared patterns into concrete package
// directories, each backed by a manifest and not excluded by the workspace.
func resolvePackages(root string, patterns patternSet) []models.PackageHealth {
	var out []models.PackageHealth
	seen := map[string]bool{}

	for _, pattern := range patterns.include {
		for _, dir := range expandPattern(root, pattern) {
			if seen[dir] || patterns.excludedBy(dir) {
				continue
			}
			seen[dir] = true

			manifest, name, eco, ok := findManifest(filepath.Join(root, filepath.FromSlash(dir)))
			if !ok {
				// The pattern matched a directory with no manifest, so it is not
				// a package. Reporting it anyway would put an unscored row in
				// the workspace table.
				continue
			}
			out = append(out, models.PackageHealth{
				Path:          dir,
				Manifest:      path.Join(dir, manifest),
				Name:          name,
				Ecosystem:     eco,
				GitApplicable: false,
			})
		}
	}
	return out
}

// ecosystemManifests maps a manifest filename to its ecosystem label.
var ecosystemManifests = []struct {
	file string
	eco  string
}{
	{manifestPackageJSON, "npm"},
	{manifestGoMod, "gomod"},
	{manifestCargoToml, "cargo"},
	{manifestPyProject, "python"},
}

// findManifest returns the manifest declaring a package, along with its
// declared name when it has one.
func findManifest(dir string) (manifest, name, eco string, ok bool) {
	for _, m := range ecosystemManifests {
		full := filepath.Join(dir, m.file)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() {
			continue
		}
		return m.file, readManifestName(full, m.file), m.eco, true
	}
	return "", "", "", false
}

// readManifestName extracts the declared package name, when the manifest
// carries one. A Go module or a Cargo member often has no name, and reporting
// an empty name is correct there.
func readManifestName(full, manifest string) string {
	info, err := os.Stat(full)
	if err != nil || info.Size() > maxManifestBytes {
		return ""
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return ""
	}

	switch manifest {
	case manifestPackageJSON:
		var doc struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return ""
		}
		return strings.TrimSpace(doc.Name)
	}

	return ""
}

// expandPattern resolves one declared glob to concrete directories.
func expandPattern(root, pattern string) []string {
	// A pattern with no wildcard names a directory directly.
	if !strings.ContainsAny(pattern, "*?[") {
		dir := filepath.Join(root, filepath.FromSlash(pattern))
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return []string{strings.TrimSuffix(pattern, "/")}
		}
		return nil
	}

	segments := strings.Split(pattern, "/")
	var found []string

	var walk func(prefix string, remaining []string)
	walk = func(prefix string, remaining []string) {
		if len(remaining) == 0 {
			if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(prefix))); err == nil && info.IsDir() {
				found = append(found, prefix)
			}
			return
		}
		if prefix != "" && strings.Count(prefix, "/")+1 >= maxDepth {
			return
		}

		seg := remaining[0]
		if seg == "**" {
			// `**` matches zero or more directories. The zero-match case is
			// only a candidate when a prefix has already been consumed: a
			// leading `**` matching zero segments would nominate the
			// repository root, which is not one of its own packages.
			if prefix != "" {
				walk(prefix, remaining[1:])
			}
			for _, sub := range subdirectories(root, prefix) {
				walk(path.Join(prefix, sub), remaining)
			}
			return
		}

		if !strings.ContainsAny(seg, "*?[") {
			walk(path.Join(prefix, seg), remaining[1:])
			return
		}

		for _, name := range subdirectories(root, prefix) {
			if matchSegment(seg, name) {
				walk(path.Join(prefix, name), remaining[1:])
			}
		}
	}
	walk("", segments)

	sort.Strings(found)
	return found
}

// subdirectories lists the immediate child directories of prefix, sorted.
func subdirectories(root, prefix string) []string {
	full := root
	if prefix != "" {
		full = filepath.Join(root, filepath.FromSlash(prefix))
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		// Build and dependency directories are never packages, and walking
		// into them is the single biggest waste available here.
		if isSkippedDir(name) {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// skippedDirs are never descended into during package discovery.
var skippedDirs = map[string]bool{
	"node_modules": true,
	".git":         true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
	"out":          true,
	"coverage":     true,
	".next":        true,
	"__pycache__":  true,
	".venv":        true,
	"venv":         true,
}

func isSkippedDir(name string) bool {
	if skippedDirs[name] {
		return true
	}
	return strings.HasPrefix(name, ".")
}

// matchSegment matches one path segment against a glob segment.
//
// Supported syntax is `*` (any run of characters except a separator) and `?`
// (exactly one character). It is deliberately not a full glob implementation:
// workspace manifests use only these two, and a general matcher would be more
// code with no additional correctness on real inputs.
func matchSegment(pattern, name string) bool {
	// Iterative wildcard match with backtracking on the last `*`. Linear in the
	// common case and immune to the exponential blowup a naive recursion has.
	var (
		p, n         int
		starP, starN = -1, 0
	)
	for n < len(name) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == name[n]):
			p++
			n++
		case p < len(pattern) && pattern[p] == '*':
			starP = p
			starN = n
			p++
		case starP >= 0:
			p = starP + 1
			starN++
			n = starN
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// sortPackages orders packages by path, then name and applies the cap,
// returning the result.
//
// The truncation has to happen here rather than at the call site because a
// cap applied in place to a slice header the caller still holds would be
// silently discarded.
func sortPackages(pkgs []models.PackageHealth) []models.PackageHealth {
	sort.SliceStable(pkgs, func(i, j int) bool {
		if pkgs[i].Path != pkgs[j].Path {
			return pkgs[i].Path < pkgs[j].Path
		}
		return pkgs[i].Name < pkgs[j].Name
	})
	if len(pkgs) > maxPackages {
		return pkgs[:maxPackages]
	}
	return pkgs
}
