package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const maxProcesses = 8192

// procfs may return ESRCH from an already-open stat file as its process exits.
func processGone(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

type processInfo struct {
	PID        int
	Parent     int
	StartTicks uint64
	State      string
}

func parseProcess(data string) (processInfo, error) {
	left := strings.IndexByte(data, '(')
	right := strings.LastIndexByte(data, ')')
	if left < 1 || right <= left {
		return processInfo{}, errors.New("invalid process identity")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(data[:left]))
	if err != nil || pid <= 0 {
		return processInfo{}, errors.New("invalid process PID")
	}
	fields := strings.Fields(data[right+1:])
	if len(fields) < 20 {
		return processInfo{}, errors.New("incomplete process identity")
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil || parent < 0 {
		return processInfo{}, errors.New("invalid process parent")
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return processInfo{}, errors.New("invalid process start time")
	}
	return processInfo{PID: pid, Parent: parent, StartTicks: ticks, State: fields[0]}, nil
}

func readProcess(root *os.Root, pid int) (processInfo, error) {
	file, err := root.Open(strconv.Itoa(pid) + "/stat")
	if err != nil {
		return processInfo{}, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return processInfo{}, err
	}
	if len(data) > 4096 {
		return processInfo{}, errors.New("process identity exceeds limit")
	}
	return parseProcess(string(data))
}

func snapshot(root *os.Root) (map[int]processInfo, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	entries, err := directory.ReadDir(maxProcesses + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxProcesses {
		return nil, errors.New("process snapshot exceeds limit")
	}
	result := make(map[int]processInfo)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		info, err := readProcess(root, pid)
		if processGone(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("reading process identity: %w", err)
		}
		result[pid] = info
	}
	return result, nil
}

func descendantDepth(pid, ancestor int, processes map[int]processInfo) int {
	for depth := 1; depth <= maxProcesses; depth++ {
		process, ok := processes[pid]
		if !ok || process.Parent <= 0 || process.Parent == process.PID {
			return 0
		}
		if process.Parent == ancestor {
			return depth
		}
		pid = process.Parent
	}
	return 0
}

func signalDescendants(root *os.Root, ancestor int, signal syscall.Signal) error {
	processes, err := snapshot(root)
	if err != nil {
		return err
	}
	type target struct {
		info  processInfo
		depth int
	}
	targets := make([]target, 0)
	for pid, info := range processes {
		if pid == ancestor || info.State == "Z" {
			continue
		}
		if depth := descendantDepth(pid, ancestor, processes); depth > 0 {
			targets = append(targets, target{info, depth})
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].depth < targets[j].depth })
	for _, target := range targets {
		if err := signalIdentity(root, target.info, signal); err != nil {
			return err
		}
	}
	return nil
}

func signalIdentity(root *os.Root, expected processInfo, signal syscall.Signal) error {
	if expected.PID <= 1 || expected.PID > 2147483647 {
		return errors.New("invalid descendant PID")
	}
	// pidfd_open / pidfd_send_signal have these ABI numbers on linux/amd64 and arm64.
	descriptor, _, errno := syscall.Syscall(434, uintptr(expected.PID), 0, 0)
	if errno == syscall.ESRCH || errno == syscall.ENOENT {
		return nil
	}
	if errno != 0 {
		return fmt.Errorf("opening process handle: %w", errno)
	}
	if descriptor > 2147483647 {
		_, _, _ = syscall.Syscall(syscall.SYS_CLOSE, descriptor, 0, 0)
		return errors.New("invalid process handle")
	}
	defer func() { _ = syscall.Close(int(descriptor)) }()
	current, err := readProcess(root, expected.PID)
	if processGone(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.StartTicks != expected.StartTicks {
		return nil
	}
	var number uintptr
	switch signal {
	case syscall.SIGTERM:
		number = 15
	case syscall.SIGKILL:
		number = 9
	default:
		return errors.New("unsupported process signal")
	}
	_, _, errno = syscall.Syscall6(424, descriptor, number, 0, 0, 0, 0)
	if errno == syscall.ESRCH {
		return nil
	}
	if errno != 0 {
		return fmt.Errorf("signalling descendant: %w", errno)
	}
	return nil
}

func reapChildren(mainPID int) ([]exitData, bool, error) {
	exits := make([]exitData, 0, 1)
	for range 256 {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if errors.Is(err, syscall.ECHILD) {
			return exits, true, nil
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("reaping child: %w", err)
		}
		if pid == 0 {
			return exits, false, nil
		}
		if pid == mainPID {
			exit := exitData{Code: status.ExitStatus()}
			if status.Signaled() {
				exit.Code = 128 + int(status.Signal())
				exit.Signal = status.Signal().String()
			}
			exits = append(exits, exit)
		}
	}
	return exits, false, nil
}

func emergencyCleanup(root *os.Root, pid int, c clock) error {
	deadline := c.Now().Add(time.Second)
	poll := c.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for round := 0; round < 100 && c.Now().Before(deadline); round++ {
		if err := signalDescendants(root, pid, syscall.SIGKILL); err != nil {
			return err
		}
		_, empty, err := reapChildren(-1)
		if err != nil {
			return err
		}
		if empty {
			return nil
		}
		<-poll.Chan()
	}
	return errors.New("emergency process cleanup unconfirmed")
}

func checkProcessHandles(pid int) error {
	if pid <= 1 || pid > 2147483647 {
		return errors.New("invalid controller PID")
	}
	descriptor, _, errno := syscall.Syscall(434, uintptr(pid), 0, 0)
	if errno != 0 {
		return fmt.Errorf("runtime requires pidfd_open: %w", errno)
	}
	if descriptor > 2147483647 {
		_, _, _ = syscall.Syscall(syscall.SYS_CLOSE, descriptor, 0, 0)
		return errors.New("invalid controller handle")
	}
	defer func() { _ = syscall.Close(int(descriptor)) }()
	_, _, errno = syscall.Syscall6(424, descriptor, 0, 0, 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("runtime requires pidfd_send_signal: %w", errno)
	}
	return nil
}
