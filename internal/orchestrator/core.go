// Package orchestrator coordinates persistent sandbox lifecycles and runtime effects.
package orchestrator

// Phase is a persisted sandbox lifecycle stage.
type Phase string

// Lifecycle phases follow the persisted PRD state machine.
const (
	Creating   Phase = "creating"
	Running    Phase = "running"
	Destroying Phase = "destroying"
	Destroyed  Phase = "destroyed"
	Failed     Phase = "failed"
	Expired    Phase = "expired"
)

// Valid reports whether a stage is part of the lifecycle contract.
func (s Phase) Valid() bool {
	switch s {
	case Creating, Running, Destroying, Destroyed, Failed, Expired:
		return true
	}
	return false
}

// Event identifies an observed lifecycle result or requested operation.
type Event uint8

// Lifecycle events are interpreted only from their matching current phase.
const (
	Created Event = iota
	CreateFailed
	DestroyRequested
	DestroyedInstance
	ExpiredInstance
	LostInstance
)

// PersistTransition requests atomic state and history persistence.
type PersistTransition struct{ From, To Phase }

// Step decides a lifecycle transition without performing effects.
// No commands means a duplicate, late or invalid operation.
func Step(state Phase, event Event) (Phase, []PersistTransition) {
	switch state {
	case Creating:
		switch event {
		case Created:
			return Running, []PersistTransition{{From: state, To: Running}}
		case CreateFailed, LostInstance:
			return Failed, []PersistTransition{{From: state, To: Failed}}
		}
	case Running:
		switch event {
		case DestroyRequested:
			return Destroying, []PersistTransition{{From: state, To: Destroying}}
		case LostInstance:
			return Failed, []PersistTransition{{From: state, To: Failed}}
		}
	case Destroying:
		switch event {
		case DestroyedInstance:
			return Destroyed, []PersistTransition{{From: state, To: Destroyed}}
		case ExpiredInstance:
			return Expired, []PersistTransition{{From: state, To: Expired}}
		case LostInstance:
			return Failed, []PersistTransition{{From: state, To: Failed}}
		}
	case Destroyed, Failed, Expired:
	}
	return state, nil
}

func terminal(state Phase) bool { return state == Destroyed || state == Failed || state == Expired }
