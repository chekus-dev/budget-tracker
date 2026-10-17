package main

// welcome_messages.go — branded system messages.
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
		Subject: "Welcome to Budget Tracker",
		Body: "Thanks for signing up. Budget Tracker is a small, private tool for keeping track of what you spend and what you save — one month at a time.\n\n" +
			"There's no feed, no ads, and no algorithm. Just you and your money, laid out clearly.\n\n" +
			"Take a minute to set a monthly budget in Settings, add your first expense, and you'll see it come to life.",
	},
	{
		Subject: "How to get the most out of it",
		Body: "A few things worth knowing:\n\n" +
			"• Set a monthly budget in Settings — the home screen will show what's left to spend.\n" +
			"• Give categories their own limits to track them separately.\n" +
			"• Log expenses as they happen. Even rough ones. A habit beats perfection.\n" +
			"• Export to CSV or PDF any time. Your data is yours.",
	},
	{
		Subject: "You're part of something small",
		Body: "Budget Tracker is built by one person, not a company. Every user matters, every piece of feedback is read, and there are no investors to please.\n\n" +
			"If it's useful to you, keep using it. If something is missing, tell us. If you ever decide to leave, your data goes with you — export it and it's yours.\n\n" +
			"Stay with us. This only works because people like you do.",
	},
}

// seedWelcomeMessagesIfNeeded inserts the welcome set exactly once per user.
// It checks for any system message first, so users who already have them
// won't see duplicates on every visit.
func seedWelcomeMessagesIfNeeded(userID int) {
	if db == nil {
		return
	}

	var existing int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM user_messages WHERE user_id = $1 AND is_system = TRUE`,
		userID,
	).Scan(&existing); err != nil {
		log.Printf("seedWelcome: check failed for %d: %v", userID, err)
		return
	}
	if existing > 0 {
		return
	}

	tx, err := db.Begin()
	if err != nil {
		log.Printf("seedWelcome: begin failed for %d: %v", userID, err)
		return
	}
	defer tx.Rollback()

	for _, m := range welcomeMessages {
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
