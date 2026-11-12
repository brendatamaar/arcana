package client

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/brendatamaar/arcana/internal/api/gen"
)

// ExecList, ExecRecord and Overview use the generated wire models.
type (
	// ExecRecord is one exec's metadata. Output is never stored.
	ExecRecord = gen.ExecRecord
	// ExecList is one newest-first page of exec history.
	ExecList = gen.ExecList
	// Overview is the dashboard landing data.
	Overview = gen.Overview
	// StateHistory lists a sandbox's state transitions.
	StateHistory = gen.StateHistory
)

// ErrReset means the server cannot resume the stream (the Last-Event-ID is too old or from before a
// restart) or dropped a client that fell behind. Refetch state, then reconnect without an ID.
var ErrReset = errors.New("arcana: event stream reset; refetch state and reconnect")

// sseLimit bounds one frame's data and any single line.
const sseLimit = 1 << 20

type sseFrame struct{ id, event, data string }

// sseReader parses Server-Sent Events from a response body, skipping comments (keep-alives).
type sseReader struct {
	body    io.ReadCloser
	scanner *bufio.Scanner
}

func newSSEReader(body io.ReadCloser) *sseReader {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), sseLimit)
	return &sseReader{body: body, scanner: scanner}
}

func (r *sseReader) next() (sseFrame, error) {
	var frame sseFrame
	var data strings.Builder
	seen := false
	for r.scanner.Scan() {
		line := r.scanner.Text()
		switch {
		case line == "":
			if !seen {
				continue
			}
			frame.data = data.String()
			return frame, nil
		case strings.HasPrefix(line, ":"):
			// keep-alive comment
		case strings.HasPrefix(line, "id:"):
			frame.id, seen = strings.TrimSpace(line[3:]), true
		case strings.HasPrefix(line, "event:"):
			frame.event, seen = strings.TrimSpace(line[6:]), true
		case strings.HasPrefix(line, "data:"):
			if data.Len()+len(line) > sseLimit {
				return sseFrame{}, ErrLimit
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(line[5:], " "))
			seen = true
		}
	}
	if err := r.scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return sseFrame{}, ErrLimit
		}
		return sseFrame{}, err
	}
	return sseFrame{}, io.EOF
}

func (c *Client) openStream(ctx context.Context, path string, headers map[string]string) (*sseReader, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint(path, nil), nil)
	if err != nil {
		return nil, err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("arcana request: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, decodeError(resp)
	}
	return newSSEReader(resp.Body), nil
}

// Event is one lifecycle fact. Data holds event-specific metadata (never exec output).
type Event struct {
	// ID increases within one server run; pass the last one to Events to resume.
	ID        string
	Type      string
	SandboxID string
	At        time.Time
	Data      json.RawMessage
}

// EventStream is an open /v1/events stream. Close it when done.
type EventStream struct{ reader *sseReader }

// Events opens the event stream. With lastEventID set the server replays what was missed; an
// unresumable ID yields ErrReset from Next. The stream ends when ctx is cancelled or Close is called.
func (c *Client) Events(ctx context.Context, lastEventID string) (*EventStream, error) {
	var headers map[string]string
	if lastEventID != "" {
		if _, err := strconv.ParseUint(lastEventID, 10, 64); err != nil {
			return nil, errors.New("invalid Last-Event-ID")
		}
		headers = map[string]string{"Last-Event-ID": lastEventID}
	}
	reader, err := c.openStream(ctx, "/events", headers)
	if err != nil {
		return nil, err
	}
	return &EventStream{reader: reader}, nil
}

// Next blocks for the next event. It returns ErrReset when the server asks the client to resync and
// io.EOF when the server closes the stream.
func (s *EventStream) Next() (Event, error) {
	frame, err := s.reader.next()
	if err != nil {
		return Event{}, err
	}
	if frame.event == "reset" {
		return Event{}, ErrReset
	}
	var wire struct {
		SandboxID string          `json:"sandbox_id"`
		At        time.Time       `json:"at"`
		Data      json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal([]byte(frame.data), &wire); err != nil {
		return Event{}, fmt.Errorf("decoding event: %w", err)
	}
	return Event{ID: frame.id, Type: frame.event, SandboxID: wire.SandboxID, At: wire.At, Data: wire.Data}, nil
}

// Close ends the stream.
func (s *EventStream) Close() error { return s.reader.body.Close() }

// OutputEvent is one item of an exec output tail: Data for "output", End for "end".
type OutputEvent struct {
	// Kind is "output", "gap" (older output was discarded) or "end".
	Kind   string
	Seq    uint64
	Stream string // "stdout" or "stderr"
	Data   []byte
	End    *OutputEnd
}

// OutputEnd is the exec's terminal result. ExitCode is nil when the real status is unknown.
// Expired means the output is no longer retained; only the result is available.
type OutputEnd struct {
	ExitCode  *int `json:"exit_code"`
	TimedOut  bool `json:"timed_out"`
	Truncated bool `json:"truncated"`
	Cancelled bool `json:"cancelled"`
	Expired   bool `json:"expired"`
}

// OutputStream is an open exec output tail. Close it when done.
type OutputStream struct{ reader *sseReader }

// ExecOutput tails an exec's buffered and live output from server memory; the server never stores output.
func (c *Client) ExecOutput(ctx context.Context, sandboxID, execID string) (*OutputStream, error) {
	p, err := resource(sandboxID)
	if err != nil {
		return nil, err
	}
	if _, err = resource(execID); err != nil {
		return nil, err
	}
	reader, err := c.openStream(ctx, p+"/execs/"+url.PathEscape(execID)+"/output", nil)
	if err != nil {
		return nil, err
	}
	return &OutputStream{reader: reader}, nil
}

// Next returns the next output item, io.EOF once the "end" item has been delivered.
func (s *OutputStream) Next() (OutputEvent, error) {
	frame, err := s.reader.next()
	if err != nil {
		return OutputEvent{}, err
	}
	switch frame.event {
	case "output":
		var wire struct {
			Seq    uint64 `json:"seq"`
			Stream string `json:"stream"`
			Data   string `json:"data"`
		}
		if err = json.Unmarshal([]byte(frame.data), &wire); err != nil {
			return OutputEvent{}, fmt.Errorf("decoding output: %w", err)
		}
		data, err := base64.StdEncoding.DecodeString(wire.Data)
		if err != nil {
			return OutputEvent{}, fmt.Errorf("decoding output bytes: %w", err)
		}
		return OutputEvent{Kind: "output", Seq: wire.Seq, Stream: wire.Stream, Data: data}, nil
	case "gap":
		return OutputEvent{Kind: "gap"}, nil
	case "end":
		var end OutputEnd
		if err = json.Unmarshal([]byte(frame.data), &end); err != nil {
			return OutputEvent{}, fmt.Errorf("decoding end: %w", err)
		}
		return OutputEvent{Kind: "end", End: &end}, nil
	default:
		return OutputEvent{}, fmt.Errorf("unknown output event %q", frame.event)
	}
}

// Close ends the stream.
func (s *OutputStream) Close() error { return s.reader.body.Close() }

// ListExecs returns one newest-first page of a sandbox's exec metadata.
func (c *Client) ListExecs(ctx context.Context, sandboxID string, o ListOptions) (ExecList, error) {
	var out ExecList
	p, err := resource(sandboxID)
	if err == nil {
		err = c.json(ctx, "GET", p+"/execs", ListOptions{Limit: o.Limit, Cursor: o.Cursor}.query(), nil, &out)
	}
	return out, err
}

// GetOverview returns the dashboard overview for the current key.
func (c *Client) GetOverview(ctx context.Context) (Overview, error) {
	var out Overview
	err := c.json(ctx, "GET", "/overview", nil, nil, &out)
	return out, err
}

// GetSandboxHistory returns a sandbox's persisted state transitions in order.
func (c *Client) GetSandboxHistory(ctx context.Context, sandboxID string) (StateHistory, error) {
	var out StateHistory
	p, err := resource(sandboxID)
	if err == nil {
		err = c.json(ctx, "GET", p+"/history", nil, nil, &out)
	}
	return out, err
}
