package doctor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"strings"
	"testing"

	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
)

func TestLive_RuntimeRegistrationAndMissingTemplateFailClosed(t *testing.T) {
	if goruntime.GOOS != "linux" {
		t.Skip("runtime adapter requires Linux")
	}
	for _, registered := range []bool{false, true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("API-Version", "1.56")
			if strings.HasSuffix(r.URL.Path, "/_ping") {
				return
			}
			if strings.HasSuffix(r.URL.Path, "/info") {
				body := `{"Runtimes":{}}`
				if registered {
					body = `{"Runtimes":{"runsc":{"path":"runsc"}}}`
				}
				_, _ = w.Write([]byte(body))
				return
			}
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"not found"}`))
		}))
		cfg := config.Default()
		cfg.DataDir = t.TempDir()
		cfg.Runtime.Docker.Host = "tcp://" + strings.TrimPrefix(server.URL, "http://")
		_, err := (Live{Config: cfg}).Check(context.Background(), "runsc")
		if err == nil {
			t.Fatal("unavailable runtime proof accepted")
		}
		if registered && !strings.Contains(err.Error(), "template") {
			t.Fatal(err)
		}
		if !registered && !strings.Contains(err.Error(), "not registered") {
			t.Fatal(err)
		}
		if _, err = (Live{Config: cfg}).Check(context.Background(), "runc"); err == nil {
			t.Fatal("insecure runtime guard bypassed")
		}
		server.Close()
	}
}

func TestLive_HostObservationsAreBounded(t *testing.T) {
	live := Live{Config: config.Default()}
	detail, err := live.Check(context.Background(), "os")
	if (err == nil) != (goruntime.GOOS == "linux") {
		t.Fatal(detail, err)
	}
	if goruntime.GOOS != "linux" {
		return
	}
	for _, name := range []string{"kernel", "cgroups", "kvm"} {
		detail, err = live.Check(context.Background(), name)
		if err == nil && detail == "" {
			t.Fatal("empty host observation", name)
		}
	}
	if _, err = boundedFile("/proc/sys/kernel/osrelease", 1); err == nil {
		t.Fatal("host file cap ignored")
	}
}

func TestProbeExec_BoundsOutputAndRejectsDiagnostics(t *testing.T) {
	for _, script := range []fake.Script{{Stdout: []byte("ok")}, {Stdout: []byte(strings.Repeat("x", 5000))}, {Stderr: []byte("unexpected")}} {
		driver := fake.New()
		ctx := context.Background()
		instance, err := driver.Create(ctx, runtime.Spec{SandboxID: "sbx_aaaaaaaaaaaaaaaaaaaa", Image: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if err = driver.QueueExec(script); err != nil {
			t.Fatal(err)
		}
		data, _, err := probeExec(ctx, driver, instance.ID, []string{"true"})
		if string(script.Stdout) == "ok" {
			if err != nil || string(data) != "ok" {
				t.Fatal(string(data), err)
			}
		} else if err == nil {
			t.Fatal("untrusted probe output accepted")
		}
	}
}

type fakeProbe struct {
	fail  string
	calls []string
}

func (p *fakeProbe) Check(ctx context.Context, name string) (string, error) {
	p.calls = append(p.calls, name)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if p.fail == name {
		return "", errors.New("injected probe failure")
	}
	return "checked", nil
}

func TestRun_IndependentChecksAndBlockingPolicy(t *testing.T) {
	cfg := config.Default()
	for _, name := range []string{"", "os", "kernel", "cgroups", "docker", "data_dir", "database", "kvm", "runsc", "image:" + cfg.Templates[0].Image} {
		t.Run(name, func(t *testing.T) {
			probe := &fakeProbe{fail: name}
			report := Run(context.Background(), cfg, probe)
			wantFail := name != "" && name != "kernel" && name != "kvm" && !strings.HasPrefix(name, "image:")
			if report.Failed() != wantFail {
				t.Fatalf("blocking=%t: %+v", wantFail, report)
			}
			if len(probe.calls) != 9 {
				t.Fatal("independent checks skipped", probe.calls)
			}
			for _, result := range report.Checks {
				if result.Name == name && name != "" {
					if result.Status == "pass" || result.Hint == "" {
						t.Fatal(result)
					}
				}
			}
		})
	}
	cfg.Runtime.Docker.OCIRuntime = "runc"
	cfg.Runtime.Docker.AllowInsecureRuntime = true
	if report := Run(context.Background(), cfg, &fakeProbe{fail: "runsc"}); report.Failed() {
		t.Fatal("explicit runc opt-in blocked by optional runsc", report)
	}
	if report := Run(context.Background(), cfg, &fakeProbe{fail: "runc"}); !report.Failed() {
		t.Fatal("runc startup proof failure ignored")
	}
	if report := Run(context.Background(), cfg, &fakeProbe{fail: "kernel"}); !report.Failed() {
		t.Fatal("runc kernel requirement ignored")
	}
	cfg.Runtime.Docker.AllowInsecureRuntime = false
	if report := Run(context.Background(), cfg, &fakeProbe{}); !report.Failed() {
		t.Fatal("runc guard bypassed")
	}
}

func TestRun_CancellationFailsClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !Run(ctx, config.Default(), &fakeProbe{}).Failed() {
		t.Fatal("cancelled checks accepted")
	}
}

func TestLive_LocalStorageAndVersionBoundaries(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	live := Live{Config: cfg}
	for _, name := range []string{"data_dir", "database"} {
		if _, err := live.Check(context.Background(), name); err != nil {
			t.Fatal(name, err)
		}
	}
	for _, test := range []struct {
		value string
		want  bool
	}{{"24.0.9", true}, {"25.0.0", true}, {"23.9.9", false}, {"bad", false}, {"24", false}} {
		if got := versionAtLeast(test.value, 24, 0); got != test.want {
			t.Fatal(test)
		}
	}
	if _, err := boundedFile(t.TempDir()+"/missing", 10); err == nil {
		t.Fatal("missing host file accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := live.Check(ctx, "data_dir"); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestLive_DockerMetadataAndImageDiagnostics(t *testing.T) {
	for _, version := range []string{"24.0.0", "23.0.0"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("API-Version", "1.56")
			switch {
			case strings.HasSuffix(r.URL.Path, "/_ping"):
				w.WriteHeader(200)
			case strings.HasSuffix(r.URL.Path, "/info"):
				_, _ = w.Write([]byte(`{"ServerVersion":"` + version + `","Driver":"overlay2","CgroupVersion":"2"}`))
			default:
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{"message":"not found"}`))
			}
		}))
		cfg := config.Default()
		cfg.Runtime.Docker.Host = "tcp://" + strings.TrimPrefix(server.URL, "http://")
		live := Live{Config: cfg}
		_, err := live.Check(context.Background(), "docker")
		if (err == nil) != (version == "24.0.0") {
			t.Fatal(version, err)
		}
		if _, err = live.Check(context.Background(), "image:missing"); err == nil {
			t.Fatal("missing image accepted")
		}
		if _, err = live.Check(context.Background(), "unknown"); err == nil {
			t.Fatal("unknown probe accepted")
		}
		server.Close()
	}
}
