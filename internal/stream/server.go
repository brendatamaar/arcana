package stream

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/brendatamaar/arcana/internal/orchestrator"
)

// Runner binds one authenticated sandbox to the supervised exec engine.
type Runner interface {
	Run(context.Context, orchestrator.ExecRequest, orchestrator.StreamOptions) (orchestrator.ExecResult, error)
}

// Clock schedules transport deadlines and keepalive without sleeps in tests.
type (
	Clock interface {
		NewTimer(time.Duration) orchestrator.Timer
	}
	// ErrorMapper translates domain/protocol failures at the API boundary.
	ErrorMapper func(error) (code, message string)
)

type (
	incoming struct {
		message input
		err     error
	}
	execution struct {
		result orchestrator.ExecResult
		err    error
	}
	attachmentReady struct {
		id       string
		controls orchestrator.StreamControls
		ack      chan struct{}
	}
	observer struct {
		ready chan<- attachmentReady
		queue *sendQueue
	}
)

func (o observer) Started(id string, controls orchestrator.StreamControls) {
	event := attachmentReady{id, controls, make(chan struct{})}
	select {
	case o.ready <- event:
	default:
		panic("duplicate runtime attachment")
	}
	// Serve acknowledges attachment before an accepted execution can finish,
	// preserving started-before-output even while cancellation is in progress.
	<-event.ack
}
func (o observer) Output(stderr bool, data []byte) bool { return o.queue.output(stderr, data) }

// Serve owns the connection and every reader, writer and exec until cleanup completes.
func Serve(ctx context.Context, connection *websocket.Conn, runner Runner, clock Clock, mapError ErrorMapper) {
	connCtx, stopConn := context.WithCancel(ctx)
	backendCtx, stopBackend := context.WithCancel(ctx)
	queue := newSendQueue()
	stdin := newInputBuffer(backendCtx)
	connection.SetReadLimit(maxMessageBytes)
	frames := make(chan incoming, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			kind, data, err := connection.Read(connCtx)
			var message input
			if err == nil {
				if kind != websocket.MessageText {
					err = ErrInvalid
				} else {
					message, err = parse(data)
				}
			}
			select {
			case frames <- incoming{message, err}:
			case <-connCtx.Done():
				return
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				return
			}
		}
	}()
	writerErrors := make(chan error, 1)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		if err := writeLoop(connCtx, connection, queue, clock); err != nil {
			writerErrors <- err
		}
	}()
	var resizeWorkers sync.WaitGroup
	defer func() {
		stopBackend()
		_ = stdin.Close()
		stopConn()
		_ = connection.CloseNow()
		<-readerDone
		<-writerDone
		resizeWorkers.Wait()
	}()
	startTimer := clock.NewTimer(10 * time.Second)
	defer startTimer.Stop()
	startDeadline := startTimer.Chan()
	readyEvents := make(chan attachmentReady, 1)
	var readyInput <-chan attachmentReady = readyEvents
	var completed <-chan execution
	var frameInput <-chan incoming = frames
	var writerInput <-chan error = writerErrors
	rootDone := ctx.Done()
	resizeQueue := make(chan resize, 1)
	resizeErrors := make(chan error, 1)
	var current state = waiting{}
	var result execution
	var pendingErr error
	var controls orchestrator.StreamControls

	var interpret func(input)
	interpret = func(event input) {
		var commands []command
		current, commands = step(current, event)
		if stopped, ok := current.(stopping); ok && pendingErr == nil {
			if stopped.reason == protocolStop {
				pendingErr = ErrInvalid
			}
			if stopped.reason == controlStop {
				pendingErr = ErrControl
			}
		}
		for _, command := range commands {
			switch effect := command.(type) {
			case begin:
				startTimer.Stop()
				startDeadline = nil
				done := make(chan execution, 1)
				completed = done
				options := orchestrator.StreamOptions{TTY: effect.start.tty, Cols: effect.start.cols, Rows: effect.start.rows, Stdin: stdin, Observer: observer{readyEvents, queue}}
				go func() {
					value, err := runner.Run(backendCtx, effect.start.request, options)
					done <- execution{value, err}
				}()
			case sendStarted:
				queue.control(encode(struct {
					Type string `json:"type"`
					ID   string `json:"exec_id"`
				}{"started", effect.id}), false)
			case writeInput:
				if err := stdin.write(effect.data); err != nil && !inputClosed(err) {
					pendingErr = ErrInputLimit
					interpret(invalid{})
				}
			case closeInput:
				stdin.eof()
			case resizeTerminal:
				select {
				case resizeQueue <- effect.size:
				default:
					select {
					case <-resizeQueue:
					default:
					}
					select {
					case resizeQueue <- effect.size:
					default:
					}
				}
			case cancelExecution:
				stopBackend()
			case sendExit:
				value := result.result
				queue.control(encode(struct {
					Type       string `json:"type"`
					ExitCode   int    `json:"exit_code"`
					TimedOut   bool   `json:"timed_out"`
					Truncated  bool   `json:"truncated"`
					Cancelled  bool   `json:"cancelled"`
					DurationMS int64  `json:"duration_ms"`
				}{"exit", value.ExitCode, value.TimedOut, value.Truncated, value.Cancelled, value.DurationMS}), true)
			case sendError:
				failure := pendingErr
				if failure == nil {
					failure = ErrInvalid
				}
				code, message := mapError(failure)
				queue.control(encode(struct {
					Type    string `json:"type"`
					Code    string `json:"code"`
					Message string `json:"message"`
				}{"error", code, message}), true)
			}
		}
	}
	attach := func(event attachmentReady) {
		readyInput = nil
		controls = event.controls
		resizeWorkers.Go(func() {
			for {
				select {
				case <-backendCtx.Done():
					return
				case size := <-resizeQueue:
					if backendCtx.Err() != nil {
						return
					}
					if err := controls.Resize(size.cols, size.rows); err != nil {
						select {
						case resizeErrors <- err:
						case <-backendCtx.Done():
						}
						return
					}
				}
			}
		})
		interpret(ready{event.id})
		close(event.ack)
	}
	for {
		if _, done := current.(finished); done {
			break
		}
		// Attachment acknowledgements cannot be starved by pipelined client input.
		select {
		case event := <-readyInput:
			attach(event)
			continue
		default:
		}
		select {
		case frame := <-frameInput:
			if frame.err != nil {
				if errors.Is(frame.err, ErrInvalid) {
					pendingErr = ErrInvalid
					interpret(invalid{})
				} else {
					frameInput = nil
					stopConn()
					interpret(disconnected{})
				}
			} else {
				interpret(frame.message)
			}
		case event := <-readyInput:
			attach(event)
		case result = <-completed:
			completed = nil
			// Attachment happens before Run can return; preserve started-before-exit
			// even when a short command and its ready notification race in this select.
			select {
			case event := <-readyInput:
				attach(event)
			default:
			}
			if pendingErr == nil {
				pendingErr = result.err
			}
			failed := result.err != nil && (result.err != context.Canceled || result.result.ID == "" || result.result.ExitCode < 0)
			interpret(ended{failed})
		case <-startDeadline:
			startDeadline = nil
			pendingErr = ErrInvalid
			interpret(invalid{})
		case <-rootDone:
			rootDone = nil
			stopConn()
			interpret(disconnected{})
		case <-writerInput:
			writerInput = nil
			stopConn()
			interpret(disconnected{})
		case <-resizeErrors:
			pendingErr = ErrControl
			interpret(controlFailed{})
		}
	}
	flush := clock.NewTimer(10 * time.Second)
	defer flush.Stop()
	select {
	case <-writerDone:
	case <-flush.Chan():
	case <-connCtx.Done():
	}
}

func writeLoop(ctx context.Context, connection *websocket.Conn, queue *sendQueue, clock Clock) error {
	pingTimer := clock.NewTimer(15 * time.Second)
	defer func() { pingTimer.Stop() }()
	ping := func() error {
		deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := connection.Ping(deadline)
		cancel()
		pingTimer.Stop()
		if err == nil {
			pingTimer = clock.NewTimer(15 * time.Second)
		}
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pingTimer.Chan():
			if err := ping(); err != nil {
				return err
			}
		default:
		}
		if packet, ok := queue.pop(); ok {
			deadline, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := connection.Write(deadline, websocket.MessageText, packet.data)
			cancel()
			if err != nil {
				return err
			}
			if packet.terminal {
				return connection.Close(websocket.StatusNormalClosure, "")
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-queue.wake:
		case <-pingTimer.Chan():
			if err := ping(); err != nil {
				return err
			}
		}
	}
}
