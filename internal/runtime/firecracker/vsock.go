package firecracker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// dialGuest connects to a guest vsock port through Firecracker's host-side unix socket:
// connect, send "CONNECT <port>\n", and expect "OK <host port>\n". The returned
// connection then carries the guest service's bytes.
func dialGuest(ctx context.Context, udsPath string, port uint32) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", udsPath)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(5 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	if _, err = fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		_ = conn.Close()
		return nil, err
	}
	// Read the reply one byte at a time so no guest bytes are consumed into a buffer
	// that would be lost to the caller.
	line, err := readLine(conn, 64)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("vsock handshake: %w", err)
	}
	if !strings.HasPrefix(line, "OK ") {
		_ = conn.Close()
		return nil, fmt.Errorf("vsock handshake refused: %q", line)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// readLine reads one byte at a time so no guest bytes after the reply are consumed.
func readLine(conn net.Conn, limit int) (string, error) {
	var line []byte
	one := make([]byte, 1)
	for len(line) <= limit {
		if _, err := io.ReadFull(conn, one); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return string(line), nil
		}
		line = append(line, one[0])
	}
	return "", errors.New("handshake reply too long")
}
