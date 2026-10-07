package dependencies

// The package.json reader.
//
// npm is the one ecosystem where the manifest is JSON, and the only one where a
// package can legitimately appear in two dependency buckets at once.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"sort"
)

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

// sortedKeys returns the map's keys, sorted and capped.
//
// The sort is what makes the package list deterministic: Go randomizes map
// iteration, so an unsorted list would reorder between two runs of the same
// manifest and break the byte-for-byte reproducibility this package promises.
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
