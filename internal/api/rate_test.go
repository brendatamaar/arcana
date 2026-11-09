package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/brendatamaar/arcana/internal/auth"
	"github.com/brendatamaar/arcana/internal/config"
	"github.com/brendatamaar/arcana/internal/orchestrator"
	"github.com/brendatamaar/arcana/internal/runtime/fake"
	"github.com/brendatamaar/arcana/internal/store"
)

type rateTestClock struct{ at time.Time }

func (c *rateTestClock) Now() time.Time { return c.at }

func TestCreationLimiter_RefillIsolationAndBackwardsClock(t *testing.T) {
	clock := &rateTestClock{at: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}
	limiter := newCreationLimiter(2, clock)
	for i, want := range []int64{0, 0, 30} {
		if got := limiter.admit("a"); got != want {
			t.Fatalf("admission %d: got %d want %d", i, got, want)
		}
	}
	if limiter.admit("b") != 0 {
		t.Fatal("key isolation incorrect")
	}
	clock.at = clock.at.Add(15 * time.Second)
	if retry := limiter.admit("a"); retry != 15 {
		t.Fatal(retry)
	}
	clock.at = clock.at.Add(-time.Minute)
	if limiter.admit("a") == 0 {
		t.Fatal("backwards clock granted tokens")
	}
	clock.at = clock.at.Add(75 * time.Second)
	if limiter.admit("a") != 0 {
		t.Fatal("budget did not refill")
	}
}

func TestCreationLimiter_ConcurrentAndBoundedCache(t *testing.T) {
	clock := &rateTestClock{at: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)}
	limiter := newCreationLimiter(30, clock)
	var count atomic.Int64
	var group sync.WaitGroup
	for range 100 {
		group.Go(func() {
			if limiter.admit("shared") == 0 {
				count.Add(1)
			}
		})
	}
	group.Wait()
	if count.Load() != 30 {
		t.Fatal(count.Load())
	}
	for i := 0; i < 9999; i++ {
		if limiter.admit(fmt.Sprint(i)) != 0 {
			t.Fatal("early cache limit")
		}
	}
	if limiter.admit("overflow") == 0 || len(limiter.buckets) != 10000 {
		t.Fatal("cache cap ignored")
	}
	clock.at = clock.at.Add(2 * time.Minute)
	if limiter.admit("overflow") != 0 || len(limiter.buckets) > 10000 {
		t.Fatal("stale buckets not reclaimed")
	}
}

func TestCreationRate_HeadersAuthAndNoRejectedResource(t *testing.T) {
	ctx := context.Background()
	database, err := store.Open(ctx, filepath.Join(t.TempDir(), "rate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	keys := auth.New(database, auth.SystemClock{})
	owner, secret, err := keys.Create(ctx, "owner", false)
	if err != nil {
		t.Fatal(err)
	}
	_, other, err := keys.Create(ctx, "other", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	driver := fake.New()
	service := orchestrator.New(database, driver, cfg, orchestrator.SystemClock{})
	clock := &rateTestClock{at: time.Now()}
	handler := New(keys, slog.New(slog.NewTextHandler(io.Discard, nil)), cfg.Limits.Files, service, Options{CreatePerMinute: 1, RateClock: clock})
	if response := request(handler, "POST", "/v1/sandboxes", "", `{"template":"base"}`); response.Code != 401 {
		t.Fatal(response.Code)
	}
	if response := request(handler, "POST", "/v1/sandboxes", secret, `{"template":"base"}`); response.Code != 201 {
		t.Fatal(response.Code, response.Body.String())
	}
	response := request(handler, "POST", "/v1/sandboxes", secret, `{"template":"base"}`)
	if response.Code != 429 || response.Header().Get("Retry-After") != "60" || !strings.Contains(response.Body.String(), "rate_limited") {
		t.Fatal(response.Code, response.Header(), response.Body.String())
	}
	rows, err := service.List(ctx, auth.Principal{ID: owner.ID}, orchestrator.ListRequest{Limit: 100})
	if err != nil || len(rows) != 1 {
		t.Fatal("rejected creation reserved resources", rows, err)
	}
	instances, err := driver.List(ctx)
	if err != nil || len(instances) != 1 {
		t.Fatal("rejected creation reached the runtime", instances, err)
	}
	if response := request(handler, "POST", "/v1/sandboxes", other, `{"template":"base"}`); response.Code != 201 {
		t.Fatal("keys shared a bucket", response.Code)
	}
	clock.at = clock.at.Add(time.Minute)
	if response := request(handler, "POST", "/v1/sandboxes", secret, `{"template":"base"}`); response.Code != 201 {
		t.Fatal("refill failed", response.Code)
	}
}
