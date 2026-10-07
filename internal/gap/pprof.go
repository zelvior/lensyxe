package gap

// The pprof loader.
//
// Everything specific to the gzipped protobuf format lives here: the schema
// field numbers, the decompression, and the single-pass decode. It is the
// highest-risk file in the package, because a wrong field number does not fail
// loudly -- it decodes into plausible-looking numbers -- which is why the field
// numbers below are written down and were confirmed against a real profile
// rather than recalled.

import (
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"time"
)

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

// nextTag reads the next field header and returns the field number, wire type,
// and remaining bytes. ok is false when the buffer is exhausted or the tag is
// malformed.
//
// The sub-parsers below all share the same shape: read a tag, act on the few
// fields they know, skip everything else, and stop cleanly at the first byte
// they cannot read. Hoisting the two error paths into helpers is what keeps
// those loops flat; inline, each one costs two levels of nesting, and five
// nested copies of the same error handling is what pushed parsePprof to a
// nesting depth of ten.
func nextTag(b []byte) (field, wire uint64, rest []byte, ok bool) {
	field, wire, rest, err := readTag(b)
	if err != nil {
		return 0, 0, nil, false
	}
	return field, wire, rest, true
}

// skipUnknown advances past a field the caller does not recognise.
func skipUnknown(b []byte, wire uint64) ([]byte, bool) {
	rest, err := skipValue(b, wire)
	return rest, err == nil
}

// readRepeated reads a repeated numeric field in either encoding: packed as a
// length-delimited run of varints, or as individual varints.
//
// Both forms occur in real profiles. gogo/protobuf emits packed and other
// encoders do not, and a decoder that handles only one reads a CPU profile as
// empty. A truncated packed run returns the values read so far rather than
// failing: the profile is still usable and the outer loop can continue.
func readRepeated(b []byte, wire uint64) (values []uint64, rest []byte, ok bool) {
	if wire == wireBytes {
		pb, rest, err := readBytes(b)
		if err != nil {
			return nil, nil, false
		}
		var values []uint64
		for len(pb) > 0 {
			v, remaining, verr := readVarint(pb)
			if verr != nil {
				return values, rest, true
			}
			values = append(values, v)
			pb = remaining
		}
		return values, rest, true
	}
	if wire == wireVarint {
		v, rest, err := readVarint(b)
		if err != nil {
			return nil, nil, false
		}
		return []uint64{v}, rest, true
	}
	return nil, nil, false
}

// parseValueType decodes one ValueType submessage into its two string-table
// indices.
//
// Extracted from the main decode loop because a nested loop inside a switch case
// is what put parsePprof's nesting at ten, and because ValueType is a message in
// its own right with its own schema.
func parseValueType(b []byte) [2]int64 {
	out := [2]int64{-1, -1}
	for len(b) > 0 {
		// `rest` rather than `b` on the left of the assignment: a `:=` here
		// would declare a new b scoped to the loop body, leaving the loop
		// condition testing the outer b that nothing advances, and the loop
		// never terminates.
		f, w, rest, ok := nextTag(b)
		if !ok {
			break
		}
		b = rest
		if w != wireVarint {
			b, ok = skipUnknown(b, w)
			if !ok {
				break
			}
			continue
		}
		v, rest2, verr := readVarint(b)
		if verr != nil {
			break
		}
		b = rest2
		// Fields 1 and 2 are the type and unit; both optional, so an absent
		// one stays -1 rather than resolving to string-table entry zero.
		if f == 1 || f == 2 {
			out[f-1] = int64(v)
		}
	}
	return out
}

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
//
// One pass, buffering the submessages that arrive before the tables they refer
// to. Protobuf field order is not guaranteed by the format, so this cannot rely
// on the string table coming first.
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
			sampleTypeIdx = append(sampleTypeIdx, parseValueType(b))

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

// sampleTypeLabel renders one ValueType entry, tolerating either index being
// absent because both are optional in the schema.
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
		f, w, rest, ok := nextTag(b)
		if !ok {
			return l
		}
		b = rest
		switch {
		case f == locationID && w == wireVarint:
			v, rest2, verr := readVarint(b)
			if verr != nil {
				return l
			}
			l.id, b = v, rest2
		case f == locationLine && w == wireBytes:
			lb, rest2, berr := readBytes(b)
			if berr != nil {
				return l
			}
			b = rest2
			if id := parseLineFunctionID(lb); id != 0 {
				l.fnIDs = append(l.fnIDs, id)
			}
		default:
			// Includes a locationLine arriving as a bare varint. A Line is a
			// message, so that encoding is not one; the value is stepped over
			// rather than reinterpreted as a function id.
			b, ok = skipUnknown(b, w)
			if !ok {
				return l
			}
		}
	}
	return l
}

func parseLineFunctionID(b []byte) uint64 {
	for len(b) > 0 {
		f, w, rest, ok := nextTag(b)
		if !ok {
			return 0
		}
		b = rest
		if w == wireVarint {
			v, rest2, verr := readVarint(b)
			if verr != nil {
				return 0
			}
			b = rest2
			if f == lineFunctionID {
				return v
			}
			continue // lineNumber and any other scalar: consumed, not needed
		}
		b, ok = skipUnknown(b, w)
		if !ok {
			return 0
		}
	}
	return 0
}

func parseFunction(b []byte) pprofFunc {
	var fn pprofFunc
	for len(b) > 0 {
		f, w, rest, ok := nextTag(b)
		if !ok {
			return fn
		}
		b = rest
		if w != wireVarint {
			b, ok = skipUnknown(b, w)
			if !ok {
				return fn
			}
			continue
		}
		v, rest2, verr := readVarint(b)
		if verr != nil {
			return fn
		}
		b = rest2
		switch f {
		case functionID:
			fn.id = v
		case functionName:
			fn.name = int64(v)
		case functionFilename:
			fn.filename = int64(v)
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
		f, w, rest, ok := nextTag(b)
		if !ok {
			return s
		}
		b = rest
		switch f {
		case sampleLocationID:
			vals, rest, rok := readRepeated(b, w)
			if !rok {
				return s
			}
			s.locationIDs = append(s.locationIDs, vals...)
			b = rest
		case sampleValue:
			vals, rest, rok := readRepeated(b, w)
			if !rok {
				return s
			}
			for _, v := range vals {
				s.values = append(s.values, int64(v))
			}
			b = rest
		default:
			b, ok = skipUnknown(b, w)
			if !ok {
				return s
			}
		}
	}
	return s
}
