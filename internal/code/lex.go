package code

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/zelvior/lensyxe/pkg/models"
)

// Per-file measurement: one streaming pass that classifies lines and counts
// complexity as it goes.
// fileScan is the per-file result of one streaming pass.
type fileScan struct {
	physical   int
	code       int
	complexity complexityScanner
}

// ChurnMap turns churn entries into the path -> added+deleted lookup that
// hotspot classification needs.
func ChurnMap(entries []models.ChurnEntry) map[string]int {
	if len(entries) == 0 {
		return nil
	}
	m := make(map[string]int, len(entries))
	for _, e := range entries {
		m[e.Path] = e.Added + e.Deleted
	}
	return m
}

// scanFile reads path once and returns physical lines, code lines, and the
// accumulated complexity estimate.
//
// Counting is intentionally simple and allocation-light: bufio.Scanner with a
// large buffer, no regex, no AST. This keeps the scan fast and the result
// reproducible across platforms (CRLF is normalized by the scanner).
func scanFile(path, language string, withComplexity bool) (fileScan, error) {
	var out fileScan

	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()

	rule, hasRule := rules[language]
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

	inBlock := false

	for sc.Scan() {
		line := sc.Text()
		out.physical++

		stripped, isComment, nextInBlock := classifyLine(line, rule, hasRule, inBlock)
		inBlock = nextInBlock
		if isComment || strings.TrimSpace(stripped) == "" {
			continue
		}
		out.code++
		if withComplexity {
			out.complexity.feed(strings.TrimSpace(stripped))
		}
	}
	if err := sc.Err(); err != nil {
		// Treat scanner failures (usually "token too long") as a read error.
		return out, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// classifyLine decides whether a raw line is a comment and returns the line
// with comment markers and string-literal contents removed, suitable for token
// and brace counting.
//
// The returned boolean reports "this whole line is comment or blank".
//
// String literals MUST be blanked. Brace counting runs on this output, and a
// single line like `strings.ContainsRune(s, '{')` would otherwise add a level
// of nesting with no matching close, inflating MaxNesting without bound across
// a file that mentions brace characters in string form. Character literals are
// blanked for the same reason.
func classifyLine(raw string, rule languageRule, hasRule bool, inBlock bool) (stripped string, skip bool, stillInBlock bool) {
	if !hasRule {
		return raw, false, inBlock
	}

	trimmed := strings.TrimSpace(raw)
	if inBlock {
		if rule.blockEnd == "" || !strings.Contains(trimmed, rule.blockEnd) {
			return "", true, true
		}
		// The block comment ends mid-line; anything after it is real code.
		idx := strings.Index(trimmed, rule.blockEnd)
		rest := strings.TrimSpace(trimmed[idx+len(rule.blockEnd):])
		return blankStringLiterals(rest, rule.stringDelims), rest == "", false
	}

	if trimmed == "" {
		return "", true, false
	}

	// String literals MUST be blanked before comment markers are searched for.
	// Doing it the other way round truncates a line like
	//     []string{"//"}, blockStart: "/*"},
	// at the "//" inside the literal, leaving the brace unbalanced and
	// inflating MaxNesting for the rest of the file.
	trimmed = blankStringLiterals(trimmed, rule.stringDelims)

	// A trailing line comment does not make the whole line a comment, but the
	// marker and everything after it must not be counted as code.
	if cut := strings.Index(trimmed, rule.lineComments[0]); cut >= 0 {
		trimmed = strings.TrimSpace(trimmed[:cut])
	}
	// Strip additional line-comment prefixes (PHP accepts both // and #).
	for _, prefix := range rule.lineComments[1:] {
		if cut := strings.Index(trimmed, prefix); cut >= 0 {
			trimmed = strings.TrimSpace(trimmed[:cut])
		}
	}

	if rule.blockStart != "" && strings.HasPrefix(trimmed, rule.blockStart) {
		rest := trimmed[len(rule.blockStart):]
		if rule.blockEnd != "" {
			if idx := strings.Index(rest, rule.blockEnd); idx >= 0 {
				// Single-line block comment: /** ... */
				return strings.TrimSpace(rest[idx+len(rule.blockEnd):]), true, false
			}
		}
		return "", true, true
	}
	if trimmed == "" {
		return "", true, false
	}
	return trimmed, false, false
}

// blankStringLiterals replaces the contents of string literals with spaces.
//
// Blanking rather than deleting preserves byte offsets and, more importantly,
// keeps tokens on either side of a literal separated, so `a"x"b` does not
// collapse into the single identifier `axb`.
//
// Escape handling is deliberately minimal but correct for the cases that
// matter here: a backslash escapes the next byte, so `\"` does not terminate a
// literal. Unterminated literals blank to end of line, which is the safe
// direction for a lexical heuristic: it can only under-count, never fabricate a
// brace.
func blankStringLiterals(line string, delims []byte) string {
	if len(delims) == 0 || !containsAnyByte(line, delims) {
		return line
	}
	out := []byte(line)
	esc := byte(0)
	for i := 0; i < len(out); i++ {
		c := out[i]
		if esc != 0 {
			out[i] = ' '
			esc = 0
			continue
		}
		if c == '\\' {
			esc = c
			continue
		}
		if !isDelim(c, delims) {
			continue
		}
		// Find the closing delimiter.
		j := i + 1
		for j < len(out) {
			if out[j] == '\\' {
				j += 2
				continue
			}
			if out[j] == c {
				break
			}
			j++
		}
		// Blank the interior, leaving both delimiters visible so the line still
		// reads as containing a literal.
		for k := i + 1; k < j && k < len(out); k++ {
			out[k] = ' '
		}
		if j >= len(out) {
			break // unterminated: nothing more to do
		}
		i = j
	}
	return string(out)
}

func isDelim(c byte, delims []byte) bool {
	for _, d := range delims {
		if c == d {
			return true
		}
	}
	return false
}

func containsAnyByte(s string, set []byte) bool {
	for i := 0; i < len(s); i++ {
		if isDelim(s[i], set) {
			return true
		}
	}
	return false
}

// countLines is the compatibility wrapper used by existing callers and tests.
// It counts physical and code lines without the complexity estimate.
func countLines(path, language string) (int, int, error) {
	s, err := scanFile(path, language, false)
	return s.physical, s.code, err
}
