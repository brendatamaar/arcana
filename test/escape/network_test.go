//go:build linux && escape

package escape

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/api"
	"github.com/brendatamaar/arcana/internal/audit"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	dockerruntime "github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/pkg/client"
)

// The trusted upstream changes its A answer after the first query. It has no
// sandbox privileges, host mounts or published ports; only the proxy uses it.
const rebindingDNS = `import socket,struct
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.bind(('0.0.0.0',53));seen=False
while True:
 q,peer=s.recvfrom(512)
 end=12
 while q[end]: end+=q[end]+1
 end+=5
 kind=struct.unpack('!H',q[end-4:end-2])[0]
 a=b''
 if kind==1:
  ip='127.0.0.1' if seen else '8.8.8.8';seen=True
  a=b'\xc0\x0c'+struct.pack('!HHIH',1,1,0,4)+socket.inet_aton(ip)
 r=q[:2]+struct.pack('!HHHHH',0x8180,1,int(bool(a)),0,0)+q[12:end]+a
 s.sendto(r,peer)
`

func TestEscapeV2_NetworkPublicAPI(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	requireRunsc(ctx, t)
	dns := docker(ctx, t, "run", "-d", "--network=bridge", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=64m", "--memory-swap=64m", "--pids-limit=32", "arcana-python:local", "python", "-c", rebindingDNS)
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 15*time.Second)
		defer done()
		docker(c, t, "rm", "-f", dns)
	})
	dnsIP := docker(ctx, t, "inspect", "--format", `{{(index .NetworkSettings.Networks "bridge").IPAddress}}`, dns)
	// Seed through the actual DNS wire protocol and prove the original answer was
	// public. Every later A response is loopback, including the proxy's lookup.
	resolver := &net.Resolver{PreferGo: true, Dial: func(c context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: time.Second}).DialContext(c, network, net.JoinHostPort(dnsIP, "53"))
	}}
	ready, stop := context.WithTimeout(ctx, 10*time.Second)
	defer stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		ips, err := resolver.LookupNetIP(ready, "ip4", "rebind.example")
		if err == nil {
			if len(ips) != 1 || ips[0].String() != "8.8.8.8" {
				t.Fatalf("initial DNS answer: %v", ips)
			}
			break
		}
		select {
		case <-ready.Done():
			t.Fatal("DNS fixture not ready", err)
		case <-tick.C:
		}
	}
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
	path, err := helper.Install(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := dockerruntime.New(dockerruntime.Config{OCIRuntime: "runsc", InstallID: db.InstallID, HelperPath: path, Network: &dockerruntime.NetworkConfig{SubnetPool: "10.202.0.0/16", SubnetPrefix: 28, ProxyImage: "arcana:local", ProxyDNS: []string{dnsIP}, OnDecision: func(d netpolicy.Decision) { recorder.Record("egress.decision", "", d.SandboxID, d) }}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = driver.Close() }()
	cfg := config.Default()
	cfg.Templates[0].Image = "arcana-python:local"
	cfg.Server.DestroyOnShutdown = true
	service := orchestrator.New(db, driver, cfg, orchestrator.SystemClock{}, recorder)
	defer func() {
		c, done := context.WithTimeout(context.Background(), 20*time.Second)
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
	allow := []string{"example.com", "rebind.example"}
	request := client.CreateSandboxRequest{Template: "base", Network: &client.Network{Mode: "allowlist", Allow: &allow}}
	target, err := c.CreateSandbox(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := c.CreateSandbox(ctx, request)
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
		docker(ctx, t, "info", "--format", "{{.ServerVersion}}")
	}
	denied := func(t *testing.T, host string) {
		t.Helper()
		run(t, target.Id, fmt.Sprintf("import urllib.request,urllib.error\ntry: urllib.request.urlopen(%q,timeout=5);raise RuntimeError('proxy allowed attack')\nexcept urllib.error.HTTPError as e: assert e.code==403,e.code", "http://"+host+"/"))
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
	t.Run("ESC15_DirectInternet", func(t *testing.T) {
		defer healthy(t)
		run(t, target.Id, "import socket\ns=socket.socket();s.settimeout(2)\ntry: s.connect(('1.1.1.1',443));raise RuntimeError('direct internet reachable')\nexcept OSError: pass")
	})
	t.Run("ESC16_NonAllowlisted", func(t *testing.T) { defer healthy(t); denied(t, "example.org") })
	t.Run("ESC17_ReservedAddresses", func(t *testing.T) {
		defer healthy(t)
		for _, host := range []string{"169.254.169.254", "127.0.0.1", "10.1.2.3", "172.16.1.1", "192.168.1.1", "[fd00::1]"} {
			denied(t, host)
		}
	})
	t.Run("ESC18_DNSRebinding", func(t *testing.T) { defer healthy(t); denied(t, "rebind.example") })
	t.Run("ESC19_PeerIP", func(t *testing.T) {
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
		// Inspect kernel/runtime metadata only; workspace data remains inside exec.
		instances, err := driver.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		for _, instance := range instances {
			if instance.SandboxID == peer.Id {
				id = instance.ID
			}
		}
		if id == "" {
			t.Fatal("peer container missing")
		}
		ip := docker(ctx, t, "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", id)
		if net.ParseIP(ip) == nil {
			t.Fatalf("invalid peer IP: %q", ip)
		}
		run(t, peer.Id, fmt.Sprintf("import socket;s=socket.create_connection((%q,8080),2);s.close()", ip))
		run(t, target.Id, fmt.Sprintf("import socket\ns=socket.socket();s.settimeout(2)\ntry: s.connect((%q,8080));raise RuntimeError('peer reachable')\nexcept OSError: pass", ip))
	})
}
