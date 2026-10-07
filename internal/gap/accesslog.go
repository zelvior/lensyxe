package gap

// The HTTP access log loader.
//
// Access logs are the weakest of the three sources and this file is explicit
// about it: a request to /users/42 executed a handler, and nothing in the log
// line names that handler. So the measurements are attributed to *routes*, and
// the join to code is refused rather than approximated by name matching. A route
// called /users is not evidence that a function called users is the thing
// running.

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Access routes are not functions. A request to /users/42 executed a handler
// that this package has no way of naming from the log alone, so what is measured
// is the route's frequency and latency, and it is reported as such rather than
// being attributed to whichever function happens to share a name.

// loadAccessLog parses a structured HTTP access log.
func loadAccessLog(pathname string, data []byte) (*Profile, error) {
	prof := &Profile{
		Source:      SourceAccessLog,
		Path:        pathname,
		Attribution: AttributionNone,
		Notes:       []string{},
	}
	agg := map[string]*FuncSample{}
	var minT, maxT time.Time
	sawUnparsed := 0

	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		route, status, micros, at, ok := parseAccessLine(line)
		if !ok {
			sawUnparsed++
			continue
		}
		prof.Observations++
		if !at.IsZero() {
			if minT.IsZero() || at.Before(minT) {
				minT = at
			}
			if at.After(maxT) {
				maxT = at
			}
		}
		entry := agg[route]
		if entry == nil {
			entry = &FuncSample{Name: route}
			agg[route] = entry
		}
		entry.Hits++
		entry.LatencyNS += micros * 1000
		_ = status
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("gap: read %s: %w", pathname, err)
	}

	prof.Functions = finishSamples(agg)
	prof.Attributed = prof.Observations
	if prof.Observations > 0 {
		prof.Attribution = AttributionFunction
	}
	if !minT.IsZero() {
		prof.Window = Window{Start: minT, End: maxT, Known: true}
		prof.Window.Duration = maxT.Sub(minT)
	}
	if sawUnparsed > 0 {
		prof.Notes = append(prof.Notes, fmt.Sprintf(
			"%d line(s) could not be parsed and were excluded from every figure",
			sawUnparsed))
	}
	prof.Notes = append(prof.Notes,
		"an access log records request routes, not source functions. Counts and "+
			"latency are attributed to routes; they are not joined to code")
	return prof, nil
}

// parseAccessLine reads one log line in the common combined format:
//
//	<ip> - - [<timestamp>] "<method> <path> <proto>" <status> <bytes> <micros>
//
// The field order is checked rather than assumed, and a line that does not match
// is rejected so it cannot contribute a fabricated route.
func parseAccessLine(line string) (route string, status int, micros int64, at time.Time, ok bool) {
	// Quoted request: the first quoted run is METHOD PATH PROTO.
	open := strings.IndexByte(line, '"')
	if open < 0 {
		return "", 0, 0, time.Time{}, false
	}
	close := strings.IndexByte(line[open+1:], '"')
	if close < 0 {
		return "", 0, 0, time.Time{}, false
	}
	request := strings.Fields(line[open+1 : open+1+close])
	if len(request) < 2 {
		return "", 0, 0, time.Time{}, false
	}
	switch request[0] {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
	default:
		return "", 0, 0, time.Time{}, false
	}
	route = normaliseRoute(request[1])

	rest := line[open+close+2:]
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return "", 0, 0, time.Time{}, false
	}
	status, err := strconv.Atoi(fields[0])
	if err != nil {
		return "", 0, 0, time.Time{}, false
	}
	// The remaining numeric fields are bytes then, commonly, a microsecond
	// duration. Only the last is used, and only when it parses.
	for _, f := range fields[1:] {
		if v, err := strconv.ParseInt(f, 10, 64); err == nil {
			micros = v
		}
	}
	at = parseBracketTimestamp(line)
	return route, status, micros, at, true
}

// normaliseRoute collapses path parameters so /users/42 and /users/7 are one
// route. Without this every distinct id becomes its own "function" and the
// frequency table is a list of single hits.
func normaliseRoute(p string) string {
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if parts[0] == "" {
		return "/"
	}
	for i, seg := range parts {
		if isNumericSegment(seg) || looksLikeID(seg) {
			parts[i] = "{id}"
		}
	}
	return "/" + strings.Join(parts, "/")
}

func isNumericSegment(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// looksLikeID recognises a UUID, which is the other common identifier in a path.
// Only the canonical 8-4-4-4-12 shape counts, so an ordinary word is left alone.
func looksLikeID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for _, i := range []int{8, 13, 18, 23} {
		if i >= len(s) {
			return false
		}
		if s[i] != '-' {
			return false
		}
	}
	return true
}

// parseBracketTimestamp reads the CLF timestamp, falling back to RFC 3339.
//
// Both are tried because nginx writes CLF by default and several frameworks
// reformat the line to ISO 8601. A missing or unrecognised timestamp yields the
// zero time, which narrows the reported window rather than failing the load.
func parseBracketTimestamp(line string) time.Time {
	open := strings.IndexByte(line, '[')
	if open < 0 {
		return time.Time{}
	}
	closing := strings.IndexByte(line[open+1:], ']')
	if closing < 0 {
		return time.Time{}
	}
	inner := line[open+1 : open+1+closing]
	// CLF: 10/Oct/2000:13:55:36 -0700
	const clf = "02/Jan/2006:15:04:05 -0700"
	if t, err := time.Parse(clf, inner); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(time.RFC3339, inner); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
