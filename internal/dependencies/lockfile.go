package dependencies

// The resolved-graph counters, one per lockfile format.
//
// Everything in this file answers the same question -- how many packages did the
// resolver actually install -- and every format answers it differently. The
// formats disagree about what a single entry looks like, so the counting rule
// is per-format rather than shared, and only the outermost key scan is common.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

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
