// Command runsc-exec probes the Docker lifecycle and attached exec API.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

type measurement struct {
	Runtime string    `json:"runtime"`
	Samples []float64 `json:"create_start_ms"`
	P50     float64   `json:"p50_ms"`
	P95     float64   `json:"p95_ms"`
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	results, err := run(ctx)
	if err == nil {
		err = json.NewEncoder(os.Stdout).Encode(results)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) ([]measurement, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	pull, err := cli.ImagePull(ctx, "debian:stable-slim", client.ImagePullOptions{})
	if err != nil {
		return nil, err
	}
	if err = pull.Wait(ctx); err != nil {
		return nil, err
	}
	var results []measurement
	// runc is used only for the explicitly requested comparison, never as fallback.
	for _, runtimeName := range []string{"runc", "runsc"} {
		m := measurement{Runtime: runtimeName}
		for n := 0; n < 20; n++ {
			elapsed, err := trial(ctx, cli, runtimeName, n == 0)
			if err != nil {
				return nil, fmt.Errorf("%s trial %d: %w", runtimeName, n, err)
			}
			m.Samples = append(m.Samples, float64(elapsed.Microseconds())/1000)
		}
		sorted := append([]float64(nil), m.Samples...)
		sort.Float64s(sorted)
		m.P50, m.P95 = sorted[9], sorted[18]
		results = append(results, m)
	}
	return results, nil
}

func trial(ctx context.Context, cli *client.Client, runtimeName string, checkExec bool) (time.Duration, error) {
	pids := int64(64)
	started := time.Now()
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: "debian:stable-slim", User: "1000:1000", Cmd: []string{"sleep", "infinity"}, Labels: map[string]string{"arcana.managed": "true", "arcana.instance": "phase0-spike", "arcana.spike": "001"}},
		HostConfig: &container.HostConfig{Runtime: runtimeName, NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=16m,mode=1777", "/run/sbx": "rw,nosuid,nodev,size=4m,uid=1000,gid=1000,mode=0700"}, Resources: container.Resources{Memory: 256 << 20, MemorySwap: 256 << 20, NanoCPUs: 500000000, PidsLimit: &pids}},
	})
	if err != nil {
		return 0, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, removeErr := cli.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); removeErr != nil {
			fmt.Fprintln(os.Stderr, "cleanup:", removeErr)
		}
	}()
	if _, err = cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return 0, err
	}
	elapsed := time.Since(started)
	if !checkExec {
		return elapsed, nil
	}
	inspect, err := cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		return 0, err
	}
	h := inspect.Container.HostConfig
	if !h.ReadonlyRootfs || h.NetworkMode != "none" || inspect.Container.Config.User != "1000:1000" || h.Runtime != runtimeName || len(h.CapDrop) != 1 || h.CapDrop[0] != "ALL" || !strings.Contains(strings.Join(h.SecurityOpt, ","), "no-new-privileges") || len(inspect.Container.Mounts) != 0 || h.Memory != 256<<20 || h.MemorySwap != h.Memory || h.PidsLimit == nil || *h.PidsLimit != pids || h.NanoCPUs != 500000000 || h.Tmpfs["/tmp"] == "" || h.Tmpfs["/run/sbx"] == "" {
		return 0, fmt.Errorf("hardening differs from requested configuration")
	}
	if err = execProbe(ctx, cli, created.ID, false); err != nil {
		return 0, err
	}
	return elapsed, execProbe(ctx, cli, created.ID, true)
}

func execProbe(ctx context.Context, cli *client.Client, id string, tty bool) error {
	command := `read line; printf 'out:%s\n' "$line"; printf 'err\n' >&2`
	if tty {
		command = `read line; stty size; printf 'tty:%s\n' "$line"`
	}
	created, err := cli.ExecCreate(ctx, id, client.ExecCreateOptions{Cmd: []string{"/bin/sh", "-c", command}, AttachStdin: true, AttachStdout: true, AttachStderr: true, TTY: tty})
	if err != nil {
		return err
	}
	attached, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{TTY: tty})
	if err != nil {
		return err
	}
	defer attached.Close()
	if tty {
		if _, err = cli.ExecResize(ctx, created.ID, client.ExecResizeOptions{Height: 35, Width: 101}); err != nil {
			return err
		}
	}
	if _, err = io.WriteString(attached.Conn, "arcana\n"); err != nil {
		return err
	}
	if !tty {
		if err = attached.CloseWrite(); err != nil {
			return err
		}
	}
	var stdout, stderr bytes.Buffer
	if tty {
		_, err = io.Copy(&stdout, io.LimitReader(attached.Reader, 1<<20))
	} else {
		_, err = stdcopy.StdCopy(&stdout, &stderr, io.LimitReader(attached.Reader, 1<<20))
	}
	if err != nil {
		return err
	}
	if tty {
		if !strings.Contains(stdout.String(), "35 101") || !strings.Contains(stdout.String(), "tty:arcana") {
			return fmt.Errorf("TTY resize/output: %q", stdout.String())
		}
	} else if stdout.String() != "out:arcana\n" || stderr.String() != "err\n" {
		return fmt.Errorf("demux: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	status, err := cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
	if err != nil {
		return err
	}
	if status.Running || status.ExitCode != 0 {
		return fmt.Errorf("unexpected exec status: %+v", status)
	}
	return nil
}
