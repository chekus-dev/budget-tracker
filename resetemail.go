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

// sendResetEmail delivers the password reset code to the user.
//
// Two modes:
//   - DEV_EMAIL=1: logs the code to the server console instead of sending.
//   - Otherwise: POSTs to Resend's API using RESEND_API_KEY.
//
// Required env when not in DEV_EMAIL mode:
//   RESEND_API_KEY     — from https://resend.com/api-keys
//   RESEND_FROM_EMAIL  — e.g. "Budget Tracker <onboarding@resend.dev>"
func sendResetEmail(to, code string) error {
	if isTruthy(os.Getenv("DEV_EMAIL")) {
		log.Printf("[DEV] Password reset code for %s: %s", to, code)
		return nil
	}

	apiKey := strings.TrimSpace(os.Getenv("RESEND_API_KEY"))
	from := strings.TrimSpace(os.Getenv("RESEND_FROM_EMAIL"))
	if apiKey == "" || from == "" {
		log.Printf("[MAIL] RESEND_API_KEY or RESEND_FROM_EMAIL not set; skipping send to %s", to)
		return nil
	}

	subject := "Your Budget Tracker reset code"
	text := fmt.Sprintf(`Hi,

Your password reset code is:

    %s

It expires in 15 minutes. If you didn't ask for this, you can ignore this email.

— Budget Tracker`, code)

	payload := map[string]any{
		"from":    from,
		"to":      []string{to},
		"subject": subject,
		"text":    text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal resend payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build resend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("resend request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("resend API returned %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("[MAIL] Reset code sent to %s via Resend", to)
	return nil
}