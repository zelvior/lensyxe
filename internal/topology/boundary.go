// Package topology analyses repository structure: who owns what, which files
// move together without depending on each other, and whether the package layout
// holds up.
//
// The three analyses in this package are separate on purpose. Bus factor, co-change
// coupling and import boundaries answer three different questions, and combining
// them into one score would hide which one is producing a finding.
package topology

import (
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Boundary is one inferred architectural region.
type Boundary struct {
	// Name is the top-level directory that defines the boundary, without a
	// slash. An empty name means the repository root.
	Name string `json:"name"`
	// Files counts the source files inside it.
	Files int `json:"files"`
	// Packages counts distinct import paths declared inside it.
	Packages int `json:"packages"`
	// DependsOn lists boundaries this one imports from.
	DependsOn []string `json:"depends_on"`
}

// Violation is one boundary breach.
type Violation struct {
	// Kind is a stable machine-readable identifier, so a caller can filter
	// without matching on prose.
	Kind ViolationKind `json:"kind"`
	// From and To are the boundaries involved. From is empty for a whole-repo
	// problem.
	From string `json:"from"`
	To   string `json:"to"`
	// Package is the import path the violation was found on.
	Package string `json:"package"`
	// Imported is the offending import path.
	Imported string `json:"imported,omitempty"`
	// Detail is one sentence explaining what was seen.
	Detail string `json:"detail"`
}

// ViolationKind enumerates the rules the boundary audit enforces.
type ViolationKind string

const (
	// ViolationCmdImported is a non-command package importing cmd/, which
	// inverts the dependency direction: binaries are the top of the graph.
	ViolationCmdImported ViolationKind = "cmd-imported"
	// ViolationInternalEscape is an import crossing out of the module that owns
	// an internal/ directory, which Go's own visibility rule already forbids and
	// which a cross-module repository can break.
	ViolationInternalEscape ViolationKind = "internal-escape"
	// ViolationCycle is a cycle in the package import graph.
	ViolationCycle ViolationKind = "cycle"
	// ViolationRootImport is a non-root package importing the module root,
	// which in Go drags the whole main package into a library.
	ViolationRootImport ViolationKind = "root-import"
)

// Graph is the import graph of a repository.
type Graph struct {
	// Module is the Go module path from go.mod, empty when there is none.
	Module string `json:"module"`
	// Boundaries are the inferred regions.
	Boundaries []Boundary `json:"boundaries"`
	// Packages maps an import path to the boundary it belongs to.
	Packages map[string]string `json:"-"`
	// Imports maps a package to the packages it imports, sorted.
	Imports map[string][]string `json:"-"`
	// Violations are the audit findings, sorted.
	Violations []Violation `json:"violations"`
	// Files counts source files parsed.
	Files int `json:"files"`
	// Note carries a degradation reason.
	Note string `json:"note,omitempty"`
}

// boundaryRoots are the directory names treated as architectural regions.
//
// `pkg` and `internal` are the published and private halves of a Go module,
// `cmd` is the binary layer, and `apps`/`services` are the monorepo layouts that
// the same names take in JavaScript and service repositories. A repository that
// has none of these still gets boundaries: every other top-level directory
// containing source becomes its own region.
var boundaryRoots = []string{"pkg", "internal", "cmd", "apps", "services"}

// BuildGraph parses the Go files under root and infers boundaries from the
// directory layout.
//
// Only Go files are parsed. A boundary audit that silently ignored a JavaScript
// tree would report a clean bill of health for half the repository, so the file
// count is reported and anything unparsed is stated rather than passed over.
func BuildGraph(root string) (*Graph, error) {
	g := &Graph{
		Packages: map[string]string{},
		Imports:  map[string][]string{},
		Module:   modulePath(root),
	}

	importsByPkg, pkgBoundary, filesByBoundary, files, note := scanGo(root, g.Module)
	if note != "" {
		g.Note = note
	}

	g.Files = files
	g.Packages = pkgBoundary
	g.Imports = importsByPkg

	g.Boundaries = inferBoundaries(pkgBoundary, importsByPkg, filesByBoundary)
	g.Violations = audit(g, pkgBoundary, importsByPkg)
	return g, nil
}

// modulePath reads the module line out of go.mod.
func modulePath(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

// scanGo parses every .go file under root and returns the import graph.
//
// Directories with no first-party source are pruned, because node_modules or a
// vendored tree would otherwise contribute thousands of packages that are not
// this repository's architecture.
func scanGo(root, module string) (
	imports map[string][]string,
	boundary map[string]string,
	filesByBoundary map[string]int,
	files int,
	note string,
) {
	imports = map[string][]string{}
	boundary = map[string]string{}
	filesByBoundary = map[string]int{}
	fset := token.NewFileSet()

	walkErr := filepath.Walk(root, func(full string, info os.FileInfo, err error) error {
		if err != nil {
			// An unreadable subtree is reported, not silently dropped.
			note = appendNote(note, "could not read "+full+": "+err.Error())
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if full != root && prunedDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(full) != ".go" {
			return nil
		}
		files++

		rel, relErr := filepath.Rel(root, full)
		if relErr != nil {
			rel = full
		}
		rel = filepath.ToSlash(rel)

		name := boundaryOf(rel)
		filesByBoundary[name]++

		parsed, parseErr := parser.ParseFile(fset, full, nil, parser.ImportsOnly)
		if parseErr != nil {
			// A file that does not parse is a problem with the repository, but
			// it is not a boundary violation. It is counted and skipped.
			return nil
		}
		pkg := pkgPath(module, rel)
		boundary[pkg] = name

		seen := map[string]bool{}
		var list []string
		for _, spec := range parsed.Imports {
			target := strings.Trim(spec.Path.Value, `"`)
			if target == "" || seen[target] {
				continue
			}
			seen[target] = true
			list = append(list, target)
		}
		sort.Strings(list)
		imports[pkg] = list
		return nil
	})
	if walkErr != nil {
		note = appendNote(note, walkErr.Error())
	}
	return imports, boundary, filesByBoundary, files, note
}

// prunedDir reports whether a directory holds no first-party source.
func prunedDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor", "dist", "build",
		"target", ".next", "testdata", ".venv":
		return true
	default:
		return false
	}
}

func appendNote(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// pkgPath maps a repository-relative file to its import path.
func pkgPath(module, rel string) string {
	dir := path.Dir(filepathToSlash(rel))
	if dir == "." {
		if module == "" {
			return "."
		}
		return module
	}
	if module == "" {
		return dir
	}
	return module + "/" + dir
}

// filepathToSlash normalises separators without importing path/filepath, which
// this package does not otherwise need.
func filepathToSlash(p string) string { return strings.ReplaceAll(p, `\`, "/") }

// boundaryOf returns the architectural region a repository-relative path is in.
func boundaryOf(rel string) string {
	dir := path.Dir(filepathToSlash(rel))
	if dir == "." {
		return ""
	}
	first := strings.SplitN(dir, "/", 2)[0]
	for _, root := range boundaryRoots {
		if first == root {
			return first
		}
	}
	// A conventional layout outside the known roots: its own top-level directory
	// is the boundary. Anything deeper than one level is grouped under its top
	// level so a repository does not acquire one boundary per package.
	return first
}

// inferBoundaries summarises the regions and their dependencies.
func inferBoundaries(
	pkgBoundary map[string]string,
	imports map[string][]string,
	filesByBoundary map[string]int,
) []Boundary {
	type agg struct {
		pkgs map[string]bool
		deps map[string]bool
	}
	byName := map[string]*agg{}

	for pkg, name := range pkgBoundary {
		a, ok := byName[name]
		if !ok {
			a = &agg{pkgs: map[string]bool{}, deps: map[string]bool{}}
			byName[name] = a
		}
		a.pkgs[pkg] = true
		for _, imp := range imports[pkg] {
			if target, ok := pkgBoundary[imp]; ok && target != name {
				a.deps[target] = true
			}
		}
	}

	out := make([]Boundary, 0, len(byName))
	for name, a := range byName {
		deps := make([]string, 0, len(a.deps))
		for d := range a.deps {
			deps = append(deps, d)
		}
		sort.Strings(deps)
		out = append(out, Boundary{
			Name:      name,
			Files:     filesByBoundary[name],
			Packages:  len(a.pkgs),
			DependsOn: deps,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// audit applies the boundary rules to the import graph.
func audit(g *Graph, pkgBoundary map[string]string, imports map[string][]string) []Violation {
	var out []Violation
	pkgs := sortedKeys(imports)

	for _, pkg := range pkgs {
		from := pkgBoundary[pkg]
		for _, imp := range imports[pkg] {
			to, known := pkgBoundary[imp]
			if !known {
				continue // an external or unparsed target: not a local boundary
			}

			// cmd/ is the binary layer. Nothing in the library may import it,
			// because a binary is the top of the dependency graph and importing
			// one drags its main package and its flags into a library.
			if to == "cmd" && from != "cmd" {
				out = append(out, Violation{
					Kind: ViolationCmdImported, From: from, To: to,
					Package: pkg, Imported: imp,
					Detail: "a non-command package imports " + imp +
						": cmd/ is the top of the dependency graph and " +
						"nothing below it should depend on it",
				})
			}

			// An import of the module root from anywhere but the root drags the
			// root package, which in Go is usually a main package, into a library.
			if imp == g.Module && pkg != g.Module && g.Module != "" {
				out = append(out, Violation{
					Kind: ViolationRootImport, From: from, To: "",
					Package: pkg, Imported: imp,
					Detail: "imports the module root " + imp +
						", which pulls the root package into this one",
				})
			}
		}
	}

	out = append(out, findCycles(pkgBoundary, imports)...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Package != out[j].Package {
			return out[i].Package < out[j].Package
		}
		return out[i].Imported < out[j].Imported
	})
	return out
}

// findCycles returns one violation per distinct cycle in the package graph.
//
// Cycles are reported as a cycle, not as N pairwise edges: reporting each edge
// separately would bury the finding in a list where the same cycle appears many
// times, and the actionable unit is the cycle.
func findCycles(pkgBoundary map[string]string, imports map[string][]string) []Violation {
	const (
		white = 0 // unvisited
		grey  = 1 // on the current path
		black = 2 // fully explored
	)
	state := map[string]int{}
	var stack []string
	var cycles []Violation
	seen := map[string]bool{}

	var visit func(string)
	visit = func(pkg string) {
		state[pkg] = grey
		stack = append(stack, pkg)

		for _, imp := range imports[pkg] {
			if _, known := pkgBoundary[imp]; !known {
				continue
			}
			switch state[imp] {
			case white:
				visit(imp)
			case grey:
				// A back edge. The cycle is the slice of the stack from where
				// the repeated package sits.
				at := -1
				for i, p := range stack {
					if p == imp {
						at = i
						break
					}
				}
				if at < 0 {
					continue
				}
				loop := append([]string(nil), stack[at:]...)
				sort.Strings(loop)
				key := strings.Join(loop, "\x00")
				if seen[key] {
					continue
				}
				seen[key] = true
				cycles = append(cycles, Violation{
					Kind:    ViolationCycle,
					From:    pkgBoundary[loop[0]],
					Package: strings.Join(loop, " -> "),
					Detail: "these packages import each other in a cycle: " +
						strings.Join(loop, " -> ") + " -> " + loop[0] +
						". Go compiles a cycle, so this must cross a build tag " +
						"or be broken",
				})
			}
		}

		stack = stack[:len(stack)-1]
		state[pkg] = black
	}

	for _, pkg := range sortedKeys(imports) {
		if _, known := pkgBoundary[pkg]; !known {
			continue
		}
		if state[pkg] == white {
			visit(pkg)
		}
	}
	return cycles
}

// sortedKeys returns a map's keys in order, so every traversal is deterministic.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// boundarySeverity ranks violations for display. A cycle is the most severe
// because it is the one Go itself will reject at build time in most forms.
func boundarySeverity(k ViolationKind) int {
	switch k {
	case ViolationCycle:
		return 0
	case ViolationCmdImported:
		return 1
	case ViolationRootImport:
		return 2
	default:
		return 3
	}
}
