package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxRecurringPerUser = 20
	recurringMaxPerRun  = 2000
)

type RecurringRule struct {
	ID          int
	Description string
	Amount      float64
	Category    string
	Kind        string
	Frequency   string
	NextDate    string
	Summary     string
	Active      bool
}

func (r RecurringRule) Income() bool { return r.Kind == kindIncome }

type recurringForm struct {
	Description, Amount, Category, Kind, Frequency, Start string
}

type RecurringPageData struct {
	PageChrome
	Rules      []RecurringRule
	Categories []string
	Today      string
	Error      string
	Form       recurringForm
	DeletedID  string
}

// nextRecurringDate returns the day after "from" that the rule is next due.
// from must be a UTC date at midnight. Monthly rules keep their anchor day and
// fall back to the last day of a shorter month.
func nextRecurringDate(freq string, anchorDay int, from time.Time) time.Time {
	switch freq {
	case "daily":
		return from.AddDate(0, 0, 1)
	case "weekly":
		return from.AddDate(0, 0, 7)
	}
	y, m, _ := from.Date()
	first := time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	d := anchorDay
	if d > last {
		d = last
	}
	return time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, time.UTC)
}

func utcDate(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func recurringSummary(freq string, anchor int, next time.Time) string {
	switch freq {
	case "daily":
		return "Every day"
	case "weekly":
		return "Every week on " + next.Weekday().String()
	}
	return fmt.Sprintf("Every month on day %d", anchor)
}

// processRecurring creates every entry that is due. Each one is made in its own
// transaction, so a failure part-way leaves earlier entries in place and never
// creates one twice. Returns how many entries were created.
func processRecurring(ctx context.Context) (int, error) {
	if db == nil {
		return 0, nil
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM recurring_entries WHERE deleted_at IS NOT NULL AND deleted_at < NOW() - INTERVAL '30 days'`); err != nil {
		log.Printf("recurring: purge: %v", err)
	}
	today := time.Now().Format("2006-01-02")
	made := 0
	for made < recurringMaxPerRun {
		done, err := processOneRecurring(ctx, today)
		if err != nil {
			return made, err
		}
		if done {
			break
		}
		made++
	}
	return made, nil
}

func processOneRecurring(ctx context.Context, today string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var id, userID, anchor int
	var desc, category, kind, freq string
	var amount float64
	var next time.Time
	err = tx.QueryRowContext(ctx,
		`SELECT r.id, r.user_id, r.description, r.amount, r.category, r.kind,
		        r.frequency, r.anchor_day, r.next_date
		 FROM recurring_entries r
		 WHERE r.active AND r.deleted_at IS NULL AND r.next_date <= $1::date
		   AND EXISTS (SELECT 1 FROM users u WHERE u.id = r.user_id AND u.deleted_at IS NULL)
		 ORDER BY r.next_date, r.id
		 LIMIT 1
		 FOR UPDATE OF r SKIP LOCKED`, today,
	).Scan(&id, &userID, &desc, &amount, &category, &kind, &freq, &anchor, &next)
	if err == sql.ErrNoRows {
		return true, nil
	}
	if err != nil {
		return false, err
	}

	due := utcDate(next)
	// Noon, so the entry lands in the right month whatever the server's time zone.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO expenses (user_id, description, amount, category, kind, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6::timestamp)`,
		userID, desc, amount, category, kind, due.Format("2006-01-02")+" 12:00:00",
	); err != nil {
		return false, err
	}
	following := nextRecurringDate(freq, anchor, due)
	if _, err := tx.ExecContext(ctx,
		`UPDATE recurring_entries SET next_date = $1::date WHERE id = $2`,
		following.Format("2006-01-02"), id,
	); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

// startRecurringJob runs once at startup and then every hour.
func startRecurringJob() {
	go func() {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			n, err := processRecurring(ctx)
			cancel()
			if err != nil {
				log.Printf("recurring: %v", err)
			} else if n > 0 {
				log.Printf("recurring: created %d entries", n)
			}
			time.Sleep(time.Hour)
		}
	}()
}

func renderRecurring(w http.ResponseWriter, userID int, username string, form recurringForm, errMsg string, deletedID string) {
	if _, err := strconv.Atoi(deletedID); err != nil {
		deletedID = ""
	}
	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}
	rows, err := db.Query(
		`SELECT id, description, amount, category, kind, frequency, anchor_day, next_date, active
		 FROM recurring_entries WHERE user_id = $1 AND deleted_at IS NULL
		 ORDER BY active DESC, next_date, id`, userID)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()
	var rules []RecurringRule
	for rows.Next() {
		var r RecurringRule
		var anchor int
		var next time.Time
		if err := rows.Scan(&r.ID, &r.Description, &r.Amount, &r.Category, &r.Kind,
			&r.Frequency, &anchor, &next, &r.Active); err != nil {
			serverError(w, err)
			return
		}
		r.NextDate = next.Format("2 Jan 2006")
		r.Summary = recurringSummary(r.Frequency, anchor, next)
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		serverError(w, err)
		return
	}

	today := time.Now().Format("2006-01-02")
	if form.Kind == "" {
		form.Kind = kindExpense
	}
	if form.Frequency == "" {
		form.Frequency = "monthly"
	}
	if form.Start == "" {
		form.Start = today
	}
	data := RecurringPageData{
		PageChrome: PageChrome{Username: username, CurrencySymbol: currencySymbol(settings.Currency)},
		Rules:      rules,
		Categories: settings.Categories,
		Today:      today,
		Error:      errMsg,
		Form:       form,
		DeletedID:  deletedID,
	}
	initChrome(&data.PageChrome, userID)
	if err := tmpl.ExecuteTemplate(w, "recurring.html", data); err != nil && !isClientDisconnect(err) {
		log.Printf("failed to render recurring template: %v", err)
	}
}

func recurringPageHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)
	renderRecurring(w, userID, username, recurringForm{}, "", r.URL.Query().Get("deleted"))
}

func recurringCreateHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, username, _ := currentUser(r)

	form := recurringForm{
		Description: strings.TrimSpace(r.FormValue("description")),
		Amount:      strings.TrimSpace(r.FormValue("amount")),
		Category:    strings.TrimSpace(r.FormValue("category")),
		Kind:        normalizeKind(r.FormValue("kind")),
		Frequency:   strings.TrimSpace(r.FormValue("frequency")),
		Start:       strings.TrimSpace(r.FormValue("start")),
	}
	fail := func(msg string) { renderRecurring(w, userID, username, form, msg, "") }

	if form.Description == "" {
		fail("Give this entry a name, for example Rent or Salary.")
		return
	}
	if len([]rune(form.Description)) > 255 {
		fail("That name is too long. Keep it under 255 characters.")
		return
	}
	amount, err := strconv.ParseFloat(strings.ReplaceAll(form.Amount, ",", ""), 64)
	if err != nil || amount <= 0 {
		fail("Enter an amount greater than zero.")
		return
	}
	if amount >= 100000000 {
		fail("That amount is too large.")
		return
	}
	switch form.Frequency {
	case "daily", "weekly", "monthly":
	default:
		fail("Choose how often this repeats.")
		return
	}
	today := time.Now().Format("2006-01-02")
	if form.Start == "" {
		form.Start = today
	}
	start, err := time.Parse("2006-01-02", form.Start)
	if err != nil {
		fail("Choose a valid start date.")
		return
	}
	if form.Start < today {
		fail("The start date can't be in the past. Choose today or a later day.")
		return
	}
	category := []rune(form.Category)
	if len(category) > 50 {
		category = category[:50]
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM recurring_entries WHERE user_id = $1 AND deleted_at IS NULL`, userID).Scan(&count); err != nil {
		serverError(w, err)
		return
	}
	if count >= maxRecurringPerUser {
		fail(fmt.Sprintf("You can have up to %d repeating entries. Delete one to add another.", maxRecurringPerUser))
		return
	}

	if _, err := db.Exec(
		`INSERT INTO recurring_entries
		   (user_id, description, amount, category, kind, frequency, anchor_day, next_date)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::date)`,
		userID, form.Description, amount, string(category), form.Kind, form.Frequency,
		start.Day(), form.Start,
	); err != nil {
		serverError(w, err)
		return
	}

	// If it starts today, make the first entry now instead of at the next hourly run.
	if _, err := processRecurring(r.Context()); err != nil {
		log.Printf("recurring: immediate run: %v", err)
	}
	http.Redirect(w, r, "/recurring", http.StatusSeeOther)
}

func recurringToggleHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/recurring/toggle/"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	tx, err := db.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, err)
		return
	}
	defer tx.Rollback()

	var active bool
	var freq string
	var anchor int
	var next time.Time
	err = tx.QueryRowContext(r.Context(),
		`SELECT active, frequency, anchor_day, next_date
		 FROM recurring_entries WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL FOR UPDATE`, id, userID,
	).Scan(&active, &freq, &anchor, &next)
	if err == sql.ErrNoRows {
		http.Redirect(w, r, "/recurring", http.StatusSeeOther)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}

	newActive := !active
	nd := utcDate(next)
	if newActive {
		// Resuming must not back-fill the weeks it was paused for.
		today := utcDate(time.Now())
		for nd.Before(today) {
			nd = nextRecurringDate(freq, anchor, nd)
		}
	}
	if _, err := tx.ExecContext(r.Context(),
		`UPDATE recurring_entries SET active = $1, next_date = $2::date WHERE id = $3 AND user_id = $4`,
		newActive, nd.Format("2006-01-02"), id, userID,
	); err != nil {
		serverError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/recurring", http.StatusSeeOther)
}

func recurringDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/recurring/delete/"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	res, err := db.Exec(
		`UPDATE recurring_entries SET deleted_at = NOW()
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL`, id, userID)
	if err != nil {
		serverError(w, err)
		return
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		http.Redirect(w, r, "/recurring", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/recurring?deleted="+strconv.Itoa(id), http.StatusSeeOther)
}

func recurringRestoreHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)
	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/recurring/restore/"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	tx, err := db.BeginTx(r.Context(), nil)
	if err != nil {
		serverError(w, err)
		return
	}
	defer tx.Rollback()

	var freq string
	var anchor int
	var next time.Time
	err = tx.QueryRowContext(r.Context(),
		`SELECT frequency, anchor_day, next_date FROM recurring_entries
		 WHERE id = $1 AND user_id = $2 AND deleted_at IS NOT NULL FOR UPDATE`, id, userID,
	).Scan(&freq, &anchor, &next)
	if err == sql.ErrNoRows {
		http.Redirect(w, r, "/recurring", http.StatusSeeOther)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}

	// Skip the days it was deleted for: restoring must not back-fill them.
	nd := utcDate(next)
	today := utcDate(time.Now())
	for nd.Before(today) {
		nd = nextRecurringDate(freq, anchor, nd)
	}
	if _, err := tx.ExecContext(r.Context(),
		`UPDATE recurring_entries SET deleted_at = NULL, next_date = $1::date
		 WHERE id = $2 AND user_id = $3`, nd.Format("2006-01-02"), id, userID,
	); err != nil {
		serverError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/recurring", http.StatusSeeOther)
}
