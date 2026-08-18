package controller

import (
	"errors"
	"testing"
)

func TestShouldEscalate_SafeAndValid_DoesNotEscalate(t *testing.T) {
	if got := ShouldEscalate(true, nil); got {
		t.Errorf("ShouldEscalate(true, nil) = %v, want false", got)
	}
}

func TestShouldEscalate_ExplicitFalse_Escalates(t *testing.T) {
	// A valid proposal that says "not safe to automate" — a real
	// classification result, not a missing/malformed one — must still
	// escalate.
	if got := ShouldEscalate(false, nil); !got {
		t.Errorf("ShouldEscalate(false, nil) = %v, want true", got)
	}
}

func TestShouldEscalate_ClassifyError_EscalatesRegardlessOfBool(t *testing.T) {
	// A malformed/invalid response is rejected by Subhashini's validator
	// before it becomes a Proposal, so it reaches this layer as an error,
	// not as a zero-value bool. Must escalate even if the (meaningless)
	// bool value happens to be true.
	err := errors.New("classifier: malformed response")

	if got := ShouldEscalate(true, err); !got {
		t.Errorf("ShouldEscalate(true, err) = %v, want true", got)
	}
	if got := ShouldEscalate(false, err); !got {
		t.Errorf("ShouldEscalate(false, err) = %v, want true", got)
	}
}
