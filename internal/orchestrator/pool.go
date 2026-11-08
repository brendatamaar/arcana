package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/brendatamaar/arcana/internal/ids"
	"github.com/brendatamaar/arcana/internal/runtime"
)

// acquirePool accepts only exact immutable creation settings. Instances are never reused.
func (s *Service) acquirePool(plan resolved) (runtime.Instance, bool) {
	baseline, err := resolve(s.cfg, CreateRequest{Template: plan.template})
	if err != nil || baseline.spec.Resources != plan.spec.Resources || baseline.spec.Workdir != plan.spec.Workdir || !slices.Equal(baseline.spec.NetworkAllow, plan.spec.NetworkAllow) || baseline.spec.NetworkMode != plan.spec.NetworkMode || !maps.Equal(baseline.spec.Env, plan.spec.Env) || len(plan.spec.Labels) != 0 {
		return runtime.Instance{}, false
	}
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	items := s.pool[plan.template]
	if len(items) == 0 {
		return runtime.Instance{}, false
	}
	item := items[len(items)-1]
	s.pool[plan.template] = items[:len(items)-1]
	return item, true
}

// PoolAvailable returns a copy suitable for metrics collectors.
func (s *Service) PoolAvailable() map[string]int {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	out := make(map[string]int, len(s.pool))
	for name, items := range s.pool {
		out[name] = len(items)
	}
	return out
}

// FillPool creates at most one member per call, bounding work and runtime admission.
func (s *Service) FillPool(ctx context.Context) error {
	s.poolFillMu.Lock()
	defer s.poolFillMu.Unlock()
	select {
	case s.createGate <- struct{}{}:
		defer func() { <-s.createGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	for _, template := range s.cfg.Templates {
		s.poolMu.Lock()
		available := len(s.pool[template.Name])
		total := 0
		for _, items := range s.pool {
			total += len(items)
		}
		s.poolMu.Unlock()
		if available >= template.WarmPool {
			continue
		}
		active, err := s.store.Queries.CountActiveSandboxes(ctx, "")
		if err != nil {
			return err
		}
		if int(active)+total >= s.cfg.Limits.MaxSandboxes {
			return nil
		}
		plan, err := resolve(s.cfg, CreateRequest{Template: template.Name})
		if err != nil {
			return err
		}
		plan.spec.SandboxID, err = ids.New("sbx")
		if err != nil {
			return err
		}
		plan.spec.PoolTemplate = template.Name
		createCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		instance, err := s.driver.Create(createCtx, plan.spec)
		cancel()
		if err != nil {
			return err
		}
		s.poolMu.Lock()
		s.pool[template.Name] = append(s.pool[template.Name], instance)
		s.poolMu.Unlock()
		return nil
	}
	return nil
}

func (s *Service) maintainPool(ctx context.Context, logger *slog.Logger) {
	delay := time.Millisecond
	for {
		timer := s.clock.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.Chan():
			timer.Stop()
		}
		if err := s.FillPool(ctx); err != nil {
			if ctx.Err() == nil {
				logger.ErrorContext(ctx, "pool fill failed", "error", err)
			}
			delay = min(max(delay*2, time.Second), time.Minute)
		} else {
			delay = time.Second
		}
	}
}

func (s *Service) drainPool(ctx context.Context) error {
	s.poolFillMu.Lock()
	defer s.poolFillMu.Unlock()
	s.poolMu.Lock()
	members := s.pool
	s.pool = make(map[string][]runtime.Instance)
	s.poolMu.Unlock()
	var result error
	for _, items := range members {
		for _, instance := range items {
			result = errors.Join(result, s.driver.Destroy(ctx, instance.ID))
		}
	}
	return result
}

// makePoolRoom evicts an unused member before a cold reservation needs its slot.
// Caller holds createGate, shared with the filler and all reservations.
func (s *Service) makePoolRoom(ctx context.Context) error {
	active, err := s.store.Queries.CountActiveSandboxes(ctx, "")
	if err != nil {
		return err
	}
	s.poolMu.Lock()
	total := 0
	for _, items := range s.pool {
		total += len(items)
	}
	var evicted runtime.Instance
	var evictedTemplate string
	if int(active)+total >= s.cfg.Limits.MaxSandboxes {
		for name, items := range s.pool {
			if len(items) > 0 {
				evicted = items[len(items)-1]
				evictedTemplate = name
				s.pool[name] = items[:len(items)-1]
				break
			}
		}
	}
	s.poolMu.Unlock()
	if evicted.ID == "" {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	err = s.driver.Destroy(cleanup, evicted.ID)
	if err != nil {
		s.poolMu.Lock()
		s.pool[evictedTemplate] = append(s.pool[evictedTemplate], evicted)
		s.poolMu.Unlock()
	}
	return err
}
