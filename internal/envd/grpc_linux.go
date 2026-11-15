package envd

import (
	"context"
	"errors"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"

	"github.com/brendatamaar/arcana/internal/envd/envdpb"
)

// maxListMessage leaves room for a full 8 MiB directory listing and its framing.
const maxListMessage = 16 * 1024 * 1024

// NewGRPCServer registers the daemon on a gRPC server with bounded messages and streams.
// The transport (a private Unix socket or host-only vsock) is the trust boundary, so the
// server uses no TLS.
func NewGRPCServer(s *Server) *grpc.Server {
	server := grpc.NewServer(
		grpc.MaxRecvMsgSize(2*MaxChunkBytes),
		grpc.MaxSendMsgSize(maxListMessage),
		grpc.MaxConcurrentStreams(uint32(s.cfg.MaxExecs+s.cfg.MaxFileOps+32)), // #nosec G115 -- both limits are validated to at most 1024 and 256
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
		// Shutdown must wait for handlers so cancelled execs finish their cleanup proof.
		grpc.WaitForHandlers(true),
	)
	envdpb.RegisterEnvdServer(server, s)
	return server
}

// Serve runs the server until ctx ends, then stops accepting, cancels running execs and
// waits for their cleanup (bounded by grace).
func Serve(ctx context.Context, server *grpc.Server, listener net.Listener, grace time.Duration) error {
	failed := make(chan error, 1)
	go func() { failed <- server.Serve(listener) }()
	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
	}
	stopped := make(chan struct{})
	go func() { server.Stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(grace):
		return errors.New("envd shutdown exceeded its grace period")
	}
	return nil
}
