package orchestrator

import (
	"bytes"
	"math"
	"strings"
	"sync"
	"unicode/utf8"
)

type capture struct {
	mu                 sync.Mutex
	limit              int64
	stdout, stderr     bytes.Buffer
	outBytes, errBytes int64
	truncated          bool
	kept               int64
	observer           StreamObserver
}
type captureWriter struct {
	target *capture
	stderr bool
}

func (w captureWriter) Write(p []byte) (int, error) {
	c := w.target
	c.mu.Lock()
	defer c.mu.Unlock()
	buffer, count := &c.stdout, &c.outBytes
	if w.stderr {
		buffer, count = &c.stderr, &c.errBytes
	}
	*count += min(int64(len(p)), math.MaxInt64-*count)
	remaining := c.limit - c.kept
	kept := min(int64(len(p)), remaining)
	c.kept += kept
	if kept < int64(len(p)) {
		c.truncated = true
	}
	// Always report the full write so producers continue draining after the cap.
	if c.observer == nil {
		_, _ = buffer.Write(p[:kept])
	} else {
		for offset := int64(0); offset < kept; {
			end := min(kept, offset+16384)
			if !c.observer.Output(w.stderr, p[offset:end]) {
				c.truncated = true
			}
			offset = end
		}
	}
	return len(p), nil
}

func sanitize(data []byte, limit int64) (string, bool) {
	var result strings.Builder
	result.Grow(int(min(int64(len(data)), limit)))
	for len(data) > 0 {
		r, size := utf8.DecodeRune(data)
		encoded := size
		if r == utf8.RuneError && size == 1 {
			encoded = 3
		}
		if int64(result.Len()+encoded) > limit {
			return result.String(), true
		}
		if r == utf8.RuneError && size == 1 {
			result.WriteRune(utf8.RuneError)
		} else {
			result.Write(data[:size])
		}
		data = data[size:]
	}
	return result.String(), false
}

func (c *capture) result() (stdout, stderr string, outBytes, errBytes int64, truncated bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stdout, cutOut := sanitize(c.stdout.Bytes(), c.limit)
	stderr, cutErr := sanitize(c.stderr.Bytes(), c.limit-int64(len(stdout)))
	return stdout, stderr, c.outBytes, c.errBytes, c.truncated || cutOut || cutErr
}
