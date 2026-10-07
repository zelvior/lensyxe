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
//
// # Layout
//
// This file holds the model every loader produces: the types, format detection,
// and the aggregation helpers all three loaders share. Each loader is a separate
// file next to it, because a pprof decoder, a telemetry reader and an HTTP log
// parser share no logic beyond these types, and keeping them together made the
// pprof schema -- the part with hand-verified field numbers that must not be
// disturbed -- one keystroke away from an unrelated edit.
//
//	profile.go    model, format detection, shared aggregation
//	pprof.go      gzipped protobuf decoding
//	protobuf.go   the minimal wire-format reader pprof needs
//	otel.go       OpenTelemetry span exports
//	accesslog.go  HTTP access logs
package gap

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
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
// shared aggregation
// ---------------------------------------------------------------------------
//
// finishSamples, attributionOf and normaliseSampleFiles are used by every
// loader. They live here rather than beside one loader because they operate on
// the shared model: whichever reader produced the samples, the ordering,
// the resolution verdict and the path normalisation are identical.

// finishSamples orders samples strongest-first.
//
// The tie-break on name is not cosmetic: without it the order of samples with
// equal counts depends on Go's map iteration, and a report that reshuffles
// between two runs of the same profile is not reproducible.
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

// attributionOf classifies how much of the profile reached source code.
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

// stringAt reads a string-table index, returning "" for an index the table does
// not contain.
//
// An out-of-range index is treated as absent rather than an error: a decoder
// that aborts on one malformed entry throws away every valid sample it already
// read, which is a worse outcome than reporting the entry as unresolvable.
func stringAt(strs []string, idx int64) string {
	if idx < 0 || idx >= int64(len(strs)) {
		return ""
	}
	return strs[idx]
}
