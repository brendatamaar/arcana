package envd

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Listen opens "unix:<path>" or "vsock:<port>". The Unix socket is created with mode
// 0600 and replaces only a stale socket. The vsock listener accepts only the host
// (CID 2): a connection from the guest itself or another VM is closed unanswered.
func Listen(address string) (net.Listener, error) {
	kind, value, ok := strings.Cut(address, ":")
	if !ok {
		return nil, errors.New(`listen address must be "unix:<path>" or "vsock:<port>"`)
	}
	switch kind {
	case "unix":
		return listenUnix(value)
	case "vsock":
		port, err := strconv.ParseUint(value, 10, 32)
		if err != nil || port == 0 {
			return nil, errors.New("invalid vsock port")
		}
		return listenVsock(uint32(port), HostOnly)
	default:
		return nil, fmt.Errorf("unsupported transport %q", kind)
	}
}

func listenUnix(path string) (net.Listener, error) {
	if path == "" || len(path) > 100 {
		return nil, errors.New("invalid unix socket path")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("refusing to replace a non-socket file")
		}
		if err = os.Remove(path); err != nil {
			return nil, err
		}
	}
	// A restrictive umask makes the socket private from its creation, with no window.
	old := syscall.Umask(0o177)
	defer syscall.Umask(old)
	return net.Listen("unix", path)
}

// HostOnly is the default peer policy: only the host's CID may connect over vsock.
func HostOnly(cid uint32) bool { return cid == unix.VMADDR_CID_HOST }

type vsockListener struct {
	file  *os.File
	port  uint32
	allow func(cid uint32) bool
}

func listenVsock(port uint32, allow func(uint32) bool) (net.Listener, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("creating vsock socket: %w", err)
	}
	if err = unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("binding vsock port %d: %w", port, err)
	}
	if err = unix.Listen(fd, 16); err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("listening on vsock: %w", err)
	}
	return &vsockListener{file: os.NewFile(uintptr(fd), "vsock-listener"), port: port, allow: allow}, nil
}

func (l *vsockListener) Accept() (net.Conn, error) {
	raw, err := l.file.SyscallConn()
	if err != nil {
		return nil, err
	}
	for {
		var nfd int
		var peer unix.Sockaddr
		var acceptErr error
		err = raw.Read(func(fd uintptr) bool {
			nfd, peer, acceptErr = unix.Accept4(int(fd), unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK)
			return acceptErr != unix.EAGAIN
		})
		if err != nil {
			return nil, net.ErrClosed
		}
		if acceptErr != nil {
			if errors.Is(acceptErr, unix.ECONNABORTED) || errors.Is(acceptErr, unix.EINTR) {
				continue
			}
			return nil, acceptErr
		}
		vm, ok := peer.(*unix.SockaddrVM)
		if !ok || !l.allow(vm.CID) {
			_ = unix.Close(nfd)
			continue
		}
		return &vsockConn{file: os.NewFile(uintptr(nfd), "vsock"), local: vsockAddr{unix.VMADDR_CID_ANY, l.port}, remote: vsockAddr{vm.CID, vm.Port}}, nil
	}
}

func (l *vsockListener) Close() error   { return l.file.Close() }
func (l *vsockListener) Addr() net.Addr { return vsockAddr{unix.VMADDR_CID_ANY, l.port} }

type vsockAddr struct{ cid, port uint32 }

func (vsockAddr) Network() string  { return "vsock" }
func (a vsockAddr) String() string { return fmt.Sprintf("%d:%d", a.cid, a.port) }

type vsockConn struct {
	file          *os.File
	local, remote vsockAddr
}

func (c *vsockConn) Read(p []byte) (int, error)         { return c.file.Read(p) }
func (c *vsockConn) Write(p []byte) (int, error)        { return c.file.Write(p) }
func (c *vsockConn) Close() error                       { return c.file.Close() }
func (c *vsockConn) LocalAddr() net.Addr                { return c.local }
func (c *vsockConn) RemoteAddr() net.Addr               { return c.remote }
func (c *vsockConn) SetDeadline(t time.Time) error      { return c.file.SetDeadline(t) }
func (c *vsockConn) SetReadDeadline(t time.Time) error  { return c.file.SetReadDeadline(t) }
func (c *vsockConn) SetWriteDeadline(t time.Time) error { return c.file.SetWriteDeadline(t) }
