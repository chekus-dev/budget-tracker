package main

// hardening.go: helpers for the security fixes. Drop this next to main.go
// (same package) and apply the edits listed in EDITS.md.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// ---------- Reset tokens ----------

// hashResetToken is what gets stored in users.reset_token. The raw token only
// ever exists in the emailed link, so a leaked database cannot be turned into
// working reset links. SHA-256 is enough here (no bcrypt): the token is 256
// bits of randomness, so there is nothing to brute-force.
func hashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// ---------- Passwords ----------

const (
	minPasswordLen   = 8
	maxPasswordBytes = 72 // bcrypt silently ignores (or rejects) anything past this
)

// validateNewPassword returns a user-facing message, or "" when the password is
// acceptable. Used by signup, change-password and reset so the rule lives in
// one place. Existing users with shorter passwords can still sign in; this only
// applies when a password is being set.
func validateNewPassword(pw string) string {
	if len(pw) < minPasswordLen {
		return fmt.Sprintf("Password must be at least %d characters.", minPasswordLen)
	}
	if len(pw) > maxPasswordBytes {
		return fmt.Sprintf("Password must be %d characters or fewer.", maxPasswordBytes)
	}
	return ""
}

// dummyHash is compared against when a username does not exist, so a missing
// account costs the same bcrypt time as a wrong password. Without it, response
// time reveals which usernames are registered.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), bcrypt.DefaultCost)

// ---------- Sessions ----------

// sessionStillValid checks the session's version stamp against users.
// session_version. Changing or resetting a password bumps the column, which
// invalidates every cookie issued before it, including a stolen one.
//
// Sessions issued before this change carry no stamp and are treated as
// version 0, which matches the column default, so nobody is logged out on
// deploy. A database error fails open: the handler's own query will surface
// the outage, and a blip should not sign everyone out.
func sessionStillValid(r *http.Request, userID int) bool {
	if db == nil {
		return true
	}
	sv, _ := getSession(r).Values["sv"].(int)

	var current int
	err := db.QueryRow("SELECT session_version FROM users WHERE id = $1", userID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return false // account no longer exists
	}
	if err != nil {
		log.Printf("session version check failed: %v", err)
		return true
	}
	return sv == current
}

// ---------- HTTP ----------

// serverError logs the real error and shows the visitor a generic one. Raw
// database errors in a response body leak table and column names.
func serverError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
}

// contentSecurityPolicy is deliberately loose where the app needs it:
//   - 'unsafe-inline' for scripts, because the templates use inline <script>
//     blocks and the PDF export uses an inline onclick. This weakens the
//     script protection a lot; moving the inline code to /static and using
//     nonces would let you drop it. The other directives still do real work.
//   - Google's reCAPTCHA hosts for the signup widget.
//   - Google Fonts (the Inter typeface): stylesheet from fonts.googleapis.com,
//     font files from fonts.gstatic.com.
//
// If a page loads a chart library or fonts from a CDN, add that host to
// script-src / style-src / font-src (the browser console names the exact one).
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline' https://www.google.com/recaptcha/ https://www.gstatic.com/recaptcha/; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"img-src 'self' data:; " +
	"font-src 'self' https://fonts.gstatic.com; " +
	"connect-src 'self'; " +
	"frame-src https://www.google.com/recaptcha/ https://recaptcha.google.com/recaptcha/; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// cspHeader picks the header from CSP_MODE:
//
//	unset / "report-only"  log violations in the browser console, block nothing (default)
//	"enforce"              block violations
//	"off"                  send no CSP header
//
// Run report-only first, click through every page, and fix what the console
// reports before switching to enforce.
func cspHeader() (name, value string) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CSP_MODE"))) {
	case "off":
		return "", ""
	case "enforce":
		return "Content-Security-Policy", contentSecurityPolicy
	default:
		return "Content-Security-Policy-Report-Only", contentSecurityPolicy
	}
}

// securityHeaders sets safe defaults on every response, including a
// Content-Security-Policy whose mode comes from CSP_MODE (see cspHeader).
func securityHeaders(next http.Handler) http.Handler {
	cspName, cspValue := cspHeader()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if cspName != "" {
			h.Set(cspName, cspValue)
		}
		next.ServeHTTP(w, r)
	})
}
