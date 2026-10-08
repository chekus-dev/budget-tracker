package main

// billing_test.go: tests for billing.go. Drop next to billing.go and run:
//
//	go test -run Billing -v ./...
//
// Part 1 (no setup needed): handlers, signature checks and routing, using a fake
// database driver and a fake Paystack server. Nothing real is contacted.
//
// Part 2 (TestBillingPostgres): the real money logic on a real Postgres through
// your real pgx driver. It is skipped unless you point it at a THROWAWAY database:
//
//	createdb billing_test
//	BILLING_TEST_DATABASE_URL='postgresql://YOU@/billing_test?host=/var/run/postgresql&sslmode=disable' \
//	  go test -run BillingPostgres -v ./...
//
// It refuses to run against a database that looks like the real app (one that has
// an "expenses" table or users.password_hash), and never use your Render database.

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/sessions"
)

const (
	billingTestKey = "sk_test_unit"
	billingTestRef = "bt_0123456789abcdef01234567"
)

// ---------- fake database/sql driver ----------

type billingFakeCall struct {
	q    string
	args []driver.Value
}

var (
	billingFakeMu      sync.Mutex
	billingFakeCalls   []billingFakeCall
	billingFakeApply   = "applied"
	billingFakePremium bool
)

func init() { sql.Register("billingfake", billingFakeDriver{}) }

type billingFakeDriver struct{}

func (billingFakeDriver) Open(string) (driver.Conn, error) { return &billingFakeConn{}, nil }

type billingFakeConn struct{}

func (*billingFakeConn) Prepare(q string) (driver.Stmt, error) { return &billingFakeStmt{q}, nil }
func (*billingFakeConn) Close() error                          { return nil }
func (*billingFakeConn) Begin() (driver.Tx, error)             { return nil, errors.New("no tx") }

type billingFakeStmt struct{ q string }

func (*billingFakeStmt) Close() error  { return nil }
func (*billingFakeStmt) NumInput() int { return -1 }

func (s *billingFakeStmt) Exec(a []driver.Value) (driver.Result, error) {
	billingFakeMu.Lock()
	billingFakeCalls = append(billingFakeCalls, billingFakeCall{s.q, a})
	billingFakeMu.Unlock()
	return driver.RowsAffected(1), nil
}

func (s *billingFakeStmt) Query(a []driver.Value) (driver.Rows, error) {
	billingFakeMu.Lock()
	billingFakeCalls = append(billingFakeCalls, billingFakeCall{s.q, a})
	apply, premium := billingFakeApply, billingFakePremium
	billingFakeMu.Unlock()
	switch {
	case strings.Contains(s.q, "FROM users"):
		return &billingFakeRows{cols: []string{"email"}, row: []driver.Value{"a@b.c"}}, nil
	case strings.Contains(s.q, "billing_apply_payment"):
		return &billingFakeRows{cols: []string{"r"}, row: []driver.Value{apply}}, nil
	case strings.Contains(s.q, "billing_is_premium"):
		return &billingFakeRows{cols: []string{"r"}, row: []driver.Value{true}}, nil
	case strings.Contains(s.q, "FROM billing_premium") && premium:
		return &billingFakeRows{cols: []string{"u"}, row: []driver.Value{time.Now().Add(48 * time.Hour)}}, nil
	}
	return &billingFakeRows{cols: []string{"x"}}, nil // no rows
}

type billingFakeRows struct {
	cols []string
	row  []driver.Value
	done bool
}

func (r *billingFakeRows) Columns() []string { return r.cols }
func (r *billingFakeRows) Close() error      { return nil }
func (r *billingFakeRows) Next(dest []driver.Value) error {
	if r.done || r.row == nil {
		return io.EOF
	}
	copy(dest, r.row)
	r.done = true
	return nil
}

func billingFakeReset() {
	billingFakeMu.Lock()
	billingFakeCalls, billingFakeApply, billingFakePremium = nil, "applied", false
	billingFakeMu.Unlock()
}

func billingFakeFind(substr string) *billingFakeCall {
	billingFakeMu.Lock()
	defer billingFakeMu.Unlock()
	for i := range billingFakeCalls {
		if strings.Contains(billingFakeCalls[i].q, substr) {
			return &billingFakeCalls[i]
		}
	}
	return nil
}

// ---------- fake Paystack ----------

type billingFakePaystack struct {
	srv          *httptest.Server
	initBody     map[string]any
	initHTTP     int    // status to answer initialize with (default 200)
	authURL      string // authorization_url to hand back
	verifyStatus string
	verifyAmount int64
	verifyCur    string
}

func newBillingFakePaystack(t *testing.T) *billingFakePaystack {
	p := &billingFakePaystack{initHTTP: 200, authURL: "https://checkout.paystack.com/abc",
		verifyStatus: "success", verifyAmount: billingPriceKobo, verifyCur: "NGN"}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+billingTestKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/transaction/initialize":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &p.initBody)
			if p.initHTTP != 200 {
				w.WriteHeader(p.initHTTP)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"status": true, "data": map[string]any{"authorization_url": p.authURL}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/transaction/verify/"):
			json.NewEncoder(w).Encode(map[string]any{"status": true, "data": map[string]any{
				"status": p.verifyStatus, "amount": p.verifyAmount, "currency": p.verifyCur}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// ---------- shared setup ----------

// billingTestSetup swaps in the fakes and restores every global afterwards.
func billingTestSetup(t *testing.T) *billingFakePaystack {
	t.Helper()
	oldDB, oldStore, oldReady, oldAPI := db, store, billingReady, billingAPIBase
	t.Cleanup(func() { db, store, billingReady, billingAPIBase = oldDB, oldStore, oldReady, oldAPI })

	t.Setenv("PAYSTACK_SECRET_KEY", billingTestKey)
	t.Setenv("APP_BASE_URL", "https://app.test")

	store = sessions.NewCookieStore([]byte(strings.Repeat("k", 32)))
	store.Options = &sessions.Options{Path: "/", MaxAge: 3600, HttpOnly: true}

	var err error
	if db, err = sql.Open("billingfake", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	billingFakeReset()

	ps := newBillingFakePaystack(t)
	billingAPIBase = ps.srv.URL
	billingReady = true
	return ps
}

// billingTestLogin returns a session cookie for a signed-in user, made by the
// app's own session store so currentUser() accepts it.
func billingTestLogin(t *testing.T, userID int) *http.Cookie {
	t.Helper()
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	s := getSession(r)
	s.Values["user_id"] = userID
	s.Values["username"] = "tester"
	if err := s.Save(r, w); err != nil {
		t.Fatal(err)
	}
	cs := w.Result().Cookies()
	if len(cs) == 0 {
		t.Fatal("no session cookie issued")
	}
	return cs[0]
}

func billingTestSig(body string) string {
	m := hmac.New(sha512.New, []byte(billingTestKey))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

func billingTestDo(h http.Handler, method, target, body string, hdr map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func billingTestSign(body string) map[string]string {
	return map[string]string{"X-Paystack-Signature": billingTestSig(body)}
}

// ---------- Part 1: unit tests ----------

func TestBillingDisabled(t *testing.T) {
	billingTestSetup(t)
	billingReady = false
	c := billingTestLogin(t, 1)
	if got := billingTestDo(http.HandlerFunc(billingCheckoutHandler), "POST", "/billing/checkout", "", nil, c).Code; got != 503 {
		t.Errorf("checkout when disabled = %d, want 503", got)
	}
	if got := billingTestDo(http.HandlerFunc(billingWebhookHandler), "POST", "/billing/webhook", "{}", nil, nil).Code; got != 503 {
		t.Errorf("webhook when disabled = %d, want 503", got)
	}
	if got := billingTestDo(http.HandlerFunc(billingCallbackHandler), "GET", "/billing/callback?reference="+billingTestRef, "", nil, nil).Code; got != 503 {
		t.Errorf("callback when disabled = %d, want 503", got)
	}
	if isPremium(1) {
		t.Error("isPremium must be false when billing is disabled")
	}
}

func TestBillingCheckout(t *testing.T) {
	ps := billingTestSetup(t)
	h := http.HandlerFunc(billingCheckoutHandler)
	cookie := billingTestLogin(t, 7)

	if got := billingTestDo(h, "GET", "/billing/checkout", "", nil, cookie).Code; got != 405 {
		t.Errorf("GET = %d, want 405", got)
	}
	w := billingTestDo(h, "POST", "/billing/checkout", "", nil, nil)
	if w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Errorf("signed-out = %d %q, want 303 /login", w.Code, w.Header().Get("Location"))
	}
	if billingFakeFind("INSERT INTO billing_payments") != nil {
		t.Error("a signed-out visitor must not create a payment row")
	}

	w = billingTestDo(h, "POST", "/billing/checkout", "", nil, cookie)
	if w.Code != 303 || w.Header().Get("Location") != "https://checkout.paystack.com/abc" {
		t.Fatalf("checkout = %d %q, want 303 to Paystack", w.Code, w.Header().Get("Location"))
	}
	ins := billingFakeFind("INSERT INTO billing_payments")
	if ins == nil {
		t.Fatal("payment row was not inserted")
	}
	ref, _ := ins.args[0].(string)
	if !billingRefPattern.MatchString(ref) {
		t.Errorf("reference %q does not match the expected format", ref)
	}
	if ins.args[1] != int64(7) || ins.args[2] != int64(billingPriceKobo) {
		t.Errorf("inserted user/amount = %v/%v, want 7/%d", ins.args[1], ins.args[2], billingPriceKobo)
	}
	// What we told Paystack must match what we stored.
	if ps.initBody["reference"] != ref || ps.initBody["currency"] != "NGN" ||
		ps.initBody["email"] != "a@b.c" || ps.initBody["amount"] != float64(billingPriceKobo) ||
		ps.initBody["callback_url"] != "https://app.test/billing/callback" {
		t.Errorf("unexpected initialize body: %v", ps.initBody)
	}
}

func TestBillingCheckoutProviderFailures(t *testing.T) {
	ps := billingTestSetup(t)
	h := http.HandlerFunc(billingCheckoutHandler)
	cookie := billingTestLogin(t, 1)

	ps.initHTTP = 500
	if got := billingTestDo(h, "POST", "/billing/checkout", "", nil, cookie).Code; got != 502 {
		t.Errorf("Paystack 500 -> %d, want 502", got)
	}
	ps.initHTTP = 200
	ps.authURL = "http://not-https.example/x"
	if got := billingTestDo(h, "POST", "/billing/checkout", "", nil, cookie).Code; got != 502 {
		t.Errorf("non-https authorization_url -> %d, want 502", got)
	}
	ps.authURL = ""
	if got := billingTestDo(h, "POST", "/billing/checkout", "", nil, cookie).Code; got != 502 {
		t.Errorf("empty authorization_url -> %d, want 502", got)
	}
}

func TestBillingCallback(t *testing.T) {
	ps := billingTestSetup(t)
	h := http.HandlerFunc(billingCallbackHandler)
	loc := func(target string) string {
		return billingTestDo(h, "GET", target, "", nil, nil).Header().Get("Location")
	}
	good := "/billing/callback?reference=" + billingTestRef

	cases := []struct {
		name, apply, want string
		target            string
	}{
		{"applied", "applied", "/billing?status=paid", good},
		{"already applied", "already_applied", "/billing?status=paid", good},
		{"trxref fallback", "applied", "/billing?status=paid", "/billing/callback?trxref=" + billingTestRef},
		{"amount mismatch", "amount_mismatch", "/billing?status=error", good},
		{"unknown reference", "unknown_reference", "/billing?status=failed", good},
		{"malformed reference", "applied", "/billing?status=failed", "/billing/callback?reference=../../x"},
		{"missing reference", "applied", "/billing?status=failed", "/billing/callback"},
	}
	for _, c := range cases {
		billingFakeReset()
		billingFakeMu.Lock()
		billingFakeApply = c.apply
		billingFakeMu.Unlock()
		if got := loc(c.target); got != c.want {
			t.Errorf("%s: redirect = %q, want %q", c.name, got, c.want)
		}
	}

	// A payment Paystack did not confirm must never reach the database.
	billingFakeReset()
	ps.verifyStatus = "abandoned"
	if got := loc(good); got != "/billing?status=failed" {
		t.Errorf("abandoned payment: %q", got)
	}
	if billingFakeFind("billing_apply_payment") != nil {
		t.Error("an unconfirmed payment reached billing_apply_payment")
	}
	ps.verifyStatus, ps.verifyCur = "success", "USD"
	billingFakeReset()
	if got := loc(good); got != "/billing?status=failed" || billingFakeFind("billing_apply_payment") != nil {
		t.Errorf("non-NGN payment must be rejected: %q", got)
	}
}

func TestBillingWebhook(t *testing.T) {
	billingTestSetup(t)
	h := http.HandlerFunc(billingWebhookHandler)
	ev := `{"event":"charge.success","data":{"reference":"` + billingTestRef + `"}}`
	big := strings.Repeat("a", billingMaxBody+5)
	other := `{"event":"transfer.success","data":{}}`
	foreign := `{"event":"charge.success","data":{"reference":"x/../y?z=1"}}`

	cases := []struct {
		name, method, body string
		hdr                map[string]string
		want               int
	}{
		{"wrong method", "GET", "", nil, 405},
		{"no signature", "POST", ev, nil, 401},
		{"bad signature", "POST", ev, map[string]string{"X-Paystack-Signature": "nope"}, 401},
		{"signature of different body", "POST", ev, billingTestSign(other), 401},
		{"malformed json", "POST", "not json", billingTestSign("not json"), 400},
		{"oversized body", "POST", big, billingTestSign(big), 413},
		{"valid charge.success", "POST", ev, billingTestSign(ev), 200},
		{"unrelated event", "POST", other, billingTestSign(other), 200},
		{"reference that is not ours", "POST", foreign, billingTestSign(foreign), 200},
	}
	for _, c := range cases {
		billingFakeReset()
		if got := billingTestDo(h, c.method, "/billing/webhook", c.body, c.hdr, nil).Code; got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}

	// A signed event for a reference we never issued is acknowledged, not retried.
	billingFakeReset()
	billingFakeMu.Lock()
	billingFakeApply = "unknown_reference"
	billingFakeMu.Unlock()
	if got := billingTestDo(h, "POST", "/billing/webhook", ev, billingTestSign(ev), nil).Code; got != 200 {
		t.Errorf("unknown reference should be acked with 200, got %d", got)
	}
	// A real problem (amount mismatch) must fail loudly so Paystack retries.
	billingFakeMu.Lock()
	billingFakeApply = "amount_mismatch"
	billingFakeMu.Unlock()
	if got := billingTestDo(h, "POST", "/billing/webhook", ev, billingTestSign(ev), nil).Code; got != 500 {
		t.Errorf("amount mismatch should be 500, got %d", got)
	}
	// An invalid signature must never reach the database.
	billingFakeReset()
	billingTestDo(h, "POST", "/billing/webhook", ev, map[string]string{"X-Paystack-Signature": "bad"}, nil)
	if billingFakeFind("billing_apply_payment") != nil {
		t.Error("unsigned webhook reached the database")
	}
}

func TestBillingPage(t *testing.T) {
	billingTestSetup(t)
	h := http.HandlerFunc(billingPageHandler)
	cookie := billingTestLogin(t, 3)

	w := billingTestDo(h, "GET", "/billing", "", nil, nil)
	if w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Errorf("signed-out = %d %q", w.Code, w.Header().Get("Location"))
	}
	w = billingTestDo(h, "GET", "/billing?status=paid", "", nil, cookie)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "Payment received") || !strings.Contains(body, `action="/billing/checkout"`) {
		t.Errorf("paid page wrong: %d %q", w.Code, body)
	}
	if strings.Contains(body, "active until") {
		t.Error("non-premium user shown as premium")
	}
	billingFakeMu.Lock()
	billingFakePremium = true
	billingFakeMu.Unlock()
	if body := billingTestDo(h, "GET", "/billing", "", nil, cookie).Body.String(); !strings.Contains(body, "active until") {
		t.Errorf("premium user not shown as premium: %q", body)
	}
	// Untrusted text in the query string must be escaped, not echoed.
	if body := billingTestDo(h, "GET", "/billing?status=%3Cscript%3E", "", nil, cookie).Body.String(); strings.Contains(body, "<script>") {
		t.Error("status parameter was echoed unescaped")
	}
}

func TestBillingSetupBilling(t *testing.T) {
	billingTestSetup(t)
	billingReady = false

	// With the key and a database: schema runs, routes work, no panic.
	mux := http.NewServeMux()
	setupBilling(mux)
	if !billingReady {
		t.Fatal("billing should be enabled when the key and database are present")
	}
	if billingFakeFind("pg_advisory_lock") == nil || billingFakeFind("CREATE TABLE IF NOT EXISTS billing_payments") == nil {
		t.Error("schema setup did not run")
	}
	if got := billingTestDo(mux, "GET", "/billing/webhook", "", nil, nil).Code; got != 405 {
		t.Errorf("webhook route via mux = %d, want 405", got)
	}
	if w := billingTestDo(mux, "GET", "/billing", "", nil, nil); w.Code != 303 || w.Header().Get("Location") != "/login" {
		t.Errorf("/billing signed-out via mux = %d %q", w.Code, w.Header().Get("Location"))
	}

	// Without the key: the app must still boot, with billing off.
	t.Setenv("PAYSTACK_SECRET_KEY", "")
	billingReady = true
	mux2 := http.NewServeMux()
	setupBilling(mux2)
	if billingReady {
		t.Error("billing must stay disabled without PAYSTACK_SECRET_KEY")
	}
	if got := billingTestDo(mux2, "POST", "/billing/webhook", "{}", nil, nil).Code; got != 503 {
		t.Errorf("webhook without key = %d, want 503", got)
	}
}

// Guards future edits: every schema statement must be safe to run on every startup.
func TestBillingSchemaIsIdempotent(t *testing.T) {
	ok := []string{"CREATE TABLE IF NOT EXISTS", "CREATE INDEX IF NOT EXISTS", "CREATE OR REPLACE FUNCTION",
		"DROP TRIGGER IF EXISTS", "CREATE TRIGGER"}
	for i, s := range billingSchemaStatements {
		good := false
		for _, p := range ok {
			if strings.HasPrefix(strings.TrimSpace(s), p) {
				good = true
			}
		}
		if !good {
			t.Errorf("schema statement %d is not idempotent: %.60q", i+1, s)
		}
		if strings.HasPrefix(strings.TrimSpace(s), "CREATE TRIGGER") && i == 0 {
			t.Errorf("CREATE TRIGGER must follow its DROP TRIGGER IF EXISTS")
		}
	}
	for _, s := range billingSchemaStatements {
		l := strings.ToLower(s)
		if strings.Contains(l, "drop table") || strings.Contains(l, "truncate") {
			t.Errorf("schema statements must never destroy data: %.60q", s)
		}
	}
}

// ---------- Part 2: real Postgres ----------

func TestBillingPostgres(t *testing.T) {
	dsn := os.Getenv("BILLING_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set BILLING_TEST_DATABASE_URL to a THROWAWAY database to run this (see top of file)")
	}
	oldDB, oldReady, oldAPI, oldStore := db, billingReady, billingAPIBase, store
	t.Cleanup(func() { db, billingReady, billingAPIBase, store = oldDB, oldReady, oldAPI, oldStore })

	real, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { real.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := real.PingContext(ctx); err != nil {
		t.Fatalf("cannot reach test database: %v", err)
	}

	// Safety: refuse anything that looks like the real application's database.
	var n int
	real.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_name = 'expenses'`).Scan(&n)
	if n > 0 {
		t.Fatal("REFUSING: this database has an 'expenses' table, so it looks like the real app. Use an empty throwaway database.")
	}
	real.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_name = 'users' AND column_name = 'password_hash'`).Scan(&n)
	if n > 0 {
		t.Fatal("REFUSING: users.password_hash exists, so this looks like the real app database.")
	}

	// Clean slate in the throwaway database.
	for _, q := range []string{
		`DROP TABLE IF EXISTS billing_premium, billing_payments CASCADE`,
		`DROP TABLE IF EXISTS users CASCADE`,
		`CREATE TABLE users (id serial PRIMARY KEY, email text)`,
	} {
		if _, err := real.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	var u1, u2 int
	real.QueryRowContext(ctx, `INSERT INTO users(email) VALUES ('one@test.dev') RETURNING id`).Scan(&u1)
	real.QueryRowContext(ctx, `INSERT INTO users(email) VALUES ('two@test.dev') RETURNING id`).Scan(&u2)

	// The schema must apply twice with no error (it runs on every startup).
	for i := 1; i <= 2; i++ {
		if err := ensureBillingSchema(ctx, real); err != nil {
			t.Fatalf("schema run %d: %v", i, err)
		}
	}

	db = real
	billingReady = true
	t.Setenv("PAYSTACK_SECRET_KEY", billingTestKey)
	t.Setenv("APP_BASE_URL", "https://app.test")
	store = sessions.NewCookieStore([]byte(strings.Repeat("k", 32)))
	store.Options = &sessions.Options{Path: "/", MaxAge: 3600, HttpOnly: true}
	ps := newBillingFakePaystack(t)
	billingAPIBase = ps.srv.URL

	days := func(uid int) float64 {
		var d float64
		err := real.QueryRowContext(ctx,
			`SELECT extract(epoch FROM premium_until - now())/86400 FROM billing_premium WHERE user_id=$1`, uid).Scan(&d)
		if err != nil {
			return -1
		}
		return d
	}
	within := func(got, want float64) bool { return got > want-0.01 && got < want+0.01 }

	// End to end through the real handlers: checkout -> callback.
	cookie := billingTestLogin(t, u1)
	w := billingTestDo(http.HandlerFunc(billingCheckoutHandler), "POST", "/billing/checkout", "", nil, cookie)
	if w.Code != 303 {
		t.Fatalf("checkout = %d %s", w.Code, w.Body.String())
	}
	ref, _ := ps.initBody["reference"].(string)
	var status string
	if err := real.QueryRowContext(ctx, `SELECT status FROM billing_payments WHERE reference=$1`, ref).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("payment row after checkout: status=%q err=%v", status, err)
	}
	cb := billingTestDo(http.HandlerFunc(billingCallbackHandler), "GET", "/billing/callback?reference="+url.QueryEscape(ref), "", nil, nil)
	if cb.Header().Get("Location") != "/billing?status=paid" {
		t.Fatalf("callback redirect = %q", cb.Header().Get("Location"))
	}
	if !isPremium(u1) || !within(days(u1), billingDays) {
		t.Fatalf("after first payment: premium=%v days=%v, want true/%d", isPremium(u1), days(u1), billingDays)
	}
	if isPremium(u2) {
		t.Error("a user who never paid is premium")
	}

	// The callback AND the webhook both arrive for one payment: it counts once.
	ev := `{"event":"charge.success","data":{"reference":"` + ref + `"}}`
	for i := 0; i < 3; i++ {
		if got := billingTestDo(http.HandlerFunc(billingWebhookHandler), "POST", "/billing/webhook", ev, billingTestSign(ev), nil).Code; got != 200 {
			t.Fatalf("webhook replay %d = %d", i, got)
		}
	}
	if !within(days(u1), billingDays) {
		t.Errorf("replays extended premium: %v days, want %d", days(u1), billingDays)
	}

	// A second, separate payment extends from the current expiry.
	w = billingTestDo(http.HandlerFunc(billingCheckoutHandler), "POST", "/billing/checkout", "", nil, cookie)
	ref2, _ := ps.initBody["reference"].(string)
	if ref2 == ref || w.Code != 303 {
		t.Fatalf("second checkout: %d ref2=%q", w.Code, ref2)
	}
	billingTestDo(http.HandlerFunc(billingCallbackHandler), "GET", "/billing/callback?reference="+ref2, "", nil, nil)
	if !within(days(u1), 2*billingDays) {
		t.Errorf("second payment: %v days, want %d", days(u1), 2*billingDays)
	}

	// Money rules enforced by the database itself (direct calls, bypassing Go).
	apply := func(ref string, amount int64) string {
		var r string
		if err := real.QueryRowContext(ctx, `SELECT billing_apply_payment($1,$2,$3)`, ref, amount, billingDays).Scan(&r); err != nil {
			t.Fatalf("apply(%s): %v", ref, err)
		}
		return r
	}
	mk := func(ref string, uid int, amt int64) {
		if _, err := real.ExecContext(ctx, `INSERT INTO billing_payments(reference,user_id,amount_kobo) VALUES ($1,$2,$3)`, ref, uid, amt); err != nil {
			t.Fatalf("insert %s: %v", ref, err)
		}
	}
	if got := apply("bt_ffffffffffffffffffffffff", 1); got != "unknown_reference" {
		t.Errorf("unknown ref -> %q", got)
	}
	mk("bt_aaaaaaaaaaaaaaaaaaaaaaaa", u2, 100000)
	if got := apply("bt_aaaaaaaaaaaaaaaaaaaaaaaa", 5); got != "amount_mismatch" {
		t.Errorf("wrong amount -> %q", got)
	}
	if isPremium(u2) {
		t.Error("a mismatched amount granted premium")
	}
	if got := apply("bt_aaaaaaaaaaaaaaaaaaaaaaaa", 100000); got != "applied" {
		t.Errorf("good payment -> %q", got)
	}
	if got := apply("bt_aaaaaaaaaaaaaaaaaaaaaaaa", 100000); got != "already_applied" {
		t.Errorf("repeat -> %q", got)
	}

	// Constraints and the audit-trail triggers must reject tampering.
	mustFail := func(name, q string, args ...any) {
		if _, err := real.ExecContext(ctx, q, args...); err == nil {
			t.Errorf("%s was allowed but must be rejected", name)
		}
	}
	mustFail("malformed reference", `INSERT INTO billing_payments(reference,user_id,amount_kobo) VALUES ('bad',$1,1)`, u1)
	mustFail("non-positive amount", `INSERT INTO billing_payments(reference,user_id,amount_kobo) VALUES ('bt_bbbbbbbbbbbbbbbbbbbbbbbb',$1,0)`, u1)
	mustFail("payment for a missing user", `INSERT INTO billing_payments(reference,user_id,amount_kobo) VALUES ('bt_cccccccccccccccccccccccc',999999,1)`)
	mustFail("reverting a success", `UPDATE billing_payments SET status='pending', paid_at=NULL WHERE reference='bt_aaaaaaaaaaaaaaaaaaaaaaaa'`)
	mustFail("rewriting an amount", `UPDATE billing_payments SET amount_kobo=1 WHERE reference='bt_aaaaaaaaaaaaaaaaaaaaaaaa'`)
	mustFail("deleting payments", `DELETE FROM billing_payments`)

	// Concurrency: 40 simultaneous deliveries of one payment grant exactly one period.
	mk("bt_dddddddddddddddddddddddd", u2, 100000)
	base := days(u2)
	var wg sync.WaitGroup
	var mu sync.Mutex
	applied := 0
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var r string
			if err := real.QueryRowContext(ctx, `SELECT billing_apply_payment($1,$2,$3)`, "bt_dddddddddddddddddddddddd", int64(100000), billingDays).Scan(&r); err != nil {
				t.Errorf("concurrent apply: %v", err)
				return
			}
			if r == "applied" {
				mu.Lock()
				applied++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if applied != 1 {
		t.Errorf("concurrent deliveries applied %d times, want exactly 1", applied)
	}
	if got := days(u2); !within(got-base, billingDays) {
		t.Errorf("concurrent deliveries added %.2f days, want %d", got-base, billingDays)
	}
}
