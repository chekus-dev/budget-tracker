package main

// ratelimit_db.go: makes the login and password-reset rate limiters shared
// between instances by keeping their counters in Postgres.
//
// The existing in-memory rateLimiter stays in place and keeps working exactly
// as before. When a limiter's `shared` field names a bucket, its methods ask
// the database first (see the hooks added to main.go by apply_rate_limiter.py)
// and only fall back to the in-memory counters if the database cannot answer.
// A database hiccup therefore degrades to per-process limiting instead of
// either locking everybody out or switching the protection off.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"os"
	"strings"
	"time"
)

// A rate-limit check sits on the login path, so it must never hang it.
const rateLimitQueryTimeout = 2 * time.Second

// Sweeping is best-effort background maintenance, not part of a request. Give
// poolers and transient database load longer to answer than login checks get.
const rateLimitSweepTimeout = 10 * time.Second

// enableSharedRateLimits switches the four global limiters to database-backed
// counters. Call it once at startup, before startRateLimiterSweeper, so the
// sweeper goroutine sees the final configuration.
//
// RATE_LIMIT_STORE=memory keeps everything in-process (the old behaviour). If
// the rate_limits table does not exist yet, it logs that and stays in memory
// rather than erroring on every login.
func enableSharedRateLimits() {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("RATE_LIMIT_STORE")), "memory") {
		log.Println("rate limits: in-memory (RATE_LIMIT_STORE=memory)")
		return
	}
	if !dbReady() {
		log.Println("rate limits: in-memory (no database)")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), rateLimitQueryTimeout)
	defer cancel()
	if _, err := db.ExecContext(ctx, "SELECT 1 FROM rate_limits LIMIT 1"); err != nil {
		log.Printf("rate limits: in-memory, rate_limits table not usable: %v", err)
		return
	}

	loginLimiterUser.shared = "login-user"
	loginLimiterIP.shared = "login-ip"
	resetLimiterEmail.shared = "reset-email"
	resetLimiterIP.shared = "reset-ip"
	log.Println("rate limits: shared via database")
}

// limiterKeyHash is what is stored in the key column. Keys contain usernames,
// email addresses and IPs; hashing means the table holds no readable personal
// data, and the primary key stays a fixed length whatever an attacker submits.
func limiterKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func rateLimitCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), rateLimitQueryTimeout)
}

// sharedFail records one failure in the database. ok is false when the shared
// store is not in use or could not answer, in which case the caller uses its
// in-memory counters instead.
//
// One atomic upsert does the whole job: start a fresh window if the old one
// has ended, otherwise add one to the count. Two instances failing the same
// key at the same moment cannot lose an update.
func (l *rateLimiter) sharedFail(key string) (wait time.Duration, ok bool) {
	if l.shared == "" || !dbReady() {
		return 0, false
	}
	now := l.now()
	ctx, cancel := rateLimitCtx()
	defer cancel()

	var count int
	var resetAt time.Time
	err := db.QueryRowContext(ctx, `
		INSERT INTO rate_limits (bucket, key, count, reset_at)
		VALUES ($1, $2, 1, $3::timestamptz)
		ON CONFLICT (bucket, key) DO UPDATE SET
			count    = CASE WHEN rate_limits.reset_at <= $4::timestamptz THEN 1
			                ELSE rate_limits.count + 1 END,
			reset_at = CASE WHEN rate_limits.reset_at <= $4::timestamptz THEN $3::timestamptz
			                ELSE rate_limits.reset_at END
		RETURNING count, reset_at`,
		l.shared, limiterKeyHash(key), now.Add(loginWindow), now,
	).Scan(&count, &resetAt)
	if err != nil {
		log.Printf("rate limit (%s): shared fail() failed, using memory: %v", l.shared, err)
		return 0, false
	}
	if count < l.max {
		return 0, true
	}
	return resetAt.Sub(now), true
}

// sharedRetryAfter reports how long key must wait, without recording anything.
func (l *rateLimiter) sharedRetryAfter(key string) (wait time.Duration, ok bool) {
	if l.shared == "" || !dbReady() {
		return 0, false
	}
	now := l.now()
	ctx, cancel := rateLimitCtx()
	defer cancel()

	var count int
	var resetAt time.Time
	err := db.QueryRowContext(ctx,
		"SELECT count, reset_at FROM rate_limits WHERE bucket = $1 AND key = $2",
		l.shared, limiterKeyHash(key),
	).Scan(&count, &resetAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, true
	}
	if err != nil {
		log.Printf("rate limit (%s): shared retryAfter() failed, using memory: %v", l.shared, err)
		return 0, false
	}
	if !now.Before(resetAt) || count < l.max {
		return 0, true
	}
	return resetAt.Sub(now), true
}

// sharedReset clears key after a successful sign-in. The caller still clears
// its in-memory copy afterwards, which is harmless.
func (l *rateLimiter) sharedReset(key string) {
	if l.shared == "" || !dbReady() {
		return
	}
	ctx, cancel := rateLimitCtx()
	defer cancel()
	if _, err := db.ExecContext(ctx,
		"DELETE FROM rate_limits WHERE bucket = $1 AND key = $2",
		l.shared, limiterKeyHash(key),
	); err != nil {
		log.Printf("rate limit (%s): shared reset() failed: %v", l.shared, err)
	}
}

// sweepSharedRateLimits drops expired rows in one best-effort query.
//
// The query does not filter by bucket. An earlier version collected the
// enabled buckets from allRateLimiters and deleted only rows in those, but
// the table exists solely for this app's limiters, and coupling the sweep to
// package-global state meant a row whose limiter had been reconfigured (or
// was never registered, as in a test) could never be swept. An expired
// counter is dead weight regardless of which bucket wrote it.
func sweepSharedRateLimits(now time.Time) {
	if !dbReady() {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), rateLimitSweepTimeout)
	defer cancel()
	if _, err := db.ExecContext(ctx,
		"DELETE FROM rate_limits WHERE reset_at <= $1::timestamptz",
		now,
	); err != nil {
		log.Printf("rate limit: shared cleanup failed (expired rows will be cleaned on a later sweep): %v", err)
	}
}
