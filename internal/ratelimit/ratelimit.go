// Package ratelimit provides the Phase 8D.1 in-process rate limiter.
//
// The implementation is deliberately dependency-free (no Redis, no Kafka):
// a single-instance deployment is fully protected, and a multi-instance
// deployment degrades to per-instance budgets — a documented limitation
// until a distributed limiter is introduced in a later phase.
//
// The limiter uses fixed windows (one bucket per key per window) and is
// safe for concurrent use. Memory stays bounded two ways:
//
//  1. Expired entries are swept at most once per window.
//  2. A hard cap on live entries makes the limiter FAIL CLOSED for brand-new
//     keys once the table is full (existing keys keep working) — the limiter
//     must never become the source of an unbounded-memory crash.
package ratelimit

import (
	"sync"
	"time"
)

// RateLimiter decides whether a request identified by key may proceed.
// Implementations must be safe for concurrent use.
type RateLimiter interface {
	// Allow reports whether the key is within its budget for the current
	// window, counting the call against the key when it is.
	Allow(key string) bool
}

// defaultMaxEntries bounds the entry table (see package comment).
const defaultMaxEntries = 65536

// windowEntry is one key's fixed-window state.
type windowEntry struct {
	count       int
	windowStart time.Time
}

// FixedWindow implements RateLimiter: at most `limit` successful Allow calls
// per key within each window of length `window`.
//
// The zero value is not usable — construct with NewFixedWindow or
// NewFixedWindowWithClock.
type FixedWindow struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	now         func() time.Time
	entries     map[string]*windowEntry
	lastSweepAt time.Time
	maxEntries  int
}

// NewFixedWindow returns a limiter allowing at most `limit` calls per key
// per `window`.
//
// It panics on non-positive inputs: configuration validation
// (config.parseRateLimitConfig) rejects bad values before construction, so a
// panic here is a programming error and must fail fast at startup rather
// than silently produce an unlimited limiter.
func NewFixedWindow(limit int, window time.Duration) *FixedWindow {
	return NewFixedWindowWithClock(limit, window, time.Now)
}

// NewFixedWindowWithClock is NewFixedWindow with an injectable clock, so
// tests can advance windows without real sleeps.
func NewFixedWindowWithClock(limit int, window time.Duration, now func() time.Time) *FixedWindow {
	if limit < 1 {
		panic("ratelimit: limit must be >= 1")
	}
	if window <= 0 {
		panic("ratelimit: window must be > 0")
	}
	if now == nil {
		now = time.Now
	}
	return &FixedWindow{
		limit:       limit,
		window:      window,
		now:         now,
		entries:     make(map[string]*windowEntry),
		lastSweepAt: now(),
		maxEntries:  defaultMaxEntries,
	}
}

// Allow implements RateLimiter.
func (f *FixedWindow) Allow(key string) bool {
	now := f.now()

	f.mu.Lock()
	defer f.mu.Unlock()

	// Sweep expired entries at most once per window — bounded memory with
	// O(entries) work no more often than the window itself.
	if now.Sub(f.lastSweepAt) >= f.window {
		for k, e := range f.entries {
			if now.Sub(e.windowStart) >= f.window {
				delete(f.entries, k)
			}
		}
		f.lastSweepAt = now
	}

	e, ok := f.entries[key]
	if !ok {
		// Hard cap for brand-new keys: the sweep above already ran, so the
		// table is genuinely full of live entries. Fail closed (deny) rather
		// than grow without bound.
		if len(f.entries) >= f.maxEntries {
			return false
		}
		f.entries[key] = &windowEntry{count: 1, windowStart: now}
		return true
	}

	// Window rollover for this key: reset the bucket.
	if now.Sub(e.windowStart) >= f.window {
		e.count = 0
		e.windowStart = now
	}
	e.count++
	return e.count <= f.limit
}
