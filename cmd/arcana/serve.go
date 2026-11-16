package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
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
	"github.com/brendatamaar/arcana/internal/events"
	"github.com/brendatamaar/arcana/internal/helper"
	"github.com/brendatamaar/arcana/internal/logging"
	"github.com/brendatamaar/arcana/internal/metrics"
	"github.com/brendatamaar/arcana/internal/netpolicy"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime/docker"
	"github.com/brendatamaar/arcana/internal/runtime/factory"
	"github.com/brendatamaar/arcana/internal/store"
	"github.com/brendatamaar/arcana/internal/webui"
	"golang.org/x/sync/errgroup"
)

// maxEventSubscribers caps concurrent /v1/events streams across all keys.
const maxEventSubscribers = 256

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
		onDecision := func(event netpolicy.Decision) { recorder.Record("egress.decision", "", event.SandboxID, event) }
		driver, err := factory.New(cfg, factory.Options{InstallID: database.InstallID, HelperPath: helperPath, OnDecision: onDecision, OnDropped: recorder.AddDropped, Docker: func(c *docker.Config) {
			c.OnOrphan = telemetry.Orphan
			c.Network = &docker.NetworkConfig{SubnetPool: cfg.Network.SubnetPool, SubnetPrefix: cfg.Network.SubnetPrefix, ProxyImage: cfg.Network.ProxyImage, ProxyDNS: cfg.Network.ProxyDNS, DataDir: cfg.DataDir, OnDecision: onDecision, OnDropped: recorder.AddDropped}
		}})
		if err != nil {
			return err
		}
		defer func() { _ = driver.Close() }()
		if oci, _ := cfg.Runtime.OCI(); oci == "runc" {
			logger.WarnContext(ctx, "INSECURE RUNTIME: runc shares the host kernel; explicitly opted in")
		}
		logger.InfoContext(ctx, "runtime driver ready", "driver", driver.Name())
		lifecycle = orchestrator.New(database, driver, cfg, orchestrator.SystemClock{}, recorder)
		var bus *events.Bus
		if cfg.UI.Enabled {
			bus = events.NewBus(orchestrator.SystemClock{}, cfg.UI.EventBuffer, maxEventSubscribers)
			lifecycle.WithEvents(bus)
		}
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
		if cfg.UI.Enabled {
			keys.WithSessions(auth.SessionPolicy{TTL: time.Duration(cfg.UI.SessionTTLSeconds) * time.Second, AllowedOrigins: cfg.UI.AllowedOrigins})
		}
		telemetry.SetLiveSources(metrics.LiveSources{Sessions: keys.ActiveSessions, EventSubscribers: bus.Subscribers, ExecOutputSubscribers: lifecycle.TailSubscribers})
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
			return api.Serve(serveCtx, listener, api.New(keys, logger, cfg.Limits.Files, lifecycle, api.Options{Audit: recorder, Middleware: telemetry.HTTP, CreatePerMinute: cfg.Limits.CreatePerMinutePerKey, MaxFork: cfg.Snapshots.MaxFork, Version: version, UI: uiHandler(cfg.UI.Enabled)}), timeout, logger)
		})
		group.Go(func() error {
			return api.Serve(serveCtx, netpolicy.BoundListener(metricsListener, 32), telemetry.Handler(), timeout, logger)
		})
		serveErr := group.Wait()
		cancelMaintenance()
		<-maintenanceDone
		shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(cfg.Server.ShutdownTimeoutSeconds)*time.Second)
		defer cancelShutdown()
		var drainErr error
		if drainer, ok := driver.(interface{ DrainDecisions(context.Context) error }); ok {
			drainErr = drainer.DrainDecisions(shutdownCtx) // only drivers with egress proxies (Docker, containerd with allowlist) implement this
		}
		return errors.Join(serveErr, drainErr, lifecycle.Shutdown(shutdownCtx))
	}}
}

// uiHandler mounts the dashboard unless ui.enabled is false.
func uiHandler(enabled bool) http.Handler {
	if !enabled {
		return nil
	}
	return webui.Handler()
}
