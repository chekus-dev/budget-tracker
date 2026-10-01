package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math"
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
	Category    string
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
	Categories   []string
	Username     string
}

type ReportData struct {
	Count          int
	Total          float64
	Average        float64
	Largest        float64
	BudgetLimit    float64
	ChartExists    bool
	CategoryTotals []CategoryTotal
	Username       string
}

type CategoryTotal struct {
	Category string
	Total    float64
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

type ChangePasswordPageData struct {
	Error    string
	Success  string
	Username string
}

type ForgotPasswordPageData struct {
	Error   string
	Success string
}

type ResetPasswordPageData struct {
	Error string
	Valid bool
	Token string
}

var db *sql.DB
var tmpl *template.Template
var store *sessions.CookieStore

const sessionName = "budget-tracker-session"
const sessionMaxAge = 86400 * 365 // 1 year, refreshed on each request (sliding expiration)

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
		dbPort := strings.TrimSpace(os.Getenv("DB_PORT"))
		if dbPort == "" {
			log.Println("DB_PORT not set; defaulting to 3306")
			dbPort = "3306"
		}
		dsn := fmt.Sprintf(
			"%s:%s@tcp(%s:%s)/%s",
			os.Getenv("DB_USER"),
			os.Getenv("DB_PASSWORD"),
			os.Getenv("DB_HOST"),
			dbPort,
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
		"templates/change-password.html",
		"templates/forgot-password.html",
		"templates/reset-password.html",
		"templates/export.html",
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
	http.HandleFunc("/account/change-password", requireAuth(changePasswordHandler))
	http.HandleFunc("/clear", requireAuth(clearHandler))
	http.HandleFunc("/archive", requireAuth(archiveHandler))
	http.HandleFunc("/export", requireAuth(exportPageHandler))
	http.HandleFunc("/export/csv", requireAuth(exportCSVHandler))
	http.HandleFunc("/export/pdf", requireAuth(exportPDFHandler))
	http.HandleFunc("/api/report-data", requireAuth(reportDataHandler))
	http.HandleFunc("/api/expenses-raw", requireAuth(expensesRawHandler))

	// Public routes
	http.HandleFunc("/login", loginHandler)
	http.HandleFunc("/register", registerHandler)
	http.HandleFunc("/logout", logoutHandler)
	http.HandleFunc("/forgot-password", forgotPasswordHandler)
	http.HandleFunc("/reset-password", resetPasswordHandler)

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
		log.Fatal("SESSION_KEY is not set. Refusing to start with an insecure default — set SESSION_KEY to a long random string (e.g. `openssl rand -base64 32`) before running the server.")
	}
	if len(key) < 32 {
		log.Fatal("SESSION_KEY is too short (must be at least 32 characters). Generate one with `openssl rand -base64 32`.")
	}
	store = sessions.NewCookieStore([]byte(key))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   sessionMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func getSession(r *http.Request) *sessions.Session {
	session, _ := store.Get(r, sessionName)
	return session
}

func currentUser(r *http.Request) (id int, username string, ok bool) {
	session := getSession(r)
	idVal, ok1 := session.Values["user_id"].(int)
	nameVal, ok2 := session.Values["username"].(string)
	if !ok1 || !ok2 {
		return 0, "", false
	}
	return idVal, nameVal, true
}

func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, ok := currentUser(r); !ok {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		session := getSession(r)
		session.Options.MaxAge = sessionMaxAge
		if err := session.Save(r, w); err != nil {
			log.Printf("failed to refresh session: %v", err)
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
		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
		password := r.FormValue("password")
		confirm := r.FormValue("confirm_password")

		if username == "" || len(password) < 6 {
			tmpl.ExecuteTemplate(w, "register.html", AuthPageData{Error: "Username is required and password must be at least 6 characters."})
			return
		}
		if email == "" || !strings.Contains(email, "@") {
			tmpl.ExecuteTemplate(w, "register.html", AuthPageData{Error: "A valid email is required for password recovery."})
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

		res, err := db.Exec("INSERT INTO users (username, email, password_hash) VALUES (?, ?, ?)", username, email, string(hash))
		if err != nil {
			tmpl.ExecuteTemplate(w, "register.html", AuthPageData{Error: "That username or email is already taken."})
			return
		}
		userID64, _ := res.LastInsertId()
		userID := int(userID64)

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

func changePasswordHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)

	if r.Method == http.MethodPost {
		current := r.FormValue("current_password")
		newPassword := r.FormValue("new_password")
		confirm := r.FormValue("confirm_password")

		var passwordHash string
		if err := db.QueryRow("SELECT password_hash FROM users WHERE id = ?", userID).Scan(&passwordHash); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(current)); err != nil {
			tmpl.ExecuteTemplate(w, "change-password.html", ChangePasswordPageData{Error: "Current password is incorrect.", Username: username})
			return
		}
		if len(newPassword) < 6 {
			tmpl.ExecuteTemplate(w, "change-password.html", ChangePasswordPageData{Error: "New password must be at least 6 characters.", Username: username})
			return
		}
		if newPassword != confirm {
			tmpl.ExecuteTemplate(w, "change-password.html", ChangePasswordPageData{Error: "New passwords do not match.", Username: username})
			return
		}

		newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := db.Exec("UPDATE users SET password_hash = ? WHERE id = ?", string(newHash), userID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		tmpl.ExecuteTemplate(w, "change-password.html", ChangePasswordPageData{Success: "Password updated.", Username: username})
		return
	}
	tmpl.ExecuteTemplate(w, "change-password.html", ChangePasswordPageData{Username: username})
}

// ---------- Password reset ----------

const resetTokenTTL = 15 * time.Minute

func generateResetToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// sendResetEmail sends the reset link via the Resend API.
// Requires RESEND_API_KEY and RESEND_FROM_EMAIL to be set in the environment.
// If they're not set, it logs the link instead of sending (useful for local dev).
func sendResetEmail(toEmail, resetLink string) error {
	apiKey := os.Getenv("RESEND_API_KEY")
	fromEmail := os.Getenv("RESEND_FROM_EMAIL")

	if apiKey == "" || fromEmail == "" {
		log.Printf("RESEND_API_KEY/RESEND_FROM_EMAIL not set — reset link for %s: %s", toEmail, resetLink)
		return nil
	}

	body := map[string]interface{}{
		"from":    fromEmail,
		"to":      []string{toEmail},
		"subject": "Reset your Budget Tracker password",
		"html": fmt.Sprintf(`<p>Someone requested a password reset for this account.</p>
<p><a href="%s">Click here to reset your password</a>. This link expires in 15 minutes.</p>
<p>If you didn't request this, you can safely ignore this email.</p>`, template.HTMLEscapeString(resetLink)),
	}
	payload, err := json.Marshal(body)
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

func forgotPasswordHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}

	if r.Method == http.MethodPost {
		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))

		// Always show the same success message whether or not the email exists,
		// so this endpoint can't be used to discover which emails are registered.
		successMsg := "If an account exists with that email, a reset link has been sent."

		if email == "" {
			tmpl.ExecuteTemplate(w, "forgot-password.html", ForgotPasswordPageData{Success: successMsg})
			return
		}

		var userID int
		err := db.QueryRow("SELECT id FROM users WHERE email = ?", email).Scan(&userID)
		if err == sql.ErrNoRows {
			tmpl.ExecuteTemplate(w, "forgot-password.html", ForgotPasswordPageData{Success: successMsg})
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		token, err := generateResetToken()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		expires := time.Now().Add(resetTokenTTL)

		if _, err := db.Exec(
			"UPDATE users SET reset_token = ?, reset_token_expires = ? WHERE id = ?",
			token, expires, userID,
		); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		scheme := "https"
		if r.TLS == nil {
			scheme = "http"
		}
		resetLink := fmt.Sprintf("%s://%s/reset-password?token=%s", scheme, r.Host, token)

		if err := sendResetEmail(email, resetLink); err != nil {
			log.Printf("failed to send reset email to %s: %v", email, err)
			// Don't leak the failure to the client — same generic message either way.
		}

		tmpl.ExecuteTemplate(w, "forgot-password.html", ForgotPasswordPageData{Success: successMsg})
		return
	}

	tmpl.ExecuteTemplate(w, "forgot-password.html", ForgotPasswordPageData{})
}

func resetPasswordHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}

	if r.Method == http.MethodPost {
		token := r.FormValue("token")
		newPassword := r.FormValue("new_password")
		confirm := r.FormValue("confirm_password")

		var userID int
		var expires time.Time
		err := db.QueryRow(
			"SELECT id, reset_token_expires FROM users WHERE reset_token = ?",
			token,
		).Scan(&userID, &expires)
		if err == sql.ErrNoRows || (err == nil && time.Now().After(expires)) {
			tmpl.ExecuteTemplate(w, "reset-password.html", ResetPasswordPageData{
				Error: "This reset link is invalid or has expired.", Valid: false,
			})
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if len(newPassword) < 6 {
			tmpl.ExecuteTemplate(w, "reset-password.html", ResetPasswordPageData{
				Error: "Password must be at least 6 characters.", Valid: true, Token: token,
			})
			return
		}
		if newPassword != confirm {
			tmpl.ExecuteTemplate(w, "reset-password.html", ResetPasswordPageData{
				Error: "Passwords do not match.", Valid: true, Token: token,
			})
			return
		}

		newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := db.Exec(
			"UPDATE users SET password_hash = ?, reset_token = NULL, reset_token_expires = NULL WHERE id = ?",
			string(newHash), userID,
		); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	// GET: validate the token before showing the form
	token := r.URL.Query().Get("token")
	if token == "" {
		tmpl.ExecuteTemplate(w, "reset-password.html", ResetPasswordPageData{Valid: false})
		return
	}

	var expires time.Time
	err := db.QueryRow("SELECT reset_token_expires FROM users WHERE reset_token = ?", token).Scan(&expires)
	if err == sql.ErrNoRows || (err == nil && time.Now().After(expires)) {
		tmpl.ExecuteTemplate(w, "reset-password.html", ResetPasswordPageData{
			Error: "This reset link is invalid or has expired.", Valid: false,
		})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tmpl.ExecuteTemplate(w, "reset-password.html", ResetPasswordPageData{Valid: true, Token: token})
}

// ---------- Settings ----------

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
		`SELECT id, description, amount, category, created_at FROM expenses WHERE user_id = ? AND DATE_FORMAT(created_at,'%Y-%m') = ? ORDER BY created_at DESC`,
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
		if err := rows.Scan(&e.ID, &e.Description, &e.Amount, &e.Category, &e.CreatedAt); err != nil {
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
		BudgetLimit: settings.BudgetLimit, Categories: settings.Categories,
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

	templateData := map[string]interface{}{
		"Total":        data.Total,
		"Expenses":     data.Expenses,
		"Month":        data.Month,
		"BudgetLimit":  data.BudgetLimit,
		"PrevMonth":    data.PrevMonth,
		"NextMonth":    data.NextMonth,
		"CurrentMonth": data.CurrentMonth,
		"Categories":   data.Categories,
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
	if settings.BudgetLimit > 0 {
		data, _ := loadExpensesByMonth(userID, currentMonth())
		if data.Total >= settings.BudgetLimit {
			http.Error(w, "budget limit reached", http.StatusForbidden)
			return
		}
	}

	_, err = db.Exec(
		"INSERT INTO expenses (user_id, description, amount, category) VALUES (?, ?, ?, ?)",
		userID, r.FormValue("description"), r.FormValue("amount"), r.FormValue("category"),
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

	rows, err := db.Query("SELECT amount, category FROM expenses WHERE user_id = ?", userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var amounts []float64
	categoryTotals := map[string]float64{}
	for rows.Next() {
		var a float64
		var cat string
		if err := rows.Scan(&a, &cat); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		amounts = append(amounts, a)
		if cat == "" {
			cat = "Uncategorized"
		}
		categoryTotals[cat] += a
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

	var breakdown []CategoryTotal
	for cat, t := range categoryTotals {
		breakdown = append(breakdown, CategoryTotal{Category: cat, Total: t})
	}

	_, err = os.Stat("reports/monthly_spending.png")
	if err := tmpl.ExecuteTemplate(w, "report.html", ReportData{
		Count: len(amounts), Total: total, Average: average, Largest: largest,
		BudgetLimit: settings.BudgetLimit, ChartExists: total > 0,
		CategoryTotals: breakdown, Username: username,
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

	_, err := db.Exec(
		"DELETE FROM expenses WHERE user_id = ? AND DATE_FORMAT(created_at, '%Y-%m') = ?",
		userID, month,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

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
		`SELECT amount, category, created_at FROM expenses WHERE user_id = ? ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type RawExpense struct {
		Amount    float64 `json:"amount"`
		Category  string  `json:"category"`
		CreatedAt string  `json:"created_at"`
	}
	var data []RawExpense
	for rows.Next() {
		var e RawExpense
		if err := rows.Scan(&e.Amount, &e.Category, &e.CreatedAt); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data = append(data, e)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// ---------- Export ----------

func exportPageHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	_, username, _ := currentUser(r)
	month := r.URL.Query().Get("month")
	if month == "" {
		month = currentMonth()
	}
	if err := tmpl.ExecuteTemplate(w, "export.html", map[string]interface{}{
		"Username":     username,
		"Month":        month,
		"Label":        monthLabel(month),
		"PrevMonth":    addMonth(month, -1),
		"NextMonth":    addMonth(month, 1),
		"CurrentMonth": currentMonth(),
	}); err != nil {
		log.Printf("failed to render export template: %v", err)
	}
}

func exportCSVHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, _, _ := currentUser(r)
	month := r.URL.Query().Get("month")
	if month == "" {
		month = currentMonth()
	}

	rows, err := db.Query(
		`SELECT description, amount, category, created_at FROM expenses
		 WHERE user_id = ? AND DATE_FORMAT(created_at,'%Y-%m') = ?
		 ORDER BY created_at ASC`,
		userID, month,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	filename := fmt.Sprintf("expenses_%s.csv", month)
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	writer := csv.NewWriter(w)
	defer writer.Flush()

	writer.Write([]string{"Description", "Amount (NGN)", "Category", "Date"})

	var grandTotal float64
	count := 0
	for rows.Next() {
		var desc, category, createdAt string
		var amount float64
		if err := rows.Scan(&desc, &amount, &category, &createdAt); err != nil {
			continue
		}
		if category == "" {
			category = "Uncategorized"
		}
		writer.Write([]string{desc, fmt.Sprintf("%.2f", amount), category, createdAt})
		grandTotal += amount
		count++
	}

	writer.Write([]string{})
	writer.Write([]string{"Total", fmt.Sprintf("%.2f", grandTotal), "", ""})
	writer.Write([]string{"Count", fmt.Sprintf("%d", count), "", ""})
}

func exportPDFHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)
	month := r.URL.Query().Get("month")
	if month == "" {
		month = currentMonth()
	}

	rows, err := db.Query(
		`SELECT description, amount, category, created_at FROM expenses
		 WHERE user_id = ? AND DATE_FORMAT(created_at,'%Y-%m') = ?
		 ORDER BY created_at ASC`,
		userID, month,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type expRow struct {
		Description string
		Amount      float64
		Category    string
		CreatedAt   string
	}
	var expenses []expRow
	var grandTotal float64
	for rows.Next() {
		var e expRow
		if err := rows.Scan(&e.Description, &e.Amount, &e.Category, &e.CreatedAt); err != nil {
			continue
		}
		if e.Category == "" {
			e.Category = "Uncategorized"
		}
		expenses = append(expenses, e)
		grandTotal += e.Amount
	}

	catMap := map[string]float64{}
	for _, e := range expenses {
		catMap[e.Category] += e.Amount
	}
	type catRow struct {
		Category string
		Total    float64
		Pct      float64
	}
	var cats []catRow
	for cat, total := range catMap {
		pct := 0.0
		if grandTotal > 0 {
			pct = math.Round((total/grandTotal)*1000) / 10
		}
		cats = append(cats, catRow{cat, total, pct})
	}

	label := monthLabel(month)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Expenses %s</title>
<style>
  @import url('https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap');
  *{box-sizing:border-box;margin:0;padding:0}
  body{font-family:Inter,sans-serif;color:#1a1a1a;background:white;padding:32px;font-size:13px}
  .header{border-bottom:2px solid #185fa5;padding-bottom:16px;margin-bottom:24px;display:flex;justify-content:space-between;align-items:flex-end}
  .header h1{font-size:22px;font-weight:700;color:#185fa5}
  .header .meta{text-align:right;color:#666;font-size:12px;line-height:1.6}
  .summary{display:flex;gap:16px;margin-bottom:24px}
  .stat{flex:1;background:#f0f6ff;border-radius:8px;padding:12px 16px}
  .stat-label{font-size:11px;color:#666;margin-bottom:4px;text-transform:uppercase;letter-spacing:0.05em}
  .stat-value{font-size:18px;font-weight:700;color:#185fa5}
  table{width:100%%;border-collapse:collapse;margin-bottom:24px}
  th{background:#185fa5;color:white;padding:8px 12px;text-align:left;font-size:12px;font-weight:600}
  td{padding:8px 12px;border-bottom:1px solid #e0e0e0}
  tr:nth-child(even) td{background:#f8f9fa}
  td.amount{font-weight:600;color:#185fa5;text-align:right}
  th.amount{text-align:right}
  .tfoot-row td{font-weight:700;background:#f0f6ff}
  .breakdown h2{font-size:14px;font-weight:600;margin-bottom:12px;color:#333}
  .cat-row{display:flex;align-items:center;gap:8px;margin-bottom:8px}
  .cat-name{width:120px;font-size:12px}
  .cat-bar-wrap{flex:1;background:#e0e0e0;border-radius:4px;height:8px}
  .cat-bar{background:#185fa5;height:8px;border-radius:4px}
  .cat-total{width:90px;text-align:right;font-size:12px;font-weight:600}
  .cat-pct{width:40px;text-align:right;font-size:11px;color:#666}
  .footer{margin-top:32px;padding-top:12px;border-top:1px solid #e0e0e0;font-size:11px;color:#999;text-align:center}
  .print-btn{margin-bottom:24px;display:inline-flex;align-items:center;gap:8px;background:#185fa5;color:white;border:none;border-radius:8px;padding:10px 20px;font-size:13px;font-family:Inter,sans-serif;font-weight:500;cursor:pointer}
  .print-btn:hover{background:#0c447c}
  @media print{.no-print{display:none}body{padding:0}}
</style>
</head>
<body>
<button class="print-btn no-print" onclick="window.print()">🖨️ Save as PDF / Print</button>
<div class="header">
  <div>
    <h1>Expense Report</h1>
    <div style="color:#666;font-size:13px;margin-top:4px;">%s</div>
  </div>
  <div class="meta">
    <div>Prepared for: <strong>%s</strong></div>
    <div>Generated: %s</div>
    <div>Budget Tracker</div>
  </div>
</div>
<div class="summary">
  <div class="stat"><div class="stat-label">Total Spent</div><div class="stat-value">&#8358;%.2f</div></div>
  <div class="stat"><div class="stat-label">Transactions</div><div class="stat-value">%d</div></div>
  <div class="stat"><div class="stat-label">Categories</div><div class="stat-value">%d</div></div>
</div>
<table>
<thead><tr><th>Description</th><th>Category</th><th>Date</th><th class="amount">Amount (&#8358;)</th></tr></thead>
<tbody>`,
		label, label, username,
		time.Now().Format("2 Jan 2006"),
		grandTotal, len(expenses), len(cats),
	)

	for _, e := range expenses {
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td class="amount">%.2f</td></tr>`,
			template.HTMLEscapeString(e.Description),
			template.HTMLEscapeString(e.Category),
			template.HTMLEscapeString(e.CreatedAt),
			e.Amount,
		)
	}

	fmt.Fprintf(w, `</tbody>
<tfoot><tr class="tfoot-row"><td colspan="3">Total</td><td class="amount">%.2f</td></tr></tfoot>
</table>`, grandTotal)

	if len(cats) > 0 {
		fmt.Fprintf(w, `<div class="breakdown"><h2>Category Breakdown</h2>`)
		for _, c := range cats {
			fmt.Fprintf(w, `<div class="cat-row">
  <div class="cat-name">%s</div>
  <div class="cat-bar-wrap"><div class="cat-bar" style="width:%.1f%%"></div></div>
  <div class="cat-total">&#8358;%.2f</div>
  <div class="cat-pct">%.1f%%</div>
</div>`,
				template.HTMLEscapeString(c.Category), c.Pct, c.Total, c.Pct,
			)
		}
		fmt.Fprintf(w, `</div>`)
	}

	fmt.Fprintf(w, `<div class="footer">Budget Tracker — Exported %s</div>
</body></html>`, time.Now().Format("2 Jan 2006 15:04"))
}