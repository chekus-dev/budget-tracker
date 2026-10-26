package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func sendResetEmail(to, code string) error {
	if isTruthy(os.Getenv("DEV_EMAIL")) {
		log.Printf("[DEV] Password reset code for %s: %s", to, code)
		return nil
	}

	return sendTransactionalEmail(to, "Your Budget Tracker reset code", resetEmailText(code), resetEmailHTML(code))
}

func sendVerificationEmail(to, code string) error {
	if isTruthy(os.Getenv("DEV_EMAIL")) {
		log.Printf("[DEV] Email verification code for %s: %s", to, code)
		return nil
	}

	text := "Your Budget Tracker verification code is " + code + ".\n" +
		"It expires in 15 minutes.\n\n" +
		"If you didn't create an account, you can ignore this email.\n"
	return sendTransactionalEmail(to, "Verify your Budget Tracker email", text, verificationEmailHTML(code))
}

func sendTransactionalEmail(to, subject, text, html string) error {
	apiKey := strings.TrimSpace(os.Getenv("SENDGRID_API_KEY"))

	// Must match a sender or domain you verified in SendGrid. Once you
	// authenticate your own domain, set SENDGRID_FROM=noreply@yourdomain.com
	// in the environment; no code change is needed.
	fromEmail := strings.TrimSpace(os.Getenv("SENDGRID_FROM"))
	if fromEmail == "" {
		fromEmail = "chekusjoseph@gmail.com"
	}

	if apiKey == "" {
		return fmt.Errorf("SENDGRID_API_KEY is not configured")
	}

	payload := map[string]any{
		"personalizations": []map[string]any{
			{
				"to": []map[string]string{
					{"email": to},
				},
			},
		},
		"from": map[string]string{
			"email": fromEmail,
			"name":  "Budget Tracker",
		},
		"subject": subject,
		// SendGrid requires text/plain before text/html.
		"content": []map[string]string{
			{"type": "text/plain", "value": text},
			{"type": "text/html", "value": html},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.sendgrid.com/v3/mail/send", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("sendgrid API returned %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("[MAIL] Transactional email sent to %s via SendGrid", to)
	return nil
}
