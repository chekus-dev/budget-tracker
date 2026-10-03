package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ---------- Reset tokens ----------

func TestHardening_HashResetToken_KnownVector(t *testing.T) {
	// SHA-256("abc"), the standard test vector.
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := hashResetToken("abc"); got != want {
		t.Fatalf("hashResetToken(abc) = %s, want %s", got, want)
	}
}

func TestHardening_HashResetToken_Properties(t *testing.T) {
	a := hashResetToken("token-one")

	if !hex64.MatchString(a) {
		t.Fatalf("hash %q is not 64 lowercase hex characters", a)
	}
	if a != hashResetToken("token-one") {
		t.Fatal("hash is not deterministic")
	}
	if a == hashResetToken("token-two") {
		t.Fatal("different tokens produced the same hash")
	}
	if a == "token-one" {
		t.Fatal("hash must not equal the raw token")
	}
}

func TestHardening_GeneratedTokenIsNeverStoredRaw(t *testing.T) {
	t1, err := generateResetToken()
	if err != nil {
		t.Fatal(err)
	}
	t2, err := generateResetToken()
	if err != nil {
		t.Fatal(err)
	}

	if !hex64.MatchString(t1) {
		t.Fatalf("token %q should be 32 random bytes as 64 hex characters", t1)
	}
	if t1 == t2 {
		t.Fatal("two generated tokens were identical")
	}
	if stored := hashResetToken(t1); stored == t1 {
		t.Fatal("the value stored in the database must differ from the emailed token")
	}
}

// ---------- Passwords ----------

func TestHardening_ValidateNewPassword(t *testing.T) {
	cases := []struct {
		name    string
		pw      string
		wantErr bool
		contain string
	}{
		{"empty", "", true, "at least 8"},
		{"seven bytes", "1234567", true, "at least 8"},
		{"old minimum of six", "abcdef", true, "at least 8"},
		{"exactly eight", "12345678", false, ""},
		{"normal passphrase", "correct horse battery staple", false, ""},
		{"exactly 72 bytes", strings.Repeat("a", 72), false, ""},
		{"73 bytes", strings.Repeat("a", 73), true, "72"},
		// len() counts bytes, and bcrypt's limit is in bytes too: 37 x 2 bytes = 74.
		{"multibyte over the limit", strings.Repeat("é", 37), true, "72"},
		{"multibyte under the limit", strings.Repeat("é", 30), false, ""},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := validateNewPassword(c.pw)
			if c.wantErr && msg == "" {
				t.Fatalf("expected an error message for %q, got none", c.name)
			}
			if !c.wantErr && msg != "" {
				t.Fatalf("expected %q to be accepted, got %q", c.name, msg)
			}
			if c.contain != "" && !strings.Contains(msg, c.contain) {
				t.Fatalf("message %q should mention %q", msg, c.contain)
			}
		})
	}
}

// A password accepted by validateNewPassword must always be hashable. If these
// two ever disagree, signup would return a 500 for a password the form allowed.
func TestHardening_AcceptedPasswordsAreHashable(t *testing.T) {
	for _, pw := range []string{"12345678", strings.Repeat("a", maxPasswordBytes)} {
		if msg := validateNewPassword(pw); msg != "" {
			t.Fatalf("setup: %q unexpectedly rejected: %s", pw, msg)
		}
		if _, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost); err != nil {
			t.Fatalf("bcrypt rejected a %d-byte password the validator accepted: %v", len(pw), err)
		}
	}
}

func TestHardening_DummyHashIsValidAndSameCost(t *testing.T) {
	if len(dummyHash) == 0 {
		t.Fatal("dummyHash is empty, so the timing equaliser would return instantly")
	}

	// A malformed hash makes CompareHashAndPassword return early with a
	// different error and defeats the point. It must fail as a *mismatch*.
	err := bcrypt.CompareHashAndPassword(dummyHash, []byte("some wrong password"))
	if err != bcrypt.ErrMismatchedHashAndPassword {
		t.Fatalf("dummy compare returned %v, want ErrMismatchedHashAndPassword", err)
	}

	cost, err := bcrypt.Cost(dummyHash)
	if err != nil {
		t.Fatal(err)
	}
	if cost != bcrypt.DefaultCost {
		t.Fatalf("dummyHash cost = %d, real password hashes use %d; timing would differ", cost, bcrypt.DefaultCost)
	}
}

// ---------- Sessions ----------

func TestHardening_SessionStillValid_NoDatabaseFailsOpen(t *testing.T) {
	old := db
	db = nil
	defer func() { db = old }()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if !sessionStillValid(req, 1) {
		t.Fatal("with no database the check should fail open and let the handler's own requireDB answer")
	}
}

// ---------- HTTP ----------

func TestHardening_SecurityHeaders(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	})

	rec := httptest.NewRecorder()
	securityHeaders(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Fatal("wrapped handler was not called")
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("status = %d, the wrapper must not change the handler's status", rec.Code)
	}

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// A handler that sets its own Referrer-Policy (the reset page uses no-referrer)
// must win over the global default.
func TestHardening_HandlerCanOverrideReferrerPolicy(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
	})

	rec := httptest.NewRecorder()
	securityHeaders(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/reset-password?token=x", nil))

	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q, want no-referrer", got)
	}
}

func TestHardening_ServerErrorHidesInternals(t *testing.T) {
	rec := httptest.NewRecorder()
	serverError(rec, errString(`pq: relation "users" does not exist`))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"pq:", "relation", "users", "does not exist"} {
		if strings.Contains(body, leak) {
			t.Fatalf("response body leaks %q: %q", leak, body)
		}
	}
	if strings.TrimSpace(body) == "" {
		t.Fatal("response body should carry a generic message")
	}
}

type errString string

func (e errString) Error() string { return string(e) }

// ---------- Content-Security-Policy ----------

func serveWithCSP(t *testing.T, mode string) http.Header {
	t.Helper()
	t.Setenv("CSP_MODE", mode)
	rec := httptest.NewRecorder()
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Header()
}

func TestHardening_CSP_DefaultIsReportOnly(t *testing.T) {
	for _, mode := range []string{"", "report-only", "garbage"} {
		hdr := serveWithCSP(t, mode)
		if hdr.Get("Content-Security-Policy-Report-Only") == "" {
			t.Errorf("mode %q: expected a report-only header", mode)
		}
		if hdr.Get("Content-Security-Policy") != "" {
			t.Errorf("mode %q: report-only must not also send an enforcing header", mode)
		}
	}
}

func TestHardening_CSP_Enforce(t *testing.T) {
	hdr := serveWithCSP(t, "Enforce ") // case and whitespace tolerant
	if hdr.Get("Content-Security-Policy") == "" {
		t.Fatal("expected an enforcing header")
	}
	if hdr.Get("Content-Security-Policy-Report-Only") != "" {
		t.Fatal("enforce mode must not also send a report-only header")
	}
}

func TestHardening_CSP_Off(t *testing.T) {
	hdr := serveWithCSP(t, "off")
	if hdr.Get("Content-Security-Policy") != "" || hdr.Get("Content-Security-Policy-Report-Only") != "" {
		t.Fatal("off mode should send no CSP header")
	}
	// The other headers are unaffected.
	if hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("other security headers should still be set")
	}
}

func TestHardening_CSP_PolicyContents(t *testing.T) {
	must := []string{
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		"https://www.google.com/recaptcha/", // signup widget
		"https://www.gstatic.com/recaptcha/",
		"https://fonts.googleapis.com", // Inter stylesheet
		"https://fonts.gstatic.com",    // Inter font files
	}
	for _, m := range must {
		if !strings.Contains(contentSecurityPolicy, m) {
			t.Errorf("policy is missing %q", m)
		}
	}
	// A bare wildcard would make the policy meaningless.
	if strings.Contains(contentSecurityPolicy, " * ") || strings.Contains(contentSecurityPolicy, "'unsafe-eval'") {
		t.Error("policy must not allow wildcards or unsafe-eval")
	}
}
