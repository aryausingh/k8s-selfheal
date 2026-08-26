package safety

import "testing"

func TestStateMachineRecoveryPath(t *testing.T) {
	machine := NewStateMachine()
	path := []State{
		StateSnapshotted,
		StateRemediating,
		StateVerifying,
		StateRecovered,
		StateLogged,
	}

	for _, next := range path {
		if err := machine.Transition(next); err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}
	if machine.Current() != StateLogged {
		t.Fatalf("current state = %s, want %s", machine.Current(), StateLogged)
	}
}

func TestStateMachineRollbackPath(t *testing.T) {
	machine := NewStateMachine()
	path := []State{
		StateSnapshotted,
		StateRemediating,
		StateVerifying,
		StateRollingBack,
		StateRolledBack,
		StateLogged,
	}

	for _, next := range path {
		if err := machine.Transition(next); err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}
}

func TestStateMachineRejectsIllegalTransition(t *testing.T) {
	machine := NewStateMachine()
	if err := machine.Transition(StateRecovered); err == nil {
		t.Fatal("illegal transition unexpectedly succeeded")
	}
	if machine.Current() != StateDetected {
		t.Fatalf("state changed after rejected transition: %s", machine.Current())
	}
}
