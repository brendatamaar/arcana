// Command sbx-exec supervises isolated command lifetimes.
package main

import "time"

type stopReason uint8

const (
	normalExit stopReason = iota
	cancelledExit
	timeoutExit
)

type exitData struct {
	Code   int
	Signal string
}
type outcome struct {
	Exit   exitData
	Reason stopReason
}

//sumtype:decl
type State interface{ isState() }

// Running awaits the main process exit or a stop request.
type Running struct{}

// Stopping awaits the main process after a bounded stop request.
type Stopping struct{ Reason stopReason }

// Draining requires proof that every adopted descendant has gone.
type Draining struct{ Outcome outcome }

// Finished is terminal and carries a result safe to persist.
type Finished struct{ Outcome outcome }

func (Running) isState()  {}
func (Stopping) isState() {}
func (Draining) isState() {}
func (Finished) isState() {}

//sumtype:decl
type Input interface{ isInput() }

// MainExited is observed by the shell's single child reaper.
type MainExited struct{ Exit exitData }

// DeadlineExpired is supplied by the deadline timer.
type DeadlineExpired struct{}

// CancellationRequested supplies a bounded grace already validated at the boundary.
type CancellationRequested struct{ Grace time.Duration }

// GraceExpired requests forced termination.
type GraceExpired struct{}

// TreeEmpty is a kernel ECHILD proof after reaping all adopted children.
type TreeEmpty struct{}

// TreePresent requests another bounded cleanup pass.
type TreePresent struct{}

func (MainExited) isInput()            {}
func (DeadlineExpired) isInput()       {}
func (CancellationRequested) isInput() {}
func (GraceExpired) isInput()          {}
func (TreeEmpty) isInput()             {}
func (TreePresent) isInput()           {}

type treeSignal uint8

const (
	terminateTree treeSignal = iota
	killTree
)

//sumtype:decl
type Command interface{ isCommand() }

// SignalTree describes pidfd-based signalling of proven descendants.
type SignalTree struct{ Signal treeSignal }

// ScheduleGrace asks the shell to deliver GraceExpired later.
type ScheduleGrace struct{ After time.Duration }

// PersistOutcome is emitted only after the kernel proves no children remain.
type PersistOutcome struct{ Outcome outcome }

func (SignalTree) isCommand()     {}
func (ScheduleGrace) isCommand()  {}
func (PersistOutcome) isCommand() {}

// Step makes helper lifecycle decisions without effects or clock reads.
func Step(state State, input Input) (State, []Command) {
	switch s := state.(type) {
	case Running:
		switch event := input.(type) {
		case MainExited:
			return Draining{Outcome: outcome{Exit: event.Exit, Reason: normalExit}}, []Command{SignalTree{killTree}}
		case DeadlineExpired:
			return Stopping{Reason: timeoutExit}, []Command{SignalTree{killTree}}
		case CancellationRequested:
			if event.Grace == 0 {
				return Stopping{Reason: cancelledExit}, []Command{SignalTree{killTree}}
			}
			return Stopping{Reason: cancelledExit}, []Command{SignalTree{terminateTree}, ScheduleGrace{event.Grace}}
		case GraceExpired, TreeEmpty, TreePresent:
		}
	case Stopping:
		switch event := input.(type) {
		case MainExited:
			return Draining{Outcome: outcome{Exit: event.Exit, Reason: s.Reason}}, []Command{SignalTree{killTree}}
		case DeadlineExpired:
			return Stopping{Reason: timeoutExit}, []Command{SignalTree{killTree}}
		case GraceExpired:
			return s, []Command{SignalTree{killTree}}
		case CancellationRequested, TreeEmpty, TreePresent:
		}
	case Draining:
		switch input.(type) {
		case TreeEmpty:
			return Finished(s), []Command{PersistOutcome(s)}
		case TreePresent, GraceExpired:
			return s, []Command{SignalTree{killTree}}
		case DeadlineExpired:
			s.Outcome.Reason = timeoutExit
			return s, []Command{SignalTree{killTree}}
		case CancellationRequested:
			if s.Outcome.Reason == normalExit {
				s.Outcome.Reason = cancelledExit
			}
			return s, []Command{SignalTree{killTree}}
		case MainExited:
		}
	case Finished:
	}
	return state, nil
}

// Run folds an input history for deterministic replay and property testing.
func Run(inputs []Input) State {
	var state State = Running{}
	for _, input := range inputs {
		state, _ = Step(state, input)
	}
	return state
}
