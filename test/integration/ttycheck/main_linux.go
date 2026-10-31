//go:build linux && (integration || escape)

// Command ttycheck compares the foreground terminal group with the process group.
package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

func main() {
	var foreground int32
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, 0, syscall.TIOCGPGRP, uintptr(unsafe.Pointer(&foreground)))
	if errno != 0 {
		fmt.Fprintln(os.Stderr, errno)
		os.Exit(1)
	}
	fmt.Printf("pgrp=%d foreground=%d\n", syscall.Getpgrp(), foreground)
	if int(foreground) != syscall.Getpgrp() {
		os.Exit(2)
	}
}
