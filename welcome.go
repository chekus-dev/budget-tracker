package main

// welcome.go: the first-run wizard at /welcome.
//
// Registering used to drop a new account on an empty home screen — "Spent this
// month ₦0.00", a list saying "Tap the + button" — with no budget set. The
// three things that make the app immediately useful (what currency you count
// in, what your monthly limit is, which categories you want) were several
// screens deep in Settings, which a new user has no reason to open yet. This
// is the four-step flow that asks.
//
// The step lives in the URL as ?step=N rather than in the session, so there is
// no second copy of the truth to drift out of sync with the page. Every write
// is a form POST followed by a redirect — the same rule as goals.go — so the
// whole flow completes with JavaScript disabled. The two buttons are submits
// carrying name="action", which is what lets "Skip setup" and "Next" mean
// different things without a line of script.
//
// Finishing or skipping stamps users.onboarded_at and the wizard never appears
// again. The gate that sends people here lives in indexHandler alone; see the
// comment there for why it is not middleware.

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

/*
The steps, in order. Adding one here adds it to the dot indicator, to the

	range welcomeStepFrom will accept, and to the copy table below — but not to
	the handler's switch, which is the part to remember.
*/
var welcomeStepLabels = []string{"Currency", "Budget", "Categories", "First entry"}

// welcomeStep is one dot in the indicator.
type welcomeStep struct {
	Number int
	Label  string
	State  string // "done" | "now" | "todo"
}

func welcomeSteps(current int) []welcomeStep {
	steps := make([]welcomeStep, 0, len(welcomeStepLabels))
	for i, label := range welcomeStepLabels {
		n := i + 1
		state := "todo"
		switch {
		case n < current:
			state = "done"
		case n == current:
			state = "now"
		}
		steps = append(steps, welcomeStep{Number: n, Label: label, State: state})
	}
	return steps
}

// welcomeCopy is the heading for one step.
func welcomeCopy(step int) (title, sub string) {
	switch step {
	case 2:
		return "What is your monthly limit?",
			"The home screen measures each month's spending against this. Leave it blank for no limit, or set one later in Settings."
	case 3:
		return "Pick your categories",
			"These are the chips you choose from when logging an entry. Give one a monthly limit in Settings and it gets its own bar on the home screen."
	case 4:
		return "Log your first entry",
			"Optional — skip it if you would rather start with a clean sheet."
	default:
		return "What do you count in?",
			"Amounts are labelled with this everywhere in the app. Past entries are not converted, and you can change it later in Settings."
	}
}

/*
welcomeStepFrom reads the current step out of the request.

	FormValue covers both cases in one call: on a GET it reads the ?step= query,
	and on a POST it reads the hidden field, the body taking precedence over any
	query string. Anything missing, unparseable or outside the range starts the
	flow again from the top — a stale or hand-edited URL lands somewhere real
	rather than on a blank page.
*/
func welcomeStepFrom(r *http.Request) int {
	n, err := strconv.Atoi(strings.TrimSpace(r.FormValue("step")))
	if err != nil || n < 1 || n > len(welcomeStepLabels) {
		return 1
	}
	return n
}

/*
userOnboarded reports whether this account has finished (or skipped) the

	wizard.

	A missing row answers true. That is a session pointing at a user that no
	longer exists, which requireAuth will drop on its next check; it is not a
	reason to start a welcome that could never be completed.
*/
func userOnboarded(userID int) (bool, error) {
	var done bool
	err := db.QueryRow(
		"SELECT onboarded_at IS NOT NULL FROM users WHERE id = $1", userID,
	).Scan(&done)
	if err == sql.ErrNoRows {
		return true, nil
	}
	return done, err
}

/*
markOnboarded closes the wizard for good. The extra IS NULL predicate makes

	this idempotent and keeps the first time it was finished, rather than
	restamping it on every redirect that happens to pass through here.
*/
func markOnboarded(userID int) error {
	_, err := db.Exec(
		"UPDATE users SET onboarded_at = CURRENT_TIMESTAMP WHERE id = $1 AND onboarded_at IS NULL",
		userID,
	)
	return err
}

// welcomeEntryForm is what the user has typed into step 4, echoed back so a
// rejected entry does not cost them the typing.
type welcomeEntryForm struct {
	Description string
	Amount      string
	Kind        string
	Category    string
	Date        string
}

type WelcomePageData struct {
	PageChrome
	Step  int
	Steps []welcomeStep
	Title string
	Sub   string
	Error string

	// The values each step's form renders from. CurrencyList is derived, the
	// way it is on the settings page; the rest come from the settings row.
	Currency          string
	CurrencyList      []CurrencyOption
	BudgetLimit       float64
	Categories        []string
	CategoryColorData string

	Form welcomeEntryForm
}

/*
welcomeEntry is what a step-4 submission amounts to: they typed nothing,

	they typed something usable, or they typed something that needs saying
	something about.
*/
type welcomeEntry int

const (
	welcomeEntrySkip welcomeEntry = iota
	welcomeEntrySave
	welcomeEntryRetry
)

/*
welcomeFirstEntry decides what step 4's two fields come to.

	The step is optional, so "both blank" has to mean "done, nothing to log"
	rather than a complaint — otherwise the only way past is to enter something,
	which is not what optional means. Every other case is a complaint, and each
	one is phrased with the way out in it, because the way out is not obvious
	when the thing you are looking at is a form you did not fill in.

	A non-positive amount is rejected here rather than by the insert: migration
	014 puts a CHECK constraint on expenses.amount, so letting one through would
	surface as a 500 instead of a sentence.
*/
func welcomeFirstEntry(description, rawAmount string) (welcomeEntry, float64, string) {
	description = strings.TrimSpace(description)
	rawAmount = strings.TrimSpace(rawAmount)

	if description == "" && rawAmount == "" {
		return welcomeEntrySkip, 0, ""
	}
	if description == "" {
		return welcomeEntryRetry, 0, "Give the entry a short description, or clear the amount to skip this step."
	}
	if rawAmount == "" {
		return welcomeEntryRetry, 0, "Enter an amount, or clear the description to skip this step."
	}
	amount, err := strconv.ParseFloat(rawAmount, 64)
	if err != nil {
		return welcomeEntryRetry, 0, "That amount is not a number."
	}
	if amount <= 0 {
		return welcomeEntryRetry, 0, "Enter an amount greater than zero, or clear the description to skip this step."
	}
	return welcomeEntrySave, amount, ""
}

func welcomeHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)

	// Someone who has already been through this has no business here, and the
	// wizard offers no way back out once it is done.
	done, err := userOnboarded(userID)
	if err != nil {
		serverError(w, err)
		return
	}
	if done {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}

	categoryColorData, err := json.Marshal(struct {
		Words    map[string]string `json:"words"`
		Colors   map[string]string `json:"colors"`
		Fallback []string          `json:"fallback"`
		Neutral  string            `json:"neutral"`
	}{
		Words:    categoryWordIcons,
		Colors:   categoryIconColors,
		Fallback: categoryFallbackColors,
		Neutral:  categoryNeutralColor,
	})
	if err != nil {
		serverError(w, err)
		return
	}

	render := func(step int, form welcomeEntryForm, errMsg string) {
		title, sub := welcomeCopy(step)
		data := WelcomePageData{
			PageChrome: PageChrome{
				Username:       username,
				CurrencySymbol: currencySymbol(settings.Currency),
				Version:        appVersion,
			},
			Step:              step,
			Steps:             welcomeSteps(step),
			Title:             title,
			Sub:               sub,
			Error:             errMsg,
			Currency:          settings.Currency,
			CurrencyList:      currencyOptions,
			BudgetLimit:       settings.BudgetLimit,
			Categories:        settings.Categories,
			CategoryColorData: string(categoryColorData),
			Form:              form,
		}
		if err := tmpl.ExecuteTemplate(w, "welcome.html", data); err != nil {
			log.Printf("failed to render welcome template: %v", err)
		}
	}

	if r.Method != http.MethodPost {
		render(welcomeStepFrom(r), welcomeEntryForm{Kind: kindExpense}, "")
		return
	}

	r.ParseForm()
	step := welcomeStepFrom(r)

	if r.FormValue("action") == "skip" {
		if err := markOnboarded(userID); err != nil {
			serverError(w, err)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	/* Every step saves by loading the settings row, changing the one field the
	   step owns, and writing the whole row back — because saveSettings writes
	   every column. Assigning a fresh SettingsData here would blank the
	   currency step 1 just set. */
	switch step {
	case 1:
		currency := strings.ToUpper(strings.TrimSpace(r.FormValue("currency")))
		if !knownCurrency(currency) {
			currency = settings.Currency
			if !knownCurrency(currency) {
				currency = "NGN"
			}
		}
		settings.Currency = currency

	case 2:
		budget, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("budget")), 64)
		if err != nil || budget < 0 {
			budget = 0
		}
		settings.BudgetLimit = budget

	case 3:
		// The submitted list is authoritative, including when the user removes
		// every category. saveSettings persists an empty slice as an empty value.
		settings.Categories = normalizeCategories(r.PostForm["category"])

	case 4:
		form := welcomeEntryForm{
			Description: strings.TrimSpace(r.FormValue("description")),
			Amount:      strings.TrimSpace(r.FormValue("amount")),
			Kind:        normalizeKind(r.FormValue("kind")),
			Category:    strings.TrimSpace(r.FormValue("category")),
			Date:        strings.TrimSpace(r.FormValue("date")),
		}
		outcome, amount, message := welcomeFirstEntry(form.Description, form.Amount)
		if outcome == welcomeEntryRetry {
			render(4, form, message)
			return
		}
		if outcome == welcomeEntrySave {
			createdAt := time.Now()
			if d, ok := parseExpenseDate(form.Date); ok {
				createdAt = d
			}
			if _, err := db.Exec(
				"INSERT INTO expenses (user_id, description, amount, category, kind, created_at) VALUES ($1, $2, $3, $4, $5, $6)",
				userID, form.Description, amount, form.Category, form.Kind, createdAt,
			); err != nil {
				serverError(w, err)
				return
			}
		}
		if err := markOnboarded(userID); err != nil {
			serverError(w, err)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if err := saveSettings(userID, settings); err != nil {
		serverError(w, err)
		return
	}

	/* Clamped rather than guarded: step can never exceed the last label coming
	   out of welcomeStepFrom, so the only way to reach the ceiling is the last
	   step itself, and it returns above. */
	next := step + 1
	if next > len(welcomeStepLabels) {
		next = len(welcomeStepLabels)
	}
	http.Redirect(w, r, "/welcome?step="+strconv.Itoa(next), http.StatusSeeOther)
}
