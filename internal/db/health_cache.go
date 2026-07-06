package db

import (
	"context"
	"errors"
	"sync"
	"time"
)

// HealthCache memoises the result of a health check for a short TTL so a flood
// of readiness probes (which is a cheap, unauthenticated resource-consumption
// vector on a public listener) cannot multiply database pings. It is safe for
// concurrent use.
//
// The lock is intentionally held across the underlying check: under a burst,
// the first caller runs the single ping while the rest block briefly, then all
// observe the freshly-cached result, collapsing N concurrent probes into one
// backend round-trip.
type HealthCache struct {
	check func(context.Context) error
	ttl   time.Duration
	now   func() time.Time // seam for tests; defaults to time.Now

	mu       sync.Mutex
	expires  time.Time
	lastErr  error
	hasValue bool
}

// NewHealthCache wraps check with a TTL cache. A non-positive ttl disables
// caching (every call runs check).
func NewHealthCache(check func(context.Context) error, ttl time.Duration) *HealthCache {
	return &HealthCache{check: check, ttl: ttl, now: time.Now}
}

// Check returns the cached health result if it is still within the TTL,
// otherwise it runs the underlying check and caches the outcome.
//
// The underlying check runs on a context DETACHED from the caller's
// cancellation (context.WithoutCancel: context values are preserved, but the
// caller's cancellation and deadline are dropped). This is a security
// requirement, not a nicety: the result is SHARED with every other caller, so a
// single caller's cancellation must never decide it. Without detachment, an
// internet client could request /readyz and abort the connection mid-ping:
// pool.Ping fails with context.Canceled, the failure is cached for the whole
// TTL, and the real load-balancer probe then sees 503 and ejects a healthy
// instance. A remotely triggerable DoS that the cache itself would introduce.
// The bound on the ping remains the underlying check's own timeout (HealthCheck
// applies 3s), which WithoutCancel leaves intact.
//
// Defence in depth: a context.Canceled result is returned to the caller but NOT
// cached. Even though detachment makes it nearly unreachable, caching a
// cancellation would still be wrong, so the next caller runs a fresh check.
// context.DeadlineExceeded IS cached: a ping that genuinely timed out is real
// signal about the database, not caller noise.
func (c *HealthCache) Check(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.hasValue && c.ttl > 0 && now.Before(c.expires) {
		return c.lastErr
	}

	err := c.check(context.WithoutCancel(ctx))
	if errors.Is(err, context.Canceled) {
		// Do not poison the shared cache with a cancellation: leave
		// hasValue/expires untouched so the next caller re-checks.
		return err
	}
	c.lastErr = err
	c.expires = now.Add(c.ttl)
	c.hasValue = true
	return err
}
