//go:build linux

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

func commandInput(input io.Reader) (io.ReadCloser, error) {
	if input == os.Stdin {
		return os.OpenFile("/dev/stdin", os.O_RDONLY|syscall.O_NONBLOCK, 0)
	}
	if closer, ok := input.(io.ReadCloser); ok {
		return closer, nil
	}
	switch input.(type) {
	case *strings.Reader, *bytes.Reader, *bytes.Buffer:
		return io.NopCloser(input), nil
	}
	return nil, errors.New("stdin must support Close for cancellation")
}

func terminalMode(input io.Reader) (func(), uint16, uint16, error) {
	file, ok := input.(*os.File)
	if !ok {
		return nil, 0, 0, errors.New("--tty requires terminal stdin")
	}
	fd := file.Fd()
	var original syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCGETS, uintptr(unsafe.Pointer(&original))); errno != 0 { // #nosec G103 -- ioctl receives a local fixed-size Linux termios struct and an operator-owned fd.
		return nil, 0, 0, errors.New("--tty requires terminal stdin")
	}
	raw := original
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&raw))); errno != 0 { // #nosec G103 -- ioctl reads only the local Linux termios struct.
		return nil, 0, 0, errno
	}
	restore := func() {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TCSETS, uintptr(unsafe.Pointer(&original))) // #nosec G103 -- restores the original local termios snapshot on the same fd.
	}
	size := struct{ Rows, Cols, X, Y uint16 }{}
	cols, rows := uint16(80), uint16(24)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size))); errno == 0 && size.Cols > 0 && size.Rows > 0 { // #nosec G103 -- kernel writes only the local fixed-size winsize struct.
		cols, rows = size.Cols, size.Rows
	}
	return restore, cols, rows, nil
}
