package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// StreamRequest adds terminal settings to the normal exec options.
type (
	StreamRequest struct {
		ExecRequest
		TTY  bool   `json:"tty,omitempty"`
		Cols uint16 `json:"cols,omitempty"`
		Rows uint16 `json:"rows,omitempty"`
	}
	// StreamResult is the terminal result, including cancellation intent.
	StreamResult struct {
		ExitCode   int   `json:"exit_code"`
		TimedOut   bool  `json:"timed_out"`
		Truncated  bool  `json:"truncated"`
		Cancelled  bool  `json:"cancelled"`
		DurationMS int64 `json:"duration_ms"`
	}
)

// Stream owns a command connection. Drain Stdout and Stderr concurrently before
// Wait; closing or canceling the context releases blocked pipe writes. Close
// always releases local resources and disconnects, which cancels the remote exec.
type Stream struct {
	ID                   string
	Stdout               io.Reader
	Stderr               io.Reader
	conn                 *websocket.Conn
	ctx                  context.Context
	cancel               context.CancelFunc
	out, errOut          *io.PipeWriter
	outReader, errReader *io.PipeReader
	done                 chan struct{}
	result               StreamResult
	err                  error
	inputMu              sync.Mutex
	inputClosed          bool
}

// ExecStream starts a command and returns after the server acknowledges its ID.
// The supplied context must remain alive until Wait or Close completes.
func (c *Client) ExecStream(ctx context.Context, id string, in StreamRequest) (*Stream, error) {
	path, err := resource(id)
	if err != nil {
		return nil, err
	}
	address := c.endpoint(path+"/exec/stream", nil)
	address = strings.Replace(address, "https:", "wss:", 1)
	address = strings.Replace(address, "http:", "ws:", 1)
	header := http.Header{}
	if c.key != "" {
		header.Set("Authorization", "Bearer "+c.key)
	}
	conn, resp, err := websocket.Dial(ctx, address, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: header})
	if err != nil {
		if resp != nil && resp.Body != nil {
			defer func() { _ = resp.Body.Close() }()
			return nil, decodeError(resp)
		}
		return nil, fmt.Errorf("dialing exec stream: %w", err)
	}
	conn.SetReadLimit(65536)
	owned, cancel := context.WithCancel(ctx)
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	s := &Stream{Stdout: outR, Stderr: errR, conn: conn, ctx: owned, cancel: cancel, out: outW, errOut: errW, outReader: outR, errReader: errR, done: make(chan struct{})}
	stop := context.AfterFunc(owned, func() {
		_ = conn.CloseNow()
		_ = outW.CloseWithError(owned.Err())
		_ = errW.CloseWithError(owned.Err())
	})
	start := struct {
		Type string `json:"type"`
		StreamRequest
	}{"start", in}
	if err = s.send(ctx, start); err != nil {
		stop()
		cancel()
		_ = conn.CloseNow()
		_ = outR.Close()
		_ = errR.Close()
		return nil, err
	}
	ready := make(chan error, 1)
	go func() {
		defer close(s.done)
		defer stop()
		defer cancel()
		defer func() { _ = conn.CloseNow() }()
		s.err = s.receive(ready)
		_ = outW.CloseWithError(s.err)
		_ = errW.CloseWithError(s.err)
	}()
	select {
	case err = <-ready:
		if err != nil {
			_ = s.Close()
			return nil, err
		}
		return s, nil
	case <-ctx.Done():
		_ = s.Close()
		return nil, ctx.Err()
	}
}

func (s *Stream) send(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > 65536 {
		return ErrLimit
	}
	return s.conn.Write(ctx, websocket.MessageText, data)
}

func (s *Stream) receive(ready chan<- error) (failure error) {
	started := false
	defer func() {
		if !started {
			ready <- failure
		}
	}()
	for {
		kind, data, err := s.conn.Read(s.ctx)
		if err != nil {
			return fmt.Errorf("reading exec stream before exit: %w", err)
		}
		if kind != websocket.MessageText {
			return errors.New("non-text exec message")
		}
		var packet struct {
			Type    string `json:"type"`
			ExecID  string `json:"exec_id"`
			Data    []byte `json:"data"`
			Code    string `json:"code"`
			Message string `json:"message"`
			*StreamResult
		}
		packet.StreamResult = &StreamResult{}
		if err = json.Unmarshal(data, &packet); err != nil {
			return fmt.Errorf("decoding exec message: %w", err)
		}
		switch packet.Type {
		case "started":
			if started || packet.ExecID == "" {
				return errors.New("invalid started message")
			}
			s.ID = packet.ExecID
			started = true
			ready <- nil
		case "stdout", "stderr":
			if !started {
				return errors.New("output before started")
			}
			writer := s.out
			if packet.Type == "stderr" {
				writer = s.errOut
			}
			if _, err = writer.Write(packet.Data); err != nil {
				return err
			}
		case "exit":
			if !started {
				return errors.New("exit before started")
			}
			var terminal struct {
				ExitCode   *int   `json:"exit_code"`
				TimedOut   *bool  `json:"timed_out"`
				Truncated  *bool  `json:"truncated"`
				Cancelled  *bool  `json:"cancelled"`
				DurationMS *int64 `json:"duration_ms"`
			}
			if err = json.Unmarshal(data, &terminal); err != nil || terminal.ExitCode == nil || terminal.TimedOut == nil || terminal.Truncated == nil || terminal.Cancelled == nil || terminal.DurationMS == nil || *terminal.ExitCode < 0 || *terminal.ExitCode > 255 || *terminal.DurationMS < 0 {
				return errors.New("invalid terminal exec result")
			}
			s.result = *packet.StreamResult
			return nil
		case "error":
			return &Error{Code: packet.Code, Message: packet.Message}
		default:
			return errors.New("unknown exec message")
		}
	}
}

// Write sends binary-safe stdin in bounded frames. It serializes with CloseStdin.
func (s *Stream) Write(data []byte) (int, error) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	if s.inputClosed {
		return 0, io.ErrClosedPipe
	}
	sent := 0
	for len(data) > 0 {
		n := min(len(data), 16384)
		err := s.send(s.ctx, struct {
			Type string `json:"type"`
			Data []byte `json:"data"`
		}{"stdin", data[:n]})
		if err != nil {
			return sent, err
		}
		sent += n
		data = data[n:]
	}
	return sent, nil
}

// CloseStdin sends EOF without cancelling the command.
func (s *Stream) CloseStdin() error {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	if s.inputClosed {
		return nil
	}
	s.inputClosed = true
	return s.send(s.ctx, struct {
		Type string `json:"type"`
	}{"stdin_close"})
}

// Cancel asks the server to terminate the entire command tree; continue draining.
func (s *Stream) Cancel(ctx context.Context) error {
	return s.send(ctx, struct {
		Type string `json:"type"`
	}{"cancel"})
}

// Resize updates a TTY's dimensions.
func (s *Stream) Resize(ctx context.Context, cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return errors.New("terminal dimensions must be positive")
	}
	return s.send(ctx, struct {
		Type string `json:"type"`
		Cols uint16 `json:"cols"`
		Rows uint16 `json:"rows"`
	}{"resize", cols, rows})
}

// Wait returns the terminal result or a protocol/transport failure. It is repeatable.
func (s *Stream) Wait() (StreamResult, error) { <-s.done; return s.result, s.err }

// Close releases local resources, cancels remote execution on disconnect and joins the reader.
func (s *Stream) Close() error {
	s.cancel()
	_ = s.outReader.Close()
	_ = s.errReader.Close()
	_ = s.conn.CloseNow()
	<-s.done
	return nil
}
