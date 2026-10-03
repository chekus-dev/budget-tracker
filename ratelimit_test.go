package main

import (
	"net/http"
	"testing"
	"time"
)

// fakeClock lets the tests move time by hand. The limiter reads time through
// its now field for exactly this reason: a window of fifteen real minutes
// would otherwise make these tests either slow or impossible.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestLimiter(max int) (*rateLimiter, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	l := newRateLimiter(max)
	l.now = clock.now
	return l, clock
}

func TestRateLimiterAllowsUpToBudgetThenBlocks(t *testing.T) {
	l, _ := newTestLimiter(3)

	// The first max-1 failures are allowed through (a zero wait means "no
	// lockout", not "no failure recorded").
	for i := 0; i < 2; i++ {
		if wait := l.fail("k"); wait != 0 {
			t.Fatalf("failure %d: got wait %v, want 0", i+1, wait)
		}
	}

	// The third failure spends the budget, so it reports the lockout.
	if wait := l.fail("k"); wait <= 0 {
		t.Fatalf("failure 3: got wait %v, want a positive lockout", wait)
	}
	if wait := l.retryAfter("k"); wait <= 0 {
		t.Fatalf("retryAfter after budget spent: got %v, want a positive lockout", wait)
	}
}

func TestRateLimiterRetryAfterIsZeroBelowBudget(t *testing.T) {
	l, _ := newTestLimiter(3)
	l.fail("k")

	if wait := l.retryAfter("k"); wait != 0 {
		t.Fatalf("retryAfter below budget: got %v, want 0", wait)
	}
}

func TestRateLimiterKeysAreIndependent(t *testing.T) {
	l, _ := newTestLimiter(2)
	l.fail("a")
	l.fail("a")

	if wait := l.retryAfter("a"); wait <= 0 {
		t.Fatalf("key a should be locked out, got %v", wait)
	}
	if wait := l.retryAfter("b"); wait != 0 {
		t.Fatalf("key b should be unaffected, got %v", wait)
	}
}

func TestRateLimiterWindowExpiryClearsTheBudget(t *testing.T) {
	l, clock := newTestLimiter(2)
	l.fail("k")
	l.fail("k")
	if wait := l.retryAfter("k"); wait <= 0 {
		t.Fatalf("expected a lockout before the window expires")
	}

	clock.advance(loginWindow + time.Second)

	if wait := l.retryAfter("k"); wait != 0 {
		t.Fatalf("after the window expired: got %v, want 0", wait)
	}
	// And the counter starts over rather than resuming at the old count.
	if wait := l.fail("k"); wait != 0 {
		t.Fatalf("first failure in the new window: got %v, want 0", wait)
	}
}

func TestRateLimiterResetClearsCount(t *testing.T) {
	l, _ := newTestLimiter(2)
	l.fail("k")
	l.fail("k")
	l.reset("k")

	if wait := l.retryAfter("k"); wait != 0 {
		t.Fatalf("after reset: got %v, want 0", wait)
	}
	if wait := l.fail("k"); wait != 0 {
		t.Fatalf("failure after reset should start a fresh budget: got %v", wait)
	}
}

func TestRateLimiterSweepDropsExpiredKeys(t *testing.T) {
	l, clock := newTestLimiter(5)
	l.fail("old")
	clock.advance(loginWindow + time.Second)
	l.fail("fresh") // also triggers nothing; sweeps are explicit

	l.sweep()

	l.mu.Lock()
	_, keptOld := l.buckets["old"]
	_, keptFresh := l.buckets["fresh"]
	l.mu.Unlock()

	if keptOld {
		t.Error("sweep should have dropped the expired key")
	}
	if !keptFresh {
		t.Error("sweep should have kept the live key")
	}
}

// The cap is what keeps a flood of unique keys from growing the map without
// bound, so it must hold even when every window is still live.
func TestRateLimiterEvictsAtCapacity(t *testing.T) {
	l, _ := newTestLimiter(5)

	for i := 0; i < maxRateKeys+500; i++ {
		l.fail(string(rune('a'+i%26)) + string(rune(i)))
	}

	l.mu.Lock()
	size := len(l.buckets)
	l.mu.Unlock()

	if size > maxRateKeys {
		t.Fatalf("map grew to %d entries, want at most %d", size, maxRateKeys)
	}
}

func TestUserRateKeyIsCaseAndSpaceInsensitive(t *testing.T) {
	// Otherwise "Alice" and "alice " would be separate budgets for the same
	// account, and the lockout could be sidestepped by changing case.
	if userRateKey(" Alice ") != userRateKey("alice") {
		t.Errorf("userRateKey(%q)=%q and userRateKey(%q)=%q should match",
			" Alice ", userRateKey(" Alice "), "alice", userRateKey("alice"))
	}
}

func TestClientIPUsesLastForwardedHop(t *testing.T) {
	// A client-supplied X-Forwarded-For must not be able to choose the value:
	// the limiter buckets by this string, so a forged entry per request would
	// be an unlimited supply of fresh buckets.
	r, err := http.NewRequest("POST", "/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.RemoteAddr = "10.0.0.9:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4, 203.0.113.7")

	if got := clientIP(r); got != "203.0.113.7" {
		t.Fatalf("clientIP = %q, want the last hop %q", got, "203.0.113.7")
	}
}

func TestClientIPFallsBackToRemoteAddr(t *testing.T) {
	r, err := http.NewRequest("POST", "/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.RemoteAddr = "10.0.0.9:5555"

	if got := clientIP(r); got != "10.0.0.9" {
		t.Fatalf("clientIP = %q, want %q", got, "10.0.0.9")
	}
}

func TestLoginLockoutMessageRoundsUp(t *testing.T) {
	cases := []struct {
		wait time.Duration
		want string
	}{
		{10 * time.Second, "Too many failed sign-in attempts. Please wait a minute and try again."},
		{90 * time.Second, "Too many failed sign-in attempts. Please wait 2 minutes and try again."},
		{15 * time.Minute, "Too many failed sign-in attempts. Please wait 15 minutes and try again."},
	}
	for _, c := range cases {
		if got := loginLockoutMessage(c.wait); got != c.want {
			t.Errorf("loginLockoutMessage(%v) = %q, want %q", c.wait, got, c.want)
		}
	}
}
