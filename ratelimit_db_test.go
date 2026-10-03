package main

import (
	"database/sql"
	"os"
	"regexp"
	"testing"
	"time"
)

// ---------- helpers ----------

var testEpoch = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// newSharedTestLimiter builds a limiter on a movable clock. Two limiters with
// the same bucket name behave like two instances of the app sharing a database.
func newSharedTestLimiter(bucket string, max int, clock *time.Time) *rateLimiter {
	l := newRateLimiter(max)
	l.shared = bucket
	l.now = func() time.Time { return *clock }
	return l
}

func near(a, b, tolerance time.Duration) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tolerance
}

// useTestDB points the global db at TEST_DATABASE_URL and creates the table.
// The test is skipped when the variable is unset, so `go test` stays green on
// a machine with no database. Use a throwaway database, not production.
func useTestDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database-backed rate limit tests")
	}
	conn, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Ping(); err != nil {
		t.Fatalf("cannot reach TEST_DATABASE_URL: %v", err)
	}
	if _, err := conn.Exec(`
		CREATE TABLE IF NOT EXISTS rate_limits (
			bucket TEXT NOT NULL, key TEXT NOT NULL, count INTEGER NOT NULL,
			reset_at TIMESTAMPTZ NOT NULL, PRIMARY KEY (bucket, key))`); err != nil {
		t.Fatal(err)
	}

	old := db
	db = conn
	t.Cleanup(func() {
		conn.Exec("DELETE FROM rate_limits WHERE bucket LIKE 'test-%'")
		db = old
		conn.Close()
	})
	conn.Exec("DELETE FROM rate_limits WHERE bucket LIKE 'test-%'")
	return conn
}

// ---------- no database needed ----------

func TestRateLimitDB_KeyHash(t *testing.T) {
	a := limiterKeyHash("user:alice")
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a) {
		t.Fatalf("hash %q is not 64 hex characters", a)
	}
	if a != limiterKeyHash("user:alice") {
		t.Fatal("hash is not deterministic")
	}
	if a == limiterKeyHash("user:bob") {
		t.Fatal("different keys collided")
	}
	if a == "user:alice" {
		t.Fatal("key must not be stored readable")
	}
}

func TestRateLimitDB_UnsharedLimiterIgnoresDatabase(t *testing.T) {
	old := db
	defer func() { db = old }()
	db = nil

	l := newRateLimiter(2) // shared == ""
	if _, ok := l.sharedFail("k"); ok {
		t.Fatal("an unshared limiter must not claim to have used the database")
	}
	if _, ok := l.sharedRetryAfter("k"); ok {
		t.Fatal("an unshared limiter must not claim to have used the database")
	}
	// And the original in-memory behaviour is untouched.
	if l.fail("k") != 0 {
		t.Fatal("first failure should not lock")
	}
	if l.fail("k") == 0 {
		t.Fatal("second failure should lock at max=2")
	}
	if l.retryAfter("k") == 0 {
		t.Fatal("key should be locked")
	}
	l.reset("k")
	if l.retryAfter("k") != 0 {
		t.Fatal("reset should unlock")
	}
}

func TestRateLimitDB_NoDatabaseFallsBackToMemory(t *testing.T) {
	old := db
	defer func() { db = old }()
	db = nil

	clock := testEpoch
	l := newSharedTestLimiter("test-nodb", 2, &clock)

	if l.fail("k") != 0 {
		t.Fatal("first failure should not lock")
	}
	if l.fail("k") == 0 {
		t.Fatal("with no database the in-memory counters must still enforce the limit")
	}
}

// A database that errors must degrade to per-process limiting. It must not
// lock everybody out, and it must not switch the protection off.
func TestRateLimitDB_DatabaseErrorFallsBackToMemory(t *testing.T) {
	broken, err := sql.Open("pgx", "postgres://nobody@127.0.0.1:1/none")
	if err != nil {
		t.Fatal(err)
	}
	broken.Close() // every query now fails with "database is closed"

	old := db
	db = broken
	defer func() { db = old }()

	clock := testEpoch
	l := newSharedTestLimiter("test-broken", 2, &clock)

	if l.fail("k") != 0 {
		t.Fatal("first failure should not lock")
	}
	if l.fail("k") == 0 {
		t.Fatal("after a database error the in-memory counters must still enforce the limit")
	}
	if l.retryAfter("k") == 0 {
		t.Fatal("retryAfter should fall back to memory too")
	}
}

// ---------- needs TEST_DATABASE_URL ----------

func TestRateLimitDB_SharedAcrossInstances(t *testing.T) {
	useTestDB(t)
	clock := testEpoch
	a := newSharedTestLimiter("test-shared", 3, &clock) // "instance A"
	b := newSharedTestLimiter("test-shared", 3, &clock) // "instance B"

	if a.fail("k") != 0 || a.fail("k") != 0 {
		t.Fatal("first two failures should not lock")
	}
	wait := b.fail("k") // the third failure arrives on a different instance
	if wait == 0 {
		t.Fatal("instance B should have seen A's failures and locked")
	}
	if !near(wait, loginWindow, time.Second) {
		t.Fatalf("lockout = %v, want about %v", wait, loginWindow)
	}
	if a.retryAfter("k") == 0 {
		t.Fatal("instance A should see the lockout B triggered")
	}
}

func TestRateLimitDB_WindowExpiresAndRestarts(t *testing.T) {
	useTestDB(t)
	clock := testEpoch
	l := newSharedTestLimiter("test-window", 2, &clock)

	l.fail("k")
	if l.fail("k") == 0 {
		t.Fatal("expected a lockout at max=2")
	}

	clock = clock.Add(loginWindow + time.Second)
	if l.retryAfter("k") != 0 {
		t.Fatal("lockout should be over once the window has passed")
	}
	if l.fail("k") != 0 {
		t.Fatal("first failure of a new window must not lock")
	}
}

func TestRateLimitDB_ResetClearsKey(t *testing.T) {
	useTestDB(t)
	clock := testEpoch
	l := newSharedTestLimiter("test-reset", 2, &clock)

	l.fail("k")
	l.fail("k")
	if l.retryAfter("k") == 0 {
		t.Fatal("setup: expected a lockout")
	}
	l.reset("k")
	if l.retryAfter("k") != 0 {
		t.Fatal("reset should clear the lockout")
	}
}

func TestRateLimitDB_BucketsAreIndependent(t *testing.T) {
	useTestDB(t)
	clock := testEpoch
	login := newSharedTestLimiter("test-b-login", 1, &clock)
	reset := newSharedTestLimiter("test-b-reset", 1, &clock)

	if login.fail("same-key") == 0 {
		t.Fatal("max=1 should lock on the first failure")
	}
	if reset.retryAfter("same-key") != 0 {
		t.Fatal("a login lockout must not affect the reset budget for the same key")
	}
}

func TestRateLimitDB_SweepRemovesOnlyExpired(t *testing.T) {
	conn := useTestDB(t)
	clock := testEpoch
	l := newSharedTestLimiter("test-sweep", 5, &clock)

	l.fail("old")
	clock = clock.Add(loginWindow + time.Minute)
	l.fail("fresh")
	l.sweep()

	var n int
	if err := conn.QueryRow("SELECT COUNT(*) FROM rate_limits WHERE bucket = 'test-sweep'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows left after sweep = %d, want 1 (only the unexpired one)", n)
	}
}

func TestRateLimitDB_KeysAreStoredHashed(t *testing.T) {
	conn := useTestDB(t)
	clock := testEpoch
	l := newSharedTestLimiter("test-hashed", 5, &clock)

	l.fail("reset:someone@example.com")

	var stored string
	if err := conn.QueryRow("SELECT key FROM rate_limits WHERE bucket = 'test-hashed'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "reset:someone@example.com" {
		t.Fatal("the email address was stored in plain text")
	}
	if len(stored) != 64 {
		t.Fatalf("stored key length = %d, want a 64-character hash", len(stored))
	}
}
