package blast

import (
	"testing"
)

// Seeds must be the changeset's files. An earlier version seeded from every
// coupled file, which made each one zero-hop and therefore excluded, so the
// prediction list was always empty.
func TestExpandsFromTheChangesetNotFromEveryCoupledFile(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2

	got := analyzeSpec(t, cfg, []string{"a"},
		[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"},
	)

	if len(got.Predictions) != 1 {
		t.Fatalf("got %d predictions, want exactly 1: %+v",
			len(got.Predictions), got.Predictions)
	}
	p := got.Predictions[0]
	if p.Path != "b" {
		t.Errorf("predicted %q, want b", p.Path)
	}
	if p.Hops != 1 {
		t.Errorf("hops = %d, want 1", p.Hops)
	}
	if p.Via != "a" {
		t.Errorf("via = %q, want a", p.Via)
	}
	if p.InChangeset {
		t.Error("b was marked as part of the changeset")
	}
}

// A changeset file must never be predicted as something to look at.
func TestChangesetFilesAreNeverPredicted(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2
	got := analyzeSpec(t, cfg, []string{"a", "b"},
		[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"},
	)
	for _, p := range got.Predictions {
		if p.Path == "a" || p.Path == "b" {
			t.Errorf("changeset file %q was predicted: %+v", p.Path, p)
		}
	}
}

// An empty changeset predicts nothing rather than everything.
func TestEmptyChangesetPredictsNothing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2
	got := analyzeSpec(t, cfg, nil,
		[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"},
	)
	if len(got.Predictions) != 0 {
		t.Errorf("an empty changeset produced predictions: %+v", got.Predictions)
	}
}

// A one-hop prediction outranks a two-hop one, so "most likely first" means
// something.
func TestDirectCouplingOutranksIndirect(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2

	got := analyzeSpec(t, cfg, []string{"seed"},
		[]string{"seed", "near"}, []string{"seed", "near"}, []string{"seed", "near"},
		[]string{"near", "far"}, []string{"near", "far"}, []string{"near", "far"},
	)

	var near, far *Prediction
	for i := range got.Predictions {
		switch got.Predictions[i].Path {
		case "near":
			near = &got.Predictions[i]
		case "far":
			far = &got.Predictions[i]
		}
	}
	if near == nil || far == nil {
		t.Fatalf("missing predictions: %+v", got.Predictions)
	}
	if near.Hops >= far.Hops {
		t.Errorf("near is %d hops and far is %d; the closer file must come first",
			near.Hops, far.Hops)
	}
	if far.Via == "" {
		t.Error("an indirect prediction did not record the file it came through")
	}
}

// MaxDistance bounds the walk.
func TestMaxDistanceBoundsTheWalk(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MinSharedCommits = 2
	cfg.MaxDistance = 1

	got := analyzeSpec(t, cfg, []string{"a"},
		[]string{"a", "b"}, []string{"a", "b"}, []string{"a", "b"},
		[]string{"b", "c"}, []string{"b", "c"}, []string{"b", "c"},
	)
	for _, p := range got.Predictions {
		if p.Path == "c" {
			t.Errorf("c is two hops away but was reported at MaxDistance 1: %+v", p)
		}
	}
}
