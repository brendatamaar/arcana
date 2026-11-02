package stream

type inputState uint8

const (
	openInput inputState = iota
	closedInput
)

type attachment uint8

const (
	awaitingAttachment attachment = iota
	attached
)

type stopReason uint8

const (
	requestedStop stopReason = iota
	protocolStop
	connectionStop
	controlStop
)

type settings struct {
	tty              bool
	input            inputState
	initial, current resize
}

//sumtype:decl
type state interface{ streamState() }

type (
	waiting  struct{}
	starting struct{ settings settings }
	running  struct{ settings settings }
	stopping struct {
		reason     stopReason
		attachment attachment
	}
	finished struct{}
)

func (waiting) streamState()  {}
func (starting) streamState() {}
func (running) streamState()  {}
func (stopping) streamState() {}
func (finished) streamState() {}

//sumtype:decl
type command interface{ streamCommand() }

type (
	begin           struct{ start start }
	sendStarted     struct{ id string }
	writeInput      struct{ data []byte }
	closeInput      struct{}
	resizeTerminal  struct{ size resize }
	cancelExecution struct{}
	sendExit        struct{}
	sendError       struct{}
)

func (begin) streamCommand()           {}
func (sendStarted) streamCommand()     {}
func (writeInput) streamCommand()      {}
func (closeInput) streamCommand()      {}
func (resizeTerminal) streamCommand()  {}
func (cancelExecution) streamCommand() {}
func (sendExit) streamCommand()        {}
func (sendError) streamCommand()       {}

func step(current state, event input) (state, []command) {
	switch s := current.(type) {
	case finished:
		return s, nil
	case waiting:
		switch e := event.(type) {
		case start:
			size := resize{e.cols, e.rows}
			return starting{settings{tty: e.tty, initial: size, current: size}}, []command{begin{e}}
		case disconnected:
			return finished{}, nil
		case inputData, inputClose, resize, cancel, ready, ended, invalid, controlFailed:
			return finished{}, []command{sendError{}}
		}
	case starting:
		switch e := event.(type) {
		case ready:
			commands := []command{sendStarted(e)}
			if s.settings.tty && s.settings.current != s.settings.initial {
				commands = append(commands, resizeTerminal{s.settings.current})
			}
			return running(s), commands
		case ended:
			return finished{}, []command{sendError{}}
		case start, inputData, inputClose, resize, cancel, disconnected, invalid, controlFailed:
			next, commands, stop := live(s.settings, event, awaitingAttachment)
			if stop != nil {
				return *stop, commands
			}
			return starting{next}, commands
		}
	case running:
		switch e := event.(type) {
		case ended:
			if e.failed {
				return finished{}, []command{sendError{}}
			}
			return finished{}, []command{sendExit{}}
		case ready:
			return s, nil
		case start, inputData, inputClose, resize, cancel, disconnected, invalid, controlFailed:
			next, commands, stop := live(s.settings, event, attached)
			if stop != nil {
				return *stop, commands
			}
			return running{next}, commands
		}
	case stopping:
		switch e := event.(type) {
		case ready:
			if s.attachment == awaitingAttachment {
				s.attachment = attached
				if s.reason != connectionStop {
					return s, []command{sendStarted(e)}
				}
			}
		case ended:
			if s.reason == connectionStop {
				return finished{}, nil
			}
			if s.reason == protocolStop || s.reason == controlStop || e.failed || s.attachment == awaitingAttachment {
				return finished{}, []command{sendError{}}
			}
			return finished{}, []command{sendExit{}}
		case disconnected:
			s.reason = connectionStop
		case start, inputData, inputClose, resize, cancel, invalid, controlFailed:
		}
		return s, nil
	}
	return current, nil
}

func live(s settings, event input, stage attachment) (settings, []command, *stopping) {
	reason := protocolStop
	switch e := event.(type) {
	case inputData:
		if s.input == openInput {
			return s, []command{writeInput(e)}, nil
		}
	case inputClose:
		if s.input == openInput {
			s.input = closedInput
			return s, []command{closeInput{}}, nil
		}
		return s, nil, nil
	case resize:
		if s.tty {
			s.current = e
			if stage == attached {
				return s, []command{resizeTerminal{e}}, nil
			}
			return s, nil, nil
		}
	case cancel:
		reason = requestedStop
	case disconnected:
		reason = connectionStop
	case controlFailed:
		reason = controlStop
	case start, invalid:
	case ready, ended:
		return s, nil, nil
	}
	stop := &stopping{reason, stage}
	return s, []command{cancelExecution{}}, stop
}
