package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/brendatamaar/arcana/internal/netpolicy"
)

func newProxyCommand() *cobra.Command {
	return &cobra.Command{Use: "proxy", Short: "Run the isolated egress proxy", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		encoded, err := hex.DecodeString(os.Getenv("ARCANA_PROXY_TOKEN_HASH"))
		if err != nil || len(encoded) != 32 {
			return errors.New("proxy requires a control token hash")
		}
		resolver, err := netpolicy.DNSResolver(strings.Split(os.Getenv("ARCANA_PROXY_DNS"), ","))
		if err != nil {
			return err
		}
		var hash [32]byte
		copy(hash[:], encoded)
		proxy := netpolicy.NewProxyHash(resolver, hash)
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		tcp, err := net.Listen("tcp", ":3128") // #nosec G102 -- isolated proxy only, no published ports; every request checks its sandbox source IP.
		if err != nil {
			return err
		}
		defer func() { _ = tcp.Close() }()
		unix, err := net.Listen("unix", "/run/control/proxy.sock")
		if err != nil {
			return err
		}
		defer func() { _ = unix.Close() }()
		if err = os.Chmod("/run/control/proxy.sock", 0o600); err != nil {
			return err
		}
		servers := []*http.Server{
			{Handler: proxy, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384, BaseContext: func(net.Listener) context.Context { return ctx }},
			{Handler: http.HandlerFunc(proxy.Control), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16384, BaseContext: func(net.Listener) context.Context { return ctx }},
		}
		done := make(chan error, 2)
		go func() { done <- servers[0].Serve(netpolicy.BoundListener(tcp, 128)) }()
		go func() { done <- servers[1].Serve(netpolicy.BoundListener(unix, 16)) }()
		var result error
		select {
		case <-ctx.Done():
		case result = <-done:
		}
		cancel()
		for _, server := range servers {
			_ = server.Close()
		}
		proxy.Close()
		// A server result was already consumed only when it triggered shutdown.
		if result != nil {
			<-done
		} else {
			<-done
			<-done
		}
		if errors.Is(result, http.ErrServerClosed) {
			return nil
		}
		return result
	}}
}

// newProxyControlCommand is a trusted exec-only bridge to the container-local socket.
func newProxyControlCommand() *cobra.Command {
	return &cobra.Command{Use: "proxy-control", Hidden: true, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var input struct {
			Token, Method, Path string
			Body                []byte
		}
		decoder := json.NewDecoder(io.LimitReader(cmd.InOrStdin(), 32769))
		if err := decoder.Decode(&input); err != nil {
			return errors.New("invalid control request")
		}
		if input.Method != "GET" && input.Method != "PUT" || input.Path != "/policy" && input.Path != "/decisions" {
			return errors.New("invalid control operation")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
		defer cancel()
		transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", "/run/control/proxy.sock")
		}}
		defer transport.CloseIdleConnections()
		request, err := http.NewRequestWithContext(ctx, input.Method, "http://control"+input.Path, bytes.NewReader(input.Body))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+input.Token)
		response, err := (&http.Client{Transport: transport, Timeout: 2 * time.Second}).Do(request)
		if err != nil {
			return errors.New("proxy control unavailable")
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != 200 && response.StatusCode != 204 {
			return errors.New("proxy control rejected request")
		}
		if response.StatusCode == 204 {
			_, err = io.WriteString(cmd.OutOrStdout(), "{}")
			return err
		}
		_, err = io.Copy(cmd.OutOrStdout(), io.LimitReader(response.Body, 1<<20))
		return err
	}}
}
