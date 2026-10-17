package main

// admin.go — internal admin portal: dashboard, users, actions, notes, audit.

import (
	"database/sql"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func adminIDs() map[int]bool {
	raw := strings.TrimSpace(os.Getenv("ADMIN_USER_IDS"))
	if raw == "" {
		return nil
	}
	out := map[int]bool{}
	for _, part := range strings.Split(raw, ",") {
		if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			out[id] = true
		}
	}
	return out
}

func requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, _, ok := currentUser(r)
		if !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		if !adminIDs()[userID] {
			http.NotFound(w, r)
			return
		}
		next(w, r)
	}
}

func logAdminAction(adminID, targetID int, action, note string) {
	if _, err := db.Exec(
		`INSERT INTO admin_actions (admin_id, target_id, action, note)
		 VALUES ($1, $2, $3, $4)`,
		adminID, targetID, action, note,
	); err != nil {
		log.Printf("logAdminAction: %v", err)
	}
}

// ---------- Dashboard ----------

func adminDashboardHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}

	var totalUsers, active7d, newThisWeek, disabled, actionsThisWeek int
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE deleted_at IS NULL`).Scan(&totalUsers)
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE deleted_at IS NULL AND last_seen_at > NOW() - INTERVAL '7 days'`).Scan(&active7d)
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE terms_accepted_at > NOW() - INTERVAL '7 days'`).Scan(&newThisWeek)
	db.QueryRow(`SELECT COUNT(*) FROM users WHERE deleted_at IS NOT NULL`).Scan(&disabled)
	db.QueryRow(`SELECT COUNT(*) FROM admin_actions WHERE created_at > NOW() - INTERVAL '7 days'`).Scan(&actionsThisWeek)

	type RecentUser struct {
		ID         int
		Username   string
		Email      string
		SignedUpAt string
	}
	var recentUsers []RecentUser
	rows, err := db.Query(`
		SELECT id, username, email, to_char(terms_accepted_at, 'DD Mon')
		FROM users WHERE deleted_at IS NULL
		ORDER BY terms_accepted_at DESC NULLS LAST LIMIT 8`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var u RecentUser
			var when sql.NullString
			if err := rows.Scan(&u.ID, &u.Username, &u.Email, &when); err != nil {
				continue
			}
			if when.Valid {
				u.SignedUpAt = when.String
			} else {
				u.SignedUpAt = "—"
			}
			recentUsers = append(recentUsers, u)
		}
	}

	type RecentAction struct {
		When       string
		Admin      string
		TargetName string
		TargetID   int
		Action     string
		Note       string
	}
	var recentActions []RecentAction
	rows2, err := db.Query(`
		SELECT a.created_at, COALESCE(adm.username, '#'||a.admin_id::text),
		       COALESCE(tgt.username, '#'||a.target_id::text),
		       a.target_id, a.action, a.note
		FROM admin_actions a
		LEFT JOIN users adm ON adm.id = a.admin_id
		LEFT JOIN users tgt ON tgt.id = a.target_id
		ORDER BY a.created_at DESC LIMIT 10`)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var ar RecentAction
			var when time.Time
			if err := rows2.Scan(&when, &ar.Admin, &ar.TargetName, &ar.TargetID, &ar.Action, &ar.Note); err != nil {
				continue
			}
			ar.When = humanAgo(when)
			recentActions = append(recentActions, ar)
		}
	}

	data := map[string]interface{}{
		"TotalUsers":      totalUsers,
		"Active7d":        active7d,
		"NewThisWeek":     newThisWeek,
		"Disabled":        disabled,
		"ActionsThisWeek": actionsThisWeek,
		"RecentUsers":     recentUsers,
		"RecentActions":   recentActions,
		"Version":         appVersion,
		"Flash":           strings.TrimSpace(r.URL.Query().Get("flash")),
	}
	if err := tmpl.ExecuteTemplate(w, "admin_dashboard.html", data); err != nil {
		if !isClientDisconnect(err) {
			log.Printf("failed to render admin_dashboard: %v", err)
		}
	}
}

// ---------- Users list ----------

type AdminUserRow struct {
	ID           int
	Username     string
	Email        string
	LastSeenAt   string
	SignedUpAt   string
	ExpenseCount int
	GoalCount    int
	Deleted      bool
	HasNote      bool
}

const (
	filterAll      = "all"
	filterActive   = "active"
	filterInactive = "inactive"
	filterDeleted  = "deleted"
)

func buildAdminUserQuery(filter, search string) (string, []any) {
	where := []string{"1=1"}
	args := []any{}

	switch filter {
	case filterActive:
		where = append(where, "u.deleted_at IS NULL", "u.last_seen_at > NOW() - INTERVAL '7 days'")
	case filterInactive:
		where = append(where, "u.deleted_at IS NULL",
			"(u.last_seen_at IS NULL OR u.last_seen_at < NOW() - INTERVAL '30 days')")
	case filterDeleted:
		where = append(where, "u.deleted_at IS NOT NULL")
	default:
		where = append(where, "u.deleted_at IS NULL")
	}

	if search != "" {
		args = append(args, "%"+strings.ToLower(search)+"%")
		where = append(where,
			fmt.Sprintf("(LOWER(u.username) LIKE $%d OR LOWER(u.email) LIKE $%d)", len(args), len(args)))
	}

	q := fmt.Sprintf(`
		SELECT u.id, u.username, u.email,
		       u.last_seen_at, u.terms_accepted_at, u.deleted_at,
		       (u.admin_note <> '') AS has_note,
		       COALESCE((SELECT COUNT(*) FROM expenses e WHERE e.user_id = u.id AND e.deleted_at IS NULL), 0),
		       COALESCE((SELECT COUNT(*) FROM goals g WHERE g.user_id = u.id), 0)
		FROM users u
		WHERE %s
		ORDER BY COALESCE(u.last_seen_at, u.terms_accepted_at) DESC NULLS LAST
		LIMIT 200`,
		strings.Join(where, " AND "))
	return q, args
}

func adminUsersHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	if filter == "" {
		filter = filterAll
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))

	q, args := buildAdminUserQuery(filter, search)
	rows, err := db.Query(q, args...)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()

	var users []AdminUserRow
	for rows.Next() {
		var u AdminUserRow
		var lastSeen, signedUp, deletedAt sql.NullTime
		if err := rows.Scan(&u.ID, &u.Username, &u.Email,
			&lastSeen, &signedUp, &deletedAt, &u.HasNote,
			&u.ExpenseCount, &u.GoalCount); err != nil {
			serverError(w, err)
			return
		}
		if lastSeen.Valid {
			u.LastSeenAt = humanAgo(lastSeen.Time)
		} else {
			u.LastSeenAt = "never"
		}
		if signedUp.Valid {
			u.SignedUpAt = signedUp.Time.Format("2 Jan 2006")
		}
		u.Deleted = deletedAt.Valid
		users = append(users, u)
	}

	counts := map[string]int{}
	for f, query := range map[string]string{
		filterAll:      `SELECT COUNT(*) FROM users WHERE deleted_at IS NULL`,
		filterActive:   `SELECT COUNT(*) FROM users WHERE deleted_at IS NULL AND last_seen_at > NOW() - INTERVAL '7 days'`,
		filterInactive: `SELECT COUNT(*) FROM users WHERE deleted_at IS NULL AND (last_seen_at IS NULL OR last_seen_at < NOW() - INTERVAL '30 days')`,
		filterDeleted:  `SELECT COUNT(*) FROM users WHERE deleted_at IS NOT NULL`,
	} {
		var n int
		db.QueryRow(query).Scan(&n)
		counts[f] = n
	}

	data := map[string]interface{}{
		"Filter":  filter,
		"Query":   search,
		"Users":   users,
		"Counts":  counts,
		"Version": appVersion,
		"Flash":   strings.TrimSpace(r.URL.Query().Get("flash")),
	}
	if err := tmpl.ExecuteTemplate(w, "admin.html", data); err != nil {
		if !isClientDisconnect(err) {
			log.Printf("failed to render admin: %v", err)
		}
	}
}

func adminExportCSVHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	filter := strings.TrimSpace(r.URL.Query().Get("filter"))
	if filter == "" {
		filter = filterAll
	}
	search := strings.TrimSpace(r.URL.Query().Get("q"))

	q, args := buildAdminUserQuery(filter, search)
	rows, err := db.Query(q, args...)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="users.csv"`)
	cw := csv.NewWriter(w)
	defer cw.Flush()
	cw.Write([]string{"ID", "Username", "Email", "Last seen", "Signed up", "Expenses", "Goals", "Status"})

	for rows.Next() {
		var u AdminUserRow
		var lastSeen, signedUp, deletedAt sql.NullTime
		if err := rows.Scan(&u.ID, &u.Username, &u.Email,
			&lastSeen, &signedUp, &deletedAt, &u.HasNote,
			&u.ExpenseCount, &u.GoalCount); err != nil {
			continue
		}
		lastSeenStr := "never"
		if lastSeen.Valid {
			lastSeenStr = lastSeen.Time.Format("2006-01-02 15:04")
		}
		signedUpStr := ""
		if signedUp.Valid {
			signedUpStr = signedUp.Time.Format("2006-01-02")
		}
		status := "active"
		if deletedAt.Valid {
			status = "disabled"
		}
		cw.Write([]string{
			strconv.Itoa(u.ID), u.Username, u.Email, lastSeenStr, signedUpStr,
			strconv.Itoa(u.ExpenseCount), strconv.Itoa(u.GoalCount), status,
		})
	}
}

// ---------- User detail ----------

type AdminUserDetail struct {
	ID           int
	Username     string
	Email        string
	SignedUpAt   string
	LastSeenAt   string
	DeletedAt    string
	BannedReason string
	ExpenseCount int
	GoalCount    int
	TotalSpent   float64
	TotalIncome  float64
	Currency     string
	Recent       []AdminActionLog
	Notes        []AdminNoteRow
	RecentTx     []AdminTx
}

type AdminActionLog struct {
	When   string
	Admin  string
	Action string
	Note   string
}

type AdminNoteRow struct {
	ID    int
	When  string
	Admin string
	Body  string
}

type AdminTx struct {
	When     string
	Kind     string
	Desc     string
	Amount   float64
	Category string
}

func adminUserHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/admin/users/"))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	var u AdminUserDetail
	var signedUp, lastSeen, deletedAt sql.NullTime
	var banned sql.NullString
	var currency sql.NullString

	err = db.QueryRow(`
		SELECT u.id, u.username, u.email,
		       u.terms_accepted_at, u.last_seen_at, u.deleted_at, u.admin_note,
		       COALESCE(s.currency, 'NGN')
		FROM users u
		LEFT JOIN settings s ON s.user_id = u.id
		WHERE u.id = $1`, id,
	).Scan(&u.ID, &u.Username, &u.Email,
		&signedUp, &lastSeen, &deletedAt, &banned, &currency)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}

	if signedUp.Valid {
		u.SignedUpAt = signedUp.Time.Format("2 Jan 2006, 15:04")
	} else {
		u.SignedUpAt = "—"
	}
	if lastSeen.Valid {
		u.LastSeenAt = humanAgo(lastSeen.Time)
	} else {
		u.LastSeenAt = "never"
	}
	if deletedAt.Valid {
		u.DeletedAt = humanAgo(deletedAt.Time)
	}
	if banned.Valid {
		u.BannedReason = banned.String
	}
	u.Currency = currency.String

	db.QueryRow(`SELECT COUNT(*) FROM expenses WHERE user_id = $1 AND deleted_at IS NULL`, u.ID).Scan(&u.ExpenseCount)
	db.QueryRow(`SELECT COUNT(*) FROM goals WHERE user_id = $1`, u.ID).Scan(&u.GoalCount)
	db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM expenses WHERE user_id = $1 AND kind = 'expense' AND deleted_at IS NULL`, u.ID).Scan(&u.TotalSpent)
	db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM expenses WHERE user_id = $1 AND kind = 'income' AND deleted_at IS NULL`, u.ID).Scan(&u.TotalIncome)

	rows, err := db.Query(`
		SELECT a.created_at, COALESCE(adm.username, '#'||a.admin_id::text), a.action, a.note
		FROM admin_actions a
		LEFT JOIN users adm ON adm.id = a.admin_id
		WHERE a.target_id = $1
		ORDER BY a.created_at DESC LIMIT 30`, u.ID)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var ar AdminActionLog
			var when time.Time
			if err := rows.Scan(&when, &ar.Admin, &ar.Action, &ar.Note); err != nil {
				continue
			}
			ar.When = humanAgo(when)
			u.Recent = append(u.Recent, ar)
		}
	}

	rows2, err := db.Query(`
		SELECT n.id, n.created_at, COALESCE(adm.username, '#'||n.admin_id::text), n.body
		FROM admin_notes n
		LEFT JOIN users adm ON adm.id = n.admin_id
		WHERE n.target_id = $1
		ORDER BY n.created_at DESC LIMIT 100`, u.ID)
	if err == nil {
		defer rows2.Close()
		for rows2.Next() {
			var nr AdminNoteRow
			var when time.Time
			if err := rows2.Scan(&nr.ID, &when, &nr.Admin, &nr.Body); err != nil {
				continue
			}
			nr.When = humanAgo(when)
			u.Notes = append(u.Notes, nr)
		}
	}

	rows3, err := db.Query(`
		SELECT created_at, kind, description, amount, COALESCE(category,'')
		FROM expenses
		WHERE user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at DESC LIMIT 10`, u.ID)
	if err == nil {
		defer rows3.Close()
		for rows3.Next() {
			var tx AdminTx
			var when time.Time
			if err := rows3.Scan(&when, &tx.Kind, &tx.Desc, &tx.Amount, &tx.Category); err != nil {
				continue
			}
			tx.When = when.Format("2 Jan 15:04")
			u.RecentTx = append(u.RecentTx, tx)
		}
	}

	data := map[string]interface{}{
		"User":    u,
		"Version": appVersion,
		"Flash":   strings.TrimSpace(r.URL.Query().Get("flash")),
	}
	if err := tmpl.ExecuteTemplate(w, "admin_user.html", data); err != nil {
		if !isClientDisconnect(err) {
			log.Printf("failed to render admin_user: %v", err)
		}
	}
}

// ---------- Actions ----------

func adminGetTargetID(path, prefix string) (int, bool) {
	id, err := strconv.Atoi(strings.TrimPrefix(path, prefix))
	if err != nil {
		return 0, false
	}
	return id, true
}

func adminRedirectToUser(w http.ResponseWriter, r *http.Request, id int, flash string) {
	http.Redirect(w, r, fmt.Sprintf("/admin/users/%d?flash=%s", id, flash), http.StatusSeeOther)
}

func adminDisableHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	adminID, _, _ := currentUser(r)
	id, ok := adminGetTargetID(r.URL.Path, "/admin/users/disable/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	category := strings.TrimSpace(r.FormValue("category"))
	detail := strings.TrimSpace(r.FormValue("reason"))
	if category == "" {
		category = "Unspecified"
	}
	reason := category
	if detail != "" {
		reason = category + ": " + detail
	}
	if len(reason) > 500 {
		reason = reason[:500]
	}
	if _, err := db.Exec(
		`UPDATE users SET deleted_at = NOW(), admin_note = $1 WHERE id = $2 AND deleted_at IS NULL`,
		reason, id,
	); err != nil {
		serverError(w, err)
		return
	}
	logAdminAction(adminID, id, "disable", reason)
	adminRedirectToUser(w, r, id, "Account+disabled")
}

func adminRestoreHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	adminID, _, _ := currentUser(r)
	id, ok := adminGetTargetID(r.URL.Path, "/admin/users/restore/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := db.Exec(`UPDATE users SET deleted_at = NULL, admin_note = '' WHERE id = $1`, id); err != nil {
		serverError(w, err)
		return
	}
	logAdminAction(adminID, id, "restore", "")
	adminRedirectToUser(w, r, id, "Account+restored")
}

func adminForceLogoutHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	adminID, _, _ := currentUser(r)
	id, ok := adminGetTargetID(r.URL.Path, "/admin/users/force-logout/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if _, err := db.Exec(`UPDATE users SET session_version = session_version + 1 WHERE id = $1`, id); err != nil {
		serverError(w, err)
		return
	}
	logAdminAction(adminID, id, "force_logout", "")
	adminRedirectToUser(w, r, id, "Sessions+invalidated")
}

func adminSendResetHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	adminID, _, _ := currentUser(r)
	id, ok := adminGetTargetID(r.URL.Path, "/admin/users/send-reset/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	var email string
	if err := db.QueryRow(`SELECT email FROM users WHERE id = $1`, id).Scan(&email); err != nil {
		serverError(w, err)
		return
	}
	code, err := generateOTP()
	if err != nil {
		serverError(w, err)
		return
	}
	if _, err := db.Exec(
		`UPDATE users SET reset_token = $1, reset_token_expires = $2 WHERE id = $3`,
		hashResetToken(code), time.Now().Add(resetTokenTTL), id,
	); err != nil {
		serverError(w, err)
		return
	}
	go func() {
		if err := sendResetEmail(email, code); err != nil {
			log.Printf("admin reset email failed: %v", err)
		}
	}()
	logAdminAction(adminID, id, "send_reset", email)
	adminRedirectToUser(w, r, id, "Reset+code+sent")
}

func adminHardDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	adminID, _, _ := currentUser(r)
	id, ok := adminGetTargetID(r.URL.Path, "/admin/users/hard-delete/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if adminIDs()[id] {
		http.Error(w, "cannot delete an admin account", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(r.FormValue("confirm")) != "DELETE" {
		adminRedirectToUser(w, r, id, "Type+DELETE+to+confirm")
		return
	}

	tx, err := db.Begin()
	if err != nil {
		serverError(w, err)
		return
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM expenses WHERE user_id = $1`,
		`DELETE FROM goals WHERE user_id = $1`,
		`DELETE FROM category_budgets WHERE user_id = $1`,
		`DELETE FROM settings WHERE user_id = $1`,
		`DELETE FROM admin_notes WHERE target_id = $1`,
		`DELETE FROM admin_actions WHERE target_id = $1`,
		`DELETE FROM users WHERE id = $1`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			serverError(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		serverError(w, err)
		return
	}
	logAdminAction(adminID, id, "hard_delete", "permanent")
	http.Redirect(w, r, "/admin/users?flash=User+permanently+deleted", http.StatusSeeOther)
}

func adminAddNoteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	adminID, _, _ := currentUser(r)
	id, ok := adminGetTargetID(r.URL.Path, "/admin/users/notes/add/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		adminRedirectToUser(w, r, id, "Note+cannot+be+empty")
		return
	}
	if len(body) > 2000 {
		body = body[:2000]
	}
	if _, err := db.Exec(
		`INSERT INTO admin_notes (target_id, admin_id, body) VALUES ($1, $2, $3)`,
		id, adminID, body,
	); err != nil {
		serverError(w, err)
		return
	}
	logAdminAction(adminID, id, "add_note", "")
	adminRedirectToUser(w, r, id, "Note+added")
}

func adminDeleteNoteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	adminID, _, _ := currentUser(r)
	noteStr := strings.TrimPrefix(r.URL.Path, "/admin/users/notes/delete/")
	noteID, err := strconv.Atoi(noteStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var targetID int
	if err := db.QueryRow(`SELECT target_id FROM admin_notes WHERE id = $1`, noteID).Scan(&targetID); err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := db.Exec(`DELETE FROM admin_notes WHERE id = $1`, noteID); err != nil {
		serverError(w, err)
		return
	}
	logAdminAction(adminID, targetID, "delete_note", strconv.Itoa(noteID))
	adminRedirectToUser(w, r, targetID, "Note+deleted")
}

// ---------- Audit log ----------

func adminAuditHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	action := strings.TrimSpace(r.URL.Query().Get("action"))

	where := []string{"1=1"}
	args := []any{}
	if action != "" {
		args = append(args, action)
		where = append(where, fmt.Sprintf("a.action = $%d", len(args)))
	}

	q := fmt.Sprintf(`
		SELECT a.created_at,
		       COALESCE(adm.username, '#'||a.admin_id::text),
		       COALESCE(tgt.username, '#'||a.target_id::text),
		       a.target_id, a.action, a.note
		FROM admin_actions a
		LEFT JOIN users adm ON adm.id = a.admin_id
		LEFT JOIN users tgt ON tgt.id = a.target_id
		WHERE %s
		ORDER BY a.created_at DESC LIMIT 500`,
		strings.Join(where, " AND "))

	rows, err := db.Query(q, args...)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()

	type AuditRow struct {
		When       string
		Admin      string
		TargetName string
		TargetID   int
		Action     string
		Note       string
	}
	var entries []AuditRow
	for rows.Next() {
		var e AuditRow
		var when time.Time
		if err := rows.Scan(&when, &e.Admin, &e.TargetName, &e.TargetID, &e.Action, &e.Note); err != nil {
			continue
		}
		e.When = when.Format("2 Jan 2006, 15:04")
		entries = append(entries, e)
	}

	data := map[string]interface{}{
		"Entries": entries,
		"Filter":  action,
		"Version": appVersion,
	}
	if err := tmpl.ExecuteTemplate(w, "admin_audit.html", data); err != nil {
		if !isClientDisconnect(err) {
			log.Printf("failed to render admin_audit: %v", err)
		}
	}
}

func humanAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 24*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	case d < 7*24*time.Hour:
		return strconv.Itoa(int(d.Hours()/24)) + "d ago"
	default:
		return t.Format("2 Jan 2006")
	}
}
