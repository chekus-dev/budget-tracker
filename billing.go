package main

// billing.go: Paystack payments for the budget tracker. Drop next to main.go.
//
// Needs one new env var: PAYSTACK_SECRET_KEY. Everything else is reused from the
// app (db, currentUser, requireAuth, externalBaseURL, serverError). If the key is
// missing the routes answer 503 and the rest of the app is untouched.
//
// All money rules live in Postgres (billing_apply_payment); Go only talks to
// Paystack and calls that function once Paystack has confirmed the payment.

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha512"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	billingPriceKobo = 100000 // ₦1,000 (Paystack amounts are in kobo)
	billingDays      = 30     // one payment buys 30 days; it does not auto-renew
	billingMaxBody   = 1 << 20
	billingLockID    = 727301 // postgres advisory lock id for schema setup
)

var (
	billingRefPattern    = regexp.MustCompile(`^bt_[0-9a-f]{24}$`)
	errBillingUnknownRef = errors.New("unknown payment reference")

	billingAPIBase = "https://api.paystack.co" // var so tests can point elsewhere
	billingHTTP    = &http.Client{Timeout: 15 * time.Second}
	billingReady   bool
)

func billingSecret() string { return strings.TrimSpace(os.Getenv("PAYSTACK_SECRET_KEY")) }

// billingSchemaStatements are run one at a time, in order, on every startup.
// Every statement is idempotent. Names are prefixed "billing_" so they cannot
// collide with anything already in your database.
var billingSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS billing_payments (
		reference    text PRIMARY KEY CHECK (reference ~ '^bt_[0-9a-f]{24}$'),
		user_id      bigint NOT NULL REFERENCES users(id),
		amount_kobo  bigint NOT NULL CHECK (amount_kobo > 0),
		status       text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','success')),
		created_at   timestamptz NOT NULL DEFAULT now(),
		paid_at      timestamptz,
		CHECK ((status = 'success') = (paid_at IS NOT NULL))
	)`,
	`CREATE INDEX IF NOT EXISTS billing_payments_user_idx ON billing_payments(user_id)`,
	`CREATE TABLE IF NOT EXISTS billing_premium (
		user_id        bigint PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
		premium_until  timestamptz NOT NULL
	)`,
	// Payment records are an audit trail: never deleted, identity fields never
	// rewritten, and a success can never revert to pending.
	`CREATE OR REPLACE FUNCTION billing_payments_guard() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN
	  IF TG_OP = 'DELETE' THEN
	    RAISE EXCEPTION 'payments cannot be deleted';
	  END IF;
	  IF NEW.reference <> OLD.reference OR NEW.user_id <> OLD.user_id
	     OR NEW.amount_kobo <> OLD.amount_kobo OR NEW.created_at <> OLD.created_at THEN
	    RAISE EXCEPTION 'payment identity fields are immutable';
	  END IF;
	  IF OLD.status = 'success' AND NEW.status <> 'success' THEN
	    RAISE EXCEPTION 'a successful payment cannot be reverted';
	  END IF;
	  RETURN NEW;
	END $$`,
	`DROP TRIGGER IF EXISTS billing_payments_guard_trg ON billing_payments`,
	`CREATE TRIGGER billing_payments_guard_trg BEFORE UPDATE OR DELETE ON billing_payments
		FOR EACH ROW EXECUTE FUNCTION billing_payments_guard()`,
	// The only place premium is granted: atomic, idempotent, race-safe.
	// Returns 'applied' | 'already_applied' | 'unknown_reference' | 'amount_mismatch'.
	`CREATE OR REPLACE FUNCTION billing_apply_payment(p_ref text, p_amount_kobo bigint, p_days int)
	RETURNS text LANGUAGE plpgsql AS $$
	DECLARE
	  r billing_payments%ROWTYPE;
	BEGIN
	  SELECT * INTO r FROM billing_payments WHERE reference = p_ref FOR UPDATE;
	  IF NOT FOUND THEN RETURN 'unknown_reference'; END IF;
	  IF r.status = 'success' THEN RETURN 'already_applied'; END IF;
	  IF r.amount_kobo <> p_amount_kobo THEN RETURN 'amount_mismatch'; END IF;
	  UPDATE billing_payments SET status = 'success', paid_at = now() WHERE reference = p_ref;
	  INSERT INTO billing_premium(user_id, premium_until)
	  VALUES (r.user_id, now() + make_interval(days => p_days))
	  ON CONFLICT (user_id) DO UPDATE
	    SET premium_until = GREATEST(billing_premium.premium_until, now()) + make_interval(days => p_days);
	  RETURN 'applied';
	END $$`,
	`CREATE OR REPLACE FUNCTION billing_is_premium(p_user bigint) RETURNS boolean
	LANGUAGE sql STABLE AS $$
	  SELECT COALESCE((SELECT premium_until > now() FROM billing_premium WHERE user_id = p_user), false)
	$$`,
}

// ensureBillingSchema runs the statements under an advisory lock so two
// instances starting together (Render overlaps old and new during a deploy)
// cannot trip over each other.
func ensureBillingSchema(ctx context.Context, d *sql.DB) error {
	conn, err := d.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("SELECT pg_advisory_lock(%d)", billingLockID)); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), fmt.Sprintf("SELECT pg_advisory_unlock(%d)", billingLockID))
	for i, stmt := range billingSchemaStatements {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("billing schema step %d: %w", i+1, err)
		}
	}
	return nil
}

// setupBilling registers the routes. Call it in main() after migrations and
// after mux is created. It never panics and never stops the server from booting.
func setupBilling(mux *http.ServeMux) {
	billingReady = false // only switched on below, after every check passes
	switch {
	case billingSecret() == "":
		log.Println("billing: PAYSTACK_SECRET_KEY not set; billing disabled")
	case db == nil:
		log.Println("billing: no database; billing disabled")
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := ensureBillingSchema(ctx, db); err != nil {
			log.Printf("billing: schema setup failed, billing disabled: %v", err)
		} else {
			billingReady = true
			log.Println("billing: enabled")
		}
	}
	mux.HandleFunc("/billing", requireAuth(billingPageHandler))
	mux.HandleFunc("/billing/checkout", requireAuth(billingCheckoutHandler))
	mux.HandleFunc("/billing/callback", billingCallbackHandler)
	mux.HandleFunc("/billing/webhook", billingWebhookHandler)
}

func billingEnabled(w http.ResponseWriter) bool {
	if !billingReady || billingSecret() == "" {
		http.Error(w, "Payments are not available right now.", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// isPremium is the single check for gating a feature, e.g. at the top of a handler:
//
//	if !isPremium(userID) { http.Redirect(w, r, "/billing", http.StatusSeeOther); return }
func isPremium(userID int) bool {
	if !billingReady {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var ok bool
	if err := db.QueryRowContext(ctx, `SELECT billing_is_premium($1)`, userID).Scan(&ok); err != nil {
		log.Printf("billing: isPremium: %v", err)
		return false
	}
	return ok
}

var billingPageTmpl = template.Must(template.New("billing").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Premium</title></head>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:2rem auto;padding:0 1rem;line-height:1.5">
<h1>Premium</h1>
{{if eq .Status "paid"}}<p><strong>Payment received. Thank you!</strong></p>{{end}}
{{if eq .Status "failed"}}<p><strong>That payment was not completed. You have not been charged.</strong></p>{{end}}
{{if eq .Status "error"}}<p><strong>We could not confirm your payment yet. If you were charged, it will appear shortly; contact support if not.</strong></p>{{end}}
{{if .Premium}}<p>Your premium access is active until <strong>{{.Until}}</strong>. Paying again adds {{.Days}} more days.</p>{{else}}<p>Unlock premium for {{.Price}} per {{.Days}} days. It is a one-time payment and does not renew automatically.</p>{{end}}
{{if .Enabled}}<form method="post" action="/billing/checkout"><button type="submit">Pay {{.Price}}</button></form>{{else}}<p>Payments are not available right now.</p>{{end}}
<p><a href="/">Back to your budget</a></p>
</body></html>`))

func billingPageHandler(w http.ResponseWriter, r *http.Request) {
	uid, _, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	data := struct {
		Premium, Enabled bool
		Until, Status    string
		Price            string
		Days             int
	}{Enabled: billingReady, Status: r.URL.Query().Get("status"), Price: "₦1,000", Days: billingDays}
	if billingReady {
		var until time.Time
		err := db.QueryRowContext(r.Context(),
			`SELECT premium_until FROM billing_premium WHERE user_id = $1 AND premium_until > now()`, uid).Scan(&until)
		if err == nil {
			data.Premium = true
			data.Until = until.In(time.FixedZone("WAT", 3600)).Format("2 January 2006")
		} else if !errors.Is(err, sql.ErrNoRows) {
			serverError(w, err)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := billingPageTmpl.Execute(w, data); err != nil {
		log.Printf("billing: render page: %v", err)
	}
}

func billingCheckoutHandler(w http.ResponseWriter, r *http.Request) {
	if !billingEnabled(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	uid, _, ok := currentUser(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	var email string
	if err := db.QueryRowContext(r.Context(), `SELECT email FROM users WHERE id = $1`, uid).Scan(&email); err != nil {
		serverError(w, err)
		return
	}
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		serverError(w, err)
		return
	}
	ref := "bt_" + hex.EncodeToString(b)
	if _, err := db.ExecContext(r.Context(),
		`INSERT INTO billing_payments(reference, user_id, amount_kobo) VALUES ($1, $2, $3)`,
		ref, uid, billingPriceKobo); err != nil {
		serverError(w, err)
		return
	}
	body, _ := json.Marshal(map[string]any{
		"email":        email,
		"amount":       billingPriceKobo,
		"currency":     "NGN",
		"reference":    ref,
		"callback_url": externalBaseURL(r) + "/billing/callback",
	})
	var out struct {
		Status bool `json:"status"`
		Data   struct {
			URL string `json:"authorization_url"`
		} `json:"data"`
	}
	if err := billingPaystack(r.Context(), http.MethodPost, "/transaction/initialize", body, &out); err != nil ||
		!out.Status || !strings.HasPrefix(out.Data.URL, "https://") {
		log.Printf("billing: initialize failed: err=%v status=%v", err, out.Status)
		http.Error(w, "The payment provider is unavailable. Please try again.", http.StatusBadGateway)
		return
	}
	http.Redirect(w, r, out.Data.URL, http.StatusSeeOther)
}

// billingCallbackHandler is where Paystack sends the browser back. It trusts
// nothing in the URL except the reference, which is re-verified with Paystack.
func billingCallbackHandler(w http.ResponseWriter, r *http.Request) {
	if !billingEnabled(w) {
		return
	}
	ref := r.URL.Query().Get("reference")
	if ref == "" {
		ref = r.URL.Query().Get("trxref")
	}
	paid, err := billingVerifyAndApply(r.Context(), ref)
	status := "failed"
	switch {
	case err != nil && !errors.Is(err, errBillingUnknownRef):
		log.Printf("billing: callback: %v", err)
		status = "error"
	case paid:
		status = "paid"
	}
	http.Redirect(w, r, "/billing?status="+status, http.StatusSeeOther)
}

// billingWebhookHandler receives Paystack's server-to-server events. It is
// public (no cookie), protected by the HMAC signature instead.
func billingWebhookHandler(w http.ResponseWriter, r *http.Request) {
	// Never check a signature against an empty key: anyone could forge that.
	if !billingReady || billingSecret() == "" {
		http.Error(w, "disabled", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, billingMaxBody+1))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if len(raw) > billingMaxBody {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	mac := hmac.New(sha512.New, []byte(billingSecret()))
	mac.Write(raw)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(r.Header.Get("X-Paystack-Signature"))) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	var ev struct {
		Event string `json:"event"`
		Data  struct {
			Reference string `json:"reference"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		http.Error(w, "malformed json", http.StatusBadRequest)
		return
	}
	if ev.Event == "charge.success" {
		if _, err := billingVerifyAndApply(r.Context(), ev.Data.Reference); err != nil && !errors.Is(err, errBillingUnknownRef) {
			log.Printf("billing: webhook: %v", err)
			http.Error(w, "retry", http.StatusInternalServerError) // Paystack retries
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// billingVerifyAndApply asks Paystack whether the payment really succeeded,
// then lets Postgres apply it. Safe to call any number of times per reference.
func billingVerifyAndApply(ctx context.Context, ref string) (bool, error) {
	if !billingRefPattern.MatchString(ref) {
		return false, errBillingUnknownRef
	}
	var out struct {
		Data struct {
			Status   string `json:"status"`
			Amount   int64  `json:"amount"`
			Currency string `json:"currency"`
		} `json:"data"`
	}
	if err := billingPaystack(ctx, http.MethodGet, "/transaction/verify/"+url.PathEscape(ref), nil, &out); err != nil {
		return false, err
	}
	if out.Data.Status != "success" || out.Data.Currency != "NGN" {
		return false, nil
	}
	var result string
	if err := db.QueryRowContext(ctx, `SELECT billing_apply_payment($1, $2, $3)`,
		ref, out.Data.Amount, billingDays).Scan(&result); err != nil {
		return false, err
	}
	switch result {
	case "applied", "already_applied":
		return true, nil
	case "unknown_reference":
		return false, errBillingUnknownRef
	default:
		return false, fmt.Errorf("payment %s rejected: %s", ref, result)
	}
}

func billingPaystack(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, billingAPIBase+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+billingSecret())
	req.Header.Set("Content-Type", "application/json")
	resp, err := billingHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("paystack http %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
