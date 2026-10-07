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
//
// # Layout
//
// The two halves are separate files because they answer different questions and
// fail differently. A manifest parser that misreads a field undercounts a
// declaration; a lockfile counter that misreads one overstates a resolved graph.
// Keeping them apart makes it obvious which half a change touches.
//
//	analyzer.go   the ecosystem registry, the walk, and the findings
//	npm.go        package.json
//	gomod.go      go.mod
//	python.go     pyproject.toml and requirements.txt
//	lockfile.go   every resolved-graph counter, one per lockfile format
package dependencies

import (
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
//
// The registry is a table rather than a switch so adding an ecosystem means
// adding one entry, not editing the detection loop and three branches inside it.
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

// ---------------------------------------------------------------------------
// filesystem helpers
// ---------------------------------------------------------------------------

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
//
// The size is checked after the open rather than from the path so the check and
// the read cannot disagree about which file was opened.
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

func relOrBase(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(path)
}
