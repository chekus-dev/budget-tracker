package main

// notifications.go — admin → user messaging.
//
// Distinct from admin_notes (admin-only). These messages are visible to the
// user they are addressed to, and can be marked read.

import (
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type UserMessage struct {
	ID        int
	Subject   string
	Body      string
	FromAdmin string
	When      string
	Unread    bool
}

func unreadMessageCount(userID int) int {
	if db == nil {
		return 0
	}
	var n int
	_ = db.QueryRow(
		`SELECT COUNT(*) FROM user_messages WHERE user_id = $1 AND read_at IS NULL`,
		userID,
	).Scan(&n)
	return n
}

func loadUserMessages(userID, limit int) []UserMessage {
	if db == nil || limit <= 0 {
		return nil
	}
	rows, err := db.Query(`
		SELECT m.id, m.subject, m.body,
		       COALESCE(a.username, 'Support'),
		       m.created_at,
		       (m.read_at IS NULL) AS unread
		FROM user_messages m
		LEFT JOIN users a ON a.id = m.admin_id
		WHERE m.user_id = $1
		ORDER BY m.created_at DESC
		LIMIT $2`,
		userID, limit,
	)
	if err != nil {
		log.Printf("loadUserMessages: %v", err)
		return nil
	}
	defer rows.Close()

	var out []UserMessage
	for rows.Next() {
		var m UserMessage
		var when time.Time
		if err := rows.Scan(&m.ID, &m.Subject, &m.Body, &m.FromAdmin, &when, &m.Unread); err != nil {
			continue
		}
		m.When = humanAgo(when)
		out = append(out, m)
	}
	return out
}

// adminSendMessageHandler — POST from the admin user detail page.
func adminSendMessageHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	adminID, _, _ := currentUser(r)
	id, ok := adminGetTargetID(r.URL.Path, "/admin/users/message/")
	if !ok {
		http.NotFound(w, r)
		return
	}

	subject := strings.TrimSpace(r.FormValue("subject"))
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		adminRedirectToUser(w, r, id, "Message+body+required")
		return
	}
	if len(subject) > 200 {
		subject = subject[:200]
	}
	if len(body) > 4000 {
		body = body[:4000]
	}

	if _, err := db.Exec(`
		INSERT INTO user_messages (user_id, admin_id, subject, body)
		VALUES ($1, $2, $3, $4)`,
		id, adminID, subject, body,
	); err != nil {
		serverError(w, err)
		return
	}
	logAdminAction(adminID, id, "send_message", subject)
	adminRedirectToUser(w, r, id, "Message+sent")
}

// notificationsHandler — user-facing /notifications page.
func notificationsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)

	// Seed welcome messages on first visit for existing accounts.
	seedWelcomeMessagesIfNeeded(userID)

	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}

	// If opened with ?read={id}, mark that message read.
	if idStr := strings.TrimSpace(r.URL.Query().Get("read")); idStr != "" {
		if id, err := strconv.Atoi(idStr); err == nil {
			_, _ = db.Exec(
				`UPDATE user_messages SET read_at = NOW()
				 WHERE id = $1 AND user_id = $2 AND read_at IS NULL`,
				id, userID,
			)
		}
		http.Redirect(w, r, "/notifications", http.StatusSeeOther)
		return
	}
	if r.URL.Query().Get("all_read") == "1" {
		_, _ = db.Exec(
			`UPDATE user_messages SET read_at = NOW()
			 WHERE user_id = $1 AND read_at IS NULL`,
			userID,
		)
		http.Redirect(w, r, "/notifications", http.StatusSeeOther)
		return
	}

	messages := loadUserMessages(userID, 100)

	data := map[string]interface{}{
		"Messages":       messages,
		"UnreadCount":    unreadMessageCount(userID),
		"Username":       username,
		"CurrencySymbol": currencySymbol(settings.Currency),
		"Version":        appVersion,
	}
	if err := tmpl.ExecuteTemplate(w, "notifications.html", data); err != nil {
		if !isClientDisconnect(err) {
			log.Printf("failed to render notifications: %v", err)
		}
	}
}

// unreadMessagesForBanner returns up to 3 unread messages for the home banner.
func unreadMessagesForBanner(userID int) []UserMessage {
	if db == nil {
		return nil
	}
	rows, err := db.Query(`
		SELECT m.id, m.subject, m.body,
		       COALESCE(a.username, 'Support'),
		       m.created_at, TRUE
		FROM user_messages m
		LEFT JOIN users a ON a.id = m.admin_id
		WHERE m.user_id = $1 AND m.read_at IS NULL
		ORDER BY m.created_at DESC
		LIMIT 3`,
		userID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []UserMessage
	for rows.Next() {
		var m UserMessage
		var when time.Time
		if err := rows.Scan(&m.ID, &m.Subject, &m.Body, &m.FromAdmin, &when, &m.Unread); err != nil {
			continue
		}
		m.When = humanAgo(when)
		out = append(out, m)
	}
	return out
}

var _ = sql.ErrNoRows // keep the import for future use

// notificationsReadHandler — POST /notifications/{id}/read
func notificationsReadHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)

	path := strings.TrimPrefix(r.URL.Path, "/notifications/")
	path = strings.TrimSuffix(path, "/read")
	id, err := strconv.Atoi(strings.TrimSuffix(path, "/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = db.Exec(
		`UPDATE user_messages SET read_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND read_at IS NULL`,
		id, userID,
	)
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}

// notificationsAllReadHandler — POST /notifications/all-read
func notificationsAllReadHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)
	_, _ = db.Exec(
		`UPDATE user_messages SET read_at = NOW()
		 WHERE user_id = $1 AND read_at IS NULL`,
		userID,
	)
	http.Redirect(w, r, "/notifications", http.StatusSeeOther)
}
