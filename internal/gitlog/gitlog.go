// Package gitlog reads dated commits together with the files they touched.
//
// It exists because three separate features need the same three facts per commit
// -- when, who, and which files -- and each had grown its own `git log` parser.
// The formats differ only in which fields sit between the NUL separators, so the
// cost of a copy is high and the cost of getting one subtly wrong is higher: a
// parser that silently returns zero commits looks identical to a repository with
// no history.
//
// The stream is read by index rather than by strings.Split. Git emits
//
//	\x00<date>\x00<author>\n<path>\n<path>\n...
//
// per commit, so splitting on NUL yields "", date, "author\npaths", date, ... --
// the date and the author land in *separate* pieces, because the NUL between them
// is itself a separator. A split-based parser looks for a header separator inside
// a piece that no longer has one and yields nothing.
//
// Paths may contain spaces, which is why the record is NUL-delimited rather than
// line-delimited.
package gitlog

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Commit is one commit and the files it changed.
type Commit struct {
	// SHA is the abbreviated commit hash, empty when the caller did not ask for
	// it.
	SHA string
	// Author is the author name as recorded by git, which is whatever was typed
	// at commit time and is not necessarily a unique identity. Two names for one
	// person read as two contributors.
	Author string
	// AuthorEmail is the author email, empty unless Requested it.
	AuthorEmail string
	// When is the author date. git log --date-order aside, this is the date the
	// author recorded, which is what an ownership question is actually about.
	When time.Time
	// Files are the paths the commit touched, sorted and deduplicated.
	Files []string
	// Insertions and Deletions are the numstat line counts, or -1 when the
	// caller did not request them or git reported a binary file.
	Insertions int
	Deletions  int
}

// Options selects which fields the reader asks git for.
type Options struct {
	// WantSHA adds the commit hash.
	WantSHA bool
	// WantEmail adds the author email.
	WantEmail bool
	// WantNumstat adds insertion and deletion counts.
	WantNumstat bool
	// SkipMerges excludes merge commits, which touch no files of their own and
	// would otherwise double-count a whole branch's changes as one event.
	SkipMerges bool
	// Since limits history to commits after this instant. Zero means no limit.
	Since time.Time
	// Limit caps the number of commits read. Zero means no limit.
	Limit int
}

// Reader returns commits for a repository.
type Reader struct {
	Root    string
	Options Options
	// Timeout bounds the git invocation.
	Timeout time.Duration
}

// Read returns the commits, oldest last, in the order git emits them.
//
// git log emits newest first and that order is preserved: git's own order is
// deliberate (it groups by topology), and re-sorting by date would lose it for
// no analytical gain.
func (r Reader) Read(ctx context.Context) ([]Commit, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not found: this analysis is read from git history")
	}

	// Every record is prefixed with its own NUL so the stream is uniformly
	// record-delimited no matter which optional fields are present. Without it
	// the first record has no leading separator, and the parser's "skip to the
	// next NUL" step eats the SHA and shifts every field by one -- which a
	// hand-written fixture with a leading NUL happily hides.
	fields := "%x00%aI"
	if r.Options.WantSHA {
		fields = "%x00%H%x00%aI"
	}
	if r.Options.WantEmail {
		fields += "%x00%aE"
	}
	fields += "%x00%aN"

	args := []string{"log", "--name-only", "--pretty=format:" + fields}
	if r.Options.SkipMerges {
		args = append(args, "--no-merges")
	}
	if !r.Options.Since.IsZero() {
		args = append(args, "--since="+r.Options.Since.Format(time.RFC3339))
	}
	if r.Options.WantNumstat {
		args = append(args, "--numstat")
	}
	if r.Options.Limit > 0 {
		args = append(args, "-n", fmt.Sprintf("%d", r.Options.Limit))
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Root

	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(r.Timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return nil, fmt.Errorf("git log timed out after %s", r.Timeout)
	}
	if err != nil {
		// A directory with no repository, or a repository with no commits, is a
		// state to report rather than a failure to analyze.
		if IsUnavailable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("git log: %s", FailureText(err))
	}
	return Parse(out, r.Options), nil
}

// Parse reads the NUL-delimited stream into commits.
//
// Split out from Read so the format can be tested against hand-written streams
// with no repository and no subprocess.
func Parse(raw []byte, opts Options) []Commit {
	var out []Commit
	s := string(raw)

	for {
		i := strings.IndexByte(s, 0)
		if i < 0 {
			return out
		}
		s = s[i+1:]

		// Header fields, separated by NUL, ending at the first newline.
		header := s
		var body string
		if k := strings.IndexByte(s, '\n'); k >= 0 {
			header = s[:k]
			s = s[k+1:]

			rest := s
			if n := strings.IndexByte(s, 0); n >= 0 {
				rest = s[:n]
				s = s[n:]
			} else {
				s = ""
			}
			body = rest
		} else {
			s = ""
		}

		parts := strings.Split(header, "\x00")
		fields := parseHeader(parts, opts)
		if fields.when.IsZero() && parts[0] == "" {
			if len(s) == 0 {
				return out
			}
			continue
		}

		files, ins, del := parseBody(body, opts)
		if len(files) == 0 {
			// An empty commit carries no ownership evidence.
			if len(s) == 0 {
				return out
			}
			continue
		}

		out = append(out, Commit{
			SHA: fields.sha, Author: fields.author, AuthorEmail: fields.email,
			When: fields.when, Files: files,
			Insertions: ins, Deletions: del,
		})
		if len(s) == 0 {
			return out
		}
	}
}

type headerFields struct {
	sha    string
	email  string
	author string
	when   time.Time
}

// parseHeader assigns the NUL-separated header fields.
//
// The first field is always the date; SHA and email are present only when asked
// for. Positional assignment rather than by name because the format string is
// built in a fixed order, and a reader that guessed which position held what
// would misattribute commits the moment a field were added.
func parseHeader(parts []string, opts Options) headerFields {
	var f headerFields
	i := 0

	if opts.WantSHA && i < len(parts) {
		f.sha = strings.TrimSpace(parts[i])
		i++
	}
	if i < len(parts) {
		f.when, _ = time.Parse(time.RFC3339, strings.TrimSpace(parts[i]))
		i++
	}
	if opts.WantEmail && i < len(parts) {
		f.email = strings.TrimSpace(parts[i])
		i++
	}
	if i < len(parts) {
		f.author = strings.TrimSpace(parts[i])
	}
	return f
}

// parseBody reads the changed-file list, and numstat when requested.
//
// numstat lines are "added\tdeleted\tpath". The name-only and numstat forms are
// mutually exclusive in one invocation, so the shape is detected rather than
// assumed.
func parseBody(body string, opts Options) (files []string, insertions, deletions int) {
	insertions, deletions = -1, -1
	// counted tracks whether any numstat line was readable at all. Guarding on
	// insertions >= 0 instead would be circular: the sentinel for "unknown" is
	// -1, so the accumulator could never start.
	counted := false
	seen := map[string]bool{}

	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if opts.WantNumstat {
			add, del, path, haveCounts := parseNumstat(line)
			if path == "" {
				continue
			}
			// A binary file reports "-\t-\tpath": the counts are unknown, but
			// the path is perfectly well known and the file did change. Keeping
			// the path while leaving the counts absent is the difference between
			// "no numbers" and "this file was never touched".
			if haveCounts {
				if !counted {
					insertions, deletions, counted = 0, 0, true
				}
				insertions += add
				deletions += del
			}
			if !seen[path] {
				seen[path] = true
				files = append(files, path)
			}
			continue
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		files = append(files, line)
	}

	if len(files) == 0 {
		return nil, insertions, deletions
	}
	sort.Strings(files)
	return files, insertions, deletions
}

// parseNumstat reads one "added\tdeleted\tpath" line.
//
// The path is returned whenever the line has three fields, independently of
// whether the counts are numbers: a binary file's line is "-\t-\tpath", and
// dropping it would erase the file from the commit entirely.
func parseNumstat(line string) (added, deleted int, path string, haveCounts bool) {
	a, rest, found := strings.Cut(line, "\t")
	if !found {
		return 0, 0, "", false
	}
	d, rest2, found := strings.Cut(rest, "\t")
	if !found {
		return 0, 0, "", false
	}
	p := strings.TrimSpace(rest2)
	if p == "" {
		return 0, 0, "", false
	}
	av, err1 := strconvAtoi(strings.TrimSpace(a))
	dv, err2 := strconvAtoi(strings.TrimSpace(d))
	if err1 != nil || err2 != nil {
		return 0, 0, p, false
	}
	return av, dv, p, true
}

// strconvAtoi avoids importing strconv for one call site in a package that is
// otherwise dependency-free; it is spelled out rather than aliased.
func strconvAtoi(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("not a number: " + s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// IsUnavailable reports whether a git failure means "no history to read".
//
// Lower-cased because git is inconsistent: `git log` says "not a git repository"
// while `git diff` says "Not a git repository. Use --no-index".
func IsUnavailable(err error) bool {
	msg := strings.ToLower(FailureText(err))
	return strings.Contains(msg, "does not have any commits yet") ||
		strings.Contains(msg, "unknown revision") ||
		strings.Contains(msg, "bad revision") ||
		strings.Contains(msg, "not a git repository")
}

// FailureText returns everything known about a failed git invocation.
//
// err.Error() alone is only "exit status 128"; git's message is on stderr and
// *exec.ExitError carries it in its own field. Matching on err.Error() alone
// never finds the phrases, so every "not a git repository" becomes a hard error.
func FailureText(err error) string {
	if err == nil {
		return ""
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return string(ee.Stderr) + " " + err.Error()
	}
	return err.Error()
}
