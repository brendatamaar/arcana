package envd

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// openPTY returns a pseudo-terminal pair. The master is non-blocking so Close unblocks
// readers; the slave is handed to the controller as its controlling terminal.
func openPTY(cols, rows uint16, owner *User) (master, slave *os.File, err error) {
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening /dev/ptmx: %w", err)
	}
	master = os.NewFile(uintptr(fd), "/dev/ptmx")
	defer func() {
		if err != nil {
			_ = master.Close()
		}
	}()
	if err = unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		return nil, nil, fmt.Errorf("unlocking pty: %w", err)
	}
	number, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		return nil, nil, fmt.Errorf("naming pty: %w", err)
	}
	if err = unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: rows}); err != nil {
		return nil, nil, fmt.Errorf("sizing pty: %w", err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("opening pty slave: %w", err)
	}
	if owner != nil && os.Geteuid() == 0 {
		if err = unix.Fchown(int(slave.Fd()), int(owner.UID), int(owner.GID)); err != nil {
			_ = slave.Close()
			return nil, nil, fmt.Errorf("handing pty to the sandbox user: %w", err)
		}
	}
	return master, slave, nil
}

func resizePTY(master *os.File, cols, rows uint16) error {
	return unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: cols, Row: rows})
}
