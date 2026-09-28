package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a manually advanced clock for window tests (no sleeping).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestFixedWindow_AllowsUpToLimitThenDenies(t *testing.T) {
	f := NewFixedWindow(3, time.Minute)

	for i := 1; i <= 3; i++ {
		if !f.Allow("k") {
			t.Errorf("call %d: got denied, want allowed (limit 3)", i)
		}
	}
	for i := 4; i <= 6; i++ {
		if f.Allow("k") {
			t.Errorf("call %d: got allowed, want denied (limit 3)", i)
		}
	}
}

func TestFixedWindow_WindowExpiresAndResets(t *testing.T) {
	clk := newFakeClock()
	f := NewFixedWindowWithClock(2, time.Minute, clk.Now)

	if !f.Allow("k") || !f.Allow("k") {
		t.Fatal("first two calls should pass")
	}
	if f.Allow("k") {
		t.Fatal("third call within window should be denied")
	}

	clk.Advance(time.Minute) // window rolls over
	if !f.Allow("k") {
		t.Error("call after window expiry should be allowed again")
	}
	if !f.Allow("k") {
		t.Error("second call after window expiry should be allowed again")
	}
	if f.Allow("k") {
		t.Error("third call after window expiry should be denied")
	}
}

func TestFixedWindow_KeysAreIsolated(t *testing.T) {
	f := NewFixedWindow(1, time.Minute)

	if !f.Allow("a") {
		t.Error("first call for key a should pass")
	}
	if f.Allow("a") {
		t.Error("second call for key a should be denied")
	}
	// Key b must have its own budget.
	if !f.Allow("b") {
		t.Error("key b should not be affected by key a's budget")
	}
}

func TestFixedWindow_ConcurrentExactlyLimitAllowed(t *testing.T) {
	f := NewFixedWindow(10, time.Minute)

	const goroutines = 200
	var (
		wg      sync.WaitGroup
		allowed atomic.Int64
	)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if f.Allow("shared") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	if got := allowed.Load(); got != 10 {
		t.Errorf("allowed under concurrency: got %d, want exactly 10", got)
	}
}

func TestFixedWindow_ExpiredEntriesAreSwept(t *testing.T) {
	clk := newFakeClock()
	f := NewFixedWindowWithClock(5, time.Minute, clk.Now)

	for _, k := range []string{"k1", "k2", "k3"} {
		if !f.Allow(k) {
			t.Fatalf("setup Allow(%s) denied", k)
		}
	}
	if len(f.entries) != 3 {
		t.Fatalf("entries before sweep: got %d, want 3", len(f.entries))
	}

	// Jump past the window; the next Allow triggers the once-per-window sweep,
	// which must drop the three expired entries (the new key remains).
	clk.Advance(2 * time.Minute)
	if !f.Allow("k4") {
		t.Error("Allow(k4) after window jump should pass")
	}
	if len(f.entries) != 1 {
		t.Errorf("entries after sweep: got %d, want 1 (expired entries removed)", len(f.entries))
	}
	if _, ok := f.entries["k1"]; ok {
		t.Error("expired entry k1 should have been removed")
	}
}

func TestFixedWindow_FailsClosedWhenTableFull(t *testing.T) {
	clk := newFakeClock()
	f := NewFixedWindowWithClock(10, time.Minute, clk.Now)
	f.maxEntries = 2 // shrink the cap for the test

	if !f.Allow("a") || !f.Allow("b") {
		t.Fatal("first two distinct keys should pass")
	}
	// Table is full: a brand-new key is denied (fail closed) …
	if f.Allow("c") {
		t.Error("new key with a full table should be denied (fail closed)")
	}
	// … while an existing key keeps its own budget.
	if !f.Allow("a") {
		t.Error("existing key should still be allowed with a full table")
	}

	// After the window passes, the sweep frees the table and c is admitted.
	clk.Advance(2 * time.Minute)
	if !f.Allow("c") {
		t.Error("new key should be allowed after expired entries are swept")
	}
}

func TestNewFixedWindow_PanicsOnInvalidInput(t *testing.T) {
	for name, tc := range map[string]func(){
		"zero limit":     func() { NewFixedWindow(0, time.Minute) },
		"negative limit": func() { NewFixedWindow(-1, time.Minute) },
		"zero window":    func() { NewFixedWindow(1, 0) },
		"nil clock":      func() { NewFixedWindowWithClock(1, time.Minute, nil) },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if name == "nil clock" {
					// nil clock is a convenience default (time.Now), not an error.
					if r := recover(); r != nil {
						t.Errorf("nil clock must not panic, got %v", r)
					}
					return
				}
				if r := recover(); r == nil {
					t.Error("expected panic, got none")
				}
			}()
			tc()
		})
	}
}
