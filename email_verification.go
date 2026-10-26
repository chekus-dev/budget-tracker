package main

import (
	"database/sql"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/sessions"
)

func verifyEmailHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}

	session := getSession(r)
	userID, _ := session.Values["signup_verify_user_id"].(int)
	email, _ := session.Values["signup_verify_email"].(string)
	if userID == 0 || email == "" {
		http.Redirect(w, r, "/register", http.StatusSeeOther)
		return
	}

	render := func(errMsg, notice string) {
		if err := tmpl.ExecuteTemplate(w, "verify-email.html", map[string]interface{}{
			"Email":  email,
			"Error":  errMsg,
			"Notice": notice,
		}); err != nil {
			serverError(w, err)
		}
	}

	if r.Method == http.MethodGet {
		var verified bool
		err := db.QueryRow(
			"SELECT email_verified_at IS NOT NULL FROM users WHERE id = $1 AND email = $2",
			userID, email,
		).Scan(&verified)
		if err == sql.ErrNoRows {
			clearSignupVerification(session)
			if err := session.Save(r, w); err != nil {
				serverError(w, err)
				return
			}
			http.Redirect(w, r, "/register", http.StatusSeeOther)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		if verified {
			clearSignupVerification(session)
			if err := session.Save(r, w); err != nil {
				serverError(w, err)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		render("", "")
		return
	}

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.FormValue("action") == "resend" {
		if wait := waitingForReset(r, email); wait > 0 {
			writeRetryAfter(w, wait)
			render(resetLockoutMessage(wait), "")
			return
		}
		if wait := chargeReset(r, email); wait > 0 {
			writeRetryAfter(w, wait)
			render(resetLockoutMessage(wait), "")
			return
		}

		code, err := generateOTP()
		if err != nil {
			serverError(w, err)
			return
		}
		result, err := db.Exec(
			`UPDATE users
			 SET email_verification_token = $1, email_verification_expires = $2
			 WHERE id = $3 AND email = $4 AND email_verified_at IS NULL`,
			hashResetToken(code), time.Now().Add(resetTokenTTL), userID, email,
		)
		if err != nil {
			serverError(w, err)
			return
		}
		rows, err := result.RowsAffected()
		if err != nil {
			serverError(w, err)
			return
		}
		if rows == 0 {
			clearSignupVerification(session)
			if err := session.Save(r, w); err != nil {
				serverError(w, err)
				return
			}
			http.Redirect(w, r, "/register", http.StatusSeeOther)
			return
		}
		if err := sendVerificationEmail(email, code); err != nil {
			log.Printf("failed to resend signup verification email: %v", err)
			render("We couldn't send your verification code just now. Please try again shortly.", "")
			return
		}
		render("", "A new verification code has been sent.")
		return
	}

	if wait := waitingForEmailVerification(r, email); wait > 0 {
		writeRetryAfter(w, wait)
		render(emailVerificationLockoutMessage(wait), "")
		return
	}

	code := strings.TrimSpace(r.FormValue("code"))
	if len(code) != 6 {
		if wait := chargeEmailVerification(r, email); wait > 0 {
			writeRetryAfter(w, wait)
			render(emailVerificationLockoutMessage(wait), "")
			return
		}
		render("Enter the 6-digit code from your email.", "")
		return
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			if wait := chargeEmailVerification(r, email); wait > 0 {
				writeRetryAfter(w, wait)
				render(emailVerificationLockoutMessage(wait), "")
				return
			}
			render("Enter the 6-digit code from your email.", "")
			return
		}
	}

	var username string
	var sessionVersion int
	err := db.QueryRow(
		`UPDATE users
		 SET email_verified_at = CURRENT_TIMESTAMP,
		     email_verification_token = NULL,
		     email_verification_expires = NULL
		 WHERE id = $1 AND email = $2
		   AND email_verified_at IS NULL
		   AND email_verification_token = $3
		   AND email_verification_expires > CURRENT_TIMESTAMP
		 RETURNING username, session_version`,
		userID, email, hashResetToken(code),
	).Scan(&username, &sessionVersion)
	if err == sql.ErrNoRows {
		if wait := chargeEmailVerification(r, email); wait > 0 {
			writeRetryAfter(w, wait)
			render(emailVerificationLockoutMessage(wait), "")
			return
		}
		render("That code is incorrect or has expired.", "")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}

	session.Values["user_id"] = userID
	session.Values["username"] = username
	session.Values["sv"] = sessionVersion
	clearSignupVerification(session)
	if err := session.Save(r, w); err != nil {
		serverError(w, err)
		return
	}
	emailVerificationLimiterEmail.reset(emailVerificationRateKey(email))
	emailVerificationLimiterIP.reset(emailVerificationIPRateKey(r))
	resetLimiterEmail.reset(resetRateKey(email))
	http.Redirect(w, r, "/welcome", http.StatusSeeOther)
}

func clearSignupVerification(session *sessions.Session) {
	delete(session.Values, "signup_verify_user_id")
	delete(session.Values, "signup_verify_email")
}
