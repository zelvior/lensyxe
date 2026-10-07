package gap

// The OpenTelemetry loader.
//
// OTLP JSON arrives in several shapes and the exporters disagree on field
// casing, so this file is mostly about accepting all of them. What it cannot do
// is manufacture attribution: a span names an *operation*, and only the
// semantic-convention code.function.name attribute ties one to a source
// function. Most exporters do not emit it, and when it is absent this loader
// reports zero coverage rather than guessing.

import (
	"encoding/json"
	"fmt"
	"math"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

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

// parseOTelTime accepts the timestamp shapes exporters emit.
//
// An unparseable timestamp returns the zero time rather than an error: one span
// with a malformed time should narrow the window, not fail the whole load.
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

// firstNonEmpty returns the first non-empty value, which is how the two casing
// variants of each timestamp field are reconciled.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
