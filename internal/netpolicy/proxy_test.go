package netpolicy

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

type resolverStub struct{ addresses []netip.Addr }

func (r resolverStub) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addresses, nil
}

func TestProxy_DenialAndPinnedAddress(t *testing.T) {
	for _, test := range []struct {
		name, target, ip string
		allowed          bool
	}{
		{"allowed", "http://example.com/", "1.1.1.1", true},
		{"wrong-host", "http://other.com/", "1.1.1.1", false},
		{"rebinding", "http://example.com/", "127.0.0.1", false},
		{"metadata", "http://169.254.169.254/", "169.254.169.254", false},
		{"ULA", "http://example.com/", "fd00::1", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := NewProxy(resolverStub{[]netip.Addr{netip.MustParseAddr(test.ip)}}, "secret")
			if err := p.SetPolicy(Policy{SandboxID: "sandbox", Source: "10.200.0.2", Allow: []string{"example.com"}}); err != nil {
				t.Fatal(err)
			}
			dialed := ""
			p.dial = func(_ context.Context, _, address string) (net.Conn, error) {
				dialed = address
				a, b := net.Pipe()
				go func() {
					defer func() { _ = b.Close() }()
					r, err := http.ReadRequest(bufio.NewReader(b))
					if err != nil {
						return
					}
					_ = r.Body.Close()
					_, _ = io.WriteString(b, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
				}()
				return a, nil
			}
			request := httptest.NewRequest("GET", test.target, nil)
			request.RemoteAddr = "10.200.0.2:1234"
			response := httptest.NewRecorder()
			p.ServeHTTP(response, request)
			if test.allowed {
				if response.Code != 200 || dialed != "1.1.1.1:80" {
					t.Fatal(response.Code, dialed)
				}
			} else if response.Code != 403 || dialed != "" {
				t.Fatal(response.Code, dialed)
			}
			event := <-p.events
			if event.Allowed != test.allowed {
				t.Fatal(event)
			}
		})
	}
}

func TestProxy_SourceControlAndQueueBounds(t *testing.T) {
	p := NewProxy(resolverStub{}, "secret")
	if err := p.SetPolicy(Policy{SandboxID: "sandbox", Source: "10.200.0.2", Allow: []string{"example.com"}}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r.RemoteAddr = "10.200.0.3:123"
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	for range 2000 {
		p.decision(Decision{})
	}
	if len(p.events) != 1024 || p.dropped.Load() == 0 {
		t.Fatal("queue unbounded")
	}
	for _, token := range []string{"", "wrong", "secret"} {
		r := httptest.NewRequest("PUT", "/policy", strings.NewReader(`{"sandbox_id":"s","source":"10.200.0.2","allow":["example.com"]}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		p.Control(w, r)
		if token == "secret" && w.Code != 204 || token != "secret" && w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
}

func TestProxy_MalformedAuthorityNeverRecordsCredentials(t *testing.T) {
	p := NewProxy(resolverStub{}, "secret")
	if err := p.SetPolicy(Policy{SandboxID: "sandbox", Source: "10.200.0.2", Allow: []string{"example.com"}}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("CONNECT", "http://example.com", nil)
	r.Host = "user:password@example.com:443"
	r.RemoteAddr = "10.200.0.2:123"
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	event := <-p.events
	if w.Code != 403 || strings.Contains(event.Host, "password") {
		t.Fatal(w.Code, event)
	}
}

func TestProxy_DeniedNumericPortIsAudited(t *testing.T) {
	p := NewProxy(resolverStub{}, "secret")
	if err := p.SetPolicy(Policy{SandboxID: "sandbox", Source: "10.200.0.2", Allow: []string{"example.com"}}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("CONNECT", "http://example.com", nil)
	r.Host = "example.com:22"
	r.RemoteAddr = "10.200.0.2:123"
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	event := <-p.events
	if w.Code != 403 || event.Allowed || event.Port != "22" {
		t.Fatal(w.Code, event)
	}
}
