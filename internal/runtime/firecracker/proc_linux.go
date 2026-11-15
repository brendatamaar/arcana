package firecracker

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brendatamaar/arcana/internal/runtime"
)

// procIdentity is a process ID plus its start time, so a recycled PID is never mistaken
// for the VMM this driver started.
type procIdentity struct {
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
}

func readStat(pid int) ([]string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") // #nosec G304 -- numeric PID under /proc
	if err != nil {
		return nil, err
	}
	text := string(data)
	right := strings.LastIndexByte(text, ')')
	if right < 0 {
		return nil, errors.New("malformed process stat")
	}
	return strings.Fields(text[right+1:]), nil
}

// identityOf returns the identity of a live process.
func identityOf(pid int) (procIdentity, error) {
	fields, err := readStat(pid)
	if err != nil {
		return procIdentity{}, err
	}
	if len(fields) < 20 {
		return procIdentity{}, errors.New("incomplete process stat")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return procIdentity{}, err
	}
	return procIdentity{PID: pid, StartTicks: ticks}, nil
}

// alive reports whether the exact process still runs and is not a zombie.
func (p procIdentity) alive() bool {
	if p.PID <= 1 {
		return false
	}
	fields, err := readStat(p.PID)
	if err != nil || len(fields) < 20 {
		return false
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	return err == nil && ticks == p.StartTicks && fields[0] != "Z" && fields[0] != "X"
}

// kill sends SIGKILL to the exact process and waits briefly for it to go. Killing a
// microVM loses nothing: sandboxes are ephemeral and the guest has no state to flush.
func (p procIdentity) kill() error {
	if !p.alive() {
		return nil
	}
	if err := syscall.Kill(p.PID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("killing VMM %d: %w", p.PID, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.alive() {
		if time.Now().After(deadline) {
			return fmt.Errorf("VMM %d survived SIGKILL", p.PID)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// stats reads the VMM's own accounting. Guest-internal process counts are not visible
// from the host, so PIDs is reported as zero.
func (p procIdentity) stats() (runtime.Stats, error) {
	fields, err := readStat(p.PID)
	if err != nil || len(fields) < 22 || !p.alive() {
		return runtime.Stats{}, runtime.NotFound{ID: strconv.Itoa(p.PID)}
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	rss, _ := strconv.ParseInt(fields[21], 10, 64)
	const ticksPerSecond = 100 // USER_HZ is 100 on every Linux ABI Go supports
	return runtime.Stats{
		CPUUsageNano:     (utime + stime) * (1_000_000_000 / ticksPerSecond),
		MemoryUsageBytes: uint64(max(rss, 0)) * uint64(os.Getpagesize()), // #nosec G115 -- clamped non-negative
	}, nil
}
