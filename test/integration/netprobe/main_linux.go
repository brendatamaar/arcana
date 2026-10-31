//go:build linux && (integration || escape)

// Command netprobe is a bounded Phase 0 CONNECT proxy and connectivity probe.
package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "proxy":
		server := &http.Server{Addr: ":3128", Handler: http.HandlerFunc(connect), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
		err = server.ListenAndServe()
	case "listen":
		server := &http.Server{Addr: ":8080", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second}
		err = server.ListenAndServe()
	case "check":
		if len(os.Args) < 3 {
			os.Exit(2)
		}
		target := os.Args[2]
		address := target
		if len(os.Args) == 4 {
			address = os.Args[3]
		}
		var conn net.Conn
		conn, err = net.DialTimeout("tcp", address, 3*time.Second)
		if err != nil {
			break
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if len(os.Args) == 4 {
			_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
			if err != nil {
				break
			}
			var response *http.Response
			response, err = http.ReadResponse(bufio.NewReader(io.LimitReader(conn, 8192)), &http.Request{Method: http.MethodConnect})
			if err == nil && response.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
				err = fmt.Errorf("CONNECT returned %s: %s", response.Status, body)
			}
		}
	default:
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("connected")
}

func connect(w http.ResponseWriter, r *http.Request) {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || r.Method != http.MethodConnect || !strings.EqualFold(host, "example.com") || port != "443" {
		http.Error(w, "denied", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// Only the trusted dual-homed proxy uses upstream DNS. Docker's loopback
	// resolver on user-defined bridges is not reachable by runsc's netstack.
	resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, "1.1.1.1:53")
	}}
	ips, err := resolver.LookupIP(ctx, "ip4", host)
	if err != nil || len(ips) == 0 {
		http.Error(w, fmt.Sprintf("DNS failed: %v", err), http.StatusBadGateway)
		return
	}
	for _, ip := range ips {
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			http.Error(w, "denied address", http.StatusForbidden)
			return
		}
	}
	// Dial the checked IP, never resolve the hostname a second time.
	var dialer net.Dialer
	upstream, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ips[0].String(), port))
	if err != nil {
		http.Error(w, fmt.Sprintf("upstream failed: %v", err), http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	_ = upstream.SetDeadline(time.Now().Add(10 * time.Second))
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unavailable", http.StatusInternalServerError)
		return
	}
	peer, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer peer.Close()
	_ = peer.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = buffer.Flush(); err != nil {
		return
	}
	done := make(chan struct{}, 1)
	go func() { _, _ = io.CopyN(upstream, buffer, 1<<20); _ = upstream.Close(); done <- struct{}{} }()
	_, _ = io.CopyN(peer, upstream, 1<<20)
	_ = peer.Close()
	<-done
}
