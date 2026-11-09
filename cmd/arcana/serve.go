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
	"github.com/brendatamaar/arcana/internal/audit"
	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/logging"
	"github.com/brendatamaar/arcana/internal/metrics"
	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/store"
	"golang.org/x/sync/errgroup"
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
		var lifecycle *orchestrator.Service
		var recorder *audit.Writer
		telemetry := metrics.New(database, cfg, func() map[string]int {
			if lifecycle == nil {
				return nil
			}
			return lifecycle.PoolAvailable()
		}, func() audit.Stats {
			if recorder == nil {
				return audit.Stats{}
			}
			return recorder.Stats()
		})
		recorder, err = audit.New(audit.SQLBackend{Store: database}, audit.SystemClock{}, cfg.Audit.RetentionDays, 256, telemetry.ObserveAudit)
		if err != nil {
			return err
		}
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.Server.ShutdownTimeoutSeconds)*time.Second)
			defer cancel()
			_ = recorder.Close(closeCtx)
		}()
		helperPath, err := helper.Install(ctx, cfg.DataDir)
		if err != nil {
			return err
		}
		driver, err := docker.New(docker.Config{OnOrphan: telemetry.Orphan, Network: &docker.NetworkConfig{SubnetPool: cfg.Network.SubnetPool, SubnetPrefix: cfg.Network.SubnetPrefix, ProxyImage: cfg.Network.ProxyImage, ProxyDNS: cfg.Network.ProxyDNS, DataDir: cfg.DataDir, OnDecision: func(event netpolicy.Decision) { recorder.Record("egress.decision", "", event.SandboxID, event) }, OnDropped: recorder.AddDropped}, Host: cfg.Runtime.Docker.Host, OCIRuntime: cfg.Runtime.Docker.OCIRuntime, AllowInsecureRuntime: cfg.Runtime.Docker.AllowInsecureRuntime, InstallID: database.InstallID, HelperPath: helperPath})
		if err != nil {
			return err
		}
		defer func() { _ = driver.Close() }()
		if cfg.Runtime.Docker.OCIRuntime == "runc" {
			logger.WarnContext(ctx, "INSECURE RUNTIME: runc shares the host kernel; explicitly opted in")
		}
		lifecycle = orchestrator.New(database, driver, cfg, orchestrator.SystemClock{}, recorder)
		if err = lifecycle.Reconcile(ctx); err != nil {
			return fmt.Errorf("startup reconciliation: %w", err)
		}
		report := startupDoctor(ctx, cfg)
		for _, check := range report.Checks {
			switch check.Status {
			case "fail":
				logger.ErrorContext(ctx, "startup prerequisite failed", "check", check.Name, "detail", check.Detail, "hint", check.Hint)
			case "warn":
				logger.WarnContext(ctx, "startup prerequisite warning", "check", check.Name, "detail", check.Detail, "hint", check.Hint)
			}
		}
		if err = requireHealthy(report); err != nil {
			return err
		}
		maintenanceCtx, cancelMaintenance := context.WithCancel(ctx)
		maintenanceDone := make(chan struct{})
		go func() { defer close(maintenanceDone); lifecycle.Maintain(maintenanceCtx, logger) }()
		defer func() { cancelMaintenance(); <-maintenanceDone }()
		keys := auth.New(database, auth.SystemClock{}, recorder)
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
		metricsListener, err := net.Listen("tcp", cfg.Server.MetricsListen)
		if err != nil {
			return fmt.Errorf("listening for metrics: %w", err)
		}
		defer func() { _ = metricsListener.Close() }()
		group, serveCtx := errgroup.WithContext(ctx)
		timeout := time.Duration(cfg.Server.ShutdownTimeoutSeconds) * time.Second
		group.Go(func() error {
			return api.Serve(serveCtx, listener, api.New(keys, logger, cfg.Limits.Files, lifecycle, api.Options{Audit: recorder, Middleware: telemetry.HTTP, CreatePerMinute: cfg.Limits.CreatePerMinutePerKey}), timeout, logger)
		})
		group.Go(func() error {
			return api.Serve(serveCtx, netpolicy.BoundListener(metricsListener, 32), telemetry.Handler(), timeout, logger)
		})
		serveErr := group.Wait()
		cancelMaintenance()
		<-maintenanceDone
		shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(cfg.Server.ShutdownTimeoutSeconds)*time.Second)
		defer cancelShutdown()
		return errors.Join(serveErr, driver.DrainDecisions(shutdownCtx), lifecycle.Shutdown(shutdownCtx))
	}}
}
