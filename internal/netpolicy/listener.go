package netpolicy

import (
	"net"
	"sync"
)

// BoundListener caps accepted connections before net/http creates handler goroutines.
func BoundListener(listener net.Listener, limit int) net.Listener {
	return &boundedListener{Listener: listener, slots: make(chan struct{}, limit), done: make(chan struct{})}
}

type boundedListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}
type boundedConnection struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *boundedConnection) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

func (l *boundedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &boundedConnection{Conn: conn, release: func() { <-l.slots }}, nil
}

func (l *boundedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}
