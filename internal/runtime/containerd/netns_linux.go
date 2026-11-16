package containerd

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// hostNet is everything allowlist networking needs from the host: network namespaces, CNI and bridge removal.
// It is an interface so the orchestration can be tested without privileges.
type hostNet interface {
	// NewNetns creates a persistent network namespace and returns its path.
	NewNetns(name string) (string, error)
	// DeleteNetns removes it; a missing one is success.
	DeleteNetns(name string) error
	// Netns lists the existing namespace names.
	Netns() ([]string, error)
	// DeleteLink removes a network interface by name; a missing one is success.
	DeleteLink(name string) error
	CNI(ctx context.Context, command string, conf []byte, containerID, netnsPath, ifName string) error
}

// linuxHost implements hostNet with real namespaces and CNI plugins. It needs root.
type linuxHost struct{ cfg NetworkConfig }

func (h linuxHost) netnsDir() string { return filepath.Join(h.cfg.StateDir, "netns") }

func (h linuxHost) NewNetns(name string) (string, error) {
	if err := os.MkdirAll(h.netnsDir(), 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", h.netnsDir(), err)
	}
	path := filepath.Join(h.netnsDir(), name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- inside a directory only root can write; the name is a validated container id
	if err != nil {
		return "", fmt.Errorf("creating the namespace file: %w", err)
	}
	_ = file.Close()
	// A new namespace belongs to the thread that unshares, so the work runs on a goroutine that locks its
	// thread and never unlocks it: the runtime then discards the thread instead of reusing it in the new namespace.
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			done <- fmt.Errorf("unshare(CLONE_NEWNET): %w", err)
			return
		}
		done <- unix.Mount(fmt.Sprintf("/proc/self/task/%d/ns/net", unix.Gettid()), path, "", unix.MS_BIND, "")
	}()
	if err = <-done; err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("creating a network namespace (Arcana must run as root): %w", err)
	}
	return path, nil
}

func (h linuxHost) DeleteNetns(name string) error {
	path := filepath.Join(h.netnsDir(), name)
	if err := unix.Unmount(path, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("unmounting the namespace: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (h linuxHost) Netns() ([]string, error) {
	entries, err := os.ReadDir(h.netnsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func (h linuxHost) CNI(ctx context.Context, command string, conf []byte, containerID, netnsPath, ifName string) error {
	return runCNI(ctx, h.cfg.CNIBinDir, command, conf, containerID, netnsPath, ifName)
}

var netlinkSeq atomic.Uint32

// DeleteLink removes an interface with one RTM_DELLINK request. The CNI bridge plugin never removes the bridge it
// creates, and a netlink library would be a new dependency for these few bytes.
func (linuxHost) DeleteLink(name string) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return fmt.Errorf("opening a netlink socket: %w", err)
	}
	defer func() { _ = unix.Close(fd) }()
	if err = unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	// nlmsghdr (16) + ifinfomsg (16) + one IFLA_IFNAME attribute (4 + name + NUL, padded to 4).
	attrLen := 4 + len(name) + 1
	padded := (attrLen + 3) &^ 3
	msg := make([]byte, 32+padded)
	order := binary.NativeEndian
	order.PutUint32(msg[0:], uint32(len(msg))) // #nosec G115 -- a few dozen bytes
	order.PutUint16(msg[4:], unix.RTM_DELLINK)
	order.PutUint16(msg[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	order.PutUint32(msg[8:], netlinkSeq.Add(1))
	msg[16] = unix.AF_UNSPEC
	order.PutUint16(msg[32:], uint16(attrLen)) // #nosec G115 -- bounded by the interface name length
	order.PutUint16(msg[34:], unix.IFLA_IFNAME)
	copy(msg[36:], name)
	if err = unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	reply := make([]byte, 4096)
	n, _, err := unix.Recvfrom(fd, reply, 0)
	if err != nil {
		return err
	}
	if n < 20 || order.Uint16(reply[4:]) != unix.NLMSG_ERROR {
		return errors.New("unexpected netlink reply")
	}
	code := int32(order.Uint32(reply[16:])) // #nosec G115 -- an errno reported by the kernel
	switch {
	case code == 0, -code == int32(unix.ENODEV):
		return nil
	default:
		return fmt.Errorf("removing interface %s: %w", name, unix.Errno(uint32(-code))) // #nosec G115 -- code is a negative errno
	}
}
