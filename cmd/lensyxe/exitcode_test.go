package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, 0},
		{"ordinary failure", errors.New("boom"), 1},
		{"gate failure", fmt.Errorf("%w: 1 threshold breached", errGateFailed), exitGateFailure},
		{"wrapped gate failure", fmt.Errorf("context: %w", errGateFailed), exitGateFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestGateFailureExitCodeIsDistinct(t *testing.T) {
	// CI must be able to tell a policy rejection from a crash.
	if exitGateFailure == 1 {
		t.Error("the gate exit code must differ from the generic failure code")
	}
}
