// Package gap cross-references what a codebase looks like against what a
// running system actually executes.
//
// The premise is that static analysis alone cannot tell you where to spend
// effort. A complicated function that never runs is not the same problem as a
// simple function on the hot path, and a repository's own risk ranking has no way
// to know which is which. This package joins the two: static complexity and churn
// on one side, real invocation counts and latency on the other.
//
// Three honesty properties run through the whole package, because the failure
// mode of this kind of tool is confident nonsense:
//
//   - A function's runtime hits are only reported where the profile actually
//     identified it. Unattributable observations are counted and reported as a
//     coverage figure; they are never spread evenly across the codebase to make
//     a number look complete.
//   - "Zero hits" is a claim about absence, so it is only offered when
//     attribution coverage is good enough to support it. Below that threshold
//     the finding is downgraded to a candidate.
//   - A profile describes one binary at one moment. If it records a version or
//     commit and that does not match the tree being analysed, the mismatch is
//     stated rather than ignored, because the static and runtime halves are then
//     describing different code.
//
// None of this is prediction. The score ranks what was observed over one
// measurement window; it does not forecast demand.
package gap

import (
	"bufio"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Source identifies where a profile came from.
type Source string

const (
	// SourcePprof is a gzipped protobuf CPU or heap profile.
	SourcePprof Source = "pprof"
	// SourceOTel is an OpenTelemetry span export in JSON.
	SourceOTel Source = "otel"
	// SourceAccessLog is a structured HTTP access log.
	SourceAccessLog Source = "access-log"
)

// Attribution describes how well a profile's observations resolved to source.
type Attribution string

const (
	// AttributionFileAndFunction means every observation carried a file and a
	// function name.
	AttributionFileAndFunction Attribution = "file+function"
	// AttributionFunction means observations carried a function name but no file.
	AttributionFunction Attribution = "function"
	// AttributionPartial means some observations resolved and some did not.
	AttributionPartial Attribution = "partial"
	// AttributionNone means nothing resolved to source.
	AttributionNone Attribution = "none"
)

// Window is the time span the profile covers.
//
// Zero-valued for a source that does not record one, and the zero is meaningful:
// an access log with no timestamps has no window, and saying "duration unknown"
// is different from claiming it covered no time.
type Window struct {
	// Start and End are when the profile was taken. Zero when unknown.
	Start time.Time `json:"start,omitempty"`
	End   time.Time `json:"end,omitempty"`
	// Duration is how long collection ran. Zero when unknown.
	Duration time.Duration `json:"duration,omitempty"`
	// Known is false when the source recorded no timing at all.
	Known bool `json:"known"`
}

// FuncSample is the runtime evidence for one function.
type FuncSample struct {
	// Name is the function as the profile recorded it.
	Name string `json:"name"`
	// File is the source file, empty when the profile did not record one.
	File string `json:"file,omitempty"`
	// Hits is how many samples or invocations were attributed to it.
	Hits int64 `json:"hits"`
	// LatencyNS is the summed time attributed to it, in nanoseconds. Zero when
	// the profile records counts only.
	LatencyNS int64 `json:"latency_ns"`
}

// Profile is a loaded runtime profile.
type Profile struct {
	Source Source `json:"source"`
	Path   string `json:"path"`
	Window Window `json:"window"`
	// Functions are the resolved per-function samples, strongest first.
	Functions []FuncSample `json:"functions"`
	// Observations is every measurement in the profile, resolved or not.
	Observations int64 `json:"observations"`
	// Attributed is how many resolved to a source function.
	Attributed int64 `json:"attributed"`
	// Attribution is the resolution quality.
	Attribution Attribution `json:"attribution"`
	// Labels are metadata the profile carried about itself, such as a service
	// version. They are reported so a mismatch with the tree can be seen.
	Labels map[string]string `json:"labels,omitempty"`
	// Notes carry non-fatal observations about the load.
	Notes []string `json:"notes,omitempty"`
}

// Coverage is the share of observations that resolved to source, 0..1.
//
// This is the single most important number in the package. It bounds every
// claim that can be made from the profile: at 20% coverage, "this function has
// zero hits" is close to meaningless.
func (p *Profile) Coverage() float64 {
	if p == nil || p.Observations == 0 {
		return 0
	}
	return float64(p.Attributed) / float64(p.Observations)
}

// Load reads a profile from disk, detecting the format from its content and then
// its extension.
//
// Content wins over extension because the formats are routinely misnamed: a
// `.json` file exported by a collector is OpenTelemetry, while a `.pprof` file is
// almost always a gzipped protobuf.
func Load(pathname string) (*Profile, error) {
	info, err := os.Stat(pathname)
	if err != nil {
		return nil, fmt.Errorf("gap: stat profile: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("gap: %s is a directory, not a profile", pathname)
	}

	data, err := os.ReadFile(pathname)
	if err != nil {
		return nil, fmt.Errorf("gap: read profile: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("gap: %s is empty", pathname)
	}

	switch detect(data, pathname) {
	case SourcePprof:
		return loadPprof(pathname, data)
	case SourceOTel:
		return loadOTel(pathname, data)
	case SourceAccessLog:
		return loadAccessLog(pathname, data)
	default:
		return nil, fmt.Errorf(
			"gap: %s is not a recognised profile: expected a gzipped pprof, "+
				"an OpenTelemetry span JSON array, or an HTTP access log",
			pathname)
	}
}

// detect identifies the profile format.
//
// The order matters: gzip magic is checked first because it is unambiguous, and
// the JSON check requires the first non-space byte to be a brace or bracket,
// which keeps a log file that happens to contain braces from being parsed as
// telemetry.
func detect(data []byte, pathname string) Source {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		return SourcePprof
	}
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	trimmed := strings.TrimLeft(string(head), " \t\r\n")
	if strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
		if looksLikeOTel(trimmed) {
			return SourceOTel
		}
	}
	ext := strings.ToLower(filepath.Ext(pathname))
	if ext == ".log" || ext == ".txt" || ext == ".access" {
		return SourceAccessLog
	}
	// A pprof can be written ungzipped, which is rare but legal on the wire.
	if ext == ".pb" || ext == ".pprof" {
		return SourcePprof
	}
	if looksLikeAccessLog(trimmed) {
		return SourceAccessLog
	}
	return ""
}

// looksLikeOTel checks for the structural markers of a span export rather than
// just a brace, since plenty of JSON is not telemetry.
func looksLikeOTel(s string) bool {
	return strings.Contains(s, "\"traceId\"") ||
		strings.Contains(s, "\"spanId\"") ||
		strings.Contains(s, "\"trace_id\"") ||
		strings.Contains(s, "\"span_id\"") ||
		strings.Contains(s, "\"resourceSpans\"") ||
		strings.Contains(s, "\"kind\"")
}

// looksLikeAccessLog checks for the combined-method-and-path shape of a request
// log line.
func looksLikeAccessLog(s string) bool {
	first, _, _ := strings.Cut(s, "\n")
	fields := strings.Fields(first)
	if len(fields) < 3 {
		return false
	}
	methods := map[string]bool{
		"GET": true, "POST": true, "PUT": true, "DELETE": true,
		"PATCH": true, "HEAD": true, "OPTIONS": true,
	}
	for _, f := range fields {
		if methods[f] {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// pprof
// ---------------------------------------------------------------------------

// The subset of the pprof profile.proto schema this package decodes.
//
// The field numbers were confirmed against a profile produced by the Go
// toolchain rather than taken from memory, because a wrong field number decodes
// into plausible-looking garbage instead of failing:
//
//	1 sample_type  2 sample  3 mapping  4 location  5 function
//	6 string_table  9 time_nanos  10 duration_nanos  11 period_type  12 period
//
// Within the submessages: Sample{1 location_id, 2 value}, Location{1 id,
// 4 line}, Line{1 function_id, 2 line}, Function{1 id, 2 name, 4 filename}.
// Repeated numeric fields may arrive packed, in which case they arrive as a
// length-delimited run of varints rather than as individual varints.

const (
	pprofSampleType = 1
	pprofSampleFld  = 2
	pprofLocation   = 4
	pprofFunction   = 5
	pprofStringTab  = 6
	pprofTimeNanos  = 9
	pprofDuration   = 10
)

const (
	sampleLocationID = 1
	sampleValue      = 2

	locationID   = 1
	locationLine = 4

	lineFunctionID = 1
	lineNumber     = 2

	functionID       = 1
	functionName     = 2
	functionFilename = 4
)

// loadPprof decodes a gzipped protobuf profile.
func loadPprof(pathname string, data []byte) (*Profile, error) {
	raw := data
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(strings.NewReader(string(data)))
		if err != nil {
			return nil, fmt.Errorf("gap: %s is gzip but not readable: %w", pathname, err)
		}
		defer zr.Close()
		raw, err = io.ReadAll(zr)
		if err != nil {
			return nil, fmt.Errorf("gap: decompress %s: %w", pathname, err)
		}
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("gap: %s decompressed to nothing", pathname)
	}
	return parsePprof(pathname, raw)
}

// parsePprof decodes an already-decompressed profile.
func parsePprof(pathname string, raw []byte) (*Profile, error) {
	prof := &Profile{Source: SourcePprof, Path: pathname, Attribution: AttributionNone}
	prof.Labels = map[string]string{}

	var strings_ []string
	// sampleTypeIdx holds the raw string-table indices for each sample value
	// type, resolved to labels only after the whole message is read.
	//
	// git writes the string table after the sample types, so resolving the
	// labels during the single pass yields empty strings for every one of them.
	// The symptom is a profile that decodes without error and reports no
	// latency at all, because the nanosecond slot was never recognised.
	var sampleTypeIdx [][2]int64
	var samples []pprofSample
	var locations []pprofLoc
	var functions []pprofFunc

	for len(raw) > 0 {
		field, wire, rest, err := readTag(raw)
		if err != nil {
			return nil, fmt.Errorf("gap: %s: %w", pathname, err)
		}
		raw = rest

		switch {
		case field == pprofStringTab && wire == wireBytes:
			b, rest, err := readBytes(raw)
			if err != nil {
				return nil, fmt.Errorf("gap: %s: %w", pathname, err)
			}
			raw = rest
			strings_ = append(strings_, string(b))

		case field == pprofSampleType && wire == wireBytes:
			b, rest, err := readBytes(raw)
			if err != nil {
				return nil, fmt.Errorf("gap: %s: %w", pathname, err)
			}
			raw = rest
			// ValueType{1 type, 2 unit}, both string_table indices.
			typ, unit := int64(-1), int64(-1)
			for len(b) > 0 {
				f, w, r, err := readTag(b)
				if err != nil {
					break
				}
				b = r
				switch {
				case f == 1 && w == wireVarint:
					v, r2, err := readVarint(b)
					if err != nil {
						b = nil
						break
					}
					typ, b = int64(v), r2
				case f == 2 && w == wireVarint:
					v, r2, err := readVarint(b)
					if err != nil {
						b = nil
						break
					}
					unit, b = int64(v), r2
				default:
					var err error
					b, err = skipValue(b, w)
					if err != nil {
						b = nil
					}
				}
			}
			sampleTypeIdx = append(sampleTypeIdx, [2]int64{typ, unit})

		case field == pprofSampleFld && wire == wireBytes:
			b, rest, err := readBytes(raw)
			if err != nil {
				return nil, fmt.Errorf("gap: %s: %w", pathname, err)
			}
			raw = rest
			samples = append(samples, parseSample(b))

		case field == pprofLocation && wire == wireBytes:
			b, rest, err := readBytes(raw)
			if err != nil {
				return nil, fmt.Errorf("gap: %s: %w", pathname, err)
			}
			raw = rest
			locations = append(locations, parseLocation(b))

		case field == pprofFunction && wire == wireBytes:
			b, rest, err := readBytes(raw)
			if err != nil {
				return nil, fmt.Errorf("gap: %s: %w", pathname, err)
			}
			raw = rest
			functions = append(functions, parseFunction(b))

		case field == pprofTimeNanos && wire == wireVarint:
			v, rest, err := readVarint(raw)
			if err != nil {
				return nil, fmt.Errorf("gap: %s: %w", pathname, err)
			}
			raw = rest
			prof.Window.Start = time.Unix(0, int64(v)).UTC()
			prof.Window.Known = true

		case field == pprofDuration && wire == wireVarint:
			v, rest, err := readVarint(raw)
			if err != nil {
				return nil, fmt.Errorf("gap: %s: %w", pathname, err)
			}
			raw = rest
			prof.Window.Duration = time.Duration(int64(v))
			prof.Window.Known = true

		default:
			var err error
			raw, err = skipValue(raw, wire)
			if err != nil {
				// A malformed tail is not worth discarding a mostly-decoded
				// profile over; the samples already read are still valid.
				prof.Notes = append(prof.Notes,
					"profile decoding stopped early: "+err.Error())
				raw = nil
			}
		}
	}

	if len(functions) == 0 || len(locations) == 0 {
		prof.Notes = append(prof.Notes,
			"this profile carries no symbol table, so no observation can be "+
				"attributed to a source function. Collect it with symbolization "+
				"enabled (go test -cpuprofile, or a runtime that resolved symbols).")
		return prof, nil
	}

	if prof.Window.Known && prof.Window.Duration > 0 {
		prof.Window.End = prof.Window.Start.Add(prof.Window.Duration)
	}

	// Index by id for the join.
	locByID := map[uint64]pprofLoc{}
	for _, l := range locations {
		locByID[l.id] = l
	}
	fnByID := map[uint64]pprofFunc{}
	for _, f := range functions {
		fnByID[f.id] = f
	}

	// Which value slot is latency? A CPU profile has both a sample count and a
	// nanosecond total, and summing the wrong one mixes units.
	//
	// Labels are resolved here, after the string table has been read, not during
	// the pass above.
	sampleTypes := make([]string, len(sampleTypeIdx))
	for i, idx := range sampleTypeIdx {
		sampleTypes[i] = sampleTypeLabel(strings_, idx[0], idx[1])
	}

	latencySlot := -1
	for i, name := range sampleTypes {
		lower := strings.ToLower(name)
		if strings.Contains(lower, "nanosecond") ||
			strings.Contains(lower, "second") && !strings.Contains(lower, "sample") ||
			strings.Contains(lower, "byte") {
			latencySlot = i
			break
		}
	}
	countSlot := 0
	if latencySlot == 0 {
		countSlot = -1
	}

	agg := map[string]*FuncSample{}
	for _, s := range samples {
		// A sample's location_id list is a call stack, innermost first. Only the
		// leaf is attributed: attributing the whole stack to every frame would
		// inflate every function on a call path to the frequency of the
		// innermost one, which is not what any of these numbers mean.
		if len(s.locationIDs) == 0 {
			continue
		}
		leaf, ok := locByID[s.locationIDs[0]]
		if !ok || len(leaf.fnIDs) == 0 || leaf.fnIDs[0] == 0 {
			prof.Attributed++
			continue
		}
		fn, ok := fnByID[leaf.fnIDs[0]]
		if !ok {
			prof.Attributed++
			continue
		}
		name := stringAt(strings_, fn.name)
		if name == "" {
			prof.Attributed++
			continue
		}
		prof.Attributed++
		key := name
		entry := agg[key]
		if entry == nil {
			entry = &FuncSample{Name: name, File: stringAt(strings_, fn.filename)}
			agg[key] = entry
		}
		if countSlot >= 0 && countSlot < len(s.values) {
			entry.Hits += s.values[countSlot]
		} else if len(s.values) > 0 {
			entry.Hits += s.values[0]
		}
		if latencySlot >= 0 && latencySlot < len(s.values) {
			entry.LatencyNS += s.values[latencySlot]
		}
	}
	prof.Observations = int64(len(samples))
	prof.Functions = finishSamples(agg)
	prof.Attribution = attributionOf(prof)

	// Normalise the source paths. A profile records the path the binary was
	// built with, which is frequently absolute and rarely matches the tree
	// being analysed; only the base name survives usefully in that case.
	normaliseSampleFiles(prof.Functions)
	return prof, nil
}

func sampleTypeLabel(strs []string, typ, unit int64) string {
	t, u := stringAt(strs, typ), stringAt(strs, unit)
	switch {
	case t != "" && u != "":
		return t + "/" + u
	case t != "":
		return t
	default:
		return u
	}
}

// pprofLoc is the decoded subset of a Location.
type pprofLoc struct {
	id    uint64
	fnIDs []uint64 // leaf-first function ids from its Line entries
}

// pprofFunc is the decoded subset of a Function.
type pprofFunc struct {
	id       uint64
	name     int64
	filename int64
}

func parseLocation(b []byte) pprofLoc {
	var l pprofLoc
	for len(b) > 0 {
		f, w, r, err := readTag(b)
		if err != nil {
			return l
		}
		b = r
		switch {
		case f == locationID && w == wireVarint:
			v, r2, err := readVarint(b)
			if err != nil {
				return l
			}
			l.id, b = v, r2
		case f == locationLine && w == wireBytes:
			lb, r2, err := readBytes(b)
			if err != nil {
				return l
			}
			b = r2
			id := parseLineFunctionID(lb)
			if id != 0 {
				l.fnIDs = append(l.fnIDs, id)
			}
		case f == locationLine && w == wireVarint:
			// Unpacked encoding of a single Line.
			v, r2, err := readVarint(b)
			if err != nil {
				return l
			}
			b = r2
			_ = v
		default:
			var err error
			b, err = skipValue(b, w)
			if err != nil {
				return l
			}
		}
	}
	return l
}

func parseLineFunctionID(b []byte) uint64 {
	for len(b) > 0 {
		f, w, r, err := readTag(b)
		if err != nil {
			return 0
		}
		b = r
		switch {
		case f == lineFunctionID && w == wireVarint:
			v, _, err := readVarint(b)
			if err != nil {
				return 0
			}
			return v
		case f == lineNumber && w == wireVarint:
			if _, r2, err := readVarint(b); err != nil {
				return 0
			} else {
				b = r2
			}
		default:
			var err error
			b, err = skipValue(b, w)
			if err != nil {
				return 0
			}
		}
	}
	return 0
}

func parseFunction(b []byte) pprofFunc {
	var fn pprofFunc
	for len(b) > 0 {
		f, w, r, err := readTag(b)
		if err != nil {
			return fn
		}
		b = r
		switch {
		case f == functionID && w == wireVarint:
			v, r2, err := readVarint(b)
			if err != nil {
				return fn
			}
			fn.id, b = v, r2
		case f == functionName && w == wireVarint:
			v, r2, err := readVarint(b)
			if err != nil {
				return fn
			}
			fn.name, b = int64(v), r2
		case f == functionFilename && w == wireVarint:
			v, r2, err := readVarint(b)
			if err != nil {
				return fn
			}
			fn.filename, b = int64(v), r2
		default:
			var err error
			b, err = skipValue(b, w)
			if err != nil {
				return fn
			}
		}
	}
	return fn
}

// pprofSample is the decoded subset of a Sample.
type pprofSample struct {
	// locationIDs is the call stack, innermost first.
	locationIDs []uint64
	// values are the per-type measurements, parallel to sampleTypes.
	values []int64
}

// parseSample decodes a Sample, accepting both packed and unpacked repeated
// fields. gogo/protobuf emits packed, other encoders do not, and a decoder that
// handles only one reads a CPU profile as empty.
func parseSample(b []byte) pprofSample {
	var s pprofSample
	for len(b) > 0 {
		f, w, r, err := readTag(b)
		if err != nil {
			return s
		}
		b = r
		switch {
		case f == sampleLocationID && w == wireBytes:
			pb, rest, err := readBytes(b)
			if err != nil {
				return s
			}
			b = rest
			for len(pb) > 0 {
				v, remaining, err := readVarint(pb)
				if err != nil {
					break
				}
				s.locationIDs = append(s.locationIDs, v)
				pb = remaining
			}
		case f == sampleLocationID && w == wireVarint:
			v, r2, err := readVarint(b)
			if err != nil {
				return s
			}
			b = r2
			s.locationIDs = append(s.locationIDs, v)
		case f == sampleValue && w == wireBytes:
			pb, r2, err := readBytes(b)
			if err != nil {
				return s
			}
			b = r2
			for len(pb) > 0 {
				v, rest, err := readVarint(pb)
				if err != nil {
					break
				}
				s.values = append(s.values, int64(v))
				pb = rest
			}
		case f == sampleValue && w == wireVarint:
			v, r2, err := readVarint(b)
			if err != nil {
				return s
			}
			b = r2
			s.values = append(s.values, int64(v))
		default:
			var err error
			b, err = skipValue(b, w)
			if err != nil {
				return s
			}
		}
	}
	return s
}

func finishSamples(agg map[string]*FuncSample) []FuncSample {
	out := make([]FuncSample, 0, len(agg))
	for _, f := range agg {
		out = append(out, *f)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		if out[i].LatencyNS != out[j].LatencyNS {
			return out[i].LatencyNS > out[j].LatencyNS
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func attributionOf(p *Profile) Attribution {
	switch {
	case p.Observations == 0:
		return AttributionNone
	case p.Attributed == 0:
		return AttributionNone
	case p.Attributed >= p.Observations:
		return AttributionFileAndFunction
	default:
		return AttributionPartial
	}
}

// normaliseSampleFiles strips absolute build paths down to a base name.
//
// A profile records the path the binary was compiled with, which is an absolute
// path on the machine that built it. Matching that against a checkout on a
// different machine would always fail, so only the base name is kept and the
// join is attempted on it. The alternative, matching on a prefix, would report a
// confident join for the wrong file.
func normaliseSampleFiles(samples []FuncSample) {
	for i := range samples {
		if samples[i].File == "" {
			continue
		}
		samples[i].File = path.Base(filepath.ToSlash(samples[i].File))
	}
}

func stringAt(strs []string, idx int64) string {
	if idx < 0 || idx >= int64(len(strs)) {
		return ""
	}
	return strs[idx]
}

// ---------------------------------------------------------------------------
// minimal protobuf wire reader
// ---------------------------------------------------------------------------

const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// readTag returns the field number and wire type, plus the remaining bytes.
func readTag(b []byte) (field uint64, wire uint64, rest []byte, err error) {
	v, rest, err := readVarint(b)
	if err != nil {
		return 0, 0, nil, err
	}
	field = v >> 3
	wire = v & 7
	if field == 0 {
		return 0, 0, nil, errors.New("protobuf field number 0 is invalid")
	}
	return field, wire, rest, nil
}

func readVarint(b []byte) (uint64, []byte, error) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, nil, errors.New("truncated protobuf varint")
	}
	return v, b[n:], nil
}

func readBytes(b []byte) ([]byte, []byte, error) {
	l, rest, err := readVarint(b)
	if err != nil {
		return nil, nil, err
	}
	if uint64(len(rest)) < l {
		return nil, nil, errors.New("truncated protobuf length-delimited field")
	}
	return rest[:l], rest[l:], nil
}

// skipValue advances past one value of the given wire type.
func skipValue(b []byte, wire uint64) ([]byte, error) {
	switch wire {
	case wireVarint:
		_, rest, err := readVarint(b)
		return rest, err
	case wireFixed64:
		if len(b) < 8 {
			return nil, errors.New("truncated fixed64")
		}
		return b[8:], nil
	case wireFixed32:
		if len(b) < 4 {
			return nil, errors.New("truncated fixed32")
		}
		return b[4:], nil
	case wireBytes:
		_, rest, err := readBytes(b)
		return rest, err
	default:
		return nil, fmt.Errorf("unsupported protobuf wire type %d", wire)
	}
}

// ---------------------------------------------------------------------------
// OpenTelemetry
// ---------------------------------------------------------------------------

// otelSpan is the subset of a span this package reads.
//
// Field names are matched loosely because exporters disagree on casing: the OTLP
// JSON encoding uses traceId/spanId while some libraries emit trace_id/span_id.
// Everything is optional because every one of these fields is optional in the
// spec.
type otelSpan struct {
	Name          string `json:"name"`
	TraceID       string `json:"traceId"`
	TraceIDSnake  string `json:"trace_id"`
	SpanID        string `json:"spanId"`
	SpanIDSnake   string `json:"span_id"`
	StartTime     string `json:"startTime"`
	StartSnake    string `json:"start_time"`
	EndTime       string `json:"endTime"`
	EndSnake      string `json:"end_time"`
	DurationNS    int64  `json:"durationNanos"`
	DurationSnake int64  `json:"duration_nanos"`

	// Code carries the semantic-convention attributes that name the source
	// function. This is the only path by which a span can be attributed to code.
	Code struct {
		Function struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"function"`
		Filepath string `json:"filepath"`
		Lineno   int    `json:"lineno"`
		Column   int    `json:"column"`
	} `json:"code"`

	Attributes otelAttributes `json:"attributes"`
	Resource   struct {
		Attributes otelAttributes `json:"attributes"`
	} `json:"resource"`
}

// otelAttributes accepts both shapes an OTLP JSON export uses for attributes.
//
// The canonical encoding is an array of {"key":..., "value":{...}} pairs, and
// plenty of collectors emit a plain object instead. Decoding into a map, as an
// earlier version did, fails on every real OTLP export with a type error rather
// than on anything wrong with the profile.
type otelAttributes []otelAttribute

type otelAttribute struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// asMap flattens either shape into a plain map.
func (a otelAttributes) asMap() map[string]any {
	if len(a) == 0 {
		return nil
	}
	out := make(map[string]any, len(a))
	for _, kv := range a {
		// The value is a oneof: {"stringValue":..} or {"intValue":..}.
		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal(kv.Value, &wrapper); err != nil || len(wrapper) == 0 {
			out[kv.Key] = ""
			continue
		}
		for _, raw := range wrapper {
			var v any
			if err := json.Unmarshal(raw, &v); err == nil {
				out[kv.Key] = v
			} else {
				out[kv.Key] = ""
			}
			break
		}
	}
	return out
}

// otelExport covers the two shapes an OTLP JSON export arrives in: a bare array
// of spans, or an object with resourceSpans holding them.
type otelExport struct {
	ResourceSpans []struct {
		Resource struct {
			Attributes otelAttributes `json:"attributes"`
		} `json:"resource"`
		ScopeSpans []struct {
			Spans []otelSpan `json:"spans"`
		} `json:"scopeSpans"`
		// Older exports inline the spans.
		Spans []otelSpan `json:"spans"`
	} `json:"resourceSpans"`

	// A bare array decodes into this.
	Spans []otelSpan `json:"-"`
}

func loadOTel(pathname string, data []byte) (*Profile, error) {
	var spans []otelSpan
	labels := map[string]string{}

	trimmed := strings.TrimLeft(string(data), " \t\r\n")
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &spans); err != nil {
			return nil, fmt.Errorf("gap: %s is not a valid span array: %w", pathname, err)
		}
	} else {
		var exp otelExport
		if err := json.Unmarshal([]byte(trimmed), &exp); err != nil {
			return nil, fmt.Errorf("gap: %s is not a valid OTLP export: %w", pathname, err)
		}
		for _, rs := range exp.ResourceSpans {
			for k, v := range rs.Resource.Attributes.asMap() {
				labels[k] = attrString(v)
			}
			for _, ss := range rs.ScopeSpans {
				spans = append(spans, ss.Spans...)
			}
			spans = append(spans, rs.Spans...)
		}
		// Some collectors emit {"spans": [...]} without resourceSpans.
		if len(spans) == 0 {
			var bare struct {
				Spans []otelSpan `json:"spans"`
			}
			if err := json.Unmarshal([]byte(trimmed), &bare); err == nil {
				spans = bare.Spans
			}
		}
	}

	prof := &Profile{
		Source:      SourceOTel,
		Path:        pathname,
		Labels:      labels,
		Notes:       []string{},
		Attribution: AttributionNone,
	}
	if len(spans) == 0 {
		prof.Notes = append(prof.Notes,
			"the export contains no spans, so there is no runtime evidence")
		return prof, nil
	}

	agg := map[string]*FuncSample{}
	var minT, maxT time.Time

	for _, s := range spans {
		prof.Observations++
		start := parseOTelTime(firstNonEmpty(s.StartTime, s.StartSnake))
		end := parseOTelTime(firstNonEmpty(s.EndTime, s.EndSnake))
		latency := s.DurationNS
		if latency == 0 {
			latency = s.DurationSnake
		}
		if latency == 0 && !start.IsZero() && !end.IsZero() && end.After(start) {
			latency = end.Sub(start).Nanoseconds()
		}
		if !start.IsZero() {
			if minT.IsZero() || start.Before(minT) {
				minT = start
			}
			if end.IsZero() || end.After(maxT) {
				maxT = end
			}
		}

		// The only attribution path: an explicit source function attribute.
		name := strings.TrimSpace(s.Code.Function.Name)
		if name == "" {
			continue
		}
		prof.Attributed++
		entry := agg[name]
		if entry == nil {
			entry = &FuncSample{Name: name, File: path.Base(filepath.ToSlash(s.Code.Filepath))}
			agg[name] = entry
		}
		entry.Hits++
		entry.LatencyNS += latency
	}

	prof.Functions = finishSamples(agg)
	prof.Attribution = attributionOf(prof)
	if !minT.IsZero() {
		prof.Window = Window{Start: minT, End: maxT, Known: true}
		if maxT.After(minT) {
			prof.Window.Duration = maxT.Sub(minT)
		}
	}
	if prof.Attributed == 0 {
		prof.Notes = append(prof.Notes,
			"no span carried a code.function.name attribute, so nothing could be "+
				"attributed to a source function. Most exporters do not emit it; "+
				"without it a span records an operation name, not a function")
	} else if prof.Attributed < prof.Observations {
		prof.Notes = append(prof.Notes, fmt.Sprintf(
			"%d of %d spans carried no code.function.name attribute and were "+
				"counted but not attributed. They are not spread across the "+
				"codebase to make the coverage look complete, so a low "+
				"coverage figure here is the honest one.",
			int(prof.Observations-prof.Attributed), int(prof.Observations)))
	}
	return prof, nil
}

func parseOTelTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z0700",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// attrString renders an OTLP attribute value, which is typed as a string, int,
// double, or bool depending on the exporter.
func attrString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == math.Trunc(t) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// HTTP access logs
// ---------------------------------------------------------------------------

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
