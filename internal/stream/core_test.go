package stream

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

func FuzzStep_StartedPrecedesExitAndTerminalStatesStayTerminal(f *testing.F) {
	f.Add([]byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{0, 5})
	f.Add([]byte{2})
	f.Add([]byte{0, 7, 1, 5})
	f.Add([]byte{0, 1, 5, 0})
	f.Fuzz(func(t *testing.T, events []byte) {
		if len(events) > 1000 {
			t.Skip()
		}
		var current state = waiting{}
		began, started, terminal, inputClosed := false, false, false, false
		for _, value := range events {
			var event input
			switch value % 10 {
			case 0:
				event = start{tty: true, cols: 80, rows: 24}
			case 1:
				event = ready{"exe_test"}
			case 2:
				event = inputData{[]byte("input")}
			case 3:
				event = inputClose{}
			case 4:
				event = resize{100, 40}
			case 5:
				event = ended{}
			case 6:
				event = ended{failed: true}
			case 7:
				event = cancel{}
			case 8:
				event = invalid{}
			case 9:
				event = disconnected{}
			}
			next, commands := step(current, event)
			if terminal && len(commands) > 0 {
				t.Fatal("terminal state produced effects")
			}
			for _, command := range commands {
				switch command.(type) {
				case begin:
					if began {
						t.Fatal("duplicate execution")
					}
					began = true
				case sendStarted:
					if !began || started {
						t.Fatal("invalid started sequence")
					}
					started = true
				case sendExit:
					if !started {
						t.Fatal("exit before started")
					}
					terminal = true
				case sendError:
					terminal = true
				case writeInput:
					if !began || inputClosed {
						t.Fatal("stdin outside open execution")
					}
				case closeInput:
					inputClosed = true
				case resizeTerminal:
					if !started {
						t.Fatal("resize before attachment")
					}
				case cancelExecution:
					if !began {
						t.Fatal("cancel without execution")
					}
				}
			}
			if _, done := next.(finished); done {
				terminal = true
			}
			current = next
		}
	})
}

func FuzzParse_BoundedFramesHaveKnownShapes(f *testing.F) {
	for _, value := range []string{`{"type":"start","cmd":["echo"]}`, `{"type":"stdin","data":"AP8="}`, `{"type":"resize","cols":80,"rows":24}`, `{"type":"cancel"}`} {
		f.Add([]byte(value))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 70000 {
			t.Skip()
		}
		message, err := parse(data)
		if err != nil {
			return
		}
		if len(data) > 65536 || !json.Valid(data) {
			t.Fatal("unbounded or invalid JSON accepted")
		}
		switch value := message.(type) {
		case start:
			if value.cols == 0 || value.rows == 0 {
				t.Fatal("invalid initial size")
			}
		case resize:
			if value.cols == 0 || value.rows == 0 {
				t.Fatal("invalid resize")
			}
		case inputData:
			if len(value.data) > 65536 {
				t.Fatal("oversized stdin")
			}
		case inputClose, cancel:
		case ready, ended, disconnected, invalid, controlFailed:
			t.Fatal("server event accepted from client")
		}
	})
}

func TestProtocol_RejectsUnknownFieldsAndMalformedFrames(t *testing.T) {
	for _, value := range []string{`null`, `[]`, `{}`, `{"type":"unknown"}`, `{"type":"stdin"}`, `{"type":"stdin","data":"%%%"}`, `{"type":"stdin_close","data":""}`, `{"type":"cancel","cmd":["x"]}`, `{"type":"start","cmd":["x"],"extra":true}`, `{"type":"start","cols":0}`, `{"type":"resize","cols":65536,"rows":1}`, `{"type":"resize","cols":1}`, `{"type":"cancel"}{}`} {
		if _, err := parse([]byte(value)); err == nil {
			t.Fatal("invalid frame accepted", value)
		}
	}
}

func TestBuffers_StayBoundedAndCloseInputWithoutLeaking(t *testing.T) {
	queue := newSendQueue()
	queue.control(encode(struct {
		Type string `json:"type"`
	}{"started"}), false)
	dropped := false
	data := make([]byte, 16384)
	for range 1000 {
		if !queue.output(false, data) {
			dropped = true
		}
	}
	if !dropped || queue.bytes > 1048576 || len(queue.packets) > 256 {
		t.Fatal("send queue is unbounded")
	}
	queue.control(encode(struct {
		Type string `json:"type"`
	}{"exit"}), true)
	count := 0
	for {
		_, ok := queue.pop()
		if !ok {
			break
		}
		count++
	}
	if count == 0 || queue.bytes != 0 {
		t.Fatal("queue failed to drain")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	buffer := newInputBuffer(ctx)
	for range 16 {
		if err := buffer.write([]byte("data")); err != nil {
			t.Fatal(err)
		}
	}
	if err := buffer.write([]byte("overflow")); err != ErrInputLimit {
		t.Fatal("stdin queue unbounded", err)
	}
	buffer.eof()
	received, err := io.ReadAll(buffer)
	if err != nil || len(received) != 64 {
		t.Fatal("stdin close lost queued input", err)
	}
	blocked := newInputBuffer(ctx)
	done := make(chan error, 1)
	go func() { _, err := blocked.Read(make([]byte, 1)); done <- err }()
	if err = blocked.Close(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != io.ErrClosedPipe {
		t.Fatal("stdin reader did not stop", err)
	}
}
