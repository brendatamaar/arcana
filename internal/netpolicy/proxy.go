package netpolicy

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Decision contains metadata only, never headers, URLs, credentials or payload.
type Decision struct {
	SandboxID string `json:"sandbox_id"`
	Host      string `json:"host"`
	Port      string `json:"port"`
	Allowed   bool   `json:"allowed"`
	Bytes     int64  `json:"bytes"`
}

// Policy associates a sandbox source address with exact allowed names.
type Policy struct {
	SandboxID string   `json:"sandbox_id"`
	Source    string   `json:"source"`
	Allow     []string `json:"allow"`
}

// Resolver makes DNS resolution injectable and explicit.
type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// Proxy owns policy, connection admission and a bounded decision queue.
type Proxy struct {
	connections map[net.Conn]bool
	closed      bool
	handlers    sync.WaitGroup
	resolver    Resolver
	dial        func(context.Context, string, string) (net.Conn, error)
	mu          sync.RWMutex
	policy      Policy
	events      chan Decision
	slots       chan struct{}
	dropped     atomic.Uint64
	token       [32]byte
}

// NewProxy builds a default-deny proxy. token protects the private control socket.
func NewProxy(resolver Resolver, token string) *Proxy {
	return &Proxy{connections: make(map[net.Conn]bool), resolver: resolver, dial: (&net.Dialer{Timeout: 10 * time.Second}).DialContext, events: make(chan Decision, 1024), slots: make(chan struct{}, 128), token: sha256.Sum256([]byte(token))}
}

// SetPolicy validates an immutable copy before publishing it.
func (p *Proxy) SetPolicy(policy Policy) error {
	if len(policy.Allow) > 50 || policy.SandboxID == "" || len(policy.SandboxID) > 128 {
		return errors.New("invalid policy")
	}
	source, err := netip.ParseAddr(policy.Source)
	if err != nil {
		return err
	}
	policy.Source = source.Unmap().String()
	allowed := make([]string, 0, len(policy.Allow))
	for _, name := range policy.Allow {
		host, err := Host(name)
		if err != nil {
			return err
		}
		allowed = append(allowed, host)
	}
	policy.Allow = allowed
	p.mu.Lock()
	p.policy = policy
	p.mu.Unlock()
	return nil
}

func (p *Proxy) decision(event Decision) {
	select {
	case p.events <- event:
	default:
		p.dropped.Add(1)
	}
}

// Control serves policy updates and bounded event draining on a private Unix socket.
func (p *Proxy) Control(w http.ResponseWriter, r *http.Request) {
	token := sha256.Sum256([]byte(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
	if subtle.ConstantTimeCompare(token[:], p.token[:]) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	switch {
	case r.Method == "PUT" && r.URL.Path == "/policy":
		var policy Policy
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if decoder.Decode(&policy) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid policy", http.StatusBadRequest)
			return
		}
		if err := p.SetPolicy(policy); err != nil {
			http.Error(w, "invalid policy", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == "GET" && r.URL.Path == "/decisions":
		events := make([]Decision, 0, 1024)
		for range 1024 {
			select {
			case event := <-p.events:
				events = append(events, event)
			default:
				goto done
			}
		}
	done:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Events  []Decision `json:"events"`
			Dropped uint64     `json:"dropped"`
		}{events, p.dropped.Swap(0)})
	default:
		http.NotFound(w, r)
	}
}

func (p *Proxy) target(ctx context.Context, host, port string, policy Policy) (net.Conn, error) {
	canonical, err := Host(host)
	if err != nil || port != "80" && port != "443" {
		return nil, errors.New("denied target")
	}
	allowed := false
	for _, name := range policy.Allow {
		if canonical == name {
			allowed = true
		}
	}
	if !allowed {
		return nil, errors.New("hostname denied")
	}
	addresses, err := p.resolver.LookupNetIP(ctx, "ip", canonical)
	if err != nil || len(addresses) == 0 || len(addresses) > 64 {
		return nil, errors.New("DNS resolution failed")
	}
	for _, address := range addresses {
		if !Public(address) {
			return nil, errors.New("non-public address")
		}
	}
	return p.dial(ctx, "tcp", net.JoinHostPort(addresses[0].String(), port))
}

// ServeHTTP permits only HTTP forwarding and CONNECT to ports 80 and 443.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		http.Error(w, "closed", http.StatusServiceUnavailable)
		return
	}
	p.handlers.Add(1)
	p.mu.Unlock()
	defer p.handlers.Done()
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "proxy busy", http.StatusServiceUnavailable)
		return
	}
	p.mu.RLock()
	policy := p.policy
	p.mu.RUnlock()
	event := Decision{SandboxID: policy.SandboxID}
	defer func() { p.decision(event) }()
	source, _, err := net.SplitHostPort(r.RemoteAddr)
	ip, ipErr := netip.ParseAddr(source)
	if err != nil || ipErr != nil || ip.Unmap().String() != policy.Source {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var host, port string
	if r.Method == "CONNECT" {
		host, port, err = net.SplitHostPort(r.Host)
	} else {
		if r.URL.Scheme != "http" || r.URL.User != nil || r.URL.Host != r.Host || r.Header.Get("Upgrade") != "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		host = r.URL.Hostname()
		port = r.URL.Port()
		if port == "" {
			port = "80"
		}
	}
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	canonical, hostErr := Host(host)
	if number, err := strconv.ParseUint(port, 10, 16); err == nil && number > 0 {
		event.Port = strconv.FormatUint(number, 10)
	}
	if hostErr != nil {
		if ip, err := netip.ParseAddr(host); err == nil {
			event.Host = ip.Unmap().String()
		}
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	event.Host = canonical
	host = canonical
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	upstream, err := p.target(ctx, host, port, policy)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !p.track(upstream) {
		return
	}
	defer p.untrack(upstream)
	defer func() { _ = upstream.Close() }()
	_ = upstream.SetDeadline(time.Now().Add(5 * time.Minute))
	event.Allowed = true
	if r.Method == "CONNECT" {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "unsupported", http.StatusInternalServerError)
			return
		}
		client, buffer, err := hijacker.Hijack()
		if err != nil {
			return
		}
		if !p.track(client) {
			return
		}
		defer p.untrack(client)
		defer func() { _ = client.Close() }()
		_ = client.SetDeadline(time.Now().Add(5 * time.Minute))
		if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err = buffer.Flush(); err != nil {
			return
		}
		// A request context cannot own hijacked connections; both directions have a
		// finite deadline, and completion in either direction closes both sockets.
		done := make(chan int64, 1)
		go func() { n, _ := io.Copy(upstream, buffer); _ = upstream.Close(); _ = client.Close(); done <- n }()
		n, _ := io.Copy(client, upstream)
		_ = upstream.Close()
		_ = client.Close()
		event.Bytes = n + <-done
		return
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 16384, DialContext: func(context.Context, string, string) (net.Conn, error) { return upstream, nil }}
	defer transport.CloseIdleConnections()
	request := r.Clone(ctx)
	request.RequestURI = ""
	request.Close = true
	stripHop(request.Header)
	upload := &countedBody{ReadCloser: http.MaxBytesReader(w, r.Body, 50<<20)}
	request.Body = upload
	response, err := transport.RoundTrip(request)
	if err != nil {
		http.Error(w, "upstream failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = response.Body.Close() }()
	stripHop(response.Header)
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	event.Bytes, _ = io.Copy(w, io.LimitReader(response.Body, 64<<20))
	event.Bytes += upload.bytes.Load()
}

func stripHop(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}

// DNSResolver uses only operator-provided upstreams and never Docker embedded DNS.
func DNSResolver(upstreams []string) (*net.Resolver, error) {
	if len(upstreams) == 0 || len(upstreams) > 4 {
		return nil, errors.New("configure 1..4 proxy DNS IPs")
	}
	for _, ip := range upstreams {
		if _, err := netip.ParseAddr(ip); err != nil {
			return nil, err
		}
	}
	return &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(upstreams[0], strconv.Itoa(53)))
	}}, nil
}

// NewProxyHash accepts the hash passed to the proxy container, never the raw control secret.
func NewProxyHash(resolver Resolver, hash [32]byte) *Proxy {
	p := NewProxy(resolver, "")
	p.token = hash
	return p
}

func (p *Proxy) track(connection net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = connection.Close()
		return false
	}
	p.connections[connection] = true
	return true
}

func (p *Proxy) untrack(connection net.Conn) {
	p.mu.Lock()
	delete(p.connections, connection)
	p.mu.Unlock()
}

// Close stops admission and joins every HTTP and tunnel handler.
func (p *Proxy) Close() {
	p.mu.Lock()
	p.closed = true
	for connection := range p.connections {
		_ = connection.Close()
	}
	p.mu.Unlock()
	p.handlers.Wait()
}

type countedBody struct {
	io.ReadCloser
	bytes atomic.Int64
}

func (body *countedBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	body.bytes.Add(int64(n))
	return n, err
}
