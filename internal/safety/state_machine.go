package safety

import "fmt"

// State is one of the frozen Owner 2 lifecycle states.
type State string

const (
	StateDetected    State = "DETECTED"
	StateSnapshotted State = "SNAPSHOTTED"
	StateRemediating State = "REMEDIATING"
	StateVerifying   State = "VERIFYING"
	StateRecovered   State = "RECOVERED"
	StateRollingBack State = "ROLLING_BACK"
	StateRolledBack  State = "ROLLED_BACK"
	StateLogged      State = "LOGGED"
)

var allowedTransitions = map[State]map[State]struct{}{
	StateDetected: {
		StateSnapshotted: {},
	},
	StateSnapshotted: {
		StateRemediating: {},
	},
	StateRemediating: {
		StateVerifying: {},
	},
	StateVerifying: {
		StateRecovered:   {},
		StateRollingBack: {},
	},
	StateRecovered: {
		StateLogged: {},
	},
	StateRollingBack: {
		StateRolledBack: {},
	},
	StateRolledBack: {
		StateLogged: {},
	},
}

// StateMachine rejects transitions outside the frozen lifecycle.
type StateMachine struct {
	current State
}

func NewStateMachine() *StateMachine {
	return &StateMachine{current: StateDetected}
}

func (m *StateMachine) Current() State {
	return m.current
}

func (m *StateMachine) Transition(next State) error {
	if _, ok := allowedTransitions[m.current][next]; !ok {
		return fmt.Errorf("illegal lifecycle transition %s -> %s", m.current, next)
	}
	m.current = next
	return nil
}
