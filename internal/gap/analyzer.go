package gap

import (
	"fmt"
	"math"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zelvior/lensyxe/internal/code"
)

// Config tunes the cross-reference.
type Config struct {
	// ComplexityWeight, SizeWeight and ChurnWeight combine the three static
	// components into the Static Complexity Score. They must sum to 1.
	ComplexityWeight float64
	SizeWeight       float64
	ChurnWeight      float64

	// Saturation points. Each component is scaled to 0..1 by these before
	// weighting, so no component can exceed its share however extreme its input.
	ComplexitySaturation float64
	SizeSaturation       int
	ChurnSaturation      int

	// MinCoverage is the attribution coverage below which a zero-hit finding is
	// downgraded from phantom to candidate.
	//
	// "This function never ran" is a claim about absence, and absence can only
	// be asserted where the profile was able to see the code at all. Below this
	// threshold the profile may simply have failed to record the part of the
	// program this function lives in.
	MinCoverage float64

	// Limit caps each output section.
	Limit int
}

// DefaultConfig returns the settings used when a caller supplies none.
func DefaultConfig() Config {
	return Config{
		ComplexityWeight: 0.45,
		SizeWeight:       0.30,
		ChurnWeight:      0.25,

		// The existing code analyzer already places complexity on the
		// conventional McCabe bands, and exports the very-high limit. Reusing it
		// keeps this package from inventing a second, subtly different
		// definition of "complex" that would disagree with the health score.
		ComplexitySaturation: code.ComplexityVeryHighLimit,
		SizeSaturation:       400,
		ChurnSaturation:      300,

		MinCoverage: 0.5,
		Limit:       20,
	}
}

// Bucket classifies one file.
type Bucket string

const (
	// BucketHot is complicated code that actually runs.
	BucketHot Bucket = "hot"
	// BucketDebt is complicated code that does not run.
	BucketDebt Bucket = "debt"
	// BucketPhantom is code with no runtime evidence at all.
	BucketPhantom Bucket = "phantom"
	// BucketQuiet is code with runtime evidence but neither complexity nor
	// velocity worth reporting.
	BucketQuiet Bucket = "quiet"
)

// Entry is one file's cross-referenced measurement.
type Entry struct {
	Path string `json:"path"`
	// Static is the Static Complexity Score, 0..100.
	Static float64 `json:"static"`
	// ComplexityScore and CodeLines are the raw inputs behind it.
	ComplexityScore float64 `json:"complexity_score"`
	CodeLines       int     `json:"code_lines"`
	Churn           int     `json:"churn"`
	// Hits is the runtime evidence attributed to this file. Zero means none was
	// found, which is only an absence claim when Coverage is at least
	// MinCoverage.
	Hits int64 `json:"hits"`
	// LatencyNS is the time attributed to it.
	LatencyNS int64 `json:"latency_ns"`
	// CriticalRisk is Static * log10(Hits+1).
	//
	// The logarithm is the whole point: it compresses an unbounded hit count
	// into a bounded multiplier, so a function called ten thousand times does
	// not swamp every other consideration by two orders of magnitude. At zero
	// hits the multiplier is log10(1) = 0, so a file that never runs cannot
	// score at all -- which is why never-running code is a separate finding
	// rather than a zero in this one.
	CriticalRisk float64 `json:"critical_risk"`
	// Bucket is which section this belongs in.
	Bucket Bucket `json:"bucket"`
	// Phantom is true only when the file had no runtime evidence and coverage
	// was good enough to assert absence.
	Phantom bool `json:"phantom"`
	// Note explains a caveat attached to this entry.
	Note string `json:"note,omitempty"`
}

// Result is the whole cross-reference.
type Result struct {
	Root string `json:"root"`
	// Profile describes the runtime evidence actually used.
	Profile ProfileSummary `json:"profile"`
	// Hot, Debt and Phantom are the three reported sections.
	Hot     []Entry `json:"hot"`
	Debt    []Entry `json:"debt"`
	Phantom []Entry `json:"phantom"`
	// Entries is every measured file, ranked by CriticalRisk.
	Entries []Entry `json:"entries"`
	// Files is how many source files were considered.
	Files int `json:"files"`
	// Matched is how many files received runtime evidence.
	Matched int `json:"matched"`
	// AmbiguousBasenames lists base names shared by more than one source file.
	//
	// A profile records the path the binary was built with, which after
	// normalisation to a base name can collide. Joining on an ambiguous name
	// would attribute one file's runtime evidence to another, so those joins are
	// refused rather than guessed.
	AmbiguousBasenames []string `json:"ambiguous_basenames,omitempty"`
	// Warnings are conditions that limit how the result may be read.
	Warnings []string `json:"warnings,omitempty"`
}

// ProfileSummary is the runtime evidence, in a form the report can print.
type ProfileSummary struct {
	Source       Source      `json:"source"`
	Path         string      `json:"path"`
	Observations int64       `json:"observations"`
	Attributed   int64       `json:"attributed"`
	Coverage     float64     `json:"coverage"`
	Attribution  Attribution `json:"attribution"`
	// WindowStart and WindowEnd bound the measurement, when the profile recorded
	// any timing at all.
	WindowStart string `json:"window_start,omitempty"`
	WindowEnd   string `json:"window_end,omitempty"`
	// Stale is set when the profile records a version that does not match the
	// tree, or when it records none and that fact matters.
	Stale string `json:"stale,omitempty"`
	// Notes are non-fatal observations from the loader.
	Notes []string `json:"notes,omitempty"`
}

// validateWeights rejects a configuration whose weights do not describe a
// weighted sum. Silently renormalising would make the documented weights a lie
// and the score unreproducible from them.
func validateWeights(cfg Config) error {
	sum := cfg.ComplexityWeight + cfg.SizeWeight + cfg.ChurnWeight
	if math.Abs(sum-1) > 1e-9 {
		return fmt.Errorf(
			"gap weights must sum to 1, got %.4f (%v + %v + %v): the score is "+
				"defined by these weights and renormalising them would make the "+
				"documented values meaningless",
			sum, cfg.ComplexityWeight, cfg.SizeWeight, cfg.ChurnWeight)
	}
	if cfg.ComplexitySaturation <= 0 || cfg.SizeSaturation <= 0 ||
		cfg.ChurnSaturation <= 0 {
		return fmt.Errorf("gap saturation points must be positive")
	}
	return nil
}

// Analyze cross-references static measurements against a runtime profile.
//
// records are the per-file static measurements, which the caller obtains from
// internal/code so this package does not walk the tree a second time. churn is
// the per-file churn total and may be empty.
func Analyze(
	root string,
	cfg Config,
	records []code.FileRecord,
	churn map[string]int,
	prof *Profile,
	buildID string,
) (Result, error) {
	res := Result{Root: root, Files: len(records)}
	if prof == nil {
		return res, fmt.Errorf("gap: no runtime profile was supplied")
	}
	if err := validateWeights(cfg); err != nil {
		return res, err
	}

	res.Profile = summariseProfile(prof, buildID)
	// Profile.Notes already carries the loader's notes, so they are not also
	// appended to Warnings: printing both lists duplicates every one of them.
	res.Warnings = nil

	// Index the runtime evidence by normalised base name, and record any name
	// that more than one source file claims.
	hitsByBase := map[string]int64{}
	latencyByBase := map[string]int64{}
	for _, f := range prof.Functions {
		if f.File == "" {
			continue
		}
		base := normaliseBase(f.File)
		if base == "" {
			continue
		}
		hitsByBase[base] += f.Hits
		latencyByBase[base] += f.LatencyNS
	}

	owners := map[string][]string{}
	for _, r := range records {
		owners[normaliseBase(r.Path)] = append(owners[normaliseBase(r.Path)], r.Path)
	}
	ambiguous := map[string]bool{}
	for base, paths := range owners {
		if len(paths) > 1 {
			ambiguous[base] = true
			sort.Strings(paths)
			res.AmbiguousBasenames = append(res.AmbiguousBasenames,
				base+" ("+strings.Join(paths, ", ")+")")
		}
	}
	sort.Strings(res.AmbiguousBasenames)
	if len(res.AmbiguousBasenames) > 0 {
		// The full list with every path runs to thousands of characters and
		// buries the report. The count is what matters; a few examples make it
		// concrete, and the whole list stays in the JSON output.
		const shown = 4
		examples := res.AmbiguousBasenames
		if len(examples) > shown {
			examples = examples[:shown]
		}
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"%d source file name(s) are shared by more than one file, so their "+
				"runtime evidence cannot be attributed to a specific file and was "+
				"not used, e.g. %s",
			len(res.AmbiguousBasenames), strings.Join(examples, "; ")))
	}

	coverage := prof.Coverage()
	absenceClaimable := coverage >= cfg.MinCoverage

	for _, r := range records {
		if r.IsTest {
			// A test's execution frequency is a function of when tests ran, not
			// of production demand. Including it would make an untested
			// production path look exercised.
			continue
		}
		if !r.ComplexityMeasured {
			continue
		}

		e := Entry{
			Path:            r.Path,
			ComplexityScore: r.ComplexityScore,
			CodeLines:       r.CodeLines,
			Churn:           churn[r.Path],
		}
		e.Static = staticScore(e, cfg)

		base := normaliseBase(r.Path)
		if !ambiguous[base] {
			e.Hits = hitsByBase[base]
			e.LatencyNS = latencyByBase[base]
		}
		if e.Hits > 0 {
			res.Matched++
		}

		e.CriticalRisk = criticalRisk(e.Static, e.Hits)

		switch {
		case e.Hits > 0 && e.Static >= complicatedThreshold:
			e.Bucket = BucketHot
		case e.Hits > 0:
			e.Bucket = BucketQuiet
		default:
			e.Bucket = BucketDebt
			if absenceClaimable {
				e.Phantom = true
				e.Bucket = BucketPhantom
			} else {
				e.Note = fmt.Sprintf(
					"no runtime evidence, but only %.0f%% of the profile resolved to "+
						"source, so this is a candidate rather than a confirmed "+
						"absence", coverage*100)
			}
		}

		if ambiguous[base] {
			e.Note = strings.TrimSpace(e.Note +
				" runtime evidence not joined: this base name is shared by several files")
		}

		res.Entries = append(res.Entries, e)
	}

	sort.SliceStable(res.Entries, func(i, j int) bool {
		if res.Entries[i].CriticalRisk != res.Entries[j].CriticalRisk {
			return res.Entries[i].CriticalRisk > res.Entries[j].CriticalRisk
		}
		if res.Entries[i].Static != res.Entries[j].Static {
			return res.Entries[i].Static > res.Entries[j].Static
		}
		return res.Entries[i].Path < res.Entries[j].Path
	})

	for _, e := range res.Entries {
		switch e.Bucket {
		case BucketHot:
			res.Hot = append(res.Hot, e)
		case BucketDebt:
			res.Debt = append(res.Debt, e)
		case BucketPhantom:
			res.Phantom = append(res.Phantom, e)
		}
	}
	res.Debt = sortByStatic(res.Debt, cfg.Limit)
	res.Phantom = sortByStatic(res.Phantom, cfg.Limit)
	if len(res.Hot) > cfg.Limit {
		res.Hot = res.Hot[:cfg.Limit]
	}

	if res.Matched == 0 {
		res.Warnings = append(res.Warnings,
			"no source file could be matched to runtime evidence, so nothing in "+
				"this result distinguishes hot code from code that was not observed")
	}
	return res, nil
}

// // complicatedThreshold is the Static Complexity Score above which a file counts
// as complicated for bucketing: a quarter of the available scale, which a file
// has to earn rather than drift into.
const complicatedThreshold = 25.0

// staticScore combines the three static components into 0..100.
func staticScore(e Entry, cfg Config) float64 {
	complexity := clamp01(e.ComplexityScore / cfg.ComplexitySaturation)
	size := clamp01(float64(e.CodeLines) / float64(cfg.SizeSaturation))
	churn := clamp01(float64(e.Churn) / float64(cfg.ChurnSaturation))

	total := cfg.ComplexityWeight*complexity +
		cfg.SizeWeight*size +
		cfg.ChurnWeight*churn
	return round2(clamp01(total) * 100)
}

// criticalRisk is the Critical Path Risk Score:
//
//	Static Complexity Score * log10(Runtime Hits + 1)
//
// Zero hits gives a multiplier of log10(1) = 0, so never-running code scores
// zero here by construction. That is deliberate and is the reason never-running
// code is reported as its own finding rather than as a low score in this one.
func criticalRisk(static float64, hits int64) float64 {
	if hits <= 0 {
		return 0
	}
	return round2(static * math.Log10(float64(hits)+1))
}

// normaliseBase reduces a path to a comparable base name.
func normaliseBase(p string) string {
	p = filepath.ToSlash(p)
	if p == "" {
		return ""
	}
	return path.Base(p)
}

func sortByStatic(entries []Entry, limit int) []Entry {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Static != entries[j].Static {
			return entries[i].Static > entries[j].Static
		}
		return entries[i].Path < entries[j].Path
	})
	if limit > 0 && len(entries) > limit {
		return entries[:limit]
	}
	return entries
}

func summariseProfile(p *Profile, buildID string) ProfileSummary {
	s := ProfileSummary{
		Source: p.Source, Path: p.Path,
		Observations: p.Observations, Attributed: p.Attributed,
		Coverage: round2(p.Coverage()), Attribution: p.Attribution,
		Notes: p.Notes,
	}
	if !p.Window.Start.IsZero() {
		s.WindowStart = p.Window.Start.Format("2006-01-02T15:04:05Z07:00")
	}
	if !p.Window.End.IsZero() {
		s.WindowEnd = p.Window.End.Format("2006-01-02T15:04:05Z07:00")
	}

	// The static half and the runtime half must describe the same code. When the
	// profile records a version and it does not match, the two halves are about
	// different programs and the join is not sound.
	for _, key := range []string{"service.version", "version", "build_id", "commit"} {
		if v, ok := p.Labels[key]; ok && v != "" {
			if buildID == "" {
				s.Stale = fmt.Sprintf(
					"the profile records %s=%s and the analysed tree's version is "+
						"unknown, so the static and runtime halves may describe "+
						"different builds", key, v)
			} else if !strings.Contains(buildID, v) && !strings.Contains(v, buildID) {
				s.Stale = fmt.Sprintf(
					"the profile records %s=%s but the analysed tree reports %s; "+
						"the static and runtime halves are describing different "+
						"builds", key, v, buildID)
			}
			break
		}
	}
	return s
}

func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
