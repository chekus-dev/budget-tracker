package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
)

func sendResetEmail(to, code string) error {
	if isTruthy(os.Getenv("DEV_EMAIL")) {
		log.Printf("[DEV] Password reset code for %s: %s", to, code)
		return nil
	}

	apiKey := strings.TrimSpace(os.Getenv("AGENTMAIL_API_KEY"))
	inboxID := strings.TrimSpace(os.Getenv("AGENTMAIL_INBOX_ID"))

	if apiKey == "" || inboxID == "" {
		log.Printf("[MAIL] AGENTMAIL_API_KEY or AGENTMAIL_INBOX_ID not set; skipping send to %s", to)
		return nil
	}

	// 1. Safely encode the email address (turns @ into %40)
	encodedInboxID := url.PathEscape(inboxID)

	// 2. Use the HTTP API endpoint
	endpoint := fmt.Sprintf("https://api.agentmail.to/v0/inboxes/%s/messages/send", encodedInboxID)

	payload := map[string]any{
		"to":      []string{to},
		"subject": "Your Budget Tracker reset code",
		"text":    fmt.Sprintf("Hi,\n\nYour password reset code is:\n\n    %s\n\nIt expires in 15 minutes.\n\n— Budget Tracker", code),
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
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
		return fmt.Errorf("agentmail API returned %d: %s", resp.StatusCode, string(respBody))
	}

	log.Printf("[MAIL] Reset code sent to %s via AgentMail HTTP API", to)
	return nil
}
