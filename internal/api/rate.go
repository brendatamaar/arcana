package api

import (
	"math"
	"sync"
	"time"
)

// RateClock makes creation admission deterministic in tests.
type (
	RateClock interface{ Now() time.Time }
	rateClock struct{}
)

func (rateClock) Now() time.Time { return time.Now() }

type bucket struct {
	tokens         float64
	refilled, seen time.Time
}
type creationLimiter struct {
	mu      sync.Mutex
	buckets map[string]bucket
	rate    int
	clock   RateClock
}

func newCreationLimiter(rate int, clock RateClock) *creationLimiter {
	if rate < 1 || rate > 10000 {
		rate = 30
	}
	if clock == nil {
		clock = rateClock{}
	}
	return &creationLimiter{rate: rate, clock: clock, buckets: make(map[string]bucket)}
}

func (l *creationLimiter) admit(key string) int64 {
	if key == "" || len(key) > 128 {
		return 120
	}
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	state, found := l.buckets[key]
	if !found {
		if len(l.buckets) >= 10000 {
			for id, item := range l.buckets {
				if now.Sub(item.seen) >= 2*time.Minute {
					delete(l.buckets, id)
				}
			}
			if len(l.buckets) >= 10000 {
				return 120
			}
		}
		state = bucket{tokens: float64(l.rate), refilled: now, seen: now}
	}
	if elapsed := now.Sub(state.refilled).Seconds(); elapsed > 0 {
		state.tokens = math.Min(float64(l.rate), state.tokens+elapsed*float64(l.rate)/60)
		state.refilled = now
	}
	if now.After(state.seen) {
		state.seen = now
	}
	if state.tokens >= 1 {
		state.tokens--
		l.buckets[key] = state
		return 0
	}
	l.buckets[key] = state
	return max(1, int64(math.Ceil((1-state.tokens)*60/float64(l.rate))))
}
