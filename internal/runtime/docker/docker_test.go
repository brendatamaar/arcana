package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/container"

	"github.com/brendatamaar/arcana/internal/runtime"
)

func testSpec() runtime.Spec {
	return runtime.Spec{SandboxID: "sbx_aaaaaaaaaaaaaaaaaaaa", Image: "test-image", Resources: runtime.Resources{CPUs: 0.5, MemoryMB: 128, DiskMB: 8, PIDs: 64}, WorkspaceBackend: "volume", NetworkMode: "none"}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "sbx-exec")
	if err := os.WriteFile(helper, []byte("helper fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{InstallID: "test-install", HelperPath: helper}
}

type fakeEngine struct {
	mu               sync.Mutex
	container        bool
	volume           bool
	foreign          bool
	startFailure     bool
	createFailure    bool
	extraImageVolume bool
	config           container.CreateRequest
	calls            []string
	execRequest      container.ExecCreateRequest
	resize           string
}

func (d *fakeEngine) serve(t *testing.T) http.Handler {
	version := regexp.MustCompile(`^/v[0-9.]+`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := version.ReplaceAllString(r.URL.Path, "")
		if p == "/_ping" {
			w.Header().Set("API-Version", "1.56")
			w.Header().Set("OSType", "linux")
			w.WriteHeader(200)
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		d.calls = append(d.calls, r.Method+" "+p)
		send := func(status int, value any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			if value != nil {
				if err := json.NewEncoder(w).Encode(value); err != nil {
					t.Error(err)
				}
			}
		}
		labels := map[string]string{"arcana.managed": "true", "arcana.instance": "test-install", "arcana.sandbox_id": testSpec().SandboxID}
		if d.foreign {
			labels["arcana.instance"] = "other-install"
		}
		switch {
		case r.Method == "GET" && p == "/images/test-image/json":
			volumes := map[string]any{}
			if d.extraImageVolume {
				volumes["/etc"] = map[string]any{}
			}
			send(200, map[string]any{"Id": "image-id", "Config": map[string]any{"Volumes": volumes}})
		case r.Method == "GET" && strings.HasPrefix(p, "/volumes/"):
			if !d.volume {
				send(404, map[string]string{"message": "missing"})
				return
			}
			send(200, map[string]any{"Name": sandboxName(testSpec().SandboxID) + "-ws", "Labels": labels})
		case r.Method == "POST" && p == "/volumes/create":
			d.volume = true
			send(201, map[string]any{"Name": sandboxName(testSpec().SandboxID) + "-ws", "Labels": labels})
		case r.Method == "POST" && p == "/containers/create":
			if err := json.NewDecoder(r.Body).Decode(&d.config); err != nil {
				t.Error(err)
			}
			if d.createFailure {
				send(500, map[string]string{"message": "injected"})
				return
			}
			d.container = true
			send(201, map[string]any{"Id": "container-id", "Warnings": []string{}})
		case r.Method == "POST" && strings.HasSuffix(p, "/start") && strings.HasPrefix(p, "/containers/"):
			if d.startFailure {
				send(500, map[string]string{"message": "injected"})
				return
			}
			send(204, nil)
		case r.Method == "GET" && p == "/containers/json":
			if r.URL.Query().Get("limit") != "1001" || !strings.Contains(r.URL.Query().Get("filters"), "arcana.instance=test-install") {
				t.Error("unbounded or unscoped list")
			}
			items := []any{}
			if d.container {
				items = append(items, map[string]any{"Id": "container-id", "Labels": labels})
			}
			send(200, items)
		case r.Method == "GET" && strings.HasSuffix(p, "/json") && strings.HasPrefix(p, "/containers/"):
			if !d.container {
				send(404, map[string]string{"message": "missing"})
				return
			}
			send(200, map[string]any{"Id": "container-id", "Name": "/" + sandboxName(testSpec().SandboxID), "Config": map[string]any{"Labels": labels}})
		case r.Method == "DELETE" && strings.HasPrefix(p, "/containers/"):
			d.container = false
			send(204, nil)
		case r.Method == "DELETE" && strings.HasPrefix(p, "/volumes/"):
			d.volume = false
			send(204, nil)
		case r.Method == "GET" && strings.HasSuffix(p, "/stats"):
			if r.URL.Query().Get("stream") != "false" || r.URL.Query().Get("one-shot") != "true" {
				t.Error("streaming stats requested")
			}
			send(200, map[string]any{"cpu_stats": map[string]any{"cpu_usage": map[string]any{"total_usage": 42}}, "memory_stats": map[string]any{"usage": 1024}, "pids_stats": map[string]any{"current": 2}})
		case r.Method == "POST" && strings.HasSuffix(p, "/exec"):
			if err := json.NewDecoder(r.Body).Decode(&d.execRequest); err != nil {
				t.Error(err)
			}
			send(201, map[string]string{"Id": "exec-id"})
		case r.Method == "POST" && p == "/exec/exec-id/start":
			var options struct {
				TTY bool `json:"Tty"`
			}
			if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
				t.Error(err)
			}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			d.mu.Unlock()
			defer d.mu.Lock()
			defer func() { _ = connection.Close() }()
			if _, err = io.WriteString(connection, "HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/vnd.docker.multiplexed-stream\r\n\r\n"); err != nil {
				return
			}
			if _, err = io.ReadAll(io.LimitReader(connection, 1024)); err != nil {
				return
			}
			if options.TTY {
				_, _ = io.WriteString(connection, "tty-output")
				return
			}
			for _, frame := range []struct {
				stream byte
				data   string
			}{{1, "stdout"}, {2, "stderr"}} {
				header := []byte{frame.stream, 0, 0, 0, 0, 0, 0, 6}
				if _, err = connection.Write(header); err != nil {
					return
				}
				if _, err = io.WriteString(connection, frame.data); err != nil {
					return
				}
			}
		case r.Method == "POST" && p == "/exec/exec-id/resize":
			d.resize = r.URL.RawQuery
			send(200, nil)
		case r.Method == "GET" && p == "/exec/exec-id/json":
			send(200, map[string]any{"ID": "exec-id", "Running": false, "ExitCode": 3})
		default:
			t.Errorf("unexpected Docker request %s %s", r.Method, p)
			send(500, map[string]string{"message": "unexpected"})
		}
	})
}

func engineDriver(t *testing.T, engine *fakeEngine) *Driver {
	t.Helper()
	server := httptest.NewServer(engine.serve(t))
	t.Cleanup(server.Close)
	config := testConfig(t)
	config.Host = strings.Replace(server.URL, "http://", "tcp://", 1)
	driver, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := driver.Close(); err != nil {
			t.Error(err)
		}
	})
	return driver
}

func TestCreate_EnforcesHardeningAndOwnership(t *testing.T) {
	engine := &fakeEngine{}
	driver := engineDriver(t, engine)
	ctx := context.Background()
	instance, err := driver.Create(ctx, testSpec())
	if err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	created := engine.config
	engine.mu.Unlock()
	cfg, host := created.Config, created.HostConfig
	if cfg.User != "1000:1000" || !cfg.NetworkDisabled || !host.ReadonlyRootfs || host.Runtime != "runsc" || host.NetworkMode != "none" || host.Init == nil || !*host.Init {
		t.Fatal("unsafe container config")
	}
	if len(host.CapDrop) != 1 || host.CapDrop[0] != "ALL" || len(host.SecurityOpt) != 1 || !strings.HasPrefix(host.SecurityOpt[0], "no-new-privileges") {
		t.Fatal("missing capabilities/security hardening")
	}
	if host.NanoCPUs != 500000000 || host.Memory != 128*megabyte || host.MemorySwap != host.Memory || host.PidsLimit == nil || *host.PidsLimit != 64 || len(host.Ulimits) != 1 || host.Ulimits[0].Hard != 1024 {
		t.Fatal("missing resource bounds")
	}
	if len(host.Mounts) != 2 || !host.Mounts[0].ReadOnly || host.Mounts[0].Target != "/opt/arcana/sbx-exec" || host.Mounts[1].Target != "/workspace" || host.Tmpfs["/tmp"] == "" || host.Tmpfs["/run/sbx"] == "" {
		t.Fatal("unsafe mounts")
	}
	if cfg.Labels["arcana.instance"] != "test-install" || cfg.Labels["arcana.sandbox_id"] != testSpec().SandboxID || host.LogConfig.Type != "none" {
		t.Fatal("unsafe metadata/log configuration")
	}
	if driver.Name() != "docker/runsc" || !driver.Capabilities().SupportsTTY {
		t.Fatal("wrong driver metadata")
	}
	instances, err := driver.List(ctx)
	if err != nil || len(instances) != 1 || instances[0].ID != instance.ID {
		t.Fatalf("list: %v %v", instances, err)
	}
	stats, err := driver.Stats(ctx, instance.ID)
	if err != nil || stats.CPUUsageNano != 42 || stats.MemoryUsageBytes != 1024 || stats.PIDs != 2 {
		t.Fatalf("stats: %+v %v", stats, err)
	}
	if err = driver.Destroy(ctx, instance.ID); err != nil {
		t.Fatal(err)
	}
	if err = driver.Destroy(ctx, instance.ID); err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.container || engine.volume {
		t.Fatal("resources leaked")
	}
}

func TestCreate_CleansPartialFailureAndRejectsUnsafeInputs(t *testing.T) {
	for _, startFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(startFailure), func(t *testing.T) {
			engine := &fakeEngine{startFailure: startFailure, createFailure: !startFailure}
			driver := engineDriver(t, engine)
			if _, err := driver.Create(context.Background(), testSpec()); err == nil {
				t.Fatal("injected failure ignored")
			}
			engine.mu.Lock()
			defer engine.mu.Unlock()
			if engine.container || engine.volume {
				t.Fatal("partial resources leaked")
			}
		})
	}
	config := testConfig(t)
	config.OCIRuntime = "runc"
	if _, err := New(config); err == nil {
		t.Fatal("runc guard bypassed")
	}
	for _, mutate := range []func(*runtime.Spec){func(s *runtime.Spec) { s.NetworkMode = "open" }, func(s *runtime.Spec) { s.Resources.CPUs = -1 }, func(s *runtime.Spec) { s.Resources.MemoryMB = 1 << 40 }, func(s *runtime.Spec) { s.Labels = map[string]string{"arcana.managed": "false"} }, func(s *runtime.Spec) { s.SandboxID = "../../host" }} {
		spec := testSpec()
		mutate(&spec)
		if err := validateSpec(spec); err == nil {
			t.Fatal("unsafe spec accepted")
		}
	}
	engine := &fakeEngine{extraImageVolume: true}
	driver := engineDriver(t, engine)
	if _, err := driver.Create(context.Background(), testSpec()); err == nil {
		t.Fatal("image with writable /etc accepted")
	}
	engine.mu.Lock()
	engine.extraImageVolume = false
	engine.container = true
	engine.volume = true
	engine.foreign = true
	before := len(engine.calls)
	engine.mu.Unlock()
	var missing runtime.NotFound
	if err := driver.Destroy(context.Background(), sandboxName(testSpec().SandboxID)); !errors.As(err, &missing) {
		t.Fatalf("unmanaged object accessed: %v", err)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if len(engine.calls) != before+1 || !engine.container || !engine.volume {
		t.Fatal("unmanaged object modified")
	}
}

func TestExec_DemultiplexesAndSupportsTTY(t *testing.T) {
	for _, tty := range []bool{false, true} {
		t.Run(fmt.Sprint(tty), func(t *testing.T) {
			engine := &fakeEngine{}
			driver := engineDriver(t, engine)
			ctx := context.Background()
			instance, err := driver.Create(ctx, testSpec())
			if err != nil {
				t.Fatal(err)
			}
			session, err := driver.Exec(ctx, instance.ID, runtime.ExecRequest{Cmd: []string{"test"}, TTY: tty})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = io.WriteString(session.Stdin(), "input"); err != nil {
				t.Fatal(err)
			}
			if tty {
				if err = session.Resize(120, 40); err != nil {
					t.Fatal(err)
				}
			}
			if err = session.Stdin().Close(); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr []byte
			var group sync.WaitGroup
			group.Add(1)
			go func() {
				defer group.Done()
				var err error
				stdout, err = io.ReadAll(session.Stdout())
				if err != nil {
					t.Error(err)
				}
			}()
			if session.Stderr() != nil {
				group.Add(1)
				go func() {
					defer group.Done()
					var err error
					stderr, err = io.ReadAll(session.Stderr())
					if err != nil {
						t.Error(err)
					}
				}()
			}
			group.Wait()
			if tty {
				if string(stdout) != "tty-output" || session.Stderr() != nil {
					t.Fatal("TTY output incorrect")
				}
			} else if string(stdout) != "stdout" || string(stderr) != "stderr" {
				t.Fatal("demultiplexed output incorrect")
			}
			result, err := session.Wait(ctx)
			if err != nil || result.ExitCode != 3 {
				t.Fatalf("exit: %+v %v", result, err)
			}
			if err = driver.Destroy(ctx, instance.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExec_DestroyAndCloseUnblockAbandonedStreams(t *testing.T) {
	engine := &fakeEngine{}
	driver := engineDriver(t, engine)
	instance, err := driver.Create(context.Background(), testSpec())
	if err != nil {
		t.Fatal(err)
	}
	session, err := driver.Exec(context.Background(), instance.ID, runtime.ExecRequest{Cmd: []string{"test"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = session.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	if err = driver.Destroy(context.Background(), instance.ID); err != nil {
		t.Fatal(err)
	}
	if err = driver.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(session.Stdout()); err == nil {
		t.Fatal("destroy failed to cancel abandoned stream")
	}
}

func TestDemultiplex_RejectsMalformedAndTruncatedFrames(t *testing.T) {
	for _, input := range [][]byte{{1}, {9, 0, 0, 0, 0, 0, 0, 0}, {1, 1, 0, 0, 0, 0, 0, 0}, {1, 0, 0, 0, 255, 255, 255, 255}} {
		if err := demultiplex(io.Discard, io.Discard, strings.NewReader(string(input))); err == nil {
			t.Fatal("invalid frame accepted")
		}
	}
}

func TestCreate_WaitingForSerializationRespectsCancellation(t *testing.T) {
	driver := engineDriver(t, &fakeEngine{})
	driver.createGate <- struct{}{}
	defer func() { <-driver.createGate }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := driver.Create(ctx, testSpec()); !errors.Is(err, context.Canceled) {
		t.Fatalf("queued create ignored cancellation: %v", err)
	}
}
