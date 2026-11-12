// Package mcp exposes a running Arcana API to MCP clients over a caller-supplied transport.
//
// The server is a thin client of the public API: every limit, ownership check and quota
// is enforced by Arcana itself, never re-implemented here.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/pkg/client"
)

// SessionLabel marks every sandbox created through one MCP session (PRD FR-MC-3).
const SessionLabel = "mcp_session"

const (
	maxTracked      = 1000
	cleanupTimeout  = 60 * time.Second
	maxReturnedText = 64 * 1024
	// DefaultReadBytes is what read_file returns to the model when max_bytes is omitted.
	DefaultReadBytes = 256 * 1024
	// MaxReadBytes is the most read_file may return to the model.
	MaxReadBytes = 1024 * 1024
	// MaxWriteBytes bounds write_file content.
	MaxWriteBytes = 1024 * 1024
)

// API is the subset of the Arcana client this server needs; *client.Client satisfies it.
type API interface {
	CreateSandbox(context.Context, client.CreateSandboxRequest) (client.Sandbox, error)
	DestroySandbox(context.Context, string) error
	Exec(context.Context, string, client.ExecRequest) (client.ExecResult, error)
	ReadFile(context.Context, string, string) (io.ReadCloser, error)
	WriteFile(context.Context, string, string, io.Reader) error
	ListFiles(context.Context, string, string, int64) (client.FileList, error)
	CreateSnapshot(context.Context, string) (client.Snapshot, error)
	Fork(context.Context, string, client.ForkRequest) (client.ForkResult, error)
}

var _ API = (*client.Client)(nil)

// Server is one MCP session's tool set and the sandboxes it must clean up.
type Server struct {
	api     API
	logger  *slog.Logger
	session string
	mu      sync.Mutex
	owned   map[string]bool
}

// New builds a server with a fresh random session ID. The logger must not write to the
// protocol stream (stdout); callers pass stderr.
func New(api API, logger *slog.Logger, version string) (*Server, *sdk.Server, error) {
	suffix, err := ids.New("ses")
	if err != nil {
		return nil, nil, fmt.Errorf("generating session ID: %w", err)
	}
	s := &Server{api: api, logger: logger, session: suffix, owned: make(map[string]bool)}
	server := sdk.NewServer(&sdk.Implementation{Name: "arcana", Version: version}, nil)
	s.register(server)
	return s, server, nil
}

// Session returns the ID stored in the mcp_session label of created sandboxes.
func (s *Server) Session() string { return s.session }

// Serve runs the protocol until the transport closes or ctx ends, then destroys every
// sandbox created by this session that the caller did not ask to keep.
func (s *Server) Serve(ctx context.Context, server *sdk.Server, transport sdk.Transport) error {
	err := server.Run(ctx, transport)
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	return errors.Join(ignoreClosed(err), s.cleanup(cleanup))
}

// ignoreClosed treats an ordinary end of the session as success.
func ignoreClosed(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, sdk.ErrConnectionClosed) {
		return nil
	}
	return err
}

func (s *Server) track(id string, keep bool) {
	if keep || id == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.owned) < maxTracked {
		s.owned[id] = true
	}
}

func (s *Server) untrack(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.owned, id)
}

func (s *Server) cleanup(ctx context.Context) error {
	s.mu.Lock()
	pending := make([]string, 0, len(s.owned))
	for id := range s.owned {
		pending = append(pending, id)
	}
	s.owned = make(map[string]bool)
	s.mu.Unlock()
	var result error
	for _, id := range pending {
		if err := s.api.DestroySandbox(ctx, id); err != nil {
			s.logger.WarnContext(ctx, "destroying session sandbox failed", "sandbox_id", id)
			result = errors.Join(result, fmt.Errorf("destroying %s: %w", id, err))
		}
	}
	return result
}
