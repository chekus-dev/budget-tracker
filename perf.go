package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"
)

// authCacheTTL bounds how long a revoked session can keep working: a disabled
// account or a "sign out everywhere" takes effect within this window. Anything
// that says "not allowed" is never trusted from the cache, it is re-checked.
const authCacheTTL = 15 * time.Second

type authState struct {
	exists         bool
	deleted        bool
	onboarded      bool
	sessionVersion int
	fetched        time.Time
}

var (
	authMu    sync.Mutex
	authCache = map[int]authState{}
)

func fetchAuthState(userID int) (authState, error) {
	st := authState{fetched: time.Now()}
	err := db.QueryRow(
		`SELECT session_version, deleted_at IS NOT NULL, onboarded_at IS NOT NULL
		 FROM users WHERE id = $1`, userID,
	).Scan(&st.sessionVersion, &st.deleted, &st.onboarded)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.exists = true
	return st, nil
}

// authStateFor returns the cached row when it is fresh and trust() accepts it,
// otherwise it reads the database once and caches the result.
func authStateFor(userID int, trust func(authState) bool) (authState, error) {
	authMu.Lock()
	st, ok := authCache[userID]
	authMu.Unlock()
	if ok && time.Since(st.fetched) < authCacheTTL && trust(st) {
		return st, nil
	}
	fresh, err := fetchAuthState(userID)
	if err != nil {
		return fresh, err
	}
	authMu.Lock()
	if len(authCache) > 5000 {
		for id, s := range authCache {
			if time.Since(s.fetched) > authCacheTTL {
				delete(authCache, id)
			}
		}
	}
	authCache[userID] = fresh
	authMu.Unlock()
	return fresh, nil
}

// sessionStillValidCached replaces sessionStillValid on the request path. Same
// behaviour: a missing account or a changed session version is invalid, and a
// database error does not sign anyone out.
func sessionStillValidCached(r *http.Request, userID int) bool {
	if db == nil {
		return true
	}
	sv, _ := getSession(r).Values["sv"].(int)
	st, err := authStateFor(userID, func(s authState) bool {
		return s.exists && s.sessionVersion == sv
	})
	if err != nil {
		log.Printf("session version check failed: %v", err)
		return true
	}
	if !st.exists {
		return false
	}
	return st.sessionVersion == sv
}

// accountDisabledCached replaces the deleted_at query in requireAuth.
func accountDisabledCached(userID int) bool {
	if db == nil {
		return false
	}
	st, err := authStateFor(userID, func(s authState) bool {
		return s.exists && !s.deleted
	})
	if err != nil {
		return true
	}
	return !st.exists || st.deleted
}

// userOnboardedCached replaces userOnboarded on Home. Only "onboarded" is
// trusted from the cache, so finishing the wizard shows up immediately.
func userOnboardedCached(userID int) (bool, error) {
	st, err := authStateFor(userID, func(s authState) bool {
		return !s.exists || s.onboarded
	})
	if err != nil {
		return false, err
	}
	if !st.exists {
		return true, nil
	}
	return st.onboarded, nil
}

var (
	seenMu      sync.Mutex
	lastStamped = map[int]time.Time{}
)

// stampLastSeenThrottled calls stampLastSeen at most once an hour per user.
func stampLastSeenThrottled(userID int) {
	seenMu.Lock()
	if t, ok := lastStamped[userID]; ok && time.Since(t) < 61*time.Minute {
		seenMu.Unlock()
		return
	}
	if len(lastStamped) > 5000 {
		for id, t := range lastStamped {
			if time.Since(t) > 61*time.Minute {
				delete(lastStamped, id)
			}
		}
	}
	lastStamped[userID] = time.Now()
	seenMu.Unlock()
	stampLastSeen(userID)
}

// requestTimer logs any request that takes longer than half a second.
func requestTimer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if d := time.Since(start); d > 500*time.Millisecond && r.URL.Path != "/healthz" {
			log.Printf("slow request: %s %s took %s", r.Method, r.URL.Path, d.Round(time.Millisecond))
		}
	})
}

// healthzHandler also reports the database round trip. The first ping may have
// to open a connection, so db_connect_ms includes that, and db_ms is a second,
// warm ping: the cost of one query from this server to the database.
func healthzHandler(w http.ResponseWriter, r *http.Request) {
	dbStatus := "ok"
	connectMS, warmMS := int64(-1), int64(-1)
	if db == nil {
		dbStatus = "not configured"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		t0 := time.Now()
		if err := db.PingContext(ctx); err != nil {
			dbStatus = "unavailable"
			log.Printf("healthz: database ping failed: %v", err)
		} else {
			connectMS = time.Since(t0).Milliseconds()
			t1 := time.Now()
			if err := db.PingContext(ctx); err == nil {
				warmMS = time.Since(t1).Milliseconds()
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"status":        "ok",
		"database":      dbStatus,
		"db_connect_ms": connectMS,
		"db_ms":         warmMS,
		"version":       appVersion,
	})
}
