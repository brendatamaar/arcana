package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/brendatamaar/arcana/internal/audit"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/store"
)

func newKeysCommand() *cobra.Command {
	keys := &cobra.Command{Use: "keys", Short: "Manage API keys in the local database"}
	create := &cobra.Command{Use: "create <name>", Short: "Create a key and print its secret once", Args: cobra.ExactArgs(1)}
	create.Flags().Bool("admin", false, "Create an administrator key")
	create.RunE = func(cmd *cobra.Command, args []string) error {
		return withKeys(cmd, func(service *auth.Service) error {
			admin, err := cmd.Flags().GetBool("admin")
			if err != nil {
				return err
			}
			_, raw, err := service.Create(cmd.Context(), args[0], admin)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), raw)
			return err
		})
	}
	list := &cobra.Command{Use: "list", Short: "List key metadata without secrets", Args: cobra.NoArgs}
	list.Flags().Int64("limit", 100, "Maximum keys (1..1000)")
	list.Flags().String("cursor", "", "List keys after this ID")
	list.RunE = func(cmd *cobra.Command, _ []string) error {
		return withKeys(cmd, func(service *auth.Service) error {
			limit, err := cmd.Flags().GetInt64("limit")
			if err != nil {
				return err
			}
			cursor, err := cmd.Flags().GetString("cursor")
			if err != nil {
				return err
			}
			result, err := service.List(cmd.Context(), cursor, limit)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		})
	}
	revoke := &cobra.Command{Use: "revoke <id>", Short: "Revoke an API key", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return withKeys(cmd, func(service *auth.Service) error { return service.Revoke(cmd.Context(), args[0]) })
	}}
	keys.AddCommand(create, list, revoke)
	return keys
}

func withKeys(cmd *cobra.Command, run func(*auth.Service) error) error {
	filename, err := cmd.Flags().GetString("config")
	if err != nil {
		return err
	}
	cfg, err := config.Load(filename)
	if err != nil {
		return err
	}
	database, err := store.Open(cmd.Context(), filepath.Join(cfg.DataDir, "arcana.db"))
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	recorder, err := audit.New(audit.SQLBackend{Store: database}, audit.SystemClock{}, cfg.Audit.RetentionDays, 256, nil)
	if err != nil {
		return err
	}
	result := run(auth.New(database, auth.SystemClock{}, recorder))
	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return errors.Join(result, recorder.Close(closeCtx))
}
