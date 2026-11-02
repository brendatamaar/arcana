package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/runtime"
	"github.com/brendatamaar/arcana/internal/store/db"
)

func (s *Service) execActivity(ctx context.Context, p auth.Principal, id string, maintenance bool) (db.Sandbox, error) {
	if !maintenance {
		return s.Activity(ctx, p, id)
	}
	row, err := s.Get(ctx, p, id)
	if err == nil && (row.State != string(Running) || !s.clock.Now().Before(row.ExpiresAt)) {
		err = ErrNotRunning
	}
	return row, err
}

func (s *Service) eachActive(ctx context.Context, visit func(db.Sandbox) error) error {
	cursor := ""
	var firstErr error
	for {
		rows, err := s.store.Queries.ListActiveSandboxes(ctx, cursor)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err = visit(row); err != nil && firstErr == nil {
				firstErr = err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			cursor = row.ID
		}
		if len(rows) < 1000 {
			return firstErr
		}
	}
}

// Reconcile must finish before admitting requests. Unsupervised execs fail closed.
func (s *Service) Reconcile(ctx context.Context) error {
	instances, err := s.driver.List(ctx)
	if err != nil {
		return err
	}
	byID := make(map[string]runtime.Instance, len(instances))
	for _, instance := range instances {
		byID[instance.SandboxID] = instance
	}
	keep := make(map[string]bool)
	err = s.eachActive(ctx, func(row db.Sandbox) error {
		instance, present := byID[row.ID]
		unfinished, err := s.store.Queries.HasUnfinishedExec(ctx, row.ID)
		if err != nil {
			return err
		}
		action := reconcileDecision(Phase(row.State), present && !instance.Stopped, unfinished)
		if action == keepInstance {
			keep[row.ID] = true
			return nil
		}
		if present {
			if err = s.driver.Destroy(ctx, instance.ID); err != nil {
				return err
			}
		} else if row.ContainerID.Valid {
			if err = s.driver.Destroy(ctx, row.ContainerID.String); err != nil {
				return err
			}
		}
		// Close records only after cleanup, before the terminal transition. A
		// crash between these writes leaves an active row for the next retry.
		if err = s.store.Queries.FailUnfinishedExecs(ctx, db.FailUnfinishedExecsParams{EndedAt: sql.NullTime{Time: s.clock.Now(), Valid: true}, SandboxID: row.ID}); err != nil {
			return err
		}
		if action == resumeDestroy {
			event := DestroyedInstance
			if row.EndReason.String == "ttl" || row.EndReason.String == "idle" {
				event = ExpiredInstance
			}
			_, err = s.transition(ctx, row, event, row.EndReason.String, "")
		} else {
			_, err = s.transition(ctx, row, LostInstance, "lost", "")
		}
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("reconciling records: %w", err)
	}
	for _, instance := range instances {
		if !keep[instance.SandboxID] {
			if err = s.driver.Destroy(ctx, instance.ID); err != nil {
				return err
			}
		}
	}
	if pruner, ok := s.driver.(interface {
		PruneOrphans(context.Context, map[string]bool) error
	}); ok {
		return pruner.PruneOrphans(ctx, keep)
	}
	return nil
}

// Reap checks absolute TTL before idle expiry.
func (s *Service) Reap(ctx context.Context) error {
	return s.eachActive(ctx, func(row db.Sandbox) error {
		reason := expiryReason(s.clock.Now(), expirySnapshot{Phase(row.State), row.ExpiresAt, row.LastActivityAt, row.IdleTimeoutSeconds})
		if row.State == string(Destroying) {
			reason = row.EndReason.String
		}
		if reason == "" {
			return nil
		}
		err := s.Destroy(ctx, auth.Principal{Admin: true}, row.ID, reason)
		if errors.Is(err, ErrNotRunning) {
			return nil
		}
		return err
	})
}

// Watch checks volume usage sequentially, bounding scan concurrency to one.
func (s *Service) Watch(ctx context.Context) error {
	if s.cfg.Workspace.Backend != "volume" {
		return nil
	}
	return s.eachActive(ctx, func(row db.Sandbox) error {
		if row.State != string(Running) {
			return nil
		}
		argv := []string{"du", "-sb", "/workspace"}
		timeout := int64(min(10, s.cfg.Limits.Exec.MaxTimeoutSeconds))
		plan, err := resolveExec(s.cfg, ExecRequest{Cmd: &argv, TimeoutSeconds: &timeout})
		if err != nil {
			return err
		}
		plan.maintenance = true
		result, err := s.executePlan(ctx, auth.Principal{Admin: true}, row.ID, plan, nil, &fileIO{limit: 4096})
		if errors.Is(err, ErrExecLimit) || errors.Is(err, ErrNotRunning) {
			return nil
		}
		if err != nil {
			return err
		}
		fields := strings.Fields(result.Stdout)
		if result.ExitCode != 0 || result.Truncated || len(fields) != 2 || fields[1] != "/workspace" {
			return fmt.Errorf("invalid workspace usage result")
		}
		size, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || size < 0 {
			return fmt.Errorf("invalid workspace usage size")
		}
		if size > row.DiskMb.Int64*1024*1024 {
			return s.Destroy(ctx, auth.Principal{Admin: true}, row.ID, "disk_quota")
		}
		return nil
	})
}

// Maintain owns independent reaper and watchdog loops and joins both on cancellation.
func (s *Service) Maintain(ctx context.Context, logger *slog.Logger) {
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		s.maintenanceLoop(ctx, time.Duration(s.cfg.Workspace.CheckIntervalSeconds)*time.Second, s.Watch, logger, "workspace watchdog failed")
	}()
	s.maintenanceLoop(ctx, time.Second, s.Reap, logger, "reaper failed")
	<-watchDone
}

func (s *Service) maintenanceLoop(ctx context.Context, interval time.Duration, action func(context.Context) error, logger *slog.Logger, message string) {
	for {
		timer := s.clock.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.Chan():
			timer.Stop()
		}
		if err := action(ctx); err != nil && ctx.Err() == nil {
			logger.ErrorContext(ctx, message, "error", err)
		}
	}
}

// Shutdown preserves healthy instances unless explicitly configured otherwise.
func (s *Service) Shutdown(ctx context.Context) error {
	if !s.cfg.Server.DestroyOnShutdown {
		return nil
	}
	return s.eachActive(ctx, func(row db.Sandbox) error { return s.Destroy(ctx, auth.Principal{Admin: true}, row.ID, "shutdown") })
}
