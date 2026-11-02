package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/brendatamaar/arcana/internal/api"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/logging"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/store"
)

func newServeCommand() *cobra.Command {
	return &cobra.Command{Use: "serve", Short: "Serve the Arcana API", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		filename, err := cmd.Flags().GetString("config")
		if err != nil {
			return err
		}
		cfg, err := config.Load(filename)
		if err != nil {
			return err
		}
		logger, err := logging.New(cmd.ErrOrStderr(), cfg.Log.Format, cfg.Log.Level)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		database, err := store.Open(ctx, filepath.Join(cfg.DataDir, "arcana.db"))
		if err != nil {
			return err
		}
		defer func() { _ = database.Close() }()
		helperPath, err := helper.Install(ctx, cfg.DataDir)
		if err != nil {
			return err
		}
		driver, err := docker.New(docker.Config{Host: cfg.Runtime.Docker.Host, OCIRuntime: cfg.Runtime.Docker.OCIRuntime, AllowInsecureRuntime: cfg.Runtime.Docker.AllowInsecureRuntime, InstallID: database.InstallID, HelperPath: helperPath})
		if err != nil {
			return err
		}
		defer func() { _ = driver.Close() }()
		if cfg.Runtime.Docker.OCIRuntime == "runc" {
			logger.WarnContext(ctx, "INSECURE RUNTIME: runc shares the host kernel; explicitly opted in")
		}
		lifecycle := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
		if err = lifecycle.Reconcile(ctx); err != nil {
			return fmt.Errorf("startup reconciliation: %w", err)
		}
		maintenanceCtx, cancelMaintenance := context.WithCancel(ctx)
		maintenanceDone := make(chan struct{})
		go func() { defer close(maintenanceDone); lifecycle.Maintain(maintenanceCtx, logger) }()
		defer func() { cancelMaintenance(); <-maintenanceDone }()
		keys := auth.New(database, auth.SystemClock{})
		if err = keys.Bootstrap(ctx, cfg.DataDir, cmd.OutOrStdout()); err != nil {
			return err
		}
		listener, err := net.Listen("tcp", cfg.Server.Listen)
		if err != nil {
			return fmt.Errorf("listening for API: %w", err)
		}
		defer func() { _ = listener.Close() }()
		host, _, err := net.SplitHostPort(cfg.Server.Listen)
		if err != nil {
			return err
		}
		address := net.ParseIP(host)
		if address == nil || !address.IsLoopback() {
			logger.WarnContext(ctx, "API listener is not loopback; use a TLS-terminating reverse proxy")
		}
		logger.InfoContext(ctx, "API listening", "address", listener.Addr().String())
		serveErr := api.Serve(ctx, listener, api.New(keys, logger, cfg.Limits.Files, lifecycle), time.Duration(cfg.Server.ShutdownTimeoutSeconds)*time.Second, logger)
		cancelMaintenance()
		<-maintenanceDone
		shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(cfg.Server.ShutdownTimeoutSeconds)*time.Second)
		defer cancelShutdown()
		return errors.Join(serveErr, lifecycle.Shutdown(shutdownCtx))
	}}
}
