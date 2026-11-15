package firecracker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"
)

// launcher starts a VMM process. It is a seam so driver tests can fake Firecracker.
type launcher interface {
	Launch(ctx context.Context, argv []string) (process, error)
}

type process interface {
	Identity() procIdentity
	Done() <-chan struct{}
	// Tail is the last bytes the VMM wrote to its console and stderr, for diagnostics.
	Tail() string
}

type execLauncher struct{}

type execProcess struct {
	id   procIdentity
	done chan struct{}
	tail *tailBuffer
}

func (p *execProcess) Identity() procIdentity { return p.id }
func (p *execProcess) Done() <-chan struct{}  { return p.done }
func (p *execProcess) Tail() string           { return p.tail.String() }

// Launch starts argv detached from ctx's lifetime: the VM must outlive the request that
// created it. The jailer re-executes Firecracker, so the PID is the VMM's.
func (execLauncher) Launch(_ context.Context, argv []string) (process, error) {
	if len(argv) == 0 {
		return nil, errors.New("empty VMM command")
	}
	tail := &tailBuffer{limit: 8192}
	// #nosec G204 -- argv is built by launchArgv from validated configuration
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr, cmd.Env = tail, tail, []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting VMM: %w", err)
	}
	identity, err := identityOf(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	p := &execProcess{id: identity, done: make(chan struct{}), tail: tail}
	go func() {
		_ = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

// tailBuffer keeps only the last limit bytes written to it.
type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, p...)
	if len(t.data) > t.limit {
		t.data = t.data[len(t.data)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.data)
}

// waitForFile polls until path exists, the process dies, or the deadline passes.
func waitForFile(ctx context.Context, path string, p process, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		select {
		case <-p.Done():
			return fmt.Errorf("VMM exited before its API socket appeared: %s", p.Tail())
		case <-deadline.C:
			return errNoAPI
		case <-ctx.Done():
			return ctx.Err()
		case <-poll.C:
		}
	}
}
