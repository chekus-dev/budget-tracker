package main

import "strings"

const resetEmailTpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Your Budget Tracker code</title>
</head>
<body style="margin:0;padding:0;background:#f3f4f8;">
<div style="display:none;max-height:0;overflow:hidden;opacity:0;">Your 6-digit code is {{CODE}}. It expires in 15 minutes.</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f3f4f8;padding:32px 12px;">
<tr><td align="center">
<table role="presentation" width="480" cellpadding="0" cellspacing="0" style="width:100%;max-width:480px;background:#ffffff;border-radius:20px;overflow:hidden;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
<tr><td align="center" bgcolor="#0c7a5e" style="background:#0c7a5e;background-image:linear-gradient(135deg,#0c7a5e,#095f49);padding:30px 24px;">
<div style="font-size:22px;font-weight:700;color:#ffffff;letter-spacing:-0.3px;">Budget Tracker</div>
<div style="font-size:13px;color:#d3efe4;margin-top:4px;">Password reset</div>
</td></tr>
<tr><td style="padding:32px 28px 8px;">
<h1 style="margin:0 0 10px;font-size:22px;line-height:1.3;color:#111827;">Your reset code</h1>
<p style="margin:0;font-size:15px;line-height:1.6;color:#4b5563;">Enter this code in the app to choose a new password. It expires in 15 minutes.</p>
</td></tr>
<tr><td align="center" style="padding:20px 28px;">
<div style="display:inline-block;background:#e0f3ec;border:1px solid #a9dcc9;border-radius:14px;padding:16px 26px;font-family:'SF Mono',Menlo,Consolas,monospace;font-size:34px;font-weight:700;letter-spacing:10px;color:#095f49;">{{CODE}}</div>
</td></tr>
<tr><td style="padding:0 28px 28px;">
<p style="margin:0;font-size:13px;line-height:1.6;color:#6b7280;">Didn't ask for this? You can ignore this email and your password will stay the same. Never share this code with anyone.</p>
</td></tr>
<tr><td style="padding:18px 28px;background:#f9fafb;border-top:1px solid #eef0f4;">
<p style="margin:0;font-size:12px;color:#9ca3af;text-align:center;">Budget Tracker &middot; You received this email because a password reset was requested for your account.</p>
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`

func resetEmailHTML(code string) string {
	return strings.ReplaceAll(resetEmailTpl, "{{CODE}}", code)
}

func resetEmailText(code string) string {
	return "Your Budget Tracker reset code is " + code + ".\n" +
		"It expires in 15 minutes.\n\n" +
		"If you didn't ask for this, ignore this email.\n"
}

const verificationEmailTpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Verify your Budget Tracker email</title>
</head>
<body style="margin:0;padding:0;background:#f3f4f8;">
<div style="display:none;max-height:0;overflow:hidden;opacity:0;">Your email verification code is {{CODE}}. It expires in 15 minutes.</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f3f4f8;padding:32px 12px;">
<tr><td align="center">
<table role="presentation" width="480" cellpadding="0" cellspacing="0" style="width:100%;max-width:480px;background:#ffffff;border-radius:20px;overflow:hidden;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;">
<tr><td align="center" bgcolor="#0c7a5e" style="background:#0c7a5e;background-image:linear-gradient(135deg,#0c7a5e,#095f49);padding:30px 24px;">
<div style="font-size:22px;font-weight:700;color:#ffffff;letter-spacing:-0.3px;">Budget Tracker</div>
<div style="font-size:13px;color:#d3efe4;margin-top:4px;">One quick step to get started</div>
</td></tr>
<tr><td style="padding:32px 28px 8px;">
<h1 style="margin:0 0 10px;font-size:22px;line-height:1.3;color:#111827;">Verify your email</h1>
<p style="margin:0;font-size:15px;line-height:1.6;color:#4b5563;">Enter this code in Budget Tracker to confirm you can receive email at this address. It expires in 15 minutes.</p>
</td></tr>
<tr><td align="center" style="padding:20px 28px;">
<div style="display:inline-block;background:#e0f3ec;border:1px solid #a9dcc9;border-radius:14px;padding:16px 26px;font-family:'SF Mono',Menlo,Consolas,monospace;font-size:34px;font-weight:700;letter-spacing:10px;color:#095f49;">{{CODE}}</div>
</td></tr>
<tr><td style="padding:0 28px 28px;">
<p style="margin:0;font-size:13px;line-height:1.6;color:#6b7280;">If you didn't create a Budget Tracker account, you can ignore this email. Never share this code with anyone.</p>
</td></tr>
<tr><td style="padding:18px 28px;background:#f9fafb;border-top:1px solid #eef0f4;">
<p style="margin:0;font-size:12px;color:#9ca3af;text-align:center;">Budget Tracker &middot; Email verification</p>
</td></tr>
</table>
</td></tr>
</table>
</body>
</html>`

func verificationEmailHTML(code string) string {
	return strings.ReplaceAll(verificationEmailTpl, "{{CODE}}", code)
}
