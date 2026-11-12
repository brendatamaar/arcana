package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/brendatamaar/arcana/pkg/client"
)

func printSnapshots(cmd *cobra.Command, rows []client.Snapshot) error {
	if jsonMode(cmd) {
		return printJSON(cmd, rows)
	}
	writer := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ID\tSOURCE\tTEMPLATE\tSIZE_BYTES\tCREATED"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%d\t%s\n", row.Id, row.SourceSandboxId, row.Template, row.SizeBytes, row.CreatedAt.Format("2006-01-02T15:04:05Z07:00")); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func newSnapshotCommand() *cobra.Command {
	snapshot := &cobra.Command{Use: "snapshot", Short: "Manage workspace snapshots (workspace only; no processes or files outside /workspace)"}
	create := &cobra.Command{Use: "create <sandbox-id>", Short: "Snapshot a running sandbox's /workspace", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		row, err := c.CreateSnapshot(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return printJSON(cmd, row)
		}
		return printSnapshots(cmd, []client.Snapshot{row})
	}}
	list := &cobra.Command{Use: "list", Short: "List one page of snapshots", Args: cobra.NoArgs}
	limit := list.Flags().Int64("limit", 100, "Page size (1..1000)")
	cursor := list.Flags().String("cursor", "", "Page cursor")
	list.RunE = func(cmd *cobra.Command, _ []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		page, err := c.ListSnapshots(cmd.Context(), client.ListOptions{Limit: *limit, Cursor: *cursor})
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			return printJSON(cmd, page)
		}
		if err = printSnapshots(cmd, page.Items); err != nil {
			return err
		}
		if page.NextCursor != nil {
			_, err = fmt.Fprintln(cmd.ErrOrStderr(), "next cursor:", *page.NextCursor)
		}
		return err
	}
	rm := &cobra.Command{Use: "rm <id>", Short: "Delete a snapshot and its archive", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		if err = c.DeleteSnapshot(cmd.Context(), args[0]); err != nil {
			return err
		}
		if jsonMode(cmd) {
			return printJSON(cmd, map[string]string{"id": args[0], "state": "deleted"})
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), args[0])
		return err
	}}
	snapshot.AddCommand(create, list, rm)
	return snapshot
}

func newForkCommand() *cobra.Command {
	fork := &cobra.Command{Use: "fork <snapshot-id>", Short: "Create several sandboxes from one snapshot", Args: cobra.ExactArgs(1)}
	count := fork.Flags().Int64("count", 2, "Number of sandboxes to create (server limit snapshots.max_fork)")
	ttl := fork.Flags().Int64("ttl", 0, "TTL in seconds (server default when omitted)")
	idle := fork.Flags().Int64("idle-timeout", 0, "Idle timeout in seconds")
	fork.RunE = func(cmd *cobra.Command, args []string) error {
		c, err := apiClient(cmd)
		if err != nil {
			return err
		}
		request := client.ForkRequest{Count: *count}
		if cmd.Flags().Changed("ttl") {
			request.TtlSeconds = ttl
		}
		if cmd.Flags().Changed("idle-timeout") {
			request.IdleTimeoutSeconds = idle
		}
		result, err := c.Fork(cmd.Context(), args[0], request)
		if err != nil {
			return err
		}
		if jsonMode(cmd) {
			err = printJSON(cmd, result)
		} else {
			err = printSandbox(cmd, result.Items)
			for _, failure := range result.Failures {
				if _, writeErr := fmt.Fprintf(cmd.ErrOrStderr(), "fork %d failed: %s: %s\n", failure.Index, failure.Code, failure.Message); writeErr != nil && err == nil {
					err = writeErr
				}
			}
		}
		if err != nil {
			return err
		}
		// Created sandboxes are already printed; a non-zero exit keeps scripts from
		// treating a partial fork as complete.
		if len(result.Failures) > 0 {
			return fmt.Errorf("%d of %d sandboxes could not be created", len(result.Failures), *count)
		}
		return nil
	}
	return fork
}
