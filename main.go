package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/sessions"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"

	_ "github.com/go-sql-driver/mysql"
)

type Expense struct {
	ID          int
	Description string
	Amount      float64
	CreatedAt   string
}

type PageData struct {
	Expenses     []Expense
	Total        float64
	Month        string
	CurrentMonth string
	PrevMonth    string
	NextMonth    string
	BudgetLimit  float64
	Username     string
}

type ReportData struct {
	Count       int
	Total       float64
	Average     float64
	Largest     float64
	BudgetLimit float64
	ChartExists bool
	Username    string
}

type SettingsData struct {
	BudgetLimit float64
	Currency    string
	Categories  []string
	Theme       string // "system", "light", "dark"
	Username    string
}

type AuthPageData struct {
	Error string
}

var db *sql.DB
var tmpl *template.Template
var store *sessions.CookieStore

const sessionName = "budget-tracker-session"

func isPlaceholderValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	lower := strings.ToLower(value)
	return strings.Contains(lower, "your_") ||
		strings.Contains(lower, "your-") ||
		strings.Contains(lower, "your_database_host") ||
		strings.Contains(lower, "your_render_mysql_host") ||
		strings.Contains(lower, "your_mysql_user") ||
		strings.Contains(lower, "your_mysql_password") ||
		strings.Contains(lower, "example.com") ||
		strings.Contains(lower, "placeholder")
}

func dbReady() bool {
	return db != nil
}

func requireDB(w http.ResponseWriter) bool {
	if !dbReady() {
		http.Error(w, "database is not configured or unavailable. Set DB_HOST, DB_USER, DB_PASSWORD, and DB_NAME to real MySQL values.", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, relying on environment variables")
	}

	if isPlaceholderValue(os.Getenv("DB_HOST")) || isPlaceholderValue(os.Getenv("DB_USER")) || isPlaceholderValue(os.Getenv("DB_PASSWORD")) || isPlaceholderValue(os.Getenv("DB_NAME")) {
		log.Println("database environment values are still placeholders; starting app without DB connectivity so the server boots")
	} else {
		dsn := fmt.Sprintf(
			"%s:%s@tcp(%s:%s)/%s",
			os.Getenv("DB_USER"),
			os.Getenv("DB_PASSWORD"),
			os.Getenv("DB_HOST"),
			os.Getenv("DB_PORT"),
			os.Getenv("DB_NAME"),
		)

		var err error
		db, err = sql.Open("mysql", dsn)
		if err != nil {
			log.Printf("database configuration error: %v; starting app without a live database connection", err)
		} else {
			if err := db.Ping(); err != nil {
				log.Printf("database ping failed: %v; starting app without a live database connection", err)
				db.Close()
				db = nil
			} else {
				log.Println("database connection established")
			}
		}
	}

	if db != nil {
		defer db.Close()
	}

	initSessionStore()

	tmpl = template.Must(template.New("index.html").Funcs(template.FuncMap{
		"mulf": func(a, b float64) float64 { return a * b },
		"divf": func(a, b float64, _ ...float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"subtract": func(a, b float64) float64 { return a - b },
	}).ParseFiles(
		"templates/index.html",
		"templates/expenses_list.html",
		"templates/report.html",
		"templates/settings.html",
		"templates/saved-settings.html",
		"templates/archive.html",
		"templates/nav.html",
		"templates/login.html",
		"templates/register.html",
	))

	// Authenticated routes
	http.HandleFunc("/", requireAuth(indexHandler))
	http.HandleFunc("/add", requireAuth(addHandler))
	http.HandleFunc("/delete/", requireAuth(deleteHandler))
	http.HandleFunc("/report", requireAuth(reportHandler))
	http.HandleFunc("/settings", requireAuth(settingsHandler))
	http.HandleFunc("/settings/saved", requireAuth(savedSettingsHandler))
	http.HandleFunc("/settings/reset", requireAuth(resetSettingsHandler))
	http.HandleFunc("/settings/reset-budget", requireAuth(resetBudgetHandler))
	http.HandleFunc("/clear", requireAuth(clearHandler))
	http.HandleFunc("/archive", requireAuth(archiveHandler))
	http.HandleFunc("/api/report-data", requireAuth(reportDataHandler))
	http.HandleFunc("/api/expenses-raw", requireAuth(expensesRawHandler))

	// Public routes
	http.HandleFunc("/login", loginHandler)
	http.HandleFunc("/register", registerHandler)
	http.HandleFunc("/logout", logoutHandler)

	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.Handle("/reports/", http.StripPrefix("/reports/", http.FileServer(http.Dir("reports"))))

	go autoMonthlyReset()

	port := os.Getenv("PORT")
	if port == "" {
		port = "4000"
	}
	log.Println("server running at http://localhost:" + port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// ---------- Auth / sessions ----------

func initSessionStore() {
	key := os.Getenv("SESSION_KEY")
	if key == "" {
		log.Println("WARNING: SESSION_KEY not set in environment - using an insecure default. Set SESSION_KEY (a long random string) before deploying.")
		key = "dev-insecure-session-key-change-me"
	}
	store = sessions.NewCookieStore([]byte(key))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7, // 7 days
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func getSession(r *http.Request) *sessions.Session {
	session, _ := store.Get(r, sessionName)
	return session
}

// currentUser returns the logged-in user's id and username from the session.
func currentUser(r *http.Request) (id int, username string, ok bool) {
	session := getSession(r)
	idVal, ok1 := session.Values["user_id"].(int)
	nameVal, ok2 := session.Values["username"].(string)
	if !ok1 || !ok2 {
		return 0, "", false
	}
	return idVal, nameVal, true
}

// requireAuth wraps a handler so it redirects to /login when there's no session.
func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := currentUser(r); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func registerHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if _, _, ok := currentUser(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if r.Method == http.MethodPost {
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		confirm := r.FormValue("confirm_password")

		if username == "" || len(password) < 6 {
			tmpl.ExecuteTemplate(w, "register.html", AuthPageData{Error: "Username is required and password must be at least 6 characters."})
			return
		}
		if password != confirm {
			tmpl.ExecuteTemplate(w, "register.html", AuthPageData{Error: "Passwords do not match."})
			return
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		res, err := db.Exec("INSERT INTO users (username, password_hash) VALUES (?, ?)", username, string(hash))
		if err != nil {
			tmpl.ExecuteTemplate(w, "register.html", AuthPageData{Error: "That username is already taken."})
			return
		}
		userID64, _ := res.LastInsertId()
		userID := int(userID64)

		// Create a default settings row for the new user.
		if _, err := saveSettingsTx(userID, defaultSettings()); err != nil {
			log.Printf("failed to create default settings for user %d: %v", userID, err)
		}

		session := getSession(r)
		session.Values["user_id"] = userID
		session.Values["username"] = username
		if err := session.Save(r, w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	tmpl.ExecuteTemplate(w, "register.html", AuthPageData{})
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if _, _, ok := currentUser(r); ok {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if r.Method == http.MethodPost {
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")

		var id int
		var passwordHash string
		err := db.QueryRow("SELECT id, password_hash FROM users WHERE username = ?", username).Scan(&id, &passwordHash)
		if err != nil {
			tmpl.ExecuteTemplate(w, "login.html", AuthPageData{Error: "Invalid username or password."})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
			tmpl.ExecuteTemplate(w, "login.html", AuthPageData{Error: "Invalid username or password."})
			return
		}

		session := getSession(r)
		session.Values["user_id"] = id
		session.Values["username"] = username
		if err := session.Save(r, w); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	tmpl.ExecuteTemplate(w, "login.html", AuthPageData{})
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	session := getSession(r)
	session.Options.MaxAge = -1
	session.Save(r, w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// ---------- Settings (MySQL-backed, per user) ----------

func defaultSettings() SettingsData {
	return SettingsData{
		BudgetLimit: 0,
		Currency:    "NGN",
		Categories:  []string{"Food", "Transport", "Bills"},
		Theme:       "system",
	}
}

func loadSettings(userID int) (SettingsData, error) {
	var s SettingsData
	var categoriesStr string
	err := db.QueryRow(
		"SELECT budget_limit, currency, categories, theme FROM settings WHERE user_id = ?",
		userID,
	).Scan(&s.BudgetLimit, &s.Currency, &categoriesStr, &s.Theme)

	if err == sql.ErrNoRows {
		s = defaultSettings()
		if _, err2 := saveSettingsTx(userID, s); err2 != nil {
			return s, err2
		}
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if categoriesStr != "" {
		s.Categories = strings.Split(categoriesStr, ",")
	}
	return s, nil
}

func saveSettingsTx(userID int, s SettingsData) (sql.Result, error) {
	return db.Exec(`
		INSERT INTO settings (user_id, budget_limit, currency, categories, theme)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			budget_limit = VALUES(budget_limit),
			currency = VALUES(currency),
			categories = VALUES(categories),
			theme = VALUES(theme)
	`, userID, s.BudgetLimit, s.Currency, strings.Join(s.Categories, ","), s.Theme)
}

func saveSettings(userID int, s SettingsData) error {
	_, err := saveSettingsTx(userID, s)
	return err
}

// ---------- Helpers ----------

func currentMonth() string { return time.Now().Format("2006-01") }

func monthLabel(ym string) string {
	t, err := time.Parse("2006-01", ym)
	if err != nil {
		return ym
	}
	return t.Format("January 2006")
}

func addMonth(ym string, n int) string {
	t, err := time.Parse("2006-01", ym)
	if err != nil {
		return ym
	}
	return t.AddDate(0, n, 0).Format("2006-01")
}

func loadExpensesByMonth(userID int, month string) (PageData, error) {
	rows, err := db.Query(
		`SELECT id, description, amount, created_at FROM expenses WHERE user_id = ? AND DATE_FORMAT(created_at,'%Y-%m') = ? ORDER BY created_at DESC`,
		userID, month,
	)
	if err != nil {
		return PageData{}, err
	}
	defer rows.Close()
	var expenses []Expense
	var total float64
	for rows.Next() {
		var e Expense
		if err := rows.Scan(&e.ID, &e.Description, &e.Amount, &e.CreatedAt); err != nil {
			return PageData{}, err
		}
		expenses = append(expenses, e)
		total += e.Amount
	}

	settings, err := loadSettings(userID)
	if err != nil {
		return PageData{}, err
	}

	return PageData{
		Expenses: expenses, Total: total, Month: month,
		CurrentMonth: currentMonth(), PrevMonth: addMonth(month, -1), NextMonth: addMonth(month, 1),
		BudgetLimit: settings.BudgetLimit,
	}, nil
}

// ---------- Handlers ----------

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)
	month := r.URL.Query().Get("month")
	if month == "" {
		month = currentMonth()
	}
	data, err := loadExpensesByMonth(userID, month)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data.Username = username

	// Add derived budget values before passing data to the template.
	templateData := map[string]interface{}{
		"Total":        data.Total,
		"Expenses":     data.Expenses,
		"Month":        data.Month,
		"BudgetLimit":  data.BudgetLimit,
		"PrevMonth":    data.PrevMonth,
		"NextMonth":    data.NextMonth,
		"CurrentMonth": data.CurrentMonth,
		"Username":     data.Username,
	}

	if data.BudgetLimit > 0 {
		budgetPercent := (data.Total / data.BudgetLimit) * 100
		if budgetPercent > 100 {
			budgetPercent = 100
		}
		templateData["BudgetPercent"] = budgetPercent
		templateData["BudgetWarning"] = data.BudgetLimit * 0.8
	}

	if err := tmpl.ExecuteTemplate(w, "index.html", templateData); err != nil {
		log.Printf("failed to render index template: %v", err)
	}
}

func addHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)

	settings, err := loadSettings(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Block adding if budget limit is reached
	if settings.BudgetLimit > 0 {
		data, _ := loadExpensesByMonth(userID, currentMonth())
		if data.Total >= settings.BudgetLimit {
			http.Error(w, "budget limit reached", http.StatusForbidden)
			return
		}
	}

	_, err = db.Exec(
		"INSERT INTO expenses (user_id, description, amount) VALUES (?, ?, ?)",
		userID, r.FormValue("description"), r.FormValue("amount"),
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("X-Requested-With") == "fetch" {
		data, err := loadExpensesByMonth(userID, currentMonth())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmpl.ExecuteTemplate(w, "expenses_list.html", data)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func deleteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)

	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/delete/"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	// Scope the delete to the logged-in user so nobody can delete another user's expense by id.
	_, err = db.Exec("DELETE FROM expenses WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("X-Requested-With") == "fetch" {
		month := r.URL.Query().Get("month")
		if month == "" {
			month = currentMonth()
		}
		data, err := loadExpensesByMonth(userID, month)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmpl.ExecuteTemplate(w, "expenses_list.html", data)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func reportHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)
	settings, err := loadSettings(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	rows, err := db.Query("SELECT amount FROM expenses WHERE user_id = ?", userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var amounts []float64
	for rows.Next() {
		var a float64
		if err := rows.Scan(&a); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		amounts = append(amounts, a)
	}
	var total, largest float64
	for _, a := range amounts {
		total += a
		if a > largest {
			largest = a
		}
	}
	var average float64
	if len(amounts) > 0 {
		average = total / float64(len(amounts))
	}
	_, err = os.Stat("reports/monthly_spending.png")
	if err := tmpl.ExecuteTemplate(w, "report.html", ReportData{
		Count: len(amounts), Total: total, Average: average, Largest: largest,
		BudgetLimit: settings.BudgetLimit, ChartExists: total > 0, Username: username,
	}); err != nil {
		log.Printf("failed to render report template: %v", err)
	}
}

func settingsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)
	settings, err := loadSettings(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	settings.Username = username
	tmpl.ExecuteTemplate(w, "settings.html", settings)
}

func savedSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)

	if r.Method == http.MethodPost {
		budget, err := strconv.ParseFloat(r.FormValue("budget"), 64)
		if err != nil || budget < 0 {
			budget = 0
		}
		r.ParseForm()
		theme := r.FormValue("theme")
		if theme == "" {
			theme = "system"
		}
		newSettings := SettingsData{
			BudgetLimit: budget,
			Currency:    "NGN",
			Categories:  r.Form["category"],
			Theme:       theme,
		}
		if err := saveSettings(userID, newSettings); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/settings/saved", http.StatusSeeOther)
		return
	}

	settings, err := loadSettings(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	settings.Username = username
	tmpl.ExecuteTemplate(w, "saved-settings.html", settings)
}

func clearHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)

	month := r.FormValue("month")
	if month == "" {
		month = currentMonth()
	}

	// Delete this user's expenses for the month
	_, err := db.Exec(
		"DELETE FROM expenses WHERE user_id = ? AND DATE_FORMAT(created_at, '%Y-%m') = ?",
		userID, month,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Reset budget limit to 0, keep currency, reset categories to defaults
	existing, err := loadSettings(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	newSettings := SettingsData{
		BudgetLimit: 0,
		Currency:    existing.Currency,
		Categories:  []string{"Food", "Transport", "Bills"},
		Theme:       existing.Theme,
	}
	if err := saveSettings(userID, newSettings); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Delete chart and CSV export
	os.Remove("reports/monthly_spending.png")
	os.Remove("reports/expenses_export.csv")

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func archiveHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)

	month := r.URL.Query().Get("month")
	if month == "" {
		rows, err := db.Query(
			`SELECT DATE_FORMAT(created_at,'%Y-%m') as ym, COUNT(*), SUM(amount) FROM expenses WHERE user_id = ? GROUP BY ym ORDER BY ym DESC`,
			userID,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		type MonthSummary struct {
			Month, Label string
			Count        int
			Total        float64
		}
		var months []MonthSummary
		for rows.Next() {
			var ms MonthSummary
			if err := rows.Scan(&ms.Month, &ms.Count, &ms.Total); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			ms.Label = monthLabel(ms.Month)
			months = append(months, ms)
		}
		tmpl.ExecuteTemplate(w, "archive.html", map[string]interface{}{
			"Months": months, "Details": nil, "Username": username,
		})
		return
	}
	data, err := loadExpensesByMonth(userID, month)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpl.ExecuteTemplate(w, "archive.html", map[string]interface{}{
		"Months": nil, "Details": data, "Label": monthLabel(month), "Month": month, "Username": username,
	})
}

func autoMonthlyReset() {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month()+1, 1, 0, 5, 0, 0, now.Location())
		time.Sleep(time.Until(next))
		prevMonth := addMonth(currentMonth(), -1)
		// Clears the previous month's expenses for every user - same behavior as before multi-user support.
		if _, err := db.Exec("DELETE FROM expenses WHERE DATE_FORMAT(created_at,'%Y-%m') = ?", prevMonth); err != nil {
			log.Printf("auto-reset failed for %s: %v", prevMonth, err)
		} else {
			log.Printf("auto-reset: cleared expenses for %s", prevMonth)
		}
	}
}

func resetSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)

	existing, err := loadSettings(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Reset budget and categories, keep theme
	newSettings := SettingsData{
		BudgetLimit: 0,
		Currency:    "NGN",
		Categories:  []string{"Food", "Transport", "Bills"},
		Theme:       existing.Theme,
	}
	if err := saveSettings(userID, newSettings); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Delete chart and CSV so report page shows fresh state
	os.Remove("reports/monthly_spending.png")
	os.Remove("reports/expenses_export.csv")
	http.Redirect(w, r, "/settings/saved", http.StatusSeeOther)
}

func resetBudgetHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)

	existing, err := loadSettings(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	existing.BudgetLimit = 0
	if err := saveSettings(userID, existing); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/settings/saved", http.StatusSeeOther)
}

func reportDataHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, _, _ := currentUser(r)

	rows, err := db.Query(`
		SELECT DATE_FORMAT(created_at, '%Y-%m') as month, SUM(amount)
		FROM expenses
		WHERE user_id = ?
		GROUP BY month
		ORDER BY month ASC
	`, userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type MonthData struct {
		Month  string  `json:"month"`
		Amount float64 `json:"amount"`
	}
	var data []MonthData
	for rows.Next() {
		var d MonthData
		if err := rows.Scan(&d.Month, &d.Amount); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data = append(data, d)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func expensesRawHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, _, _ := currentUser(r)

	rows, err := db.Query(
		`SELECT amount, created_at FROM expenses WHERE user_id = ? ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type RawExpense struct {
		Amount    float64 `json:"amount"`
		CreatedAt string  `json:"created_at"`
	}
	var data []RawExpense
	for rows.Next() {
		var e RawExpense
		if err := rows.Scan(&e.Amount, &e.CreatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data = append(data, e)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}
