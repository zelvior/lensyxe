package monorepo

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/zelvior/lensyxe/internal/code"
	"github.com/zelvior/lensyxe/internal/dependencies"
	"github.com/zelvior/lensyxe/internal/metrics"
	"github.com/zelvior/lensyxe/pkg/models"
)

// Attribute scores each detected package from an existing repository walk.
//
// It is deliberately given the already-walked per-file records rather than
// being handed a directory to scan: the walk is the expensive part of the
// analysis, and repeating it once per package would make a monorepo breakdown
// cost proportional to the package count for no new information. Only the
// package manifests are read separately, which is a handful of small files.
//
// Git-derived components are excluded from every package score. Commit
// cadence, authorship, and bus factor describe the repository, not a directory,
// and there is no honest way to attribute them. The scorer renormalizes the
// remaining weights, so a package score covers code and dependencies;
// PackageHealth.GitApplicable says so explicitly.
func Attribute(
	ctx context.Context,
	root string,
	ws *models.Workspace,
	perFile []code.FileRecord,
	churnByPath map[string]int,
	cfg code.Config,
) error {
	if ws == nil {
		return nil
	}

	depCfg := dependencies.DefaultConfig()

	for i := range ws.Packages {
		if err := ctx.Err(); err != nil {
			return err
		}
		pkg := &ws.Packages[i]

		agg := code.Aggregate(selectRecords(perFile, pkg.Path), churnByPath, cfg, "")

		// A package whose manifest cannot be read is scored on code alone
		// rather than failing the whole run.
		deps := models.DependencyStats{Ecosystems: []models.EcosystemStats{}}
		if res, err := dependencies.Analyze(
			filepath.Join(root, filepath.FromSlash(pkg.Path)), depCfg,
		); err == nil {
			deps = res.Stats
		}

		score := metrics.Compute(agg.Stats, models.GitStats{}, deps)

		pkg.Health = score.Health
		pkg.Risks = score.Risks
		pkg.Files = agg.Stats.Files
		pkg.SourceFiles = agg.Stats.SourceFiles
		pkg.TestFiles = agg.Stats.TestFiles
		pkg.CodeLines = agg.Stats.CodeLines
		pkg.TestFileRatio = agg.Stats.TestFileRatio
		pkg.Hotspots = agg.Stats.Hotspots
		pkg.Dependencies = deps
		pkg.GitApplicable = false

		// Empty slices rather than nil: the JSON contract and the dashboard
		// both iterate these without a nil check, and `null` reads as missing
		// data rather than as "none found".
		if pkg.Hotspots == nil {
			pkg.Hotspots = []models.Hotspot{}
		}
		if pkg.Risks == nil {
			pkg.Risks = []models.Risk{}
		}
		if deps.Ecosystems == nil {
			pkg.Dependencies.Ecosystems = []models.EcosystemStats{}
		}
	}

	return nil
}

// AttributeRisks attaches repository-level risks to the packages that contain
// their subject.
//
// A risk's Subject is the path it concerns. Attributing by that path is what
// makes the breakdown actionable: a risk listed under a package is one whose
// evidence points inside it. A risk with no path, or with a subject that is an
// identifier rather than a path, is attributed to nobody, because guessing
// which package owns a global problem would be fabrication.
func AttributeRisks(ws *models.Workspace, risks []models.Risk) {
	if ws == nil {
		return
	}
	for i := range ws.Packages {
		ws.Packages[i].Attribution = nil
	}

	for _, r := range risks {
		if r.Subject == "" || !looksLikePath(r.Subject) {
			continue
		}
		for i := range ws.Packages {
			if insidePackage(r.Subject, ws.Packages[i].Path) {
				ws.Packages[i].Attribution = append(ws.Packages[i].Attribution, r.ID)
				break
			}
		}
	}

	for i := range ws.Packages {
		ws.Packages[i].Attribution = dedupeSorted(ws.Packages[i].Attribution)
	}
}

// selectRecords returns the records whose path lies inside prefix.
func selectRecords(records []code.FileRecord, prefix string) []code.FileRecord {
	out := make([]code.FileRecord, 0, len(records)/2)
	for _, r := range records {
		if insidePackage(r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// insidePackage reports whether a repo-relative file path lies in a package.
//
// The boundary check compares path segments rather than raw string prefixes:
// without it, `apps/web-legacy/src/a.ts` would be attributed to `apps/web`.
func insidePackage(filePath, pkgPath string) bool {
	if pkgPath == "" {
		return false
	}
	if filePath == pkgPath {
		return true
	}
	return len(filePath) > len(pkgPath) &&
		filePath[:len(pkgPath)] == pkgPath &&
		filePath[len(pkgPath)] == '/'
}

// looksLikePath reports whether a risk subject is a file path rather than an
// identifier such as a module or package name.
//
// The test is deliberately narrow: a subject counts as a path only when its
// final segment looks like a filename. Package names contain separators too
// (`@scope/pkg`, `github.com/spf13/cobra`), so "contains a slash" would
// misattribute module-wide risks to whichever package happens to share a name
// prefix with them.
func looksLikePath(s string) bool {
	if s == "" {
		return false
	}

	// Reject anything with a path separator that cannot be a repo-relative
	// path: an absolute path, a Windows drive letter, or a URI scheme.
	if strings.Contains(s, "\\") || strings.Contains(s, "://") {
		return false
	}
	if strings.HasPrefix(s, "/") || filepath.IsAbs(s) {
		return false
	}
	if len(s) >= 2 && s[1] == ':' {
		return false
	}

	base := s
	if i := strings.LastIndex(s, "/"); i >= 0 {
		base = s[i+1:]
	}
	if base == "" || base == "." || base == ".." {
		return false
	}

	// A dotfile like ".gitignore" is a path; a bare word is not. Requiring a
	// character before the dot keeps a hidden file from being read as an
	// extension.
	dot := strings.LastIndex(base, ".")
	return dot > 0 && dot < len(base)-1
}

// dedupeSorted sorts and removes duplicates, returning nil for an empty input
// so an unattributed package serializes as absent rather than as [].
func dedupeSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sortStrings(out)

	deduped := out[:1]
	for _, s := range out[1:] {
		if s != deduped[len(deduped)-1] {
			deduped = append(deduped, s)
		}
	}
	return deduped
}

// sortStrings is an insertion sort: these slices hold at most a handful of
// risk IDs, so the asymptotics are irrelevant and the allocation is not.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
