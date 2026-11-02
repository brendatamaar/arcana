//go:build !linux

package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
)

func commandInput(input io.Reader) (io.ReadCloser, error) {
	if closer, ok := input.(io.ReadCloser); ok {
		return closer, nil
	}
	switch input.(type) {
	case *strings.Reader, *bytes.Reader, *bytes.Buffer:
		return io.NopCloser(input), nil
	}
	return nil, errors.New("stdin must support Close for cancellation")
}

func terminalMode(io.Reader) (func(), uint16, uint16, error) {
	return nil, 0, 0, errors.New("--tty requires a Linux terminal")
}
