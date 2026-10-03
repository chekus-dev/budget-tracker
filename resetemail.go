package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// Table layout and inline styles on purpose: email clients (Gmail, Outlook)
// ignore <style> blocks and modern CSS. Colours match the app's brand tokens.
const resetEmailHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Reset your password</title>
</head>
<body style="margin:0;padding:0;background:#f4f6f9;">
<div style="display:none;max-height:0;overflow:hidden;opacity:0;">Use this link within 15 minutes to choose a new password.</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f4f6f9;padding:32px 16px;">
<tr><td align="center">
  <table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:480px;background:#ffffff;border:1px solid #e2e8f0;border-radius:16px;">
  <tr><td style="padding:32px 28px;font-family:-apple-system,'Segoe UI',Roboto,Arial,sans-serif;color:#0f172a;">

    <table role="presentation" cellpadding="0" cellspacing="0" style="margin-bottom:24px;"><tr>
      <td width="32" height="32" align="center" valign="middle" style="background:#185fa5;border-radius:9px;color:#ffffff;font-size:15px;font-weight:700;">&#8358;</td>
      <td style="padding-left:10px;font-size:16px;font-weight:700;color:#0f172a;">Budget Tracker</td>
    </tr></table>

    <h1 style="margin:0 0 8px;font-size:22px;line-height:1.3;font-weight:700;letter-spacing:-0.01em;color:#0f172a;">Reset your password</h1>
    <p style="margin:0 0 24px;font-size:15px;line-height:1.6;color:#5b6b82;">Someone asked to reset the password for your account. Tap the button below to choose a new one.</p>

    <table role="presentation" cellpadding="0" cellspacing="0" style="margin:0 0 24px;"><tr>
      <td align="center" style="background:#185fa5;border-radius:10px;">
        <a href="{{LINK}}" style="display:inline-block;padding:14px 28px;font-size:15px;font-weight:600;color:#ffffff;text-decoration:none;border-radius:10px;">Reset password</a>
      </td>
    </tr></table>

    <p style="margin:0 0 20px;font-size:13px;line-height:1.5;color:#5b6b82;">This link works once and expires in <strong style="color:#0f172a;">15 minutes</strong>.</p>

    <p style="margin:0 0 6px;font-size:12px;line-height:1.5;color:#5b6b82;">Button not working? Copy this link into your browser:</p>
    <p style="margin:0 0 24px;font-size:12px;line-height:1.5;word-break:break-all;"><a href="{{LINK}}" style="color:#0c447c;">{{LINK}}</a></p>

    <hr style="border:0;border-top:1px solid #e2e8f0;margin:0 0 16px;">
    <p style="margin:0;font-size:12px;line-height:1.5;color:#5b6b82;">Didn't ask for this? You can ignore this email &mdash; your password won't change.</p>

  </td></tr>
  </table>
</td></tr>
</table>
</body>
</html>`

const resetEmailText = `Reset your Budget Tracker password

Someone asked to reset the password for your account. Open this link to choose a new one:

{{LINK}}

The link works once and expires in 15 minutes.

Didn't ask for this? You can ignore this email - your password won't change.
`

// resetEmailContent fills the link into both versions. The HTML copy is
// escaped; the plain-text copy is not, because it is not parsed as markup.
func resetEmailContent(resetLink string) (subject, htmlBody, textBody string) {
	subject = "Reset your Budget Tracker password"
	htmlBody = strings.ReplaceAll(resetEmailHTML, "{{LINK}}", html.EscapeString(resetLink))
	textBody = strings.ReplaceAll(resetEmailText, "{{LINK}}", resetLink)
	return
}

// sendResetEmail sends the reset link via the Resend API.
// Requires RESEND_API_KEY and RESEND_FROM_EMAIL. If either is missing it logs
// the link instead of sending (useful for local development).
func sendResetEmail(toEmail, resetLink string) error {
	apiKey := os.Getenv("RESEND_API_KEY")
	fromEmail := os.Getenv("RESEND_FROM_EMAIL")

	if apiKey == "" || fromEmail == "" {
		log.Printf("RESEND_API_KEY/RESEND_FROM_EMAIL not set — reset link for %s: %s", toEmail, resetLink)
		return nil
	}

	subject, htmlBody, textBody := resetEmailContent(resetLink)
	payload, err := json.Marshal(map[string]interface{}{
		"from":    fromEmail,
		"to":      []string{toEmail},
		"subject": subject,
		"html":    htmlBody,
		"text":    textBody,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("resend API returned status %d", resp.StatusCode)
	}
	return nil
}