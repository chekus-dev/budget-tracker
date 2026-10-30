package main

// welcome_messages.go: branded system messages.
//
// These are seeded once per account (first time they open /notifications, or
// immediately after signup). They use admin_id = 0 so they never appear as an
// action by a real admin, and is_system = true so they can be styled and
// filtered separately.

import (
	"log"
)

// welcomeMessages is the set a fresh account receives, in order.
// Keep these short, warm, and useful. No marketing jargon.
var welcomeMessages = []struct {
	Subject string
	Body    string
}{
	{
		Subject: "Welcome to Budget tracker. How to get the most out of it",
		Body: "A few things worth knowing:\n\n" +
			"Getting started\n" +
			"• Set a monthly budget in Settings and the home screen shows what's left to spend. Or leave it blank and just log.\n" +
			"• Log expenses as they happen, even rough ones. A habit beats perfection.\n" +
			"• In Settings, tap a suggested category or type your own. Every category gets its own icon and colour automatically, and you can give any of them a monthly limit.\n" +
			"• Set goals for things you plan to do this month or this year.\n" +
			"• Open the menu with your name and choose Repeating entries to add rent, salary, subscriptions and other regular money in or out.\n\n" +
			"Keeping your account safe\n" +
			"• Sign in with your fingerprint or Face ID. Go to Settings, find Fingerprint sign-in, and add a passkey on each device you use. It works with a fingerprint, face or screen lock on Android, and with Face ID or Touch ID on iPhone. Your password still works as a backup. If it says no passkeys are available, remove your passkeys in Settings and add one again.\n" +
			"• Choose a password you have not used before. When you change or reset it, the app rejects your current password and your last five.\n" +
			"• If you forget your password, we email a 6-digit code that expires in 15 minutes. We never ask for your password or codes in a message.\n" +
			"• Tap the eye at the top to hide your amounts.\n\n" +
			"Your data\n" +
			"• Export any month to CSV or PDF any time. Your data is yours.",
	},
}

// seedWelcomeMessagesIfNeeded inserts the welcome set exactly once per user.
// It checks for any system message first, so users who already have them
// won't see duplicates on every visit.
func seedWelcomeMessagesIfNeeded(userID int) {
	if db == nil {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Printf("seedWelcome: begin failed for %d: %v", userID, err)
		return
	}
	defer tx.Rollback()

	for _, m := range welcomeMessages {
		result, err := tx.Exec(`
            UPDATE user_messages
            SET body = $3
            WHERE user_id = $1 AND is_system = TRUE AND subject = $2`,
			userID, m.Subject, m.Body,
		)
		if err != nil {
			log.Printf("seedWelcome: update failed for %d: %v", userID, err)
			return
		}
		updated, err := result.RowsAffected()
		if err != nil {
			log.Printf("seedWelcome: update count failed for %d: %v", userID, err)
			return
		}
		if updated > 0 {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO user_messages (user_id, admin_id, subject, body, is_system)
             VALUES ($1, 0, $2, $3, TRUE)`,
			userID, m.Subject, m.Body,
		); err != nil {
			log.Printf("seedWelcome: insert failed for %d: %v", userID, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		log.Printf("seedWelcome: commit failed for %d: %v", userID, err)
	}
}
