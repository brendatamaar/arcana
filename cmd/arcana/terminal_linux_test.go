//go:build linux

package main

import (
	"fmt"
	"os"
	"syscall"
	"testing"
	"unsafe"
)

func TestTerminal_RawModeRestoresOriginalSettings(t *testing.T) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = master.Close() }()
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		t.Fatal(errno)
	} // #nosec G103 -- local fixed-size PTY ioctl argument.
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&number))); errno != 0 {
		t.Fatal(errno)
	} // #nosec G103 -- kernel writes only the local uint32 PTY number.
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0) // #nosec G304 -- number comes from this test's newly allocated kernel PTY.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = slave.Close() }()
	var original syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&original))); errno != 0 {
		t.Fatal(errno)
	} // #nosec G103 -- local fixed-size termios snapshot.
	restore, cols, rows, err := terminalMode(slave)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	if cols == 0 || rows == 0 {
		t.Fatal("empty terminal dimensions")
	}
	var raw syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&raw))); errno != 0 {
		t.Fatal(errno)
	} // #nosec G103 -- local fixed-size termios snapshot.
	if raw.Lflag&(syscall.ICANON|syscall.ECHO) != 0 {
		t.Fatal("terminal did not enter raw mode")
	}
	restore()
	var actual syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&actual))); errno != 0 {
		t.Fatal(errno)
	} // #nosec G103 -- local fixed-size termios snapshot.
	if actual != original {
		t.Fatal("terminal settings were not restored")
	}
}
