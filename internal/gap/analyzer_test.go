package gap

import (
	"math"
	"strings"
	"testing"

	"github.com/zelvior/lensyxe/internal/code"
)

// rec builds a static measurement for a file.
func rec(path string, complexity float64, lines, churn int) code.FileRecord {
	return code.FileRecord{
		Path: path, ComplexityScore: complexity, ComplexityMeasured: true,
		CodeLines: lines,
	}
}

func profileOf(obs, attributed int64, fns ...FuncSample) *Profile {
	p := &Profile{
		Source: SourcePprof, Path: "test.pprof",
		Observations: obs, Attributed: attributed, Functions: fns,
	}
	p.Attribution = attributionOf(p)
	return p
}

// The formula is the specified one, and it has a consequence worth pinning: at
// zero hits the log term is log10(1) = 0, so never-running code cannot score at
// all in the critical-risk column. That is why phantom code is a separate
// finding rather than a low score in this one.
func TestCriticalRiskIsStaticTimesLogHits(t *testing.T) {
	cases := []struct {
		static float64
		hits   int64
		want   float64
	}{
		{50, 0, 0},
		{50, 1, 50 * math.Log10(2)},
		{50, 9, 50 * math.Log10(10)},
		{50, 999, 50 * math.Log10(1000)},
		{0, 100, 0},
	}
	for _, c := range cases {
		got := criticalRisk(c.static, c.hits)
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("criticalRisk(%v, %d) = %v, want %v", c.static, c.hits, got, c.want)
		}
	}
}

// The logarithm is there to stop one very hot function swamping every other
// consideration. Ten thousand times the hits must move the score by a small
// multiple, not by ten thousand.
func TestLogTermCompressesAHugeHitCount(t *testing.T) {
	const hitsSmall, hitsHuge = 100, 1000000
	small := criticalRisk(50, hitsSmall)
	huge := criticalRisk(50, hitsHuge)

	hitRatio := float64(hitsHuge) / float64(hitsSmall)
	scoreRatio := huge / small
	if scoreRatio >= hitRatio/100 {
		t.Errorf("%dx the hits moved the score %.1fx; the score should grow "+
			"logarithmically, not linearly", int(hitRatio), scoreRatio)
	}
	// log10(1000001)/log10(101) is about 3.
	if scoreRatio < 2.5 || scoreRatio > 3.5 {
		t.Errorf("score ratio = %.2f, want about 3 for these hit counts", scoreRatio)
	}
}

func TestStaticScoreStaysInRange(t *testing.T) {
	cfg := DefaultConfig()
	for _, c := range []Entry{
		{ComplexityScore: -5, CodeLines: -10, Churn: -1},
		{ComplexityScore: 1e6, CodeLines: 1e7, Churn: 1e9},
	} {
		got := staticScore(c, cfg)
		if got < 0 || got > 100 {
			t.Errorf("staticScore(%+v) = %v, out of range", c, got)
		}
	}
}

func TestWeightsMustSumToOne(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ChurnWeight = 0.9
	if _, err := Analyze("/r", cfg, nil, nil, profileOf(0, 0), ""); err == nil {
		t.Fatal("Analyze accepted weights that do not sum to 1")
	}
}

// --- bucketing ---

func TestHotDebtAndPhantomAreSeparated(t *testing.T) {
	records := []code.FileRecord{
		rec("hot.go", 45, 400, 300),
		rec("debt.go", 45, 400, 300),
		rec("small.go", 3, 20, 5),
	}
	prof := profileOf(10, 10,
		FuncSample{Name: "hot", File: "hot.go", Hits: 5000},
		FuncSample{Name: "small", File: "small.go", Hits: 900},
	)

	res, err := Analyze("/r", DefaultConfig(), records,
		map[string]int{"hot.go": 300, "debt.go": 300, "small.go": 5}, prof, "")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Hot) != 1 || res.Hot[0].Path != "hot.go" {
		t.Errorf("hot = %+v, want only hot.go", res.Hot)
	}
	if len(res.Phantom) != 1 || res.Phantom[0].Path != "debt.go" {
		t.Errorf("phantom = %+v, want only debt.go", res.Phantom)
	}
	if res.Phantom[0].CriticalRisk != 0 {
		t.Errorf("phantom code scored %v on critical risk; it must be 0 by "+
			"the formula", res.Phantom[0].CriticalRisk)
	}
	if res.Matched != 2 {
		t.Errorf("matched = %d, want 2", res.Matched)
	}
}

// This is the property the whole package exists to protect. Absence is only
// claimable where the profile could see the code at all.
func TestPhantomIsGatedOnAttributionCoverage(t *testing.T) {
	records := []code.FileRecord{rec("never.go", 45, 400, 300)}

	// 20% coverage: the profile mostly could not resolve anything, so "no hits"
	// is not evidence of absence.
	prof := profileOf(100, 20, FuncSample{Name: "other", File: "elsewhere.go", Hits: 1})
	res, err := Analyze("/r", DefaultConfig(), records, nil, prof, "")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Phantom) != 0 {
		t.Errorf("a candidate was reported as phantom at 20%% coverage: %+v", res.Phantom)
	}
	if len(res.Debt) != 1 {
		t.Fatalf("debt = %+v, want the file downgraded to debt", res.Debt)
	}
	if res.Debt[0].Note == "" {
		t.Error("the downgrade was not explained")
	}
	if !strings.Contains(res.Debt[0].Note, "candidate") {
		t.Errorf("the note does not say it is a candidate: %q", res.Debt[0].Note)
	}

	// 90% coverage: absence is claimable.
	prof = profileOf(100, 90, FuncSample{Name: "other", File: "elsewhere.go", Hits: 5})
	res, _ = Analyze("/r", DefaultConfig(), records, nil, prof, "")
	if len(res.Phantom) != 1 {
		t.Errorf("at 90%% coverage a never-run file was not reported as phantom: %+v", res.Debt)
	}
}

// Two files with the same base name cannot be told apart from a normalised
// profile path. The join must be refused rather than guessed.
func TestAmbiguousBasenamesAreNotJoined(t *testing.T) {
	records := []code.FileRecord{
		rec("a/config.go", 45, 400, 300),
		rec("b/config.go", 45, 400, 300),
	}
	prof := profileOf(10, 10, FuncSample{Name: "cfg", File: "config.go", Hits: 9000})

	res, err := Analyze("/r", DefaultConfig(), records, nil, prof, "")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	for _, e := range res.Entries {
		if e.Hits != 0 {
			t.Errorf("%s was credited with %d hits from an ambiguous base name",
				e.Path, e.Hits)
		}
	}
	if len(res.AmbiguousBasenames) != 1 {
		t.Errorf("ambiguous = %v, want one entry", res.AmbiguousBasenames)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "shared by more than one") {
		t.Errorf("the ambiguity was not reported: %v", res.Warnings)
	}
}

// A test file's execution frequency reflects when tests ran, not production
// demand. Counting it would make an untested production path look exercised.
func TestTestFilesAreExcluded(t *testing.T) {
	r := rec("internal/x_test.go", 45, 400, 300)
	r.IsTest = true
	res, err := Analyze("/r", DefaultConfig(), []code.FileRecord{r}, nil,
		profileOf(10, 10, FuncSample{Name: "t", File: "x_test.go", Hits: 500}), "")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Entries) != 0 {
		t.Errorf("a test file was included: %+v", res.Entries)
	}
}

// A language the lexical estimator does not score has no complexity figure, and
// must not be reported as simple.
func TestUnmeasuredComplexityIsExcluded(t *testing.T) {
	r := rec("config.yaml", 0, 300, 0)
	r.ComplexityMeasured = false
	res, err := Analyze("/r", DefaultConfig(), []code.FileRecord{r}, nil,
		profileOf(10, 10, FuncSample{Name: "cfg", File: "config.yaml", Hits: 5}), "")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(res.Entries) != 0 {
		t.Errorf("a file with unmeasured complexity was reported: %+v", res.Entries)
	}
}

// --- staleness ---

func TestProfileVersionMismatchIsReported(t *testing.T) {
	prof := profileOf(10, 10, FuncSample{Name: "f", File: "a.go", Hits: 5})
	prof.Labels = map[string]string{"service.version": "1.4.2"}

	res, err := Analyze("/r", DefaultConfig(), []code.FileRecord{rec("a.go", 40, 400, 200)},
		nil, prof, "1.0.0")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Profile.Stale == "" {
		t.Fatal("a version mismatch was not reported")
	}
	if !strings.Contains(res.Profile.Stale, "different") {
		t.Errorf("the mismatch is not explained: %q", res.Profile.Stale)
	}
}

func TestMatchingProfileVersionIsNotFlagged(t *testing.T) {
	prof := profileOf(10, 10, FuncSample{Name: "f", File: "a.go", Hits: 5})
	prof.Labels = map[string]string{"service.version": "v1.0.0"}
	res, _ := Analyze("/r", DefaultConfig(), []code.FileRecord{rec("a.go", 40, 400, 200)},
		nil, prof, "1.0.0")
	if res.Profile.Stale != "" {
		t.Errorf("matching versions were flagged as stale: %q", res.Profile.Stale)
	}
}

// A profile with no version at all is not a mismatch, but it is an absence worth
// naming, because the two halves cannot be confirmed to match.
func TestUnknownProfileVersionIsNamed(t *testing.T) {
	prof := profileOf(10, 10, FuncSample{Name: "f", File: "a.go", Hits: 5})
	prof.Labels = map[string]string{"service.version": "1.4.2"}
	res, _ := Analyze("/r", DefaultConfig(), nil, nil, prof, "")
	if res.Profile.Stale == "" {
		t.Error("an unversioned tree with a versioned profile was not flagged")
	}
}

// --- coverage plumbing ---

func TestCoverageIsCarriedThrough(t *testing.T) {
	prof := profileOf(200, 50)
	res, _ := Analyze("/r", DefaultConfig(), nil, nil, prof, "")
	if res.Profile.Coverage != 0.25 {
		t.Errorf("coverage = %v, want 0.25", res.Profile.Coverage)
	}
	if res.Profile.Observations != 200 || res.Profile.Attributed != 50 {
		t.Errorf("counts = %d/%d, want 200/50",
			res.Profile.Observations, res.Profile.Attributed)
	}
}

// When nothing at all could be joined, the result must say so rather than
// presenting an empty hot list as good news.
func TestNoMatchesIsReportedAsAWarning(t *testing.T) {
	res, err := Analyze("/r", DefaultConfig(),
		[]code.FileRecord{rec("a.go", 40, 400, 200)}, nil,
		profileOf(10, 10, FuncSample{Name: "f", File: "totally-different.go", Hits: 9}), "")
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "could be matched") {
		t.Errorf("no warning about the empty join: %v", res.Warnings)
	}
}

func TestAnalyzeRejectsANilProfile(t *testing.T) {
	if _, err := Analyze("/r", DefaultConfig(), nil, nil, nil, ""); err == nil {
		t.Error("a nil profile was accepted")
	}
}

func TestEntriesAreRankedByCriticalRisk(t *testing.T) {
	records := []code.FileRecord{
		rec("a.go", 45, 400, 300),
		rec("b.go", 45, 400, 300),
		rec("c.go", 45, 400, 300),
	}
	prof := profileOf(30, 30,
		FuncSample{Name: "a", File: "a.go", Hits: 100000},
		FuncSample{Name: "b", File: "b.go", Hits: 900},
		FuncSample{Name: "c", File: "c.go", Hits: 100},
	)
	res, _ := Analyze("/r", DefaultConfig(), records, nil, prof, "")
	if len(res.Entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(res.Entries))
	}
	for i := 1; i < len(res.Entries); i++ {
		if res.Entries[i-1].CriticalRisk < res.Entries[i].CriticalRisk {
			t.Errorf("entries are not ranked by critical risk: %+v", res.Entries)
			break
		}
	}
}

func TestNormaliseBase(t *testing.T) {
	cases := map[string]string{
		"internal/git/analyzer.go": "analyzer.go",
		"/abs/path/to/file.go":     "file.go",
		"a\\b\\c.go":               "c.go",
		"":                         "",
	}
	for in, want := range cases {
		if got := normaliseBase(in); got != want {
			t.Errorf("normaliseBase(%q) = %q, want %q", in, got, want)
		}
	}
}
