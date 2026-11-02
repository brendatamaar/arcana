// Package stream implements the bounded v0.1 WebSocket exec transport.
package stream

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/brendatamaar/arcana/internal/orchestrator"
)

// Protocol failures are translated into public error codes by the API boundary.
var (
	ErrInvalid    = errors.New("invalid stream message")
	ErrInputLimit = errors.New("stream stdin buffer limit reached")
	ErrControl    = errors.New("stream control failed")
)

const maxMessageBytes = 65536

type (
	start struct {
		request    orchestrator.ExecRequest
		tty        bool
		cols, rows uint16
	}
	inputData     struct{ data []byte }
	inputClose    struct{}
	resize        struct{ cols, rows uint16 }
	cancel        struct{}
	ready         struct{ id string }
	ended         struct{ failed bool }
	disconnected  struct{}
	invalid       struct{}
	controlFailed struct{}
)

//sumtype:decl
type input interface{ streamInput() }

func (start) streamInput()         {}
func (inputData) streamInput()     {}
func (inputClose) streamInput()    {}
func (resize) streamInput()        {}
func (cancel) streamInput()        {}
func (ready) streamInput()         {}
func (ended) streamInput()         {}
func (disconnected) streamInput()  {}
func (invalid) streamInput()       {}
func (controlFailed) streamInput() {}

func decode(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return ErrInvalid
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalid
	}
	return nil
}

func parse(data []byte) (input, error) {
	if len(data) > maxMessageBytes {
		return nil, ErrInvalid
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, ErrInvalid
	}
	switch envelope.Type {
	case "start":
		var wire struct {
			Type    string            `json:"type"`
			Cmd     *[]string         `json:"cmd"`
			Shell   *string           `json:"shell"`
			Cwd     *string           `json:"cwd"`
			Env     map[string]string `json:"env"`
			Stdin   []byte            `json:"stdin"`
			Timeout *int64            `json:"timeout_seconds"`
			TTY     bool              `json:"tty"`
			Cols    *uint16           `json:"cols"`
			Rows    *uint16           `json:"rows"`
		}
		if err := decode(data, &wire); err != nil {
			return nil, err
		}
		cols, rows := uint16(80), uint16(24)
		if wire.Cols != nil {
			cols = *wire.Cols
		}
		if wire.Rows != nil {
			rows = *wire.Rows
		}
		if cols == 0 || rows == 0 {
			return nil, ErrInvalid
		}
		return start{orchestrator.ExecRequest{Cmd: wire.Cmd, Shell: wire.Shell, Cwd: wire.Cwd, Env: wire.Env, Stdin: wire.Stdin, TimeoutSeconds: wire.Timeout}, wire.TTY, cols, rows}, nil
	case "stdin":
		var wire struct {
			Type string  `json:"type"`
			Data *[]byte `json:"data"`
		}
		if err := decode(data, &wire); err != nil || wire.Data == nil {
			return nil, ErrInvalid
		}
		return inputData{*wire.Data}, nil
	case "stdin_close", "cancel":
		var wire struct {
			Type string `json:"type"`
		}
		if err := decode(data, &wire); err != nil {
			return nil, err
		}
		if wire.Type == "cancel" {
			return cancel{}, nil
		}
		return inputClose{}, nil
	case "resize":
		var wire struct {
			Type string `json:"type"`
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		}
		if err := decode(data, &wire); err != nil || wire.Cols == 0 || wire.Rows == 0 {
			return nil, ErrInvalid
		}
		return resize{wire.Cols, wire.Rows}, nil
	}
	return nil, ErrInvalid
}

func encode(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic("invalid server stream message")
	}
	return data
}
