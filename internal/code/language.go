package code

import (
	"path/filepath"
	"strings"
)

// The language table: which extensions map to which language, and how each
// language family is stripped before it is counted.
// languageRule describes how to strip comments for one language family.
type languageRule struct {
	language string
	// lineComments are prefixes that mark a full-line comment.
	lineComments []string
	// blockStart/blockEnd delimit block comments; empty disables block handling.
	blockStart string
	blockEnd   string
	// stringDelims are the quote characters that open a string or character
	// literal. Literal contents are blanked before token and brace counting,
	// so a line like ContainsRune(s, '{') cannot fake a nesting level.
	stringDelims []byte
}

// controlFlowLanguages is the set of languages the lexical complexity estimator
// is allowed to score.
//
// Membership is opt-in rather than opt-out so a newly added language is treated
// as declarative until someone shows it has control flow. The failure mode of
// guessing wrong is what this guards against: in a GitHub Actions workflow,
// `if:`, `for:`, and `when:` are mapping keys and any shell embedded in a
// `run: |` block would be counted as YAML branching, producing a confident,
// meaningless number. "Not measured" is the honest answer for those files;
// a fabricated score is worse than no score, because it drives the risk engine.
var controlFlowLanguages = map[string]bool{
	"Go": true, "C": true, "C++": true, "C#": true, "Java": true,
	"JavaScript": true, "TypeScript": true, "Rust": true, "Zig": true,
	"Swift": true, "Kotlin": true, "Scala": true, "PHP": true, "Ruby": true,
	"Python": true, "Shell": true, "SQL": true, "Lua": true,
}

// hasControlFlow reports whether the lexical estimator applies to a language.
func hasControlFlow(lang string) bool { return controlFlowLanguages[lang] }

var (
	dq   = []byte{'"'}
	dqS  = []byte{'"', '\''}
	dqB  = []byte{'"', '\'', '`'}
	dqCh = []byte{'"', '\''}
)

var rules = map[string]languageRule{
	"Go":         {language: "Go", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqB},
	"C":          {language: "C", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"C++":        {language: "C++", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"C#":         {language: "C#", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dq},
	"Java":       {language: "Java", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dq},
	"JavaScript": {language: "JavaScript", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqB},
	"TypeScript": {language: "TypeScript", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqB},
	"Rust":       {language: "Rust", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"Zig":        {language: "Zig", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"Swift":      {language: "Swift", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dq},
	"Kotlin":     {language: "Kotlin", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dq},
	"Scala":      {language: "Scala", lineComments: []string{"//"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"PHP":        {language: "PHP", lineComments: []string{"//", "#"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"Ruby":       {language: "Ruby", lineComments: []string{"#"}, blockStart: "=begin", blockEnd: "=end", stringDelims: dqCh},
	"Python":     {language: "Python", lineComments: []string{"#"}, stringDelims: dqS},
	"Shell":      {language: "Shell", lineComments: []string{"#"}, stringDelims: dqB},
	"YAML":       {language: "YAML", lineComments: []string{"#"}, stringDelims: dqS},
	"SQL":        {language: "SQL", lineComments: []string{"--"}, blockStart: "/*", blockEnd: "*/", stringDelims: dqCh},
	"Lua":        {language: "Lua", lineComments: []string{"--"}, blockStart: "--[[", blockEnd: "]]", stringDelims: dqCh},
}

// extensions maps lowercase file extensions to a language name. Keys are the
// full extension including the dot.
var extensions = map[string]string{
	".go": "Go", ".c": "C", ".h": "C", ".cc": "C++", ".cpp": "C++", ".cxx": "C++",
	".hpp": "C++", ".hh": "C++", ".cs": "C#", ".java": "Java",
	".js": "JavaScript", ".jsx": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript",
	".ts": "TypeScript", ".tsx": "TypeScript", ".mts": "TypeScript", ".cts": "TypeScript",
	".rs": "Rust", ".zig": "Zig", ".swift": "Swift", ".kt": "Kotlin", ".kts": "Kotlin",
	".scala": "Scala", ".sc": "Scala", ".php": "PHP",
	".rb": "Ruby", ".py": "Python", ".pyi": "Python", ".sh": "Shell", ".bash": "Shell", ".zsh": "Shell",
	".yml": "YAML", ".yaml": "YAML", ".sql": "SQL", ".lua": "Lua",
	".vue": "TypeScript", ".svelte": "TypeScript",
}

// languageOf resolves a filename to a language, or reports false for files
// Lensyxe does not count (docs, lockfiles, assets, images).
func languageOf(name string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	lang, ok := extensions[ext]
	return lang, ok
}
