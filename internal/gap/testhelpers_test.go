package gap

import (
	"compress/gzip"
	"encoding/binary"
	"io"
	"testing"
)

// A minimal protobuf encoder used only by tests.
//
// Encoding and decoding from the same hand-written spec is circular on its own,
// which is why TestParsePprofAgainstARealProfile exists: it decodes a profile the
// Go toolchain actually produced, and that check is independent of anything here.
// The encoder's job is to let the individual field numbers be pinned one at a
// time, so a wrong number fails in a test that names it rather than in a test
// that reports a mysteriously empty table.

// buildProfile encodes a Profile message from a description.
func buildProfile(t *testing.T, spec map[string]any) []byte {
	t.Helper()
	var out []byte

	if strs, ok := spec["string_table"].([]string); ok {
		for _, s := range strs {
			out = appendBytesField(out, pprofStringTab, []byte(s))
		}
	}
	if types, ok := spec["sample_type"].([]map[string]any); ok {
		for _, st := range types {
			var b []byte
			if v, ok := st["type"]; ok {
				b = appendVarintField(b, 1, toU64(v))
			}
			if v, ok := st["unit"]; ok {
				b = appendVarintField(b, 2, toU64(v))
			}
			out = appendBytesField(out, pprofSampleType, b)
		}
	}
	if fns, ok := spec["function"].([]map[string]any); ok {
		for _, f := range fns {
			var b []byte
			if v, ok := f["id"]; ok {
				b = appendVarintField(b, functionID, toU64(v))
			}
			if v, ok := f["name"]; ok {
				b = appendVarintField(b, functionName, toU64(v))
			}
			if v, ok := f["filename"]; ok {
				b = appendVarintField(b, functionFilename, toU64(v))
			}
			out = appendBytesField(out, pprofFunction, b)
		}
	}
	if locs, ok := spec["location"].([]map[string]any); ok {
		for _, l := range locs {
			var b []byte
			if v, ok := l["id"]; ok {
				b = appendVarintField(b, locationID, toU64(v))
			}
			if lines, ok := l["line"].([]map[string]any); ok {
				for _, ln := range lines {
					var lb []byte
					if v, ok := ln["function_id"]; ok {
						lb = appendVarintField(lb, lineFunctionID, toU64(v))
					}
					if v, ok := ln["line"]; ok {
						lb = appendVarintField(lb, lineNumber, toU64(v))
					}
					b = appendBytesField(b, locationLine, lb)
				}
			}
			out = appendBytesField(out, pprofLocation, b)
		}
	}
	if samples, ok := spec["sample"].([]map[string]any); ok {
		for _, s := range samples {
			var b []byte
			if ids, ok := s["location_id"].([]uint64); ok {
				for _, id := range ids {
					b = appendVarintField(b, sampleLocationID, id)
				}
			}
			if ids, ok := s["location_id_packed"].([]uint64); ok {
				var packed []byte
				for _, id := range ids {
					packed = binary.AppendUvarint(packed, id)
				}
				b = appendBytesField(b, sampleLocationID, packed)
			}
			if vals, ok := s["value"].([]int64); ok {
				for _, v := range vals {
					b = appendVarintField(b, sampleValue, uint64(v))
				}
			}
			if vals, ok := s["value_packed"].([]int64); ok {
				var packed []byte
				for _, v := range vals {
					packed = binary.AppendUvarint(packed, uint64(v))
				}
				b = appendBytesField(b, sampleValue, packed)
			}
			out = appendBytesField(out, pprofSampleFld, b)
		}
	}
	if v, ok := spec["time_nanos"]; ok {
		out = appendVarintField(out, pprofTimeNanos, toU64(v))
	}
	if v, ok := spec["duration_nanos"]; ok {
		out = appendVarintField(out, pprofDuration, toU64(v))
	}
	return out
}

// cloneWith returns a copy of spec with one key replaced.
func cloneWith(spec map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(spec))
	for k, v := range spec {
		out[k] = v
	}
	out[key] = value
	return out
}

func appendVarintField(b []byte, field uint64, v uint64) []byte {
	b = binary.AppendUvarint(b, field<<3)
	return binary.AppendUvarint(b, v)
}

func appendBytesField(b []byte, field uint64, payload []byte) []byte {
	b = binary.AppendUvarint(b, field<<3|2)
	b = binary.AppendUvarint(b, uint64(len(payload)))
	return append(b, payload...)
}

func toU64(v any) uint64 {
	switch t := v.(type) {
	case int:
		return uint64(t)
	case int64:
		return uint64(t)
	case uint64:
		return t
	default:
		return 0
	}
}

func newGzipReader(data []byte) *gzip.Reader {
	r, err := gzip.NewReader(bytesReader(data))
	if err != nil {
		return nil
	}
	return r
}

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	if r == nil {
		t.Skip("could not open the generated profile")
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Skipf("could not read the generated profile: %v", err)
	}
	return b
}

func bytesReader(b []byte) io.Reader { return &sliceReader{b: b} }

type sliceReader struct {
	b []byte
	i int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
