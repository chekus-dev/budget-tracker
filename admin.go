package main

// admin.go — internal admin gate.
//
// Access is controlled by ADMIN_USER_IDS, a comma-separated list of user
// IDs in the environment. Non-admins get 404, not 403 — a probing attacker
// cannot confirm the route exists. There is no is_admin column: admin-ness
// lives in the environment, so a database dump cannot leak it and a
// migration cannot corrupt it.

import (
	"net/http"
	"os"
	"strconv"
	"strings"
)

// adminIDs parses ADMIN_USER_IDS once per request. Cheap enough — the
// environment read is a single map lookup in the OS — and it means revoking
// admin access only requires editing the env var on Render, not a redeploy
// of new code.
func adminIDs() map[int]bool {
	raw := strings.TrimSpace(os.Getenv("ADMIN_USER_IDS"))
	if raw == "" {
		return nil
	}
	out := map[int]bool{}
	for _, part := range strings.Split(raw, ",") {
		if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			out[id] = true
		}
	}
	return out
}

// requireAdmin wraps a handler and rejects anyone who is not signed in and
// listed in ADMIN_USER_IDS. The 404 is deliberate: a 403 would confirm the
// route exists, which is a useful signal for an attacker probing endpoints.
func requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, _, ok := currentUser(r)
		if !ok {
			// Not signed in — send to login as usual. requireAuth would
			// also do this, but requireAdmin is often used without it.
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !adminIDs()[userID] {
			// Signed in but not an admin. Pretend the route does not exist.
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}