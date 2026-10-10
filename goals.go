package main

// goals.go: the Goals page (GET /goals) and its write endpoints.
//
// Goals are to-do items scoped to a month or a year, with an optional
// amount. Two things make them different from a savings balance:
//
//   1. They are things to do, not money to hold. "Read 12 books" belongs
//      here and has no amount; "pay rent" belongs here and is a task, not
//      a transfer into savings.
//
//   2. They are for a specific period. A month goal belongs to October
//      2026, not to "someday". That is what makes them a plan.
//
// Every write is a form POST followed by a redirect, so the page works
// with JavaScript disabled.

import (
	"database/sql"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Goal struct {
	ID          int
	Title       string
	Note        string
	HasAmount   bool
	Amount      float64
	Scope       string // "month" | "year"
	Period      string // "2026-10" | "2026"
	Label       string // "October 2026" | "2026"
	Completed   bool
	CompletedAt string
	CreatedAt   string
}

// GoalGroup is one period's worth of active goals — a month or a year.
type GoalGroup struct {
	Period string
	Scope  string
	Label  string
	Goals  []Goal
}

type GoalsPageData struct {
	PageChrome
	ActiveGroups []GoalGroup
	Completed    []Goal
	HasAny       bool
	DefaultMonth string // "2026-10" — the create form's default
	CurrentMonth string
	CurrentYear  string
}

// goalPeriodLabel turns a scope+period pair into something a person reads.
func goalPeriodLabel(scope, period string) string {
	if scope == "year" {
		return period
	}
	if t, err := time.Parse("2006-01", period); err == nil {
		return t.Format("January 2006")
	}
	return period
}

func loadGoals(userID int) ([]Goal, error) {
	rows, err := db.Query(`
		SELECT id, title, note, target_amount, scope, period,
		       to_char(completed_at, 'YYYY-MM-DD HH24:MI:SS'),
		       to_char(created_at, 'YYYY-MM-DD')
		FROM goals
		WHERE user_id = $1
		ORDER BY period DESC, scope ASC, completed_at IS NOT NULL, created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var goals []Goal
	for rows.Next() {
		var g Goal
		var amount sql.NullFloat64
		var completedAt sql.NullString
		if err := rows.Scan(&g.ID, &g.Title, &g.Note, &amount, &g.Scope, &g.Period,
			&completedAt, &g.CreatedAt); err != nil {
			return nil, err
		}
		g.HasAmount = amount.Valid
		if amount.Valid {
			g.Amount = amount.Float64
		}
		g.Completed = completedAt.Valid
		g.CompletedAt = completedAt.String
		g.Label = goalPeriodLabel(g.Scope, g.Period)
		goals = append(goals, g)
	}
	return goals, rows.Err()
}

func goalsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)

	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}

	goals, err := loadGoals(userID)
	if err != nil {
		serverError(w, err)
		return
	}

	// Split active from completed, and group the active ones by period.
	// The query already orders by period DESC, so a single pass produces
	// groups in the right order without a second sort.
	var active, completed []Goal
	for _, g := range goals {
		if g.Completed {
			completed = append(completed, g)
		} else {
			active = append(active, g)
		}
	}

	var groups []GoalGroup
	lastKey := ""
	for _, g := range active {
		key := g.Scope + ":" + g.Period
		if key != lastKey {
			groups = append(groups, GoalGroup{
				Period: g.Period,
				Scope:  g.Scope,
				Label:  g.Label,
			})
			lastKey = key
		}
		groups[len(groups)-1].Goals = append(groups[len(groups)-1].Goals, g)
	}

	data := GoalsPageData{
		PageChrome: PageChrome{
			Username:       username,
			CurrencySymbol: currencySymbol(settings.Currency),
		},
		ActiveGroups: groups,
		Completed:    completed,
		HasAny:       len(goals) > 0,
		DefaultMonth: currentMonth(),
		CurrentMonth: currentMonth(),
		CurrentYear:  time.Now().Format("2006"),
	}

	if err := tmpl.ExecuteTemplate(w, "goals.html", data); err != nil {
		if !isClientDisconnect(err) {
			log.Printf("failed to render goals template: %v", err)
		}
	}
}

func goalsCreateHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Redirect(w, r, "/goals", http.StatusSeeOther)
		return
	}
	if len(title) > 200 {
		title = title[:200]
	}

	note := strings.TrimSpace(r.FormValue("note"))
	if len(note) > 500 {
		note = note[:500]
	}

	// A blank or unparseable amount means "no amount", which is stored as
	// NULL. Zero is not accepted either — it would be indistinguishable
	// from a goal whose amount has not been decided yet.
	var amount any
	if raw := strings.TrimSpace(r.FormValue("target_amount")); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			amount = v
		}
	}

	// The period. A month input always supplies "YYYY-MM"; the whole-year
	// checkbox changes the scope and reduces the period to its year part.
	// Unparseable values fall back to the current month rather than
	// failing the request.
	rawMonth := strings.TrimSpace(r.FormValue("period"))
	wholeYear := r.FormValue("whole_year") != ""

	scope := "month"
	period := currentMonth()

	if t, err := time.Parse("2006-01", rawMonth); err == nil {
		if wholeYear {
			scope = "year"
			period = t.Format("2006")
		} else {
			period = t.Format("2006-01")
		}
	} else if wholeYear {
		scope = "year"
		period = time.Now().Format("2006")
	}

	if _, err := db.Exec(`
		INSERT INTO goals (user_id, title, note, target_amount, scope, period)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		userID, title, note, amount, scope, period,
	); err != nil {
		serverError(w, err)
		return
	}

	http.Redirect(w, r, "/goals", http.StatusSeeOther)
}

func goalsToggleHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)

	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/goals/toggle/"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	// One statement flips the state either way: completing an open goal
	// stamps the time, un-completing a done one clears it. Doing it in SQL
	// means two racing requests cannot leave the goal half-toggled.
	if _, err := db.Exec(`
		UPDATE goals
		SET completed_at = CASE WHEN completed_at IS NULL THEN NOW() ELSE NULL END
		WHERE id = $1 AND user_id = $2`,
		id, userID,
	); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/goals", http.StatusSeeOther)
}

func goalsDeleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)

	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/goals/delete/"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if _, err := db.Exec(
		"DELETE FROM goals WHERE id = $1 AND user_id = $2",
		id, userID,
	); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/goals", http.StatusSeeOther)
}
