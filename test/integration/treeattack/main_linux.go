//go:build linux && (integration || escape)

// Command treeattack creates detached, double-forked descendants that ignore TERM.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	signal.Ignore(syscall.SIGTERM)
	role := "root"
	if len(os.Args) > 1 {
		role = os.Args[1]
	}
	name := "parent"
	if role == "first" {
		name = "middle"
	}
	if role == "detached" {
		name = "detached"
	}
	if err := os.WriteFile("/tmp/tree_"+name, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if role != "detached" {
		next := "first"
		if role == "first" {
			next = "detached"
		}
		process, err := os.StartProcess(os.Args[0], []string{os.Args[0], next}, &os.ProcAttr{Env: os.Environ(), Files: []*os.File{os.Stdin, os.Stdout, os.Stderr}, Sys: &syscall.SysProcAttr{Setsid: true}})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_ = process.Release()
		if role == "first" {
			return
		}
	}
	if role == "signal" {
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		poll := time.NewTicker(time.Millisecond)
		defer poll.Stop()
		ready := false
		for !ready {
			if info, err := os.Stat("/tmp/tree_detached"); err == nil && info.Size() > 0 {
				ready = true
				break
			}
			select {
			case <-deadline.C:
				fmt.Fprintln(os.Stderr, "detached descendant readiness deadline")
				os.Exit(1)
			case <-poll.C:
			}
		}
		if err := syscall.Kill(os.Getppid(), syscall.SIGQUIT); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	for {
		_ = syscall.Pause()
	}
}
