// Command arcana runs the Arcana sandbox orchestrator.
package main

import (
	"fmt"
	"os"

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
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
