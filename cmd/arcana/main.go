// Command arcana runs the Arcana sandbox orchestrator.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "arcana",
		Short:         "Self-hosted sandboxes for AI agents",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "/etc/arcana/config.yaml", "Path to the YAML configuration file")
	server := os.Getenv("ARCANA_URL")
	if server == "" {
		server = "http://127.0.0.1:7070"
	}
	root.PersistentFlags().String("server", server, "Arcana server URL (ARCANA_URL)")
	root.PersistentFlags().String("api-key", os.Getenv("ARCANA_API_KEY"), "API key (ARCANA_API_KEY)")
	root.PersistentFlags().Bool("json", false, "Print structured JSON for scripting")
	root.AddCommand(newKeysCommand(), newServeCommand(), newProxyCommand(), newProxyControlCommand())
	root.AddCommand(newSandboxCommand(), newExecCommand(), newFilesCommand(), newDoctorCommand())
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version, commit and build date",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "arcana %s (commit %s, built %s)\n", version, commit, date)
			return err
		},
	})
	return root
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCommand().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}
