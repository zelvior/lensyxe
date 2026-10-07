package ai

import (
	"net/http"
	"testing"
)

func TestNumbersIn(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"no digits here", nil},
		{"score 82.5 of 100", []string{"82.5", "100"}},
		{"v1.2.3 build", []string{"1.2", "3"}},
		{"1,234 lines", []string{"1", "234"}},
		{"a.b.c", nil},
		{"ratio 0.25", []string{"0.25"}},
		{"trailing 5.", []string{"5"}},
		{"-3 delta", []string{"3"}},
	}
	for _, c := range cases {
		got := numbersIn(c.in)
		if len(got) != len(c.want) {
			t.Errorf("numbersIn(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("numbersIn(%q) = %v, want %v", c.in, got, c.want)
				break
			}
		}
	}
}

// Equal values must compare equal regardless of formatting, or the fabrication
// guard would reject a model that correctly restated "80" as "80.0".
func TestNumberNormalization(t *testing.T) {
	facts := map[string]bool{"80": true, "2": true, "0": true}

	for _, text := range []string{
		"the score is 80",
		"the score is 80.0",
		"the score is +80",
	} {
		if extra := unsupportedNumbers(text, facts); len(extra) > 0 {
			t.Errorf("%q should be accepted, rejected %v", text, extra)
		}
	}

	if extra := unsupportedNumbers("the score is 81", facts); len(extra) != 1 || extra[0] != "81" {
		t.Errorf("a genuinely different figure must be rejected, got %v", extra)
	}
}

// Small integers appear in ordinary prose. Rejecting them would make the guard
// useless, because every legitimate summary contains some.
func TestSmallIntegersAreNotTreatedAsMeasurements(t *testing.T) {
	facts := map[string]bool{}
	for _, text := range []string{
		"two risks were found in one package",
		"three components, one of which is weakest",
		"a single team owns it",
	} {
		if extra := unsupportedNumbers(text, facts); len(extra) > 0 {
			t.Errorf("%q should be accepted with no facts supplied, got %v", text, extra)
		}
	}
}

func TestUnsupportedNumbersReportsSortedUnique(t *testing.T) {
	got := unsupportedNumbers("about 95 and 42 and 95 and 7", map[string]bool{})
	if len(got) != 2 || got[0] != "42" || got[1] != "95" {
		t.Errorf("got %v, want [42 95]", got)
	}
}

// -------------------------------------------------------------- Explain

// responder is a fake provider endpoint.
type responder struct {
	t        *testing.T
	status   int
	body     string
	seenPath string
	seenBody []byte
	seenHead http.Header
}
