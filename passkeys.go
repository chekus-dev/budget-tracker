package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/gorilla/sessions"
)

var wa *webauthn.WebAuthn

// initWebAuthn sets up the relying party. A passkey is tied to the hostname it
// was created on, so on the live site this must be the final domain.
// Override with WEBAUTHN_RPID (hostname only) and WEBAUTHN_ORIGIN (full URL).
func initWebAuthn() {
	rpID := strings.TrimSpace(os.Getenv("WEBAUTHN_RPID"))
	origin := strings.TrimSpace(os.Getenv("WEBAUTHN_ORIGIN"))
	if base := strings.TrimRight(strings.TrimSpace(firstEnv("APP_BASE_URL", "RENDER_EXTERNAL_URL")), "/"); base != "" {
		if u, err := url.Parse(base); err == nil && u.Hostname() != "" {
			if rpID == "" {
				rpID = u.Hostname()
			}
			if origin == "" {
				origin = base
			}
		}
	}
	if rpID == "" {
		rpID = "localhost"
	}
	if origin == "" {
		port := strings.TrimSpace(os.Getenv("PORT"))
		if port == "" {
			port = "4000"
		}
		origin = "http://localhost:" + port
	}
	w, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "Budget Tracker",
		RPID:          rpID,
		RPOrigins:     []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationPreferred,
		},
	})
	if err != nil {
		log.Printf("passkeys disabled: %v", err)
		return
	}
	wa = w
	log.Printf("passkeys enabled for %s (%s)", rpID, origin)
}

type waUser struct {
	id    int
	name  string
	creds []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return []byte(strconv.Itoa(u.id)) }
func (u *waUser) WebAuthnName() string                       { return u.name }
func (u *waUser) WebAuthnDisplayName() string                { return u.name }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func loadWAUser(id int) (*waUser, error) {
	var name string
	var deleted sql.NullTime
	if err := db.QueryRow(
		`SELECT username, deleted_at FROM users WHERE id = $1`, id,
	).Scan(&name, &deleted); err != nil {
		return nil, err
	}
	if deleted.Valid {
		return nil, errors.New("account disabled")
	}
	rows, err := db.Query(`SELECT credential::text FROM webauthn_credentials WHERE user_id = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	u := &waUser{id: id, name: name}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var c webauthn.Credential
		if json.Unmarshal([]byte(raw), &c) == nil {
			u.creds = append(u.creds, c)
		}
	}
	return u, rows.Err()
}

func waJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func putWASession(s *sessions.Session, key string, sd *webauthn.SessionData) error {
	b, err := json.Marshal(sd)
	if err != nil {
		return err
	}
	s.Values[key] = string(b)
	return nil
}

func popWASession(s *sessions.Session, key string) (webauthn.SessionData, bool) {
	var sd webauthn.SessionData
	raw, _ := s.Values[key].(string)
	delete(s.Values, key)
	if raw == "" || json.Unmarshal([]byte(raw), &sd) != nil {
		return sd, false
	}
	return sd, true
}

func passkeyRegisterBeginHandler(w http.ResponseWriter, r *http.Request) {
	if wa == nil {
		waJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Passkeys are not available right now."})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)
	u, err := loadWAUser(userID)
	if err != nil {
		serverError(w, err)
		return
	}
	creation, sd, err := wa.BeginRegistration(u)
	if err != nil {
		serverError(w, err)
		return
	}
	s := getSession(r)
	if err := putWASession(s, "wa_reg", sd); err != nil {
		serverError(w, err)
		return
	}
	if err := s.Save(r, w); err != nil {
		serverError(w, err)
		return
	}
	waJSON(w, http.StatusOK, creation)
}

func passkeyRegisterFinishHandler(w http.ResponseWriter, r *http.Request) {
	if wa == nil {
		waJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Passkeys are not available right now."})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)
	s := getSession(r)
	fail := func(code int, msg string) {
		_ = s.Save(r, w)
		waJSON(w, code, map[string]string{"error": msg})
	}
	sd, ok := popWASession(s, "wa_reg")
	if !ok {
		fail(http.StatusBadRequest, "That took too long. Please try again.")
		return
	}
	u, err := loadWAUser(userID)
	if err != nil {
		fail(http.StatusInternalServerError, "Something went wrong. Please try again.")
		return
	}
	cred, err := wa.FinishRegistration(u, sd, r)
	if err != nil {
		log.Printf("passkey registration failed: %v", err)
		fail(http.StatusBadRequest, "Could not save the passkey. Please try again.")
		return
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		fail(http.StatusInternalServerError, "Something went wrong. Please try again.")
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		name = "Passkey"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	if _, err := db.Exec(
		`INSERT INTO webauthn_credentials (user_id, credential_id, credential, name)
		 VALUES ($1, $2, $3::jsonb, $4)`,
		userID, cred.ID, string(raw), name,
	); err != nil {
		log.Printf("passkey insert failed: %v", err)
		fail(http.StatusInternalServerError, "Could not save the passkey. Please try again.")
		return
	}
	_ = s.Save(r, w)
	waJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func passkeyRemoveAllHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)
	if _, err := db.Exec(`DELETE FROM webauthn_credentials WHERE user_id = $1`, userID); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func passkeyLoginBeginHandler(w http.ResponseWriter, r *http.Request) {
	if wa == nil {
		waJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Passkeys are not available right now."})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if wait := loginLimiterIP.retryAfter(ipRateKey(r)); wait > 0 {
		waJSON(w, http.StatusTooManyRequests, map[string]string{"error": loginLockoutMessage(wait)})
		return
	}
	assertion, sd, err := wa.BeginDiscoverableLogin()
	if err != nil {
		serverError(w, err)
		return
	}
	s := getSession(r)
	if err := putWASession(s, "wa_login", sd); err != nil {
		serverError(w, err)
		return
	}
	if err := s.Save(r, w); err != nil {
		serverError(w, err)
		return
	}
	waJSON(w, http.StatusOK, assertion)
}

func passkeyLoginFinishHandler(w http.ResponseWriter, r *http.Request) {
	if wa == nil {
		waJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Passkeys are not available right now."})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s := getSession(r)
	fail := func(code int, msg string) {
		_ = s.Save(r, w)
		waJSON(w, code, map[string]string{"error": msg})
	}
	if wait := loginLimiterIP.retryAfter(ipRateKey(r)); wait > 0 {
		fail(http.StatusTooManyRequests, loginLockoutMessage(wait))
		return
	}
	sd, ok := popWASession(s, "wa_login")
	if !ok {
		fail(http.StatusBadRequest, "That took too long. Please try again.")
		return
	}
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		id, err := strconv.Atoi(string(userHandle))
		if err != nil {
			return nil, errors.New("bad user handle")
		}
		return loadWAUser(id)
	}
	user, cred, err := wa.FinishPasskeyLogin(handler, sd, r)
	if err != nil {
		log.Printf("passkey login failed: %v", err)
		loginLimiterIP.fail(ipRateKey(r))
		fail(http.StatusUnauthorized, "Fingerprint sign-in failed. Please use your password instead.")
		return
	}
	u, ok := user.(*waUser)
	if !ok {
		fail(http.StatusInternalServerError, "Something went wrong. Please try again.")
		return
	}
	var sv int
	if err := db.QueryRow(`SELECT session_version FROM users WHERE id = $1`, u.id).Scan(&sv); err != nil {
		fail(http.StatusInternalServerError, "Something went wrong. Please try again.")
		return
	}
	// Persist the updated signature counter and last-used time.
	if raw, err := json.Marshal(cred); err == nil {
		if _, err := db.Exec(
			`UPDATE webauthn_credentials SET credential = $1::jsonb, last_used_at = NOW() WHERE credential_id = $2`,
			string(raw), cred.ID,
		); err != nil {
			log.Printf("passkey counter update failed: %v", err)
		}
	}
	s.Values["user_id"] = u.id
	s.Values["username"] = u.name
	s.Values["sv"] = sv
	if err := s.Save(r, w); err != nil {
		waJSON(w, http.StatusInternalServerError, map[string]string{"error": "Something went wrong. Please try again."})
		return
	}
	waJSON(w, http.StatusOK, map[string]any{"ok": true, "redirect": "/"})
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}
