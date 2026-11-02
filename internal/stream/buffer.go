package stream

import (
	"context"
	"errors"
	"io"
	"sync"
)

const (
	sendBufferBytes = 1048576
	maxPackets      = 256
)

type (
	packet struct {
		data     []byte
		terminal bool
	}
	sendQueue struct {
		mu      sync.Mutex
		packets []packet
		bytes   int
		wake    chan struct{}
	}
)

func newSendQueue() *sendQueue { return &sendQueue{wake: make(chan struct{}, 1)} }
func (q *sendQueue) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *sendQueue) output(stderr bool, data []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	// Include base64/JSON framing in the budget and reserve control-message space.
	size := (len(data)+2)/3*4 + 40
	if len(q.packets) >= maxPackets-2 || q.bytes+size > sendBufferBytes-4096 {
		return false
	}
	kind := "stdout"
	if stderr {
		kind = "stderr"
	}
	encoded := encode(struct {
		Type string `json:"type"`
		Data []byte `json:"data"`
	}{kind, data})
	q.packets = append(q.packets, packet{data: encoded})
	q.bytes += len(encoded)
	q.notify()
	return true
}

func (q *sendQueue) control(data []byte, terminal bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(data) > 4096 || q.bytes+len(data) > sendBufferBytes || len(q.packets) >= maxPackets {
		panic("stream control reserve exceeded")
	}
	q.packets = append(q.packets, packet{data, terminal})
	q.bytes += len(data)
	q.notify()
}

func (q *sendQueue) pop() (packet, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.packets) == 0 {
		return packet{}, false
	}
	result := q.packets[0]
	q.packets[0] = packet{}
	q.packets = q.packets[1:]
	q.bytes -= len(result.data)
	if len(q.packets) > 0 {
		q.notify()
	}
	return result, true
}

type inputBuffer struct {
	mu      sync.Mutex
	ctx     context.Context
	chunks  chan []byte
	current []byte
	done    chan struct{}
	once    sync.Once
	closed  bool
}

func newInputBuffer(ctx context.Context) *inputBuffer {
	return &inputBuffer{ctx: ctx, chunks: make(chan []byte, 16), done: make(chan struct{})}
}

func (b *inputBuffer) write(data []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return io.ErrClosedPipe
	}
	select {
	case <-b.done:
		return io.ErrClosedPipe
	default:
	}
	if len(data) > maxMessageBytes {
		return ErrInputLimit
	}
	select {
	case b.chunks <- data:
		return nil
	default:
		return ErrInputLimit
	}
}

func (b *inputBuffer) eof() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		close(b.chunks)
	}
}
func (b *inputBuffer) Close() error { b.once.Do(func() { close(b.done) }); return nil }
func (b *inputBuffer) Read(data []byte) (int, error) {
	for len(b.current) == 0 {
		select {
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		case <-b.done:
			return 0, io.ErrClosedPipe
		case chunk, ok := <-b.chunks:
			if !ok {
				return 0, io.EOF
			}
			b.current = chunk
		}
	}
	n := copy(data, b.current)
	b.current = b.current[n:]
	return n, nil
}

func inputClosed(err error) bool { return errors.Is(err, io.ErrClosedPipe) }
