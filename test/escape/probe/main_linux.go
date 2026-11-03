// Escape probe is an adversarial test program, never a production helper.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	if err := attack(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func attack(name string) error {
	switch name {
	case "fork":
		for i := 0; i < 256; i++ {
			cmd := exec.Command("/bin/sleep", "60")
			if err := cmd.Start(); err != nil {
				if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.ENOMEM) {
					fmt.Printf("fork-blocked after %d children: %v\n", i, err)
					return nil
				}
				return err
			}
			_ = cmd.Process.Release()
		}
		return errors.New("PID limit was not enforced")
	case "memory":
		blocks := make([][]byte, 0, 64)
		for i := 0; i < 64; i++ {
			block := make([]byte, 16<<20)
			for j := range block {
				block[j] = 0xff
			}
			blocks = append(blocks, block)
		}
		runtime.KeepAlive(blocks)
		return errors.New("memory allocation escaped the sandbox limit")
	case "cpu":
		runtime.GOMAXPROCS(4)
		var before, after syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &before)
		fmt.Println("cpu-started")
		start := time.Now()
		end := start.Add(3 * time.Second)
		var group sync.WaitGroup
		for i := 0; i < 4; i++ {
			group.Add(1)
			go func() {
				defer group.Done()
				for time.Now().Before(end) {
				}
			}()
		}
		group.Wait()
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &after)
		micros := func(r syscall.Rusage) int64 { return r.Utime.Sec*1e6 + r.Utime.Usec + r.Stime.Sec*1e6 + r.Stime.Usec }
		return json.NewEncoder(os.Stdout).Encode(map[string]int64{"cpu_ms": (micros(after) - micros(before)) / 1000, "wall_ms": time.Since(start).Milliseconds()})
	case "fill", "volume":
		file, err := os.Create("/workspace/fill")
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		block := make([]byte, 64<<10)
		count := 2048
		if name == "volume" {
			count = 160
		}
		for i := 0; i < count; i++ {
			if _, err = file.Write(block); err != nil {
				if name == "fill" && errors.Is(err, syscall.ENOSPC) {
					fmt.Println("enospc")
					return nil
				}
				return err
			}
		}
		if name == "volume" {
			if err = file.Sync(); err != nil {
				return err
			}
			time.Sleep(10 * time.Second)
			return errors.New("volume watchdog did not destroy sandbox")
		}
		return errors.New("tmpfs size limit was not enforced")
	case "readonly":
		for _, path := range []string{"/etc/arcana-escape", "/usr/arcana-escape"} {
			file, err := os.Create(path) // #nosec G304 -- deliberate sandbox-only writes to the two constant protected paths in this test fixture.
			if err == nil {
				_ = file.Close()
				return errors.New("protected root filesystem was writable")
			}
			if !errors.Is(err, syscall.EROFS) && !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
				return err
			}
		}
	case "raw":
		fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_RAW, syscall.IPPROTO_ICMP)
		if err == nil {
			_ = syscall.Close(fd)
			return errors.New("raw socket was permitted")
		}
		if !errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EACCES) {
			return err
		}
	case "setuid":
		var st syscall.Stat_t
		if err := syscall.Stat("/usr/local/bin/escape-probe", &st); err != nil {
			return err
		}
		if st.Uid != 0 || st.Mode&syscall.S_ISUID == 0 {
			return errors.New("setuid fixture is missing")
		}
		if os.Geteuid() != 1000 {
			return errors.New("setuid escalation succeeded")
		}
		status, err := os.ReadFile("/proc/self/status")
		if err != nil {
			return err
		}
		if !strings.Contains(string(status), "NoNewPrivs:\t1") {
			return errors.New("no-new-privileges is absent")
		}
	case "privileged":
		if err := os.Mkdir("/tmp/mount-test", 0o700); err != nil {
			return err
		}
		denied := func(err error) bool {
			return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.EACCES)
		}
		if err := syscall.Mount("none", "/tmp/mount-test", "tmpfs", 0, ""); !denied(err) {
			return fmt.Errorf("mount was not denied by isolation: %v", err)
		}
		if err := syscall.PtraceAttach(os.Getppid()); !denied(err) {
			return fmt.Errorf("ptrace of supervisor was not denied: %v", err)
		}
		_, _, errno := syscall.Syscall(syscall.SYS_INIT_MODULE, 0, 0, 0)
		if !denied(errno) {
			return fmt.Errorf("kernel module operation was not denied: %v", errno)
		}
	case "socket":
		for _, path := range []string{"/var/run/docker.sock", "/run/docker.sock"} {
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				return errors.New("docker socket path is present or accessible")
			}
		}
	case "network":
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		dialer := net.Dialer{Timeout: 500 * time.Millisecond}
		for _, address := range []string{"1.1.1.1:80", "169.254.169.254:80"} {
			conn, err := dialer.DialContext(ctx, "tcp", address)
			if err == nil {
				_ = conn.Close()
				return errors.New("direct TCP egress succeeded")
			}
		}
		fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, syscall.IPPROTO_UDP)
		if err != nil {
			return err
		}
		defer func() { _ = syscall.Close(fd) }()
		err = syscall.Sendto(fd, []byte("escape-test"), 0, &syscall.SockaddrInet4{Port: 53, Addr: [4]byte{1, 1, 1, 1}})
		if err == nil {
			return errors.New("direct UDP egress succeeded")
		}
		resolver := net.Resolver{PreferGo: true}
		if _, err = resolver.LookupHost(ctx, "example.com"); err == nil {
			return errors.New("DNS egress succeeded")
		}
	default:
		return errors.New("unknown attack")
	}
	fmt.Println("contained")
	return nil
}
