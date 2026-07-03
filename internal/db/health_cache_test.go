package db

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHealthCache_CachesWithinTTL verifies that calls within the TTL reuse the
// cached result and do NOT re-run the underlying check.
func TestHealthCache_CachesWithinTTL(t *testing.T) {
	var calls atomic.Int64
	now := time.Unix(0, 0)
	c := NewHealthCache(func(context.Context) error {
		calls.Add(1)
		return nil
	}, 2*time.Second)
	c.now = func() time.Time { return now }

	for range 5 {
		if err := c.Check(context.Background()); err != nil {
			t.Fatalf("Check: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("underlying check ran %d times within TTL, want 1", got)
	}
}

// TestHealthCache_RefreshesAfterTTL verifies the check re-runs once the TTL has
// elapsed, and that a newly-failing backend is reflected.
func TestHealthCache_RefreshesAfterTTL(t *testing.T) {
	var calls atomic.Int64
	now := time.Unix(0, 0)
	wantErr := errors.New("db down")
	c := NewHealthCache(func(context.Context) error {
		if calls.Add(1) == 1 {
			return nil // first window healthy
		}
		return wantErr // later windows unhealthy
	}, time.Second)
	c.now = func() time.Time { return now }

	if err := c.Check(context.Background()); err != nil {
		t.Fatalf("first Check: %v", err)
	}
	// Still within TTL — cached healthy, no new call.
	if err := c.Check(context.Background()); err != nil {
		t.Fatalf("cached Check: %v", err)
	}
	// Advance past the TTL — the check re-runs and now reports the failure.
	now = now.Add(2 * time.Second)
	if err := c.Check(context.Background()); !errors.Is(err, wantErr) {
		t.Errorf("after TTL: got %v want %v", err, wantErr)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("check ran %d times, want 2 (one per window)", got)
	}
}

// TestHealthCache_DetachesCallerCancellation verifies the security fix: the
// underlying check runs on a context detached from the caller's cancellation,
// so a caller that has already disconnected cannot make the shared ping fail.
func TestHealthCache_DetachesCallerCancellation(t *testing.T) {
	var gotCancelled bool
	c := NewHealthCache(func(ctx context.Context) error {
		// The detached context must NOT be cancelled even though the caller's was.
		gotCancelled = ctx.Err() != nil
		return ctx.Err()
	}, time.Minute)

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // caller already gone

	if err := c.Check(cancelledCtx); err != nil {
		t.Errorf("Check should complete on a detached context, got err: %v", err)
	}
	if gotCancelled {
		t.Error("underlying check received a cancelled context — WithoutCancel detachment missing")
	}
}

// TestHealthCache_DetachedContextPreservesValues verifies WithoutCancel keeps
// context values (only cancellation/deadline are dropped).
func TestHealthCache_DetachedContextPreservesValues(t *testing.T) {
	type ctxKey struct{}
	var gotVal any
	c := NewHealthCache(func(ctx context.Context) error {
		gotVal = ctx.Value(ctxKey{})
		return nil
	}, time.Minute)

	ctx := context.WithValue(context.Background(), ctxKey{}, "v")
	_ = c.Check(ctx)
	if gotVal != "v" {
		t.Errorf("context value not preserved through the cache: got %v want \"v\"", gotVal)
	}
}

// TestHealthCache_DoesNotCacheCanceled verifies the defence-in-depth rule: a
// context.Canceled result is returned but not cached, so the next caller runs a
// fresh check rather than inheriting a poisoned failure.
func TestHealthCache_DoesNotCacheCanceled(t *testing.T) {
	var calls atomic.Int64
	now := time.Unix(0, 0)
	c := NewHealthCache(func(context.Context) error {
		if calls.Add(1) == 1 {
			return context.Canceled // first call "cancels"
		}
		return nil // subsequent calls healthy
	}, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Check(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Check: got %v want context.Canceled", err)
	}
	// Immediately (well within TTL) a second caller must trigger a FRESH check,
	// not see a cached cancellation.
	if err := c.Check(context.Background()); err != nil {
		t.Errorf("second Check should re-run and succeed, got: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("check ran %d times, want 2 (cancellation must not be cached)", got)
	}
}

// TestHealthCache_CachesDeadlineExceeded verifies that a genuine ping timeout
// (context.DeadlineExceeded) IS cached within the TTL — it is real database
// signal, unlike a caller cancellation.
func TestHealthCache_CachesDeadlineExceeded(t *testing.T) {
	var calls atomic.Int64
	now := time.Unix(0, 0)
	c := NewHealthCache(func(context.Context) error {
		calls.Add(1)
		return context.DeadlineExceeded
	}, time.Minute)
	c.now = func() time.Time { return now }

	if err := c.Check(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Check: got %v want context.DeadlineExceeded", err)
	}
	// Within TTL: cached, no second call.
	if err := c.Check(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second Check: got %v want cached context.DeadlineExceeded", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("check ran %d times, want 1 (timeout must be cached)", got)
	}
}

// TestHealthCache_ConcurrentFloodCollapses verifies that a burst of concurrent
// probes collapses to a single underlying check (the lock serialises them and
// all but the first observe the cached value). Run with -race.
func TestHealthCache_ConcurrentFloodCollapses(t *testing.T) {
	var calls atomic.Int64
	now := time.Unix(0, 0)
	c := NewHealthCache(func(context.Context) error {
		calls.Add(1)
		return nil
	}, time.Minute)
	c.now = func() time.Time { return now }

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Check(context.Background())
		}()
	}
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("flood of 50 probes ran the check %d times, want 1", got)
	}
}
