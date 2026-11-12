package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/brendatamaar/arcana/pkg/client"
)

type remoteExit struct{ code int }

func (e remoteExit) Error() string {
	return fmt.Sprintf("remote command exited with status %d", e.code)
}

func exitCode(err error) int {
	var remote remoteExit
	if errors.As(err, &remote) {
		return remote.code
	}
	return 1
}

func apiClient(cmd *cobra.Command) (*client.Client, error) {
	server, err := cmd.Flags().GetString("server")
	if err != nil {
		return nil, err
	}
	key, err := cmd.Flags().GetString("api-key")
	if err != nil {
		return nil, err
	}
	return client.New(server, key, client.Options{})
}

func printJSON(cmd *cobra.Command, value any) error {
	return json.NewEncoder(cmd.OutOrStdout()).Encode(value)
}
func jsonMode(cmd *cobra.Command) bool { value, _ := cmd.Flags().GetBool("json"); return value }
func printSandbox(cmd *cobra.Command, rows []client.Sandbox) error {
	if jsonMode(cmd) {
		return printJSON(cmd, rows)
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ID\tSTATE\tTEMPLATE\tEXPIRES"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", row.Id, row.State, row.Template, row.ExpiresAt.Format("2006-01-02T15:04:05Z07:00")); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func newSandboxCommand() *cobra.Command {
	sandbox := &cobra.Command{Use: "sandbox", Short: "Manage remote sandboxes"}
	create := &cobra.Command{Use: "create", Short: "Create a sandbox", Args: cobra.NoArgs}
	networkMode := create.Flags().String("network", "none", "Network mode (none or allowlist)")
	allow := create.Flags().StringSlice("allow", nil, "Exact allowed hostnames")
	presets := create.Flags().StringSlice("network-preset", nil, "Configured allowlist presets")
	template := create.Flags().String("template", "base", "Template name")
	ttl := create.Flags().Int64("ttl", 0, "TTL in seconds (server default when omitted)")
	idle := create.Flags().Int64("idle-timeout", 0, "Idle timeout in seconds")
	fromSnapshot := create.Flags().String("from-snapshot", "", "Restore this snapshot into /workspace (template defaults to the snapshot's)")
	create.RunE = func(cmd *cobra.Command, _ []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		request := client.CreateSandboxRequest{Template: *template}
		if *fromSnapshot != "" {
			request.FromSnapshot = fromSnapshot
			if !cmd.Flags().Changed("template") {
				snap, getErr := c.GetSnapshot(cmd.Context(), *fromSnapshot)
				if getErr != nil {
					return getErr
				}
				request.Template = snap.Template
			}
		}
		if cmd.Flags().Changed("network") || len(*allow) > 0 || len(*presets) > 0 {
			request.Network = &client.Network{Mode: client.NetworkMode(*networkMode), Allow: allow, Presets: presets}
		}
		if cmd.Flags().Changed("ttl") {
			request.TtlSeconds = ttl
		}
		if cmd.Flags().Changed("idle-timeout") {
			request.IdleTimeoutSeconds = idle
		}
		row, err := c.CreateSandbox(cmd.Context(), request)
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return printJSON(cmd, row)
		}
		return printSandbox(cmd, []client.Sandbox{row})
	}
	list := &cobra.Command{Use: "list", Short: "List one page of sandboxes", Args: cobra.NoArgs}
	limit := list.Flags().Int64("limit", 100, "Page size (1..1000)")
	cursor := list.Flags().String("cursor", "", "Page cursor")
	state := list.Flags().String("state", "", "Filter lifecycle state")
	label := list.Flags().String("label", "", "Filter key:value label")
	list.RunE = func(cmd *cobra.Command, _ []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		page, err := c.ListSandboxes(cmd.Context(), client.ListOptions{Limit: *limit, Cursor: *cursor, State: *state, Label: *label})
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return printJSON(cmd, page)
		}
		if err = printSandbox(cmd, page.Items); err != nil {
			return err
		}
		if page.NextCursor != nil {
			_, err = fmt.Fprintln(cmd.ErrOrStderr(), "next cursor:", *page.NextCursor)
		}
		return err
	}
	get := &cobra.Command{Use: "get <id>", Short: "Get a sandbox", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		row, err := c.GetSandbox(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return printJSON(cmd, row)
		}
		return printSandbox(cmd, []client.Sandbox{row})
	}}
	rm := &cobra.Command{Use: "rm <id>", Short: "Destroy a sandbox", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		if err = c.DestroySandbox(cmd.Context(), args[0]); err != nil {
			return err
		}
		if jsonMode(cmd) {
			return printJSON(cmd, map[string]string{"id": args[0], "state": "destroyed"})
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), args[0])
		return err
	}}
	sandbox.AddCommand(create, list, get, rm)
	return sandbox
}

func newExecCommand() *cobra.Command {
	command := &cobra.Command{Use: "exec <id> -- <argv...>", Short: "Stream a remote command and return its exit code", Args: func(cmd *cobra.Command, args []string) error {
		if len(args) < 2 || cmd.ArgsLenAtDash() != 1 {
			return errors.New("use exec <id> -- <argv...>")
		}
		return nil
	}}
	tty := command.Flags().Bool("tty", false, "Allocate a TTY (requires terminal stdin)")
	input := command.Flags().Bool("stdin", false, "Forward stdin to the command (implied by --tty)")
	timeout := command.Flags().Int64("timeout", 0, "Command timeout in seconds")
	cwd := command.Flags().String("cwd", "", "Sandbox working directory")
	command.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		request := client.StreamRequest{ExecRequest: client.ExecRequest{Cmd: args[1:], Cwd: *cwd}, TTY: *tty}
		if cmd.Flags().Changed("timeout") {
			request.TimeoutSeconds = timeout
		}
		var restore func()
		if *tty {
			if jsonMode(cmd) {
				return errors.New("--tty cannot be combined with --json")
			}
			var cols, rows uint16
			restore, cols, rows, err = terminalMode(cmd.InOrStdin())
			if err != nil {
				return err
			}
			defer restore()
			request.Cols = cols
			request.Rows = rows
		}
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		stream, err := c.ExecStream(ctx, args[0], request)
		if err != nil {
			return err
		}
		defer func() { _ = stream.Close() }()
		var group errgroup.Group
		defer func() { _ = stream.Close(); _ = group.Wait() }()
		stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
		if jsonMode(cmd) {
			stdout, stderr = io.Discard, io.Discard
		}
		group.Go(func() error {
			_, err := io.Copy(stdout, stream.Stdout)
			if err != nil {
				cancel()
			}
			return err
		})
		group.Go(func() error {
			_, err := io.Copy(stderr, stream.Stderr)
			if err != nil {
				cancel()
			}
			return err
		})
		var inputDone chan struct{}
		var reader io.ReadCloser
		if *input || *tty {
			reader, err = commandInput(cmd.InOrStdin())
			if err != nil {
				return err
			}
			inputDone = make(chan struct{})
			go func() { defer close(inputDone); _, _ = io.Copy(stream, reader); _ = stream.CloseStdin() }()
		} else {
			_ = stream.CloseStdin()
		}
		result, waitErr := stream.Wait()
		if reader != nil {
			_ = reader.Close()
			<-inputDone
		}
		if err = group.Wait(); err != nil {
			return err
		}
		if waitErr != nil {
			return waitErr
		}
		if jsonMode(cmd) {
			if err = printJSON(cmd, result); err != nil {
				return err
			}
		}
		if result.ExitCode != 0 {
			return remoteExit{result.ExitCode}
		}
		return nil
	}
	return command
}

func newFilesCommand() *cobra.Command {
	files := &cobra.Command{Use: "files", Short: "Transfer sandbox files"}
	put := &cobra.Command{Use: "put <id> <local-file|-> <sandbox-path>", Short: "Upload a file", Args: cobra.ExactArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		reader := cmd.InOrStdin()
		if args[1] != "-" {
			file, err := os.Open(args[1])
			if err != nil {
				return err
			}
			defer func() { _ = file.Close() }()
			reader = file
		}
		if err = c.WriteFile(cmd.Context(), args[0], args[2], reader); err != nil {
			return err
		}
		if jsonMode(cmd) {
			return printJSON(cmd, map[string]string{"path": args[2]})
		}
		return nil
	}}
	get := &cobra.Command{Use: "get <id> <sandbox-path> [local-file|-]", Short: "Download a file (stdout by default)", Args: cobra.RangeArgs(2, 3), RunE: func(cmd *cobra.Command, args []string) error {
		if jsonMode(cmd) {
			return errors.New("files get returns raw bytes; omit --json")
		}
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		body, err := c.ReadFile(cmd.Context(), args[0], args[1])
		if err != nil {
			return err
		}
		defer func() { _ = body.Close() }()
		writer := cmd.OutOrStdout()
		var file *os.File
		if len(args) == 3 && args[2] != "-" {
			file, err = os.OpenFile(args[2], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if err != nil {
				return err
			}
			defer func() { _ = file.Close() }()
			writer = file
		}
		_, err = io.Copy(writer, body)
		return err
	}}
	ls := &cobra.Command{Use: "ls <id> <sandbox-path>", Short: "List a directory", Args: cobra.ExactArgs(2)}
	depth := ls.Flags().Int64("depth", 1, "Directory recursion depth (1..100)")
	ls.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		list, err := c.ListFiles(cmd.Context(), args[0], args[1], *depth)
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return printJSON(cmd, list)
		}
		writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		if _, err = fmt.Fprintln(writer, "TYPE\tBYTES\tPATH"); err != nil {
			return err
		}
		for _, entry := range list.Items {
			if _, err = fmt.Fprintf(writer, "%s\t%d\t%s\n", entry.Type, entry.SizeBytes, entry.Path); err != nil {
				return err
			}
		}
		return writer.Flush()
	}
	files.AddCommand(put, get, ls)
	return files
}
