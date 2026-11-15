package envd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/brendatamaar/arcana/internal/envd/envdpb"
	"github.com/brendatamaar/arcana/internal/pathutil"
)

// User is the sandbox identity commands and file operations run as.
type User struct{ UID, GID uint32 }

// Config holds the daemon settings. Zero values take the documented defaults.
type Config struct {
	Version  string
	Self     string   // executable re-run as controller and file utility; default: this binary
	StateDir string   // controller metadata directory; default /run/sbx
	Roots    []string // allowed file and cwd roots; default /workspace and /tmp
	// User, when set, is the identity every child runs as. The daemon must either run as
	// root (to drop privileges) or already be that user.
	User         *User
	BaseEnv      []string // environment every command starts with; default: the daemon's own
	MaxExecs     int      // concurrent execs; default 64
	MaxFileBytes int64    // largest file a single request may move; default 50 MiB
	MaxFileOps   int      // concurrent file operations; default 16
	FileTimeout  time.Duration
	Now          func() time.Time
	Logger       *slog.Logger
}

// Server implements envdpb.EnvdServer.
type Server struct {
	envdpb.UnimplementedEnvdServer
	cfg     Config
	execs   *registry
	fileOps chan struct{}
}

// NewServer applies defaults and validates the configuration.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Self == "" {
		self, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("locating the daemon executable: %w", err)
		}
		cfg.Self = self
	}
	if cfg.StateDir == "" {
		cfg.StateDir = "/run/sbx"
	}
	if len(cfg.Roots) == 0 {
		cfg.Roots = []string{"/workspace", "/tmp"}
	}
	if _, err := pathutil.Clean(cfg.Roots[0], cfg.Roots); err != nil {
		return nil, fmt.Errorf("invalid allowed roots: %w", err)
	}
	if cfg.BaseEnv == nil {
		cfg.BaseEnv = os.Environ()
	}
	if cfg.MaxExecs == 0 {
		cfg.MaxExecs = 64
	}
	if cfg.MaxFileBytes == 0 {
		cfg.MaxFileBytes = 50 * 1024 * 1024
	}
	if cfg.MaxFileOps == 0 {
		cfg.MaxFileOps = 16
	}
	if cfg.FileTimeout == 0 {
		cfg.FileTimeout = 10 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.MaxExecs < 1 || cfg.MaxExecs > 1024 || cfg.MaxFileBytes < 1 || cfg.MaxFileOps < 1 || cfg.MaxFileOps > 256 {
		return nil, errors.New("limits out of range")
	}
	if cfg.User != nil {
		if os.Geteuid() != 0 && (os.Geteuid() != int(cfg.User.UID) || os.Getegid() != int(cfg.User.GID)) {
			return nil, errors.New("the daemon can only run commands as its own user unless it runs as root")
		}
		if cfg.User.UID == 0 {
			return nil, errors.New("commands must not run as root")
		}
	}
	return &Server{cfg: cfg, execs: newRegistry(cfg.MaxExecs), fileOps: make(chan struct{}, cfg.MaxFileOps)}, nil
}

// Info reports the version and enforced limits.
func (s *Server) Info(context.Context, *envdpb.InfoRequest) (*envdpb.InfoResponse, error) {
	return &envdpb.InfoResponse{
		Version:            s.cfg.Version,
		MaxConcurrentExecs: uint32(s.cfg.MaxExecs), // #nosec G115 -- validated to 1..1024
		MaxChunkBytes:      MaxChunkBytes,
		AllowedRoots:       append([]string(nil), s.cfg.Roots...),
	}, nil
}

func (s *Server) sysProc() *syscall.SysProcAttr {
	attr := &syscall.SysProcAttr{}
	if s.cfg.User != nil {
		attr.Credential = &syscall.Credential{Uid: s.cfg.User.UID, Gid: s.cfg.User.GID, Groups: []uint32{}}
	}
	return attr
}

var (
	errTooManyExecs = status.Error(codes.ResourceExhausted, "too many concurrent execs")
	errExecExists   = status.Error(codes.AlreadyExists, "exec_id already in use")
	errExecUnknown  = status.Error(codes.NotFound, "exec not running")
)

// registry tracks running execs and the cancellations still in flight for each, so the
// completion status is consumed only after every cancel has seen it.
type registry struct {
	mu     sync.Mutex
	max    int
	active map[string]*entry
}

type entry struct {
	closing bool
	cancels sync.WaitGroup
	done    chan struct{}
}

func newRegistry(limit int) *registry { return &registry{max: limit, active: make(map[string]*entry)} }

func (r *registry) acquire(id string) (*entry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.active[id]; ok {
		return nil, errExecExists
	}
	if len(r.active) >= r.max {
		return nil, errTooManyExecs
	}
	e := &entry{done: make(chan struct{})}
	r.active[id] = e
	return e, nil
}

// beginCancel registers an in-flight cancellation; it fails once the exec is closing.
func (r *registry) beginCancel(id string) (*entry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.active[id]
	if !ok || e.closing {
		return nil, false
	}
	e.cancels.Add(1)
	return e, true
}

// close stops new cancellations and waits for the in-flight ones.
func (r *registry) close(id string) {
	r.mu.Lock()
	e := r.active[id]
	if e != nil {
		e.closing = true
	}
	r.mu.Unlock()
	if e != nil {
		e.cancels.Wait()
	}
}

func (r *registry) release(id string) {
	r.mu.Lock()
	delete(r.active, id)
	r.mu.Unlock()
}
