package blast

import (
	"fmt"
	"testing"
)

// analyzeSpec runs the whole computation against a hand-built commit set.
//
// Testing through this rather than through git means the coupling arithmetic
// can be checked against cases whose answers are known by hand, with no
// repository, no clock, and no subprocess involved.
func analyzeSpec(t *testing.T, cfg Config, changeset []string, specs ...[]string) Result {
	t.Helper()
	return analyze("/repo", cfg, commits(specs...), changeset)
}

// commits builds a synthetic commit set.
func commits(specs ...[]string) []commitFiles {
	out := make([]commitFiles, len(specs))
	for i, files := range specs {
		out[i] = commitFiles{sha: fmt.Sprintf("sha%d", i), files: files}
	}
	return out
}

func findPair(got Result, a, b string) *Pair {
	for i := range got.Pairs {
		p := got.Pairs[i]
		if (p.A == a && p.B == b) || (p.A == b && p.B == a) {
			return &got.Pairs[i]
		}
	}
	return nil
}

func TestCouplingIsSharedOverTheRarerFile(t *testing.T) {
	// a and b always change together; c changes on its own twice.
	got := analyzeSpec(t, DefaultConfig(), []string{"a"},
		[]string{"a", "b"},
		[]string{"a", "b"},
		[]string{"a", "b"},
		[]string{"c"},
		[]string{"c"},
	)

	ab := findPair(got, "a", "b")
	if ab == nil {
		t.Fatalf("the a/b pair was not reported: %+v", got.Pairs)
	}
	if ab.SharedCommits != 3 {
		t.Errorf("shared_commits = %d, want 3", ab.SharedCommits)
	}
	if ab.CommitsA != 3 || ab.CommitsB != 3 {
		t.Errorf("per-file counts = %d/%d, want 3/3", ab.CommitsA, ab.CommitsB)
	}
	if ab.Coupling != 1 {
		t.Errorf("coupling = %v, want 1", ab.Coupling)
	}

	// Files sharing no commits must not be coupled.
	for _, name := range []string{"a", "b"} {
		if p := findPair(got, name, "c"); p != nil {
			t.Errorf("%s and c share no commits but were coupled: %+v", name, p)
		}
	}
}

// The rare-file denominator is the point: if one file changes every commit and
// the other changes once, they share one commit, which is 1.0 coupling.
func TestCouplingNormalisesByTheRarerFile(t *testing.T) {
	cfg := DefaultConfig()
	// The evidence floor is lowered so a single shared commit is admitted; this
	// test is about the denominator, not the sample size.
	cfg.MinSharedCommits = 1

	got := analyzeSpec(t, cfg, []string{"common"},
		[]string{"common", "rare"},
		[]string{"common"},
		[]string{"common"},
		[]string{"common"},
	)

	p := findPair(got, "common", "rare")
	if p == nil {
		t.Fatalf("the pair was not reported: %+v", got.Pairs)
	}
	if p.Coupling != 1 {
		t.Errorf("coupling = %v, want 1: every time rare changed, common did too", p.Coupling)
	}
	if p.CommitsA != 4 || p.CommitsB != 1 {
		t.Errorf("counts = %d/%d, want 4/1", p.CommitsA, p.CommitsB)
	}
}

// The floor on shared commits is what stops a young repository from reporting
// every pair as perfectly coupled.
func TestPairsBelowTheEvidenceFloorAreDropped(t *testing.T) {
	got := analyzeSpec(t, DefaultConfig(), []string{"a"},
		[]string{"a", "b"},
		[]string{"c", "d"}, // only one shared commit
	)
	if len(got.Pairs) != 0 {
		t.Errorf("a pair seen in one commit was reported: %+v", got.Pairs)
	}
}

// Weak coupling is dropped rather than reported with a caveat.
//
// The denominator is min(commitsA, commitsB), so coupling only falls below 1
// when the pair misses at least one commit that the rarer file was in. Here y
// changes 4 times, only 2 of them alongside x, so coupling is 2/4 = 0.5.
func TestWeakCouplingIsDropped(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2
	cfg.CouplingThreshold = 0.9

	got := analyzeSpec(t, cfg, []string{"x"},
		[]string{"x", "y"}, // 1 shared
		[]string{"x", "y"}, // 2 shared
		[]string{"y"},      // y alone
		[]string{"y"},      // y alone
		[]string{"x"},      // x alone
	)
	if p := findPair(got, "x", "y"); p != nil {
		t.Errorf("coupling %v was below the threshold but reported: %+v", p.Coupling, p)
	}
}

// The min-denominator means full coupling whenever the rarer file never changes
// alone. This is the intended behaviour, not an off-by-one, and it is what makes
// the measure readable: 1.0 means "every time this file changed, the other one
// did too".
func TestRarerFileNeverChangingAloneGivesFullCoupling(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 1

	got := analyzeSpec(t, cfg, []string{"x"},
		[]string{"x", "y"},
		[]string{"x"},
		[]string{"x"},
		[]string{"x", "y"},
	)
	p := findPair(got, "x", "y")
	if p == nil {
		t.Fatalf("pair not reported: %+v", got.Pairs)
	}
	if p.CommitsB != 2 {
		t.Fatalf("y changed %d times, want 2", p.CommitsB)
	}
	if p.Coupling != 1 {
		t.Errorf("coupling = %v, want 1", p.Coupling)
	}
}

// A file listed twice in one commit counts once.
func TestDuplicatePathsInACommitCountOnce(t *testing.T) {
	got := analyzeSpec(t, DefaultConfig(), []string{"a"},
		[]string{"a", "b", "b"},
		[]string{"a", "b"},
		[]string{"a", "b"},
	)
	for _, p := range got.Pairs {
		if p.A == p.B {
			t.Errorf("a file was paired with itself: %+v", p)
		}
	}
	if p := findPair(got, "a", "b"); p == nil || p.SharedCommits != 3 {
		t.Errorf("shared_commits = %+v, want 3", p)
	}
}

// Coupling must stay within 0..1 or a score above 100 would be meaningless.
func TestCouplingIsClampedToOne(t *testing.T) {
	got := analyzeSpec(t, DefaultConfig(), []string{"a"},
		[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"},
	)
	for _, p := range got.Pairs {
		if p.Coupling < 0 || p.Coupling > 1 {
			t.Errorf("coupling %v is out of range: %+v", p.Coupling, p)
		}
	}
}

// Deterministic output: identical history must give identical bytes, or the
// tool cannot be diffed between runs.
func TestOutputIsDeterministic(t *testing.T) {
	specs := [][]string{
		{"a", "b"}, {"b", "c"}, {"a", "c"}, {"d", "e"}, {"a", "b", "c"},
	}
	first := formatResult(analyzeSpec(t, DefaultConfig(), []string{"a"}, specs...))
	for i := 0; i < 20; i++ {
		again := formatResult(analyzeSpec(t, DefaultConfig(), []string{"a"}, specs...))
		if again != first {
			t.Fatalf("run %d differs:\n%s\n---\n%s", i, first, again)
		}
	}
}
