package dependencies

// The go.mod reader.
//
// go.mod is scanned line by line rather than parsed through
// golang.org/x/mod, which keeps this package's dependency surface to none. That
// is a deliberate trade: the scanner below only has to be correct about the
// `require` block and the `// indirect` marker, which is all the counts need.

import (
	"bufio"
	"fmt"
	"sort"
	"strings"
)

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
