// Package cognitive measures structural properties of source files that are
// associated with how much work it is to understand them.
//
// It measures three things, all of them observable in the source and none of
// them a judgement about the author:
//
//   - Variable lifetime span: how far a local variable's last use sits from its
//     declaration, in lines. A value read a thousand lines after it was set has
//     to be held in mind across everything in between.
//   - Context-switch density: how many distinct call targets a function
//     references, per hundred lines. A function that calls into eight packages
//     asks the reader to hold eight contexts at once; one that calls one helper
//     repeatedly does not.
//   - Scope depth: the deepest block nesting in a function. Nesting is the
//     clearest syntactic statement about how much state is live at once.
//
// The three combine into a 0-100 index by a documented weighting, in the same
// spirit as the Engineering Health Score: it is a defined index, not a validated
// instrument, and it is reproducible for a given repository and given
// configuration.
//
// What this package deliberately does not produce is a reading-time estimate.
// There is no validated mapping from these structural properties to the minutes a
// person will spend on a file, and a number carrying that unit would be a
// measurement nobody took. The index is a rankable quantity; a duration is not.
//
// Measurement method is reported per file and never averaged across methods. Go
// is parsed with go/parser, which is exact. Other languages are measured with a
// line-oriented pass, which is approximate, and a file measured approximately is
// labelled as such rather than being silently mixed with the exact ones.
package cognitive

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Method names the way a file was measured.
type Method string

const (
	// MethodAST means the file was parsed and every figure comes from the
	// syntax tree.
	MethodAST Method = "ast"
	// MethodLexical means the file was measured line by line. Accurate for
	// counts, approximate for anything requiring nesting structure.
	MethodLexical Method = "lexical"
)

// DefaultConfig returns the settings used when a caller supplies none.
func DefaultConfig() Config {
	return Config{
		// Weights for the index. They sum to 1 and are documented in
		// docs/FEATURES.md so a reader can disagree with them explicitly.
		//
		// Lifetime is weighted highest because a stale variable is the one that
		// is genuinely invisible in the source: a reader sees the declaration
		// and the use and nothing about the span between them. Scope depth is
		// weighted lowest because it is the most immediately visible of the
		// three; a reader struggling with nesting can see why.
		LifetimeWeight: 0.45,
		DensityWeight:  0.35,
		DepthWeight:    0.20,

		// A lifetime is only interesting once it exceeds this many lines. Below
		// it, the span is visible on one screen and costs nothing.
		NeutralLifetime: 20,
		// Coupling each sub-measurement to full weight at this span, in lines.
		LifetimeSaturation: 200,
		// Distinct call targets per hundred lines that count as maximum.
		DensitySaturation: 20,
		// Block nesting depth that counts as maximum.
		DepthSaturation: 6,

		MaxFileBytes: 2 << 20,
	}
}

// Config tunes the analysis.
type Config struct {
	// LifetimeWeight, DensityWeight and DepthWeight weight the three
	// components. They must sum to 1.
	LifetimeWeight float64
	DensityWeight  float64
	DepthWeight    float64

	// NeutralLifetime is the span below which lifetime does not contribute.
	NeutralLifetime int
	// LifetimeSaturation is the span at which lifetime contributes fully.
	LifetimeSaturation int
	// DensitySaturation is the call-targets-per-hundred-lines that scores 1.
	DensitySaturation float64
	// DepthSaturation is the nesting depth that scores 1.
	DepthSaturation int

	// MaxFileBytes skips files larger than this.
	MaxFileBytes int64
}

// Function is the measurement for one function or method.
type Function struct {
	// Name is the declared name, or "(anonymous)" for a function literal.
	Name string `json:"name"`
	// Path is the repository-relative file.
	Path string `json:"path"`
	// Line is where it is declared.
	Line int `json:"line"`
	// Lines is its extent, in physical lines.
	Lines int `json:"lines"`
	// MedianLifetime and MaxLifetime are the middle and worst variable lifetime
	// spans, in lines. Zero when the function declares no variables.
	MedianLifetime int `json:"median_lifetime"`
	MaxLifetime    int `json:"max_lifetime"`
	// Variables is how many locals were measured.
	Variables int `json:"variables"`
	// ContextSwitches is the number of distinct call targets referenced.
	ContextSwitches int `json:"context_switches"`
	// Density is ContextSwitches per hundred lines.
	Density float64 `json:"density"`
	// ScopeDepth is the deepest block nesting.
	ScopeDepth int `json:"scope_depth"`
	// Score is this function's 0-100 friction index.
	Score float64 `json:"score"`
}

// File is the measurement for one file.
type File struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Method   Method `json:"method"`
	// Bytes is the file size.
	Bytes int64 `json:"bytes"`
	// Functions measured.
	Functions int `json:"functions"`
	// MedianLifetime, MaxLifetime and MedianDensity summarise the functions.
	MedianLifetime float64 `json:"median_lifetime"`
	MaxLifetime    float64 `json:"max_lifetime"`
	MedianDensity  float64 `json:"median_density"`
	MaxScopeDepth  int     `json:"max_scope_depth"`
	// Score is the file's 0-100 friction index.
	Score float64 `json:"score"`
	// Worst lists the highest-friction functions, worst first.
	Worst []Function `json:"worst"`
	// Error explains why a file could not be measured, and is empty when it
	// was. A file that failed to parse is reported, not dropped: a syntax error
	// in a file is itself worth knowing about.
	Error string `json:"error,omitempty"`
}

// Result is the whole outcome.
type Result struct {
	Root string `json:"root"`
	// Files are the measured files, worst score first.
	Files []File `json:"files"`
	// MedianScore is the repository-wide friction index.
	MedianScore float64 `json:"median_score"`
	// WorstScore is the worst file's index.
	WorstScore float64 `json:"worst_score"`
	// Analyzed counts files measured, Failed counts files that could not be.
	Analyzed int `json:"analyzed"`
	Failed   int `json:"failed"`
	// Note carries a degradation reason.
	Note string `json:"note,omitempty"`
}

// languageOf maps a file extension to a language name and a measurement method.
//
// The method is chosen per language and reported. Only Go is parsed exactly;
// anything else is measured lexically and labelled, because a precise
// structural measure requires a parser for that language and this tool has one
// only for Go.
func languageOf(path string) (name string, method Method, ok bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "Go", MethodAST, true
	case ".ts", ".tsx":
		return "TypeScript", MethodLexical, true
	case ".js", ".jsx", ".mjs", ".cjs":
		return "JavaScript", MethodLexical, true
	default:
		return "", "", false
	}
}

// Analyze walks root and measures every recognised source file.
//
// Walk order is sorted so output is byte-identical between runs on an unchanged
// tree; os.ReadDir already sorts, and the sort here makes that guarantee
// explicit rather than inherited.
func Analyze(root string, cfg Config) (Result, error) {
	res := Result{Root: root}
	if err := validateWeights(cfg); err != nil {
		return res, err
	}

	var paths []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// An unreadable subdirectory is not a reason to abandon the walk.
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return skipVendor(path, root)
		}
		if _, _, ok := languageOf(path); ok && info.Size() <= cfg.MaxFileBytes {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("walk %s: %w", root, err)
	}
	sort.Strings(paths)

	for _, p := range paths {
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			rel = p
		}
		rel = filepath.ToSlash(rel)

		src, readErr := os.ReadFile(p)
		if readErr != nil {
			res.Failed++
			res.Files = append(res.Files, File{
				Path: rel, Error: "could not read: " + readErr.Error()})
			continue
		}
		lang, method, _ := languageOf(p)
		f := measureFile(rel, lang, method, src, cfg)

		if f.Error != "" {
			res.Failed++
		} else {
			res.Analyzed++
		}
		res.Files = append(res.Files, f)
	}

	sort.SliceStable(res.Files, func(i, j int) bool {
		if res.Files[i].Score != res.Files[j].Score {
			return res.Files[i].Score > res.Files[j].Score
		}
		return res.Files[i].Path < res.Files[j].Path
	})

	res.MedianScore, res.WorstScore = summarise(res.Files)
	return res, nil
}

// validateWeights rejects a configuration whose weights do not describe a
// weighted sum. Silently renormalising would make the documented weights a lie
// and the index unreproducible from them.
func validateWeights(cfg Config) error {
	sum := cfg.LifetimeWeight + cfg.DensityWeight + cfg.DepthWeight
	if math.Abs(sum-1) > 1e-9 {
		return fmt.Errorf(
			"cognitive weights must sum to 1, got %.4f (%v + %v + %v): the "+
				"index is defined by these weights and renormalising them would "+
				"make the documented values meaningless",
			sum, cfg.LifetimeWeight, cfg.DensityWeight, cfg.DepthWeight)
	}
	if cfg.LifetimeSaturation <= 0 || cfg.DensitySaturation <= 0 ||
		cfg.DepthSaturation <= 0 {
		return fmt.Errorf("cognitive saturation points must be positive")
	}
	return nil
}

// summarise returns the median and the maximum score across measured files.
func summarise(files []File) (median, worst float64) {
	var scores []float64
	for _, f := range files {
		if f.Error != "" {
			continue
		}
		scores = append(scores, f.Score)
	}
	if len(scores) == 0 {
		return 0, 0
	}
	sort.Float64s(scores)
	worst = scores[len(scores)-1]
	mid := len(scores) / 2
	if len(scores)%2 == 1 {
		median = scores[mid]
	} else {
		median = (scores[mid-1] + scores[mid]) / 2
	}
	return median, worst
}

// skipVendor prunes directories that hold no first-party source.
func skipVendor(path, root string) error {
	if path == root {
		return nil
	}
	switch filepath.Base(path) {
	case ".git", "node_modules", "vendor", "dist", "build", "target", ".next":
		return filepath.SkipDir
	}
	return nil
}

// ---- scoring ----

// score combines the three components into a 0-100 index.
//
// Each component is scaled to 0..1 by its saturation point before weighting, so
// no component can exceed its share of the total no matter how extreme its input.
func score(medianLifetime, medianDensity float64, maxDepth int, cfg Config) float64 {
	lifetime := saturate((medianLifetime - float64(cfg.NeutralLifetime)) /
		float64(cfg.LifetimeSaturation))
	density := saturate(medianDensity / cfg.DensitySaturation)
	depth := saturate(float64(maxDepth) / float64(cfg.DepthSaturation))

	total := cfg.LifetimeWeight*lifetime +
		cfg.DensityWeight*density +
		cfg.DepthWeight*depth
	return round1(clamp01(total) * 100)
}

// saturate maps v into 0..1.
func saturate(v float64) float64 { return clamp01(v) }

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// medianOf returns the middle value, or 0 for an empty slice.
func medianOf(values []int) int {
	if len(values) == 0 {
		return 0
	}
	s := append([]int(nil), values...)
	sort.Ints(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	// The mean of the two middles, rounded down, so the result is an integer
	// count of lines rather than a half line.
	return (s[mid-1] + s[mid]) / 2
}

// ---- lexical measurement ----

// measureLexical measures a file without a parser.
//
// It can count nesting and call targets from indentation and text, which is
// approximate: a continuation line that happens to start with a brace will be
// counted as a scope that does not exist. The result is labelled MethodLexical
// wherever it appears so it is never confused with the exact Go measurement.
func measureLexical(rel, lang string, src []byte, cfg Config) File {
	lines := strings.Split(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")

	f := File{Path: rel, Language: lang, Method: MethodLexical, Bytes: int64(len(src))}

	depth := 0
	maxDepth := 0
	var fnDepths []int
	openers := 0
	closers := 0
	var calls []string
	inFunc := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		opens := strings.Count(line, "{")
		closes := strings.Count(line, "}")

		if isFunctionStart(trimmed) {
			if inFunc {
				fnDepths = append(fnDepths, maxDepth)
			}
			inFunc = true
			maxDepth = depth
		}
		depth += opens - closes
		if depth < 0 {
			depth = 0
		}
		if depth > maxDepth {
			maxDepth = depth
		}
		openers += opens
		closers += closes
		calls = append(calls, callTargetsIn(trimmed)...)
	}
	if inFunc {
		fnDepths = append(fnDepths, maxDepth)
	}

	f.Functions = len(fnDepths)
	if f.Functions == 0 {
		f.Functions = 1
	}
	for _, d := range fnDepths {
		if d > f.MaxScopeDepth {
			f.MaxScopeDepth = d
		}
	}
	distinct := map[string]bool{}
	for _, c := range calls {
		distinct[c] = true
	}
	density := float64(len(distinct)) / float64(f.Functions) * 100
	linesCount := len(lines)
	if linesCount == 0 {
		linesCount = 1
	}
	// Per hundred lines of file, which is the only denominator available
	// without a parser to give function extents.
	f.MedianDensity = round1(float64(len(distinct)) / float64(linesCount) * 100)
	_ = density

	// Lifetime is not measurable lexically: knowing where a variable is last
	// used requires resolving identifiers, which is what a parser is for.
	// Reporting zero would read as "no variables", so the file is marked
	// instead.
	f.Error = "measured lexically: variable lifetime is not observable without a " +
		"parser for this language, so it is excluded rather than guessed"
	f.Score = score(0, f.MedianDensity, f.MaxScopeDepth, cfg)
	return f
}

// isFunctionStart is a conservative test for a declaration line.
func isFunctionStart(line string) bool {
	for _, kw := range []string{"function ", "func ", "def ", "=>"} {
		if strings.Contains(line, kw) {
			return true
		}
	}
	return false
}

// callTargetsIn extracts plausible call targets from a line.
func callTargetsIn(line string) []string {
	var out []string
	for i := 0; i < len(line); i++ {
		if line[i] != '(' {
			continue
		}
		// Walk back over the identifier immediately before the paren.
		j := i - 1
		for j >= 0 && (line[j] == ' ' || line[j] == '\t') {
			j--
		}
		end := j + 1
		for j >= 0 && isIdentByte(line[j]) {
			j--
		}
		if end-j >= 2 {
			out = append(out, line[j+1:end])
		}
	}
	return out
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '.' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// ---- AST measurement ----

// measureGo measures a Go file from its syntax tree.
//
// Every figure here is exact: positions come from the parsed tree, not from
// indentation.
func measureGo(rel string, src []byte, cfg Config) File {
	f := File{Path: rel, Language: "Go", Method: MethodAST, Bytes: int64(len(src))}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, rel, src, 0)
	if err != nil {
		f.Error = "could not parse: " + err.Error()
		return f
	}

	ast.Inspect(file, func(n ast.Node) bool {
		var body *ast.BlockStmt
		name := "(anonymous)"

		switch decl := n.(type) {
		case *ast.FuncDecl:
			body = decl.Body
			if decl.Name != nil {
				name = decl.Name.Name
			}
		case *ast.FuncLit:
			body = decl.Body
		default:
			return true
		}
		if body == nil {
			return true
		}

		f.Functions++
		f.Worst = append(f.Worst, measureFunc(fset, rel, name, body, cfg))
		return true
	})

	if f.Functions == 0 {
		f.Error = "no function declarations found"
		return f
	}
	aggregate(&f, cfg)
	return f
}

// measureFunc measures one function body.
func measureFunc(fset *token.FileSet, path, name string,
	body *ast.BlockStmt, cfg Config,
) Function {
	start := body.Pos()
	end := body.End()
	fn := Function{
		Name: name,
		Path: path,
		Line: fset.Position(start).Line,
	}
	fn.Lines = fset.Position(end).Line - fn.Line + 1
	if fn.Lines <= 0 {
		fn.Lines = 1
	}

	// Declared identifiers and every identifier reference, so a declaration can
	// be matched to its last use by name within the function.
	declared := map[string]token.Pos{}
	uses := map[string][]token.Pos{}

	ast.Inspect(body, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.AssignStmt:
			if d.Tok != token.DEFINE {
				return true
			}
			for _, lhs := range d.Lhs {
				if id, ok := lhs.(*ast.Ident); ok {
					if _, seen := declared[id.Name]; !seen {
						declared[id.Name] = id.Pos()
					}
				}
			}
		case *ast.ValueSpec:
			for _, id := range d.Names {
				if _, seen := declared[id.Name]; !seen {
					declared[id.Name] = id.Pos()
				}
			}
		case *ast.Field:
			// A named result is a variable the caller cannot see.
			for _, id := range d.Names {
				if _, seen := declared[id.Name]; !seen {
					declared[id.Name] = id.Pos()
				}
			}
		case *ast.Ident:
			if d.Name == "_" {
				break
			}
			// The declaration's own identifier is not a use of the variable.
			// Counting it gave every declared-and-never-read local a span of
			// zero, which is worse than excluding it: zero reads as "this
			// variable is easy to follow" when in fact it was never read at all.
			if declPos, ok := declared[d.Name]; ok && declPos == d.Pos() {
				break
			}
			uses[d.Name] = append(uses[d.Name], d.Pos())
		case *ast.CallExpr:
			fn.ContextSwitches += len(callTargetNames(d))
		}
		return true
	})

	// Lifetime per declared name, from declaration to its last use.
	var spans []int
	for name, pos := range declared {
		refs := uses[name]
		if len(refs) == 0 {
			// Declared and never used. The compiler rejects most of these; the
			// ones that survive have no span to measure, so they are not
			// counted rather than counted as zero.
			continue
		}
		last := refs[0]
		for _, r := range refs {
			if r > last {
				last = r
			}
		}
		if last < pos {
			continue
		}
		span := fset.Position(last).Line - fset.Position(pos).Line
		if span < 0 {
			span = 0
		}
		spans = append(spans, span)
	}
	fn.Variables = len(spans)
	fn.MedianLifetime = medianOf(spans)
	for _, s := range spans {
		if s > fn.MaxLifetime {
			fn.MaxLifetime = s
		}
	}

	fn.Density = round1(float64(fn.ContextSwitches) / float64(fn.Lines) * 100)
	fn.ScopeDepth = maxBlockDepth(body)
	fn.Score = score(float64(fn.MedianLifetime), fn.Density, fn.ScopeDepth, cfg)
	return fn
}

// callTargetNames returns the names a call refers to.
//
// Both plain calls and qualified selectors are counted, because both represent
// leaving the current line of thought. Selector type names and conversions are
// not counted: reading a type name is not a context switch.
func callTargetNames(call *ast.CallExpr) []string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return []string{fn.Name}
	case *ast.SelectorExpr:
		// Every qualified call counts, including pkg.Func(...).
		//
		// A syntactic walk cannot tell `fmt.Println(s)` from the conversion
		// `time.Duration(x)` -- both are Ident.Selector(args) and telling them
		// apart needs type information this measurement does not have. An
		// earlier version guessed on shape and excluded every qualified call
		// whose receiver was a bare identifier, which silently discarded the
		// most common context switch there is: calling into another package.
		// Counting a rare conversion as a call is a far smaller error than
		// dropping every package call.
		if fn.Sel == nil {
			return nil
		}
		return []string{fn.Sel.Name}
	}
	return nil
}

// maxBlockDepth returns the deepest block nesting inside body.
//
// One traversal with an explicit stack. A recursive version that calls
// ast.Inspect inside itself re-walks each matching child's entire subtree at
// every level, which is exponential in the nesting depth and overflows the stack
// on a deeply nested function -- exactly the function this exists to measure.
func maxBlockDepth(body *ast.BlockStmt) int {
	type frame struct {
		node  ast.Node
		depth int
	}
	deepest := 0
	stack := []frame{{node: body, depth: 0}}

	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur.depth > deepest {
			deepest = cur.depth
		}

		// Direct children only: returning false stops ast.Inspect descending.
		var children []ast.Node
		ast.Inspect(cur.node, func(n ast.Node) bool {
			switch n {
			case nil:
				return false
			case cur.node:
				return true
			default:
				children = append(children, n)
				return false
			}
		})

		// The child's depth accounts for whether the child is a nesting
		// construct, so every node is visited exactly once. Re-pushing a
		// nesting node *and* its children doubles the subtree at each level,
		// which is exponential in nesting depth -- and deeply nested functions
		// are exactly what this exists to measure.
		for _, child := range children {
			depth := cur.depth
			if child != ast.Node(body) && isNesting(child) {
				depth++
			}
			stack = append(stack, frame{node: child, depth: depth})
		}
	}
	return deepest
}

// isNesting reports whether a node opens a new block level.
func isNesting(n ast.Node) bool {
	switch n.(type) {
	case *ast.BlockStmt, *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt,
		*ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt, *ast.FuncLit:
		return true
	default:
		return false
	}
}

// measureFile dispatches to the AST or lexical measurement.
func measureFile(rel, lang string, method Method, src []byte, cfg Config) File {
	if method == MethodAST {
		return measureGo(rel, src, cfg)
	}
	return measureLexical(rel, lang, src, cfg)
}

// aggregate fills the file-level figures from its functions.
func aggregate(f *File, cfg Config) {
	var lives []int
	var densities []float64
	depths := 0

	for _, fn := range f.Worst {
		if fn.Variables > 0 {
			lives = append(lives, fn.MedianLifetime)
		}
		densities = append(densities, fn.Density)
		depths += fn.ScopeDepth
	}

	f.MedianLifetime = float64(medianOf(lives))
	for _, v := range lives {
		if float64(v) > f.MaxLifetime {
			f.MaxLifetime = float64(v)
		}
	}
	// Median density, so one enormous function does not define the file.
	sorted := append([]float64(nil), densities...)
	sort.Float64s(sorted)
	if len(sorted) > 0 {
		mid := len(sorted) / 2
		if len(sorted)%2 == 1 {
			f.MedianDensity = sorted[mid]
		} else {
			f.MedianDensity = round1((sorted[mid-1] + sorted[mid]) / 2)
		}
	}
	f.MaxScopeDepth = depths / len(f.Worst)
	f.Score = score(f.MedianLifetime, f.MedianDensity, f.MaxScopeDepth, cfg)

	// Keep the worst functions, so a reader can drill in.
	sort.SliceStable(f.Worst, func(i, j int) bool {
		if f.Worst[i].Score != f.Worst[j].Score {
			return f.Worst[i].Score > f.Worst[j].Score
		}
		if f.Worst[i].Line != f.Worst[j].Line {
			return f.Worst[i].Line < f.Worst[j].Line
		}
		return f.Worst[i].Name < f.Worst[j].Name
	})
	if len(f.Worst) > worstFunctionsKept {
		f.Worst = f.Worst[:worstFunctionsKept]
	}
}

// worstFunctionsKept bounds the drill-down list. The file score is the answer;
// this is where to look inside it.
const worstFunctionsKept = 5
