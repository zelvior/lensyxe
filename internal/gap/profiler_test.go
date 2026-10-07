package gap

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The whole point of hand-decoding pprof rather than importing a library is that
// it has to actually work. These tests are the justification: they decode a real
// profile produced by the Go toolchain and check the numbers against what the
// profile was built to contain.

func TestParsePprofAgainstARealProfile(t *testing.T) {
	raw := realProfile(t)

	prof, err := parsePprof("cpu.pprof", raw)
	if err != nil {
		t.Fatalf("parsePprof: %v", err)
	}
	if len(prof.Functions) == 0 {
		t.Fatalf("no functions decoded. Notes: %v", prof.Notes)
	}
	if prof.Observations == 0 {
		t.Error("no observations decoded")
	}
	if prof.Attributed == 0 {
		t.Error("nothing was attributed to a function")
	}

	// Every decoded function must carry a name. A decoder that mis-assigns the
	// string table index produces plausible-looking garbage here rather than
	// failing, so the check is on the shape of what came out.
	//
	// Non-Go files are expected and correct: the profiler burns CPU in
	// math.Sqrt, which on Windows is an assembly intrinsic, so the profile
	// legitimately contains frames like time_windows_amd64.s. What must be
	// present is at least one real Go frame.
	goFrames, otherFrames := 0, 0
	for _, f := range prof.Functions {
		if f.Name == "" {
			t.Fatal("a decoded function has no name")
		}
		if f.Hits < 0 {
			t.Errorf("function %q has negative hits %d", f.Name, f.Hits)
		}
		if strings.HasSuffix(f.File, ".go") {
			goFrames++
		} else {
			otherFrames++
		}
	}
	if goFrames == 0 {
		t.Errorf("no Go source frames decoded; %d non-Go frames: %v",
			otherFrames, sampleNames(prof, 8))
	}

	// The profile's own package must appear: the test ran in it.
	found := false
	for _, f := range prof.Functions {
		if strings.Contains(f.Name, "Parse") ||
			strings.Contains(f.Name, "Test") ||
			strings.Contains(f.File, "gitlog") {
			found = true
		}
	}
	if !found {
		t.Errorf("none of the decoded functions look like they came from the "+
			"profiled package. Decoded %d functions, first few: %v",
			len(prof.Functions), sampleNames(prof, 5))
	}

	// A CPU profile records nanoseconds, so the latency slot must have been found
	// and some latency attributed.
	var totalLatency int64
	for _, f := range prof.Functions {
		totalLatency += f.LatencyNS
	}
	if totalLatency == 0 {
		t.Error("no latency was attributed; the nanosecond value slot was not found")
	}

	if !prof.Window.Known {
		t.Error("the profile window was not read")
	}
	if prof.Window.Duration <= 0 {
		t.Errorf("window duration = %v, want positive", prof.Window.Duration)
	}
	if prof.Window.Start.IsZero() {
		t.Error("profile start time was not decoded")
	}
	if prof.Attribution != AttributionFileAndFunction {
		t.Errorf("attribution = %q, want %q", prof.Attribution, AttributionFileAndFunction)
	}
}

func sampleNames(p *Profile, n int) []string {
	var out []string
	for i, f := range p.Functions {
		if i >= n {
			break
		}
		out = append(out, f.Name+" ("+f.File+")")
	}
	return out
}

// A profile with no symbol table must be reported as unattributable rather than
// producing an empty table that reads like "nothing ran".
func TestSymbollessProfileIsReportedNotSilentlyEmpty(t *testing.T) {
	// A minimal profile with only a string table: structurally valid, no symbols.
	raw := buildProfile(t, map[string]any{
		"string_table": []string{"", "samples", "count", "cpu", "nanoseconds"},
	})
	prof, err := parsePprof("nosym.pprof", raw)
	if err != nil {
		t.Fatalf("parsePprof: %v", err)
	}
	if len(prof.Functions) != 0 {
		t.Errorf("a symbol-less profile decoded functions: %+v", prof.Functions)
	}
	if prof.Attribution != AttributionNone {
		t.Errorf("attribution = %q, want none", prof.Attribution)
	}
	joined := strings.Join(prof.Notes, "\n")
	if !strings.Contains(joined, "no symbol table") {
		t.Errorf("no explanation for the empty result: %v", prof.Notes)
	}
}

// --- the profile.proto wire format, verified field by field ---

func TestProfileProtoFieldNumbers(t *testing.T) {
	// A profile with one function, one location and one sample. If any field
	// number is wrong the sample will not join to the function and the decoded
	// function list stays empty.
	raw := buildProfile(t, map[string]any{
		"string_table": []string{
			"", "myfunc", "myfile.go", "samples", "count", "cpu", "nanoseconds",
		},
		"sample_type": []map[string]any{
			{"type": 3, "unit": 4},
			{"type": 5, "unit": 6},
		},
		"function": []map[string]any{
			{"id": 1, "name": 1, "filename": 2},
		},
		"location": []map[string]any{
			{"id": 1, "line": []map[string]any{{"function_id": 1, "line": 42}}},
		},
		"sample": []map[string]any{
			{"location_id": []uint64{1}, "value": []int64{7, 12345}},
		},
	})

	prof, err := parsePprof("handmade.pprof", raw)
	if err != nil {
		t.Fatalf("parsePprof: %v", err)
	}
	if len(prof.Functions) != 1 {
		t.Fatalf("decoded %d functions, want 1: %+v", len(prof.Functions), prof.Functions)
	}
	f := prof.Functions[0]
	if f.Name != "myfunc" {
		t.Errorf("name = %q, want myfunc", f.Name)
	}
	if f.File != "myfile.go" {
		t.Errorf("file = %q, want myfile.go", f.File)
	}
	// Value slot 0 is the count and slot 1 the nanoseconds.
	if f.Hits != 7 {
		t.Errorf("hits = %d, want 7 from the count slot", f.Hits)
	}
	if f.LatencyNS != 12345 {
		t.Errorf("latency = %d, want 12345 from the nanosecond slot", f.LatencyNS)
	}
}

// Packed and unpacked repeated fields are both legal encodings of the same
// message. A decoder that handles only one reads a real CPU profile as empty,
// because gogo/protobuf -- which Go uses -- emits packed.
func TestPackedAndUnpackedRepeatedFieldsAgree(t *testing.T) {
	base := map[string]any{
		"string_table": []string{"", "myfunc", "myfile.go", "samples", "count"},
		"sample_type":  []map[string]any{{"type": 3, "unit": 4}},
		"function":     []map[string]any{{"id": 1, "name": 1, "filename": 2}},
		"location": []map[string]any{
			{"id": 1, "line": []map[string]any{{"function_id": 1, "line": 42}}},
		},
	}

	packed := cloneWith(base, "sample", []map[string]any{
		{"location_id_packed": []uint64{1}, "value_packed": []int64{7}},
	})
	p1, err := parsePprof("packed", buildProfile(t, packed))
	if err != nil {
		t.Fatalf("packed: %v", err)
	}

	unpacked := cloneWith(base, "sample", []map[string]any{
		{"location_id": []uint64{1}, "value": []int64{7}},
	})
	p2, err := parsePprof("unpacked", buildProfile(t, unpacked))
	if err != nil {
		t.Fatalf("unpacked: %v", err)
	}

	if len(p1.Functions) != 1 || len(p2.Functions) != 1 {
		t.Fatalf("packed decoded %d, unpacked decoded %d; both must be 1",
			len(p1.Functions), len(p2.Functions))
	}
	if p1.Functions[0].Hits != p2.Functions[0].Hits {
		t.Errorf("packed hits %d != unpacked %d", p1.Functions[0].Hits, p2.Functions[0].Hits)
	}
}

// --- OpenTelemetry ---

func TestLoadOTelArrayWithFunctionAttributes(t *testing.T) {
	spans := []map[string]any{
		{
			"name": "GET /users", "traceId": "a1", "spanId": "b1",
			"startTime": "2026-10-06T10:00:00Z", "endTime": "2026-10-06T10:00:00.02Z",
			"code": map[string]any{
				"function": map[string]any{"name": "handleUsers"},
				"filepath": "internal/http/users.go",
			},
		},
		{
			"name": "GET /users", "traceId": "a2", "spanId": "b2",
			"startTime": "2026-10-06T10:00:01Z", "endTime": "2026-10-06T10:00:01.05Z",
			"code": map[string]any{
				"function": map[string]any{"name": "handleUsers"},
				"filepath": "internal/http/users.go",
			},
		},
		{
			"name": "db.query", "traceId": "a3", "spanId": "b3",
			"startTime": "2026-10-06T10:00:02Z", "endTime": "2026-10-06T10:00:02.01Z",
			"code": map[string]any{
				"function": map[string]any{"name": "query"},
			},
		},
	}
	path := writeJSONFile(t, "otel.json", mustJSON(t, spans))
	prof, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if prof.Source != SourceOTel {
		t.Errorf("source = %q, want otel", prof.Source)
	}
	if prof.Observations != 3 {
		t.Errorf("observations = %d, want 3", prof.Observations)
	}
	if len(prof.Functions) != 2 {
		t.Fatalf("functions = %+v, want 2", prof.Functions)
	}
	if prof.Functions[0].Name != "handleUsers" || prof.Functions[0].Hits != 2 {
		t.Errorf("top function = %+v, want handleUsers with 2 hits", prof.Functions[0])
	}
	// 20ms + 50ms.
	if prof.Functions[0].LatencyNS != int64(70*time.Millisecond) {
		t.Errorf("latency = %v, want 70ms", time.Duration(prof.Functions[0].LatencyNS))
	}
	if prof.Functions[0].File != "users.go" {
		t.Errorf("file = %q, want users.go", prof.Functions[0].File)
	}
}

// Spans with no source attributes must not be silently spread across the
// codebase. They are counted, and the shortfall is stated.
func TestOTelSpansWithoutCodeAttributesAreCountedNotAttributed(t *testing.T) {
	spans := []map[string]any{
		{"name": "GET /a", "traceId": "t1", "spanId": "s1"},
		{"name": "GET /a", "traceId": "t2", "spanId": "s2"},
		{
			"name": "GET /b", "traceId": "t3", "spanId": "s3",
			"code": map[string]any{"function": map[string]any{"name": "handleB"}},
		},
	}
	path := writeJSONFile(t, "partial.json", mustJSON(t, spans))
	prof, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if prof.Observations != 3 {
		t.Errorf("observations = %d, want 3", prof.Observations)
	}
	if prof.Attributed != 1 {
		t.Errorf("attributed = %d, want 1", prof.Attributed)
	}
	if got := prof.Coverage(); got < 0.33 || got > 0.34 {
		t.Errorf("coverage = %v, want about 0.33", got)
	}
	if prof.Attribution != AttributionPartial {
		t.Errorf("attribution = %q, want partial", prof.Attribution)
	}
	if !strings.Contains(strings.Join(prof.Notes, "\n"), "code.function.name") {
		t.Errorf("no note about the missing attribute: %v", prof.Notes)
	}
}

// A file with no attributable spans at all must say so rather than implying the
// program never ran.
func TestOTelWithNoAttributableSpansIsExplained(t *testing.T) {
	path := writeJSONFile(t, "noattr.json", mustJSON(t, []map[string]any{
		{"name": "GET /a", "traceId": "t1", "spanId": "s1"},
	}))
	prof, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if prof.Attribution != AttributionNone {
		t.Errorf("attribution = %q, want none", prof.Attribution)
	}
	if prof.Coverage() != 0 {
		t.Errorf("coverage = %v, want 0", prof.Coverage())
	}
}

func TestOTelResourceSpansShape(t *testing.T) {
	export := map[string]any{
		"resourceSpans": []map[string]any{
			{
				"resource": map[string]any{
					"attributes": []map[string]any{
						{"key": "service.name", "value": map[string]any{"stringValue": "api"}},
						{"key": "service.version", "value": map[string]any{"stringValue": "1.4.2"}},
					},
				},
				"scopeSpans": []map[string]any{
					{"spans": []map[string]any{
						{"name": "op", "spanId": "s1",
							"code": map[string]any{"function": map[string]any{"name": "Run"}}},
					}},
				},
			},
		},
	}
	path := writeJSONFile(t, "rs.json", mustJSON(t, export))
	prof, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(prof.Functions) != 1 || prof.Functions[0].Name != "Run" {
		t.Errorf("functions = %+v, want Run", prof.Functions)
	}
	// The service version is the label that lets a caller notice a profile from
	// a different build than the tree being analysed.
	if got := prof.Labels["service.version"]; got != "1.4.2" {
		t.Errorf("service.version label = %q, want 1.4.2", got)
	}
}

// --- access logs ---

func TestLoadAccessLog(t *testing.T) {
	log := strings.Join([]string{
		`127.0.0.1 - - [06/Oct/2026:10:00:00 +0000] "GET /users/42 HTTP/1.1" 200 512 1200`,
		`127.0.0.1 - - [06/Oct/2026:10:00:01 +0000] "GET /users/7 HTTP/1.1" 200 480 900`,
		`127.0.0.1 - - [06/Oct/2026:10:00:02 +0000] "POST /users HTTP/1.1" 201 88 4000`,
		`127.0.0.1 - - [06/Oct/2026:10:00:03 +0000] "GET /users/9 HTTP/1.1" 500 12 30000`,
		``,                                   // blank
		`garbage line that is not a request`, // must be excluded and counted
	}, "\n")

	path := writeTemp(t, "access.log", log)
	prof, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if prof.Source != SourceAccessLog {
		t.Errorf("source = %q, want access-log", prof.Source)
	}
	if prof.Observations != 4 {
		t.Errorf("observations = %d, want 4 (the garbage line excluded)", prof.Observations)
	}
	// /users/42 and /users/7 must collapse into one route.
	var users, usersID int64
	for _, f := range prof.Functions {
		switch f.Name {
		case "/users":
			users = f.Hits
		case "/users/{id}":
			usersID = f.Hits
		}
	}
	if usersID != 3 {
		t.Errorf("/users/{id} hits = %d, want 3 after collapsing numeric ids", usersID)
	}
	if users != 1 {
		t.Errorf("/users hits = %d, want 1", users)
	}
	if !strings.Contains(strings.Join(prof.Notes, "\n"), "1 line(s) could not be parsed") {
		t.Errorf("the unparsed line was not reported: %v", prof.Notes)
	}
	if !strings.Contains(strings.Join(prof.Notes, "\n"), "not source functions") {
		t.Errorf("no note that routes are not functions: %v", prof.Notes)
	}
}

func TestNormaliseRoute(t *testing.T) {
	cases := map[string]string{
		"/users/42":       "/users/{id}",
		"/users/42/posts": "/users/{id}/posts",
		"/a/550e8400-e29b-41d4-a716-446655440000": "/a/{id}",
		"/users/john":      "/users/john", // a name is not an id
		"/users/42?page=2": "/users/{id}",
		"/":                "/",
	}
	for in, want := range cases {
		if got := normaliseRoute(in); got != want {
			t.Errorf("normaliseRoute(%q) = %q, want %q", in, got, want)
		}
	}
}

// A query string must not create a distinct route per request, or every URL
// with a cache-buster becomes its own "function" with one hit.
func TestRouteCollapsingIgnoresQueryStrings(t *testing.T) {
	if a, b := normaliseRoute("/search?q=go"), normaliseRoute("/search?q=rust"); a != b {
		t.Errorf("query strings created two routes: %q and %q", a, b)
	}
}

// --- format detection ---

func TestDetectPprofByMagicBytes(t *testing.T) {
	if got := detect([]byte{0x1f, 0x8b, 0x08, 0x00}, "x.json"); got != SourcePprof {
		t.Errorf("gzip magic detected as %q; content must beat the extension", got)
	}
}

func TestDetectRejectsUnrelatedJSON(t *testing.T) {
	// A JSON file that is not telemetry must not be parsed as a profile.
	data := []byte(`{"name":"package.json","version":"1.0.0"}`)
	if got := detect(data, "package.json"); got != "" {
		t.Errorf("an unrelated JSON file was detected as %q", got)
	}
}

func TestLoadRejectsAnUnknownFormat(t *testing.T) {
	path := writeTemp(t, "random.bin", "\x01\x02\x03 not a profile at all")
	if _, err := Load(path); err == nil {
		t.Fatal("an unrecognised file was accepted")
	}
}

func TestLoadRejectsAnEmptyFile(t *testing.T) {
	path := writeTemp(t, "empty.pprof", "")
	if _, err := Load(path); err == nil {
		t.Fatal("an empty file was accepted")
	}
}

func TestLoadRejectsADirectory(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("a directory was accepted as a profile")
	}
}

func TestCoverageOfAnEmptyProfile(t *testing.T) {
	p := &Profile{}
	if got := p.Coverage(); got != 0 {
		t.Errorf("coverage of an empty profile = %v, want 0", got)
	}
	var nilp *Profile
	if got := nilp.Coverage(); got != 0 {
		t.Errorf("coverage of a nil profile = %v, want 0", got)
	}
}

// --- helpers ---

// realProfile produces a genuine pprof by running a test of this package with
// the profiler enabled, then decompressing it.
//
// Generating the profile from the code under test means the assertions can check
// against symbols the toolchain really emitted, rather than against a fixture
// encoding this package's own assumptions back at itself.
func realProfile(t *testing.T) []byte {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "cpu.pprof")

	cmd := exec.Command("go", "test",
		"-count=1",
		"-run", "TestParsePprofAgainstARealProfileHelper",
		"-cpuprofile", out,
		"-o", filepath.Join(dir, "gap.test"),
		".")
	cmd.Dir = mustPkgDir(t)
	cmd.Env = append(os.Environ(),
		"GOCACHE="+os.Getenv("LENSYXE_TEST_GOCACHE"),
		"GOMODCACHE="+os.Getenv("LENSYXE_TEST_GOMODCACHE"),
	)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not generate a profile with the toolchain (%v): %s", err, combined)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Skipf("the toolchain wrote no profile: %v", err)
	}
	zr := newGzipReader(data)
	defer zr.Close()
	raw := readAll(t, zr)
	if len(raw) == 0 {
		t.Skip("the generated profile decompressed to nothing")
	}
	return raw
}

// TestParsePprofAgainstARealProfileHelper exists only so the profiler has
// something to run when the outer test generates a profile.
// TestParsePprofAgainstARealProfileHelper burns CPU long enough that the sampler
// actually collects.
//
// A workload that finishes in microseconds collects zero samples, and Go then
// writes a profile with no location or function tables at all. The result decodes
// to nothing, which is indistinguishable from a broken decoder -- it took a
// profile built by the toolchain to tell those apart.
func TestParsePprofAgainstARealProfileHelper(t *testing.T) {
	deadline := time.Now().Add(1500 * time.Millisecond)
	sink := 0.0
	for time.Now().Before(deadline) {
		for i := 1; i < 20000; i++ {
			sink += math.Sqrt(float64(i))
		}
	}
	_ = sink
}

func mustPkgDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return wd
}

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeJSONFile(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
