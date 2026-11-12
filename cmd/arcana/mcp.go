package main

import (
	"io"
	"log/slog"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	arcanamcp "github.com/brendatamaar/arcana/internal/mcp"
)

// mcpFrameLimit bounds one inbound JSON-RPC frame (write_file content plus encoding overhead).
const mcpFrameLimit = 4 << 20

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func newMCPCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server on stdio, acting as a client of a running Arcana API",
		Long: "Runs a Model Context Protocol server on stdin/stdout for MCP clients such as Claude Desktop or Claude Code. " +
			"It calls the Arcana API selected by --server/--api-key (ARCANA_URL/ARCANA_API_KEY). Sandboxes it creates are labelled " +
			"mcp_session=<id> and destroyed when the session ends unless the tool call asked to keep them. Logs go to stderr; stdout carries only the protocol.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := apiClient(cmd)
			if err != nil {
				return err
			}
			logger := slog.New(slog.NewJSONHandler(cmd.ErrOrStderr(), nil))
			session, protocol, err := arcanamcp.New(c, logger, version)
			if err != nil {
				return err
			}
			transport := &sdk.IOTransport{Reader: io.NopCloser(cmd.InOrStdin()), Writer: nopWriteCloser{cmd.OutOrStdout()}, MaxLineLength: mcpFrameLimit}
			return session.Serve(cmd.Context(), protocol, transport)
		},
	}
}
