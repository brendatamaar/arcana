//go:build linux && escape

package escape

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/api"
	"github.com/brendatamaar/arcana/internal/audit"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	containerdruntime "github.com/brendatamaar/arcana/internal/runtime/containerd"
	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/pkg/client"
)

// rebindingDNSServer is the proxy's trusted resolver. It answers the first A query for rebind.example with a public
// address and every later one with loopback, the DNS-rebinding trick ESC-18 uses, and relays every other name to a
// real resolver so allowlisted hosts still work. It has no other role.
func rebindingDNSServer(t *testing.T, address string) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var seen atomic.Bool
	go func() {
		buf := make([]byte, 512)
		for {
			n, peer, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			if n < 17 {
				continue
			}
			end, name := 12, ""
			for end < n && q[end] != 0 {
				if end+1+int(q[end]) > n {
					break
				}
				name += string(q[end+1:end+1+int(q[end])]) + "."
				end += int(q[end]) + 1
			}
			end += 5 // the terminating zero, QTYPE and QCLASS
			if end > n {
				continue
			}
			if name != "rebind.example." {
				go relayDNS(conn, peer, q)
				continue
			}
			reply := append([]byte{}, q[:2]...)
			answers := 0
			var answer []byte
			if binary.BigEndian.Uint16(q[end-4:]) == 1 { // type A
				ip := net.IPv4(8, 8, 8, 8)
				if seen.Swap(true) {
					ip = net.IPv4(127, 0, 0, 1)
				}
				answer = append([]byte{0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4}, ip.To4()...)
				answers = 1
			}
			reply = binary.BigEndian.AppendUint16(reply, 0x8180)
			reply = binary.BigEndian.AppendUint16(reply, 1)
			reply = binary.BigEndian.AppendUint16(reply, uint16(answers)) // #nosec G115 -- 0 or 1
			reply = append(reply, 0, 0, 0, 0)
			reply = append(reply, q[12:end]...)
			reply = append(reply, answer...)
			_, _ = conn.WriteTo(reply, peer)
		}
	}()
}

func relayDNS(conn net.PacketConn, peer net.Addr, query []byte) {
	upstream, err := net.DialTimeout("udp4", "1.1.1.1:53", 3*time.Second)
	if err != nil {
		return
	}
	defer func() { _ = upstream.Close() }()
	_ = upstream.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = upstream.Write(query); err != nil {
		return
	}
	reply := make([]byte, 1500)
	if n, err := upstream.Read(reply); err == nil {
		_, _ = conn.WriteTo(reply[:n], peer)
	}
}

// TestEscapeV2_ContainerdNetworkPublicAPI is ESC-15 to ESC-19 against the containerd driver's allowlist networking,
// through the public API. It needs root, the CNI plugins and the proxy image, so scripts/containerd-escape.sh runs it
// as root inside a throwaway containerd; elsewhere it skips with the reason.
func TestEscapeV2_ContainerdNetworkPublicAPI(t *testing.T) {
	address, cni := os.Getenv("ARCANA_ESCAPE_CONTAINERD_ADDRESS"), os.Getenv("ARCANA_ESCAPE_CNI_BIN_DIR")
	proxyImage, pythonImage := os.Getenv("ARCANA_ESCAPE_PROXY_IMAGE"), os.Getenv("ARCANA_ESCAPE_PYTHON_IMAGE")
	if address == "" || cni == "" || proxyImage == "" || pythonImage == "" || os.Geteuid() != 0 {
		t.Skip("needs root and ARCANA_ESCAPE_CONTAINERD_ADDRESS, ARCANA_ESCAPE_CNI_BIN_DIR, ARCANA_ESCAPE_PROXY_IMAGE, ARCANA_ESCAPE_PYTHON_IMAGE (scripts/containerd-escape.sh)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	// The proxy reaches this resolver through the egress bridge's gateway address.
	dnsServer := "172.29.0.1"
	rebindingDNSServer(t, "0.0.0.0:53")

	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "arcana.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	recorder, err := audit.New(audit.SQLBackend{Store: db}, audit.SystemClock{}, 30, 256, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		c, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := recorder.Close(c); err != nil {
			t.Error(err)
		}
	}()
	helperPath, err := helper.Install(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "fifo")
	oci := escapeRuntime()
	driver, err := containerdruntime.New(containerdruntime.Config{
		Address: address, Namespace: "arcana", OCIRuntime: oci, AllowInsecureRuntime: oci == "runc", InstallID: db.InstallID, HelperPath: helperPath, FIFODir: fifo,
		Network: &containerdruntime.NetworkConfig{
			ProxyImage: proxyImage, ProxyDNS: []string{dnsServer}, CNIBinDir: cni,
			OnDecision: func(d netpolicy.Decision) { recorder.Record("egress.decision", "", d.SandboxID, d) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = driver.Close() }()
	cfg := config.Default()
	cfg.Templates[0].Image = pythonImage
	cfg.Workspace.Backend = "tmpfs"
	cfg.Runtime.Driver = "containerd"
	cfg.Runtime.Containerd.Allowlist = true
	cfg.Server.DestroyOnShutdown = true
	service := orchestrator.New(db, driver, cfg, orchestrator.SystemClock{}, recorder)
	defer func() {
		c, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if err := service.Shutdown(c); err != nil {
			t.Error(err)
		}
	}()
	keys := auth.New(db, auth.SystemClock{}, recorder)
	_, key, err := keys.Create(ctx, "network-attacker", true)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(api.New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, service, api.Options{Audit: recorder}))
	defer server.Close()
	c, err := client.New(server.URL, key, client.Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Different allowlists, so a sandbox that reached its peer's proxy would be allowed what it must not be.
	targetAllow, peerAllow := []string{"example.com", "rebind.example"}, []string{"example.org"}
	target, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base", Network: &client.Network{Mode: "allowlist", Allow: &targetAllow}})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := c.CreateSandbox(ctx, client.CreateSandboxRequest{Template: "base", Network: &client.Network{Mode: "allowlist", Allow: &peerAllow}})
	if err != nil {
		t.Fatal(err)
	}
	run := func(t *testing.T, id, source string) string {
		t.Helper()
		r, err := c.Exec(ctx, id, client.ExecRequest{Cmd: []string{"python", "-c", source}})
		if err != nil || r.ExitCode != 0 {
			t.Fatalf("probe: %+v %v", r, err)
		}
		return r.Stdout
	}
	healthy := func(t *testing.T) {
		t.Helper()
		if _, err := c.Health(ctx); err != nil {
			t.Error(err)
		}
		run(t, peer.Id, "print('healthy')")
		if _, err := driver.Check(ctx); err != nil {
			t.Errorf("containerd driver unhealthy: %v", err)
		}
	}
	fetch := func(host string) string {
		return fmt.Sprintf("import urllib.request,urllib.error\ntry:\n print(urllib.request.urlopen(%q,timeout=15).status)\nexcept urllib.error.HTTPError as e:\n print('HTTP',e.code)", "http://"+host+"/")
	}
	denied := func(t *testing.T, host string) {
		t.Helper()
		if out := strings.TrimSpace(run(t, target.Id, fetch(host))); out != "HTTP 403" {
			t.Fatalf("the proxy must refuse %s with 403, got %q", host, out)
		}
		if err := driver.DrainDecisions(ctx); err != nil {
			t.Fatal(err)
		}
		deadline, finish := context.WithTimeout(ctx, 5*time.Second)
		defer finish()
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			events, err := c.ListAudit(deadline, client.AuditOptions{Action: "egress.decision", SandboxID: target.Id, Limit: 1000})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events.Items {
				if event.Details["host"] == strings.Trim(host, "[]") && event.Details["allowed"] == false {
					return
				}
			}
			select {
			case <-deadline.Done():
				t.Fatalf("missing denial audit for %s", host)
			case <-ticker.C:
			}
		}
	}
	t.Run("Allowlisted", func(t *testing.T) {
		defer healthy(t)
		if out := strings.TrimSpace(run(t, target.Id, fetch("example.com"))); out != "200" {
			t.Fatalf("the allowlisted host must work through the proxy: %q", out)
		}
	})
	t.Run("ESC15_DirectInternet", func(t *testing.T) {
		defer healthy(t)
		run(t, target.Id, "import socket\ns=socket.socket();s.settimeout(2)\ntry: s.connect(('1.1.1.1',443));raise RuntimeError('direct internet reachable')\nexcept OSError: pass")
	})
	t.Run("ESC16_NonAllowlisted", func(t *testing.T) { defer healthy(t); denied(t, "example.org") })
	t.Run("ESC17_ReservedAddresses", func(t *testing.T) {
		defer healthy(t)
		for _, host := range []string{"169.254.169.254", "127.0.0.1", "10.1.2.3", "172.16.1.1", "192.168.1.1", "172.29.0.1", "[fd00::1]"} {
			denied(t, host)
		}
	})
	t.Run("ESC18_DNSRebinding", func(t *testing.T) { defer healthy(t); denied(t, "rebind.example") })
	t.Run("ESC19_PeerSandbox", func(t *testing.T) {
		defer healthy(t)
		timeout := int64(60)
		stream, err := c.ExecStream(ctx, peer.Id, client.StreamRequest{ExecRequest: client.ExecRequest{Cmd: []string{"python", "-u", "-c", "import socket;s=socket.socket();s.bind(('0.0.0.0',8080));s.listen(8);print('ready')\nwhile True:\n c,a=s.accept();c.close()"}, TimeoutSeconds: &timeout}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = stream.Close() }()
		if line, err := bufio.NewReader(stream.Stdout).ReadString('\n'); err != nil || line != "ready\n" {
			t.Fatalf("peer listener: %q %v", line, err)
		}
		// Every sandbox has the same link addresses, each on its own segment: the peer's address, the proxy's
		// address and a guess at another sandbox's proxy port all lead nowhere but the sandbox's own proxy.
		for _, addr := range []string{"169.254.77.2", "169.254.77.3"} {
			run(t, target.Id, fmt.Sprintf("import socket\ns=socket.socket();s.settimeout(2)\ntry: s.connect((%q,8080));raise RuntimeError('reached a listener on the link')\nexcept OSError: pass", addr))
		}
		// The peer's own proxy applies the peer's allowlist: example.org is allowed for it (what the site then
		// answers is not the proxy's business), while the same name was denied for the target in ESC-16.
		run(t, peer.Id, fetch("example.org"))
		if err := driver.DrainDecisions(ctx); err != nil {
			t.Fatal(err)
		}
		events, err := c.ListAudit(ctx, client.AuditOptions{Action: "egress.decision", SandboxID: peer.Id, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		var allowed bool
		for _, event := range events.Items {
			allowed = allowed || (event.Details["host"] == "example.org" && event.Details["allowed"] == true)
		}
		if !allowed {
			t.Fatalf("the peer's allowlist is its own, so example.org must be allowed for it: %+v", events.Items)
		}
	})
}
