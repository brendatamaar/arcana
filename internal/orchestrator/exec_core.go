package orchestrator

import "time"

//sumtype:decl
type execState interface{ execState() }

type (
	execRunning      struct{}
	execCancelling   struct{}
	execAwaitCancel  struct{}
	execAwaitCommand struct{}
	execCompleted    struct{}
	execFailed       struct{ reason string }
)

func (execRunning) execState()      {}
func (execCancelling) execState()   {}
func (execAwaitCancel) execState()  {}
func (execAwaitCommand) execState() {}
func (execCompleted) execState()    {}
func (execFailed) execState()       {}

//sumtype:decl
type execInput interface{ execInput() }

type (
	commandCompleted      struct{}
	cancellationCompleted struct{}
	clientCancelled       struct{}
	helperFailed          struct{}
	hardDeadline          struct{}
)

func (commandCompleted) execInput()      {}
func (cancellationCompleted) execInput() {}
func (clientCancelled) execInput()       {}
func (helperFailed) execInput()          {}
func (hardDeadline) execInput()          {}

//sumtype:decl
type execCommand interface{ execCommand() }

type (
	cancelHelper           struct{}
	scheduleCancelDeadline struct{ after time.Duration }
	destroyExecSandbox     struct{ reason string }
	persistExec            struct{}
)

func (cancelHelper) execCommand()           {}
func (scheduleCancelDeadline) execCommand() {}
func (destroyExecSandbox) execCommand()     {}
func (persistExec) execCommand()            {}

func execStep(state execState, input execInput) (execState, []execCommand) {
	switch state.(type) {
	case execCompleted, execFailed:
		return state, nil
	case execRunning, execCancelling, execAwaitCancel, execAwaitCommand:
	}
	switch input.(type) {
	case helperFailed:
		return execFailed{"exec_uncertain"}, []execCommand{destroyExecSandbox{"exec_uncertain"}}
	case hardDeadline:
		return execFailed{"exec_deadline"}, []execCommand{destroyExecSandbox{"exec_deadline"}}
	case commandCompleted, cancellationCompleted, clientCancelled:
	}
	switch state.(type) {
	case execRunning:
		switch input.(type) {
		case commandCompleted:
			return execCompleted{}, []execCommand{persistExec{}}
		case clientCancelled:
			return execCancelling{}, []execCommand{cancelHelper{}, scheduleCancelDeadline{10 * time.Second}}
		case cancellationCompleted, helperFailed, hardDeadline:
		}
	case execCancelling:
		switch input.(type) {
		case commandCompleted:
			return execAwaitCancel{}, nil
		case cancellationCompleted:
			return execAwaitCommand{}, nil
		case clientCancelled, helperFailed, hardDeadline:
		}
	case execAwaitCancel:
		switch input.(type) {
		case cancellationCompleted:
			return execCompleted{}, []execCommand{persistExec{}}
		case commandCompleted, clientCancelled, helperFailed, hardDeadline:
		}
	case execAwaitCommand:
		switch input.(type) {
		case commandCompleted:
			return execCompleted{}, []execCommand{persistExec{}}
		case cancellationCompleted, clientCancelled, helperFailed, hardDeadline:
		}
	case execCompleted, execFailed:
	}
	return state, nil
}
