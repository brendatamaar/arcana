package firecracker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedCall struct {
	Method, Path string
	Body         map[string]any
}

// fakeAPI is a Firecracker API server on a unix socket that records calls.
type fakeAPI struct {
	mu     sync.Mutex
	calls  []recordedCall
	status map[string]int // path -> forced status
	onCall func(recordedCall)
}

func serveFakeAPI(t *testing.T, socket string, api *fakeAPI) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		call := recordedCall{Method: r.Method, Path: r.URL.Path}
		_ = json.Unmarshal(raw, &call.Body)
		api.mu.Lock()
		api.calls = append(api.calls, call)
		code := api.status[r.URL.Path]
		api.mu.Unlock()
		if api.onCall != nil {
			api.onCall(call)
		}
		if code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"fault_message":"rejected by the fake"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

func TestConfigure_SendsEveryDeviceBeforeStartInOrder(t *testing.T) {
	dir, err := os.MkdirTemp("", "fc")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "a")
	api := &fakeAPI{}
	serveFakeAPI(t, socket, api)
	cfg := testConfig()
	plan, _ := planVM(cfg, testSpec())
	l := layoutFor(cfg, plan.VMID)
	if err = newAPIClient(socket).configure(context.Background(), plan, l); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, c := range api.calls {
		if c.Method != http.MethodPut {
			t.Fatalf("unexpected method %s", c.Method)
		}
		paths = append(paths, c.Path)
	}
	if got, want := strings.Join(paths, " "), "/boot-source /machine-config /drives/rootfs /drives/workspace /vsock /actions"; got != want {
		t.Fatalf("order\n got %s\nwant %s", got, want)
	}
	boot := api.calls[0].Body
	if boot["kernel_image_path"] != "/kernel" || !strings.Contains(boot["boot_args"].(string), "arcana.workspace=/dev/vdb") {
		t.Fatalf("boot source %v", boot)
	}
	machine := api.calls[1].Body
	if machine["vcpu_count"] != float64(2) || machine["mem_size_mib"] != float64(512) || machine["smt"] != false {
		t.Fatalf("machine %v", machine)
	}
	if root := api.calls[2].Body; root["is_read_only"] != true || root["is_root_device"] != true || root["path_on_host"] != "/rootfs.ext4" {
		t.Fatalf("the root drive must be read-only: %v", root)
	}
	if ws := api.calls[3].Body; ws["is_read_only"] != false || ws["is_root_device"] != false {
		t.Fatalf("workspace drive %v", ws)
	}
	if v := api.calls[4].Body; v["guest_cid"] != float64(3) || v["uds_path"] != "/v.sock" {
		t.Fatalf("vsock %v", v)
	}
	if api.calls[5].Body["action_type"] != "InstanceStart" {
		t.Fatal("last call must start the instance")
	}
	for _, c := range api.calls {
		if strings.Contains(c.Path, "network") {
			t.Fatalf("mode none must not configure a network interface: %s", c.Path)
		}
	}
}

func TestConfigure_TmpfsWorkspaceHasNoSecondDrive(t *testing.T) {
	dir, _ := os.MkdirTemp("", "fc")
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "a")
	api := &fakeAPI{}
	serveFakeAPI(t, socket, api)
	cfg := testConfig()
	spec := testSpec()
	spec.WorkspaceBackend = "tmpfs"
	plan, _ := planVM(cfg, spec)
	if err := newAPIClient(socket).configure(context.Background(), plan, layoutFor(cfg, plan.VMID)); err != nil {
		t.Fatal(err)
	}
	for _, c := range api.calls {
		if c.Path == "/drives/workspace" {
			t.Fatal("tmpfs workspace must not attach a disk")
		}
	}
}

func TestConfigure_StopsAtTheFirstRejectionAndKeepsTheFaultMessage(t *testing.T) {
	dir, _ := os.MkdirTemp("", "fc")
	defer func() { _ = os.RemoveAll(dir) }()
	socket := filepath.Join(dir, "a")
	api := &fakeAPI{status: map[string]int{"/machine-config": http.StatusBadRequest}}
	serveFakeAPI(t, socket, api)
	cfg := testConfig()
	plan, _ := planVM(cfg, testSpec())
	err := newAPIClient(socket).configure(context.Background(), plan, layoutFor(cfg, plan.VMID))
	var apiErr apiError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 || apiErr.Fault != "rejected by the fake" {
		t.Fatalf("%v", err)
	}
	if len(api.calls) != 2 {
		t.Fatalf("configuration continued after a rejection: %d calls", len(api.calls))
	}
}

func TestDialGuest_HandshakeAndNoLostBytes(t *testing.T) {
	dir, _ := os.MkdirTemp("", "fc")
	defer func() { _ = os.RemoveAll(dir) }()
	uds := filepath.Join(dir, "v")
	listener, err := net.Listen("unix", uds)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	reply := "OK 1073741824\nGUEST-BYTES"
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				line := make([]byte, 0, 32)
				one := make([]byte, 1)
				for {
					if _, readErr := conn.Read(one); readErr != nil {
						return
					}
					line = append(line, one[0])
					if one[0] == '\n' {
						break
					}
				}
				if string(line) == "CONNECT 1024\n" {
					_, _ = conn.Write([]byte(reply))
				} else {
					_, _ = conn.Write([]byte("NOPE\n"))
				}
			}()
		}
	}()
	conn, err := dialGuest(context.Background(), uds, 1024)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(io.LimitReader(conn, 11))
	_ = conn.Close()
	if string(data) != "GUEST-BYTES" {
		t.Fatalf("bytes after the OK line were lost or altered: %q", data)
	}
	if _, err = dialGuest(context.Background(), uds, 7); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("a refused port must fail: %v", err)
	}
	if _, err = dialGuest(context.Background(), filepath.Join(dir, "missing"), 1024); err == nil {
		t.Fatal("a missing socket must fail")
	}
}

func TestDialGuest_RejectsEndlessReplies(t *testing.T) {
	dir, _ := os.MkdirTemp("", "fc")
	defer func() { _ = os.RemoveAll(dir) }()
	uds := filepath.Join(dir, "v")
	listener, err := net.Listen("unix", uds)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for range 1000 {
			if _, writeErr := conn.Write([]byte("OKOKOKOKOKOKOKOK")); writeErr != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = dialGuest(ctx, uds, 1024); err == nil {
		t.Fatal("an unbounded handshake reply was accepted")
	}
}
