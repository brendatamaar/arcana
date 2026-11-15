// Command envd is the in-sandbox daemon: a gRPC server for exec, files, streaming and
// signals over a Unix socket or vsock. It also contains the sbx-exec controller and file
// utilities, because the daemon runs every command and file operation as a child of
// itself. Release and install paths for it arrive with the Firecracker driver.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/brendatamaar/arcana/internal/envd"
	"github.com/brendatamaar/arcana/internal/sbxexec"
)

var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	dir := sbxexec.StateDir
	if len(args) >= 2 && args[0] == "--state-dir" {
		dir, args = args[1], args[2:]
	}
	if len(args) > 0 && args[0] == "serve" {
		if err := serve(args[1:], dir, stderr); err != nil {
			_, _ = fmt.Fprintln(stderr, "envd:", err)
			return 1
		}
		return 0
	}
	if len(args) == 1 && args[0] == "version" {
		_, _ = fmt.Fprintln(os.Stdout, version)
		return 0
	}
	return sbxexec.MainWithDir(args, dir, stderr)
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func serve(args []string, dir string, stderr io.Writer) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listen := flags.String("listen", "", `transport: "unix:<path>" or "vsock:<port>"`)
	user := flags.String("user", "", "run commands as uid:gid (needs root, or the same identity)")
	maxExecs := flags.Int("max-execs", 0, "concurrent execs (default 64)")
	maxFile := flags.Int64("max-file-bytes", 0, "largest file moved per request (default 50 MiB)")
	var roots multiFlag
	flags.Var(&roots, "root", "allowed file root (repeatable; default /workspace and /tmp)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *listen == "" || flags.NArg() != 0 {
		return errors.New("expected: envd [--state-dir DIR] serve --listen unix:<path>|vsock:<port> [--user uid:gid] [--root DIR]")
	}
	cfg := envd.Config{Version: version, StateDir: dir, Roots: roots, MaxExecs: *maxExecs, MaxFileBytes: *maxFile, Logger: slog.New(slog.NewJSONHandler(stderr, nil))}
	if *user != "" {
		parsed, err := parseUser(*user)
		if err != nil {
			return err
		}
		cfg.User = &parsed
	}
	if err := os.MkdirAll(dir, 0o1777); err != nil { // #nosec G301 G703 -- operator-chosen sandbox-user-writable controller state, like /tmp
		return fmt.Errorf("preparing state directory: %w", err)
	}
	server, err := envd.NewServer(cfg)
	if err != nil {
		return err
	}
	listener, err := envd.Listen(*listen)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	cfg.Logger.Info("envd listening", "address", *listen, "version", version)
	return envd.Serve(ctx, envd.NewGRPCServer(server), listener, 30*time.Second)
}

func parseUser(value string) (envd.User, error) {
	uid, gid, ok := strings.Cut(value, ":")
	u, uerr := strconv.ParseUint(uid, 10, 32)
	g, gerr := strconv.ParseUint(gid, 10, 32)
	if !ok || uerr != nil || gerr != nil {
		return envd.User{}, errors.New("--user must be uid:gid")
	}
	return envd.User{UID: uint32(u), GID: uint32(g)}, nil
}
