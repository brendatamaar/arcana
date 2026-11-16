package containerd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	cgroup1 "github.com/containerd/cgroups/v3/cgroup1/stats"
	cgroup2 "github.com/containerd/cgroups/v3/cgroup2/stats"
	containerdclient "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"github.com/distribution/reference"
	specs "github.com/opencontainers/runtime-spec/specs-go"

	"github.com/brendatamaar/arcana/internal/runtime"
)

// containerdEngine is the production engine. None of it has run against a real containerd.
type containerdEngine struct {
	cfg    Config
	client *containerdclient.Client
}

func newContainerdEngine(cfg Config) (engine, error) {
	if err := os.MkdirAll(cfg.FIFODir, 0o700); err != nil {
		return nil, fmt.Errorf("creating the exec pipe directory %s (set a writable fifo_dir): %w", cfg.FIFODir, err)
	}
	c, err := containerdclient.New(cfg.Address, containerdclient.WithDefaultNamespace(cfg.Namespace))
	if err != nil {
		return nil, fmt.Errorf("connecting to containerd: %w", err)
	}
	return &containerdEngine{cfg: cfg, client: c}, nil
}

func (e *containerdEngine) Close() error { return e.client.Close() }

func (e *containerdEngine) ctx(ctx context.Context) context.Context {
	return namespaces.WithNamespace(ctx, e.cfg.Namespace)
}

func (e *containerdEngine) Create(ctx context.Context, p plan) error {
	ctx = e.ctx(ctx)
	// containerd stores images under their fully qualified name; Docker-style short names
	// (arcana-base:local) are normalised the way Docker would.
	named, err := reference.ParseDockerRef(p.Image)
	if err != nil {
		return runtime.Invalid{Field: "image"}
	}
	image, err := e.client.GetImage(ctx, named.String())
	if err != nil {
		return fmt.Errorf("template image %q is not in containerd namespace %q: %w", named.String(), e.cfg.Namespace, err)
	}
	imageSpec, err := image.Spec(ctx)
	if err != nil {
		return fmt.Errorf("reading the image config: %w", err)
	}
	// The image's environment (PATH, package-manager locations) applies, then the sandbox's own.
	// oci.WithImageConfig is not used: it resolves a named USER by mounting the rootfs in this
	// process, which a non-root client cannot do. The user is the numeric 1000:1000 set below.
	args := p.Args
	if len(args) == 0 {
		args = []string{"/bin/sleep", "infinity"}
	}
	opts := []oci.SpecOpts{oci.WithEnv(imageSpec.Config.Env), oci.WithProcessArgs(args...), oci.WithEnv(p.Env)}
	opts = append(opts, p.SpecOpts...)
	container, err := e.client.NewContainer(ctx, p.ID,
		containerdclient.WithImage(image),
		containerdclient.WithNewSnapshot(p.ID+"-rootfs", image),
		containerdclient.WithRuntime(e.cfg.shimName(), nil),
		containerdclient.WithAdditionalContainerLabels(p.Labels),
		containerdclient.WithNewSpec(opts...),
	)
	if err != nil {
		return err
	}
	// No IO: the init process is `sleep infinity`-like and its output is not kept.
	task, err := container.NewTask(ctx, cio.NullIO)
	if err != nil {
		return err
	}
	return task.Start(ctx)
}

func (e *containerdEngine) Delete(ctx context.Context, id string) error {
	ctx = e.ctx(ctx)
	container, err := e.client.LoadContainer(ctx, id)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if task, taskErr := container.Task(ctx, nil); taskErr == nil {
		status, _ := task.Wait(ctx)
		_ = task.Kill(ctx, syscall.SIGKILL, containerdclient.WithKillAll)
		select {
		case <-status:
		case <-time.After(10 * time.Second):
		}
		if _, delErr := task.Delete(ctx, containerdclient.WithProcessKill); delErr != nil && !errdefs.IsNotFound(delErr) {
			return delErr
		}
	} else if !errdefs.IsNotFound(taskErr) {
		return taskErr
	}
	if err = container.Delete(ctx, containerdclient.WithSnapshotCleanup); err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	return nil
}

func (e *containerdEngine) List(ctx context.Context) ([]containerInfo, error) {
	ctx = e.ctx(ctx)
	all, err := e.client.Containers(ctx, "labels.\"arcana.managed\"==true")
	if err != nil {
		return nil, err
	}
	out := make([]containerInfo, 0, len(all))
	for _, c := range all {
		info, infoErr := c.Info(ctx)
		if infoErr != nil {
			continue
		}
		stopped := true
		if task, taskErr := c.Task(ctx, nil); taskErr == nil {
			if status, statusErr := task.Status(ctx); statusErr == nil && status.Status == containerdclient.Running {
				stopped = false
			}
		}
		out = append(out, containerInfo{ID: info.ID, Labels: info.Labels, Stopped: stopped})
	}
	return out, nil
}

func (e *containerdEngine) Stats(ctx context.Context, id string) (runtime.Stats, error) {
	ctx = e.ctx(ctx)
	container, err := e.client.LoadContainer(ctx, id)
	if errdefs.IsNotFound(err) {
		return runtime.Stats{}, runtime.NotFound{ID: id}
	}
	if err != nil {
		return runtime.Stats{}, err
	}
	task, err := container.Task(ctx, nil)
	if err != nil {
		return runtime.Stats{}, runtime.NotFound{ID: id}
	}
	metric, err := task.Metrics(ctx)
	if err != nil {
		return runtime.Stats{}, err
	}
	data, err := typeurl.UnmarshalAny(metric.Data)
	if err != nil {
		return runtime.Stats{}, err
	}
	var stats runtime.Stats
	switch m := data.(type) {
	case *cgroup2.Metrics: // runc on a cgroup v2 host
		if m.CPU != nil {
			stats.CPUUsageNano = m.CPU.UsageUsec * 1000
		}
		if m.Memory != nil {
			stats.MemoryUsageBytes = m.Memory.Usage
		}
		if m.Pids != nil {
			stats.PIDs = m.Pids.Current
		}
	case *cgroup1.Metrics: // the gVisor shim reports v1-shaped metrics
		if m.CPU != nil && m.CPU.Usage != nil {
			stats.CPUUsageNano = m.CPU.Usage.Total
		}
		if m.Memory != nil && m.Memory.Usage != nil {
			stats.MemoryUsageBytes = m.Memory.Usage.Usage
		}
		if m.Pids != nil {
			stats.PIDs = m.Pids.Current
		}
	default:
		return runtime.Stats{}, runtime.Unsupported{Feature: fmt.Sprintf("metrics of type %T", data)}
	}
	return stats, nil
}

// Exec runs a process in the sandbox's task and adapts it to a runtime.ExecSession.
func (e *containerdEngine) Exec(ctx context.Context, id string, req runtime.ExecRequest) (runtime.ExecSession, error) {
	nsCtx := e.ctx(ctx)
	container, err := e.client.LoadContainer(nsCtx, id)
	if errdefs.IsNotFound(err) {
		return nil, runtime.NotFound{ID: id}
	}
	if err != nil {
		return nil, err
	}
	task, err := container.Task(nsCtx, nil)
	if err != nil {
		return nil, runtime.NotFound{ID: id}
	}
	base, err := container.Spec(nsCtx)
	if err != nil || base.Process == nil {
		return nil, fmt.Errorf("reading the container spec: %w", errors.Join(err))
	}
	process := *base.Process
	process.Args = req.Cmd
	process.Terminal = req.TTY
	if req.Cwd != "" {
		process.Cwd = req.Cwd
	}
	process.Env = append([]string(nil), base.Process.Env...)
	for k, v := range req.Env {
		process.Env = append(process.Env, k+"="+v)
	}
	if req.TTY {
		process.ConsoleSize = &specs.Box{Width: uint(req.Cols), Height: uint(req.Rows)}
	}
	return startProcess(nsCtx, task, process, req.TTY, e.cfg.FIFODir)
}

// session adapts a containerd process to runtime.ExecSession through in-process pipes.
type session struct {
	process containerdclient.Process
	nsCtx   context.Context
	stdin   io.WriteCloser
	stdout  *io.PipeReader
	stderr  *io.PipeReader
	done    chan struct{}
	once    sync.Once
	exit    runtime.ExitInfo
	exitErr error
}

func startProcess(ctx context.Context, task containerdclient.Task, spec specs.Process, tty bool, fifoDir string) (runtime.ExecSession, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	var stderrR *io.PipeReader
	var stderrW *io.PipeWriter
	opts := []cio.Opt{cio.WithStreams(stdinR, stdoutW, nil), cio.WithFIFODir(fifoDir)}
	if tty {
		opts = append(opts, cio.WithTerminal)
	} else {
		stderrR, stderrW = io.Pipe()
		opts = append(opts, cio.WithStreams(stdinR, stdoutW, stderrW), cio.WithFIFODir(fifoDir))
	}
	id := "x" + strconv.FormatInt(time.Now().UnixNano(), 36)
	process, err := task.Exec(ctx, id, &spec, cio.NewCreator(opts...))
	if err != nil {
		return nil, err
	}
	exited, err := process.Wait(ctx)
	if err != nil {
		_, _ = process.Delete(ctx)
		return nil, err
	}
	if err = process.Start(ctx); err != nil {
		_, _ = process.Delete(ctx, containerdclient.WithProcessKill)
		return nil, err
	}
	if tty && spec.ConsoleSize != nil {
		// runc honours the spec's console size; the gVisor shim does not, so send it as a resize too.
		_ = process.Resize(ctx, uint32(spec.ConsoleSize.Width), uint32(spec.ConsoleSize.Height)) // #nosec G115 -- the console size comes from uint16 request fields
	}
	s := &session{process: process, nsCtx: ctx, stdin: &stdinCloser{PipeWriter: stdinW, process: process, ctx: ctx}, stdout: stdoutR, stderr: stderrR, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		status := <-exited
		code, _, resultErr := status.Result()
		// Drain the IO before closing the streams so no output is lost.
		process.IO().Wait()
		s.exit, s.exitErr = runtime.ExitInfo{ExitCode: int(code)}, resultErr
		_ = stdoutW.Close()
		if stderrW != nil {
			_ = stderrW.Close()
		}
		_ = stdinR.Close()
		_, _ = process.Delete(context.WithoutCancel(ctx))
	}()
	return s, nil
}

func (s *session) Stdin() io.WriteCloser { return s.stdin }
func (s *session) Stdout() io.Reader     { return s.stdout }
func (s *session) Stderr() io.Reader {
	if s.stderr == nil {
		return nil
	}
	return s.stderr
}

func (s *session) Resize(cols, rows uint16) error {
	return s.process.Resize(s.nsCtx, uint32(cols), uint32(rows))
}

// Wait returns the exit; a cancelled context kills the process first.
func (s *session) Wait(ctx context.Context) (runtime.ExitInfo, error) {
	select {
	case <-s.done:
	case <-ctx.Done():
		s.once.Do(func() {
			_ = s.process.Kill(context.WithoutCancel(s.nsCtx), syscall.SIGKILL, containerdclient.WithKillAll)
		})
		<-s.done
		return runtime.ExitInfo{}, ctx.Err()
	}
	return s.exit, s.exitErr
}

// stdinCloser ends the process's stdin. Closing the local pipe only stops the copy into the FIFO;
// the shim keeps the process's stdin open until it is told to close it, so a command that reads to
// EOF (cat, tar) would wait forever without CloseIO. The pipe closes first, so every written byte has
// already been copied when the shim is told.
type stdinCloser struct {
	*io.PipeWriter
	process containerdclient.Process
	ctx     context.Context
	once    sync.Once
}

func (c *stdinCloser) Close() error {
	var err error
	c.once.Do(func() {
		err = c.PipeWriter.Close()
		if closeErr := c.process.CloseIO(context.WithoutCancel(c.ctx), containerdclient.WithStdinCloser); closeErr != nil && !errdefs.IsNotFound(closeErr) {
			err = errors.Join(err, closeErr)
		}
	})
	return err
}

func (e *containerdEngine) Version(ctx context.Context) (string, error) {
	v, err := e.client.Version(e.ctx(ctx))
	if err != nil {
		return "", err
	}
	return v.Version, nil
}

func (e *containerdEngine) HasImage(ctx context.Context, image string) bool {
	named, err := reference.ParseDockerRef(image)
	if err != nil {
		return false
	}
	_, err = e.client.GetImage(e.ctx(ctx), named.String())
	return err == nil
}
