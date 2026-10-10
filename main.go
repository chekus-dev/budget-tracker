package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/sessions"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"golang.org/x/crypto/bcrypt"
)

// appVersion is shown in the footer of Settings and Terms, and returned by
// /healthz. Bump it before each deploy so a bug report can name the build.
const appVersion = "2.2.1"

const (
	kindExpense = "expense"
	kindIncome  = "income"

	// noCategorySentinel is the value the category filter carries to mean
	// "entries with an empty category". An out-of-band value rather than a
	// second boolean on ExpenseFilters, because the sentinel only has to
	// survive a URL round trip.
	noCategorySentinel = "__none__"

	// Contact details for the support page. Kept as constants here rather
	// than hard-coded in the template, so one edit changes them everywhere.
	//
	// IMPORTANT: replace all three with real details before going live.
	// supportWhatsApp must be the international format with no "+" and no
	// spaces — wa.me rejects both. supportPhone is the display value; the
	// tel: link is derived by stripping spaces.
	supportEmail    = "chekusjoseph@gmail.com"
	supportPhone    = "+234 9130 789 611"
	supportWhatsApp = "2349130789611"
)

// normalizeKind maps whatever arrived in a form to a storable kind, defaulting
// to expense. A missing or unrecognised value becoming an *expense* is the safe
// direction: it is the kind that counts against a budget, so a mistake shows up
// as money the user did not mean to spend, in a list they are looking at,
// rather than as income quietly inflating what they think they have.
func normalizeKind(raw string) string {
	if strings.EqualFold(strings.TrimSpace(raw), kindIncome) {
		return kindIncome
	}
	return kindExpense
}

type Expense struct {
	ID          int
	Description string
	Amount      float64
	Category    string
	Kind        string
	CreatedAt   string
	// Date is the same instant as CreatedAt, formatted "YYYY-MM-DD" — the only
	// shape an <input type="date"> will accept. Carried as its own column
	// rather than sliced out of CreatedAt, because templates cannot slice a
	// string and a helper func for it would be harder to read than the extra
	// to_char.
	Date string
}

// Income reports whether this entry is money in. Templates use it to choose the
// sign, the colour and the wording of a row.
func (e Expense) Income() bool { return e.Kind == kindIncome }

// SignedAmount is the amount as it counts towards a running total: positive for
// income, negative for spending. Kept next to Income so the sign convention
// lives in one place.
func (e Expense) SignedAmount() float64 {
	if e.Income() {
		return e.Amount
	}
	return -e.Amount
}

// PageChrome is the slice of a page's data that the shared chrome reads: the
// navigation knows who is signed in, what currency to label the amount field
// with, and — for the add/edit modal — which month it is showing and whether a
// search is in force to return to.
//
// Every full-page data type embeds it. The point is structural: html/template
// reports an absent *struct* field as an execution error and then stops
// rendering, so a field the chrome needs but one page's data lacks does not
// degrade that page, it truncates it mid-document under a 200. The currency
// label was added to nav.html exactly that way and silently cut the
// change-password page in half. Embedding means a new chrome field is present
// on every page the moment it is declared, and the only way to omit it is to
// leave it out of this struct — where it is obviously missing.
type PageChrome struct {
	Username       string
	CurrencySymbol string
	Month          string
	Filters        ExpenseFilters
	// Version is stamped onto every page's chrome so a template that shows
	// the footer can print it without the handler passing it separately.
	Version string
}

type PageData struct {
	Expenses []Expense
	// Spent and Received are the month's two totals, kept apart rather than
	// netted. A single "total" would have to mean one of them, and the screen
	// has to say both: "you spent ₦80,000" and "you received ₦250,000" answer
	// different questions, and their difference is a third figure again.
	Spent        float64
	Received     float64
	CurrentMonth string
	PrevMonth    string
	NextMonth    string
	BudgetLimit  float64
	Categories   []string
	// CategoryBudgets are the per-category limits that are actually in force
	// this month, each with what has been spent against it. Categories with no
	// limit are absent — a limit of zero means "no limit", not "spend nothing".
	CategoryBudgets []CategoryBudget
	PageChrome

	// Searching is read by expenses_list.html, which the archive renders with
	// a PageData directly. Inside a {{template}} invocation $ is rebound to
	// that template's own argument, so the partial cannot reach back to the
	// page around it — whatever it needs has to be on the value it is given.
	// The archive has no search, so it takes the zero value and the partial's
	// search-specific branches simply do not render.
	Searching bool
}

// Net is what the month did to the balance: money in minus money out. Negative
// means more went out than came in.
func (p PageData) Net() float64 { return p.Received - p.Spent }

// CategoryBudget is one category's limit for the month, with the spending
// measured against it.
type CategoryBudget struct {
	Category string
	Limit    float64
	Spent    float64
	// PercentOfLimit is capped at 100 so the bar cannot overflow its track;
	// PercentUsed is the true figure, which the text beside the bar reports —
	// that is how a category at 240% still reads as 240%.
	PercentOfLimit float64
	PercentUsed    float64
	Remaining      float64 // negative once over
	Over           bool
	Near           bool
}

type ReportData struct {
	Count           int
	Total           float64
	Average         float64
	Largest         float64
	BudgetLimit     float64
	PercentOfBudget float64
	ChartExists     bool
	CategoryTotals  []CategoryTotal
	// CategoryJSON is the same breakdown as CategoryTotals, pre-marshalled for
	// the chart. It is typed as template.JS because it is interpolated into a
	// <script> block; json.Marshal escapes <, > and & so the value cannot
	// terminate the element.
	CategoryJSON template.JS
	PageChrome
}

type CategoryTotal struct {
	Category string
	Total    float64
}

// CategoryOption is one entry in the category filter select. Value is what the
// form submits; Label is what the user reads. They differ in one place: the
// sentinel that means "the empty category" is submitted as "__none__" and
// shown as "Uncategorized".
type CategoryOption struct {
	Value string
	Label string
}

type SettingsData struct {
	BudgetLimit float64
	Currency    string // ISO code, as stored — binds the settings <select>
	Categories  []string
	PageChrome

	// CategoryLimits is the per-category monthly cap, keyed by category name.
	// It is filled in by the settings page rather than by loadSettings, because
	// it lives in its own table and only the form needs it; the home screen
	// reads the same table through categoryBudgetProgress, which needs the
	// spending as well.
	CategoryLimits map[string]float64

	// CurrencyList is the choices the settings form offers. Derived, not
	// stored: the database holds only the ISO code in Currency, and
	// CurrencySymbol on PageChrome is the glyph it renders as.
	CurrencyList []CurrencyOption
}

type AuthPageData struct {
	Error string

	// Echoed back after a failed signup attempt so a rejected form does not
	// make the user retype everything. Never populated with the password.
	Username string
	Email    string

	// Whether the terms box was ticked when the form was rejected.
	TermsChecked bool

	// reCAPTCHA. The site key is public by design (it is rendered into the
	// page); the secret key stays server-side and is never sent to the client.
	// When no site key is configured the widget is omitted entirely.
	RecaptchaSiteKey string
}

type ChangePasswordPageData struct {
	Error   string
	Success string
	PageChrome
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

// SupportPageData is what support.html receives. All four contact fields
// are derived from the three support* constants in main.go, so the template
// never has to build a URL or strip a space.
type SupportPageData struct {
	PageChrome
	Email        string
	Phone        string // display value, e.g. "+234 800 000 0000"
	PhoneLink    string // tel: compatible, no spaces
	WhatsApp     string // wa.me number, no +, no spaces
	WhatsAppText string // pre-filled message, URL-encoded
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
		strings.Contains(lower, "your_postgres_host") ||
		strings.Contains(lower, "your_supabase_host") ||
		strings.Contains(lower, "your_postgres_user") ||
		strings.Contains(lower, "your_postgres_password") ||
		strings.Contains(lower, "example.com") ||
		strings.Contains(lower, "placeholder")
}

func dbReady() bool {
	return db != nil
}

func buildDSN() (string, error) {
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); !isPlaceholderValue(dsn) {
		return dsn, nil
	}

	if isPlaceholderValue(os.Getenv("DB_HOST")) ||
		isPlaceholderValue(os.Getenv("DB_USER")) ||
		isPlaceholderValue(os.Getenv("DB_NAME")) {
		return "", fmt.Errorf("database environment values are still placeholders or missing (set DATABASE_URL, or DB_HOST/DB_USER/DB_PASSWORD/DB_NAME)")
	}

	if pw := os.Getenv("DB_PASSWORD"); pw != "" && isPlaceholderValue(pw) {
		return "", fmt.Errorf("DB_PASSWORD is still a placeholder value (set DATABASE_URL, or a real DB_PASSWORD)")
	}

	dbPort := strings.TrimSpace(os.Getenv("DB_PORT"))
	if dbPort == "" {
		dbPort = "5432"
	}

	sslmode := strings.TrimSpace(os.Getenv("DB_SSLMODE"))
	if sslmode == "" {
		host := strings.TrimSpace(os.Getenv("DB_HOST"))
		if host == "localhost" || host == "127.0.0.1" || host == "::1" {
			sslmode = "disable"
		} else {
			sslmode = "require"
		}
	}

	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD")),
		Host:     net.JoinHostPort(os.Getenv("DB_HOST"), dbPort),
		Path:     "/" + os.Getenv("DB_NAME"),
		RawQuery: url.Values{"sslmode": {sslmode}}.Encode(),
	}
	return u.String(), nil
}

func requireDB(w http.ResponseWriter) bool {
	if !dbReady() {
		http.Error(w, "database is not configured or unavailable. Set DATABASE_URL to your PostgreSQL connection string.", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func openPostgresDB(dsn string) (*sql.DB, error) {
	config, err := postgresConfig(dsn)
	if err != nil {
		return nil, err
	}
	return stdlib.OpenDB(*config), nil
}

func postgresConfig(dsn string) (*pgx.ConnConfig, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	config.DefaultQueryExecMode = pgx.QueryExecModeExec
	return config, nil
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
	dbStatus := "ok"
	if db == nil {
		dbStatus = "not configured"
	} else {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			dbStatus = "unavailable"
			log.Printf("healthz: database ping failed: %v", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":   "ok",
		"database": dbStatus,
		"version":  appVersion,
	})
}

// ---------------------------------------------------------------------------
// Response compression and static asset caching
// ---------------------------------------------------------------------------

// gzipMiddleware compresses text responses. HTML and CSS are highly
// compressible — typically a 70-85% reduction — while images, fonts and
// already-compressed assets are not, and are left alone.
//
// It sits outside securityHeaders so that error pages produced by any handler
// are also compressed. It skips compression when the client has not advertised
// gzip support, and deletes Content-Length on the way out because the length of
// the uncompressed body is not the length of the compressed one — leaving it
// would truncate the response.
func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		// Already-compressed formats gain nothing from gzip; skipping them
		// saves CPU on the server and bytes on the wire either way.
		switch {
		case strings.HasSuffix(r.URL.Path, ".png"),
			strings.HasSuffix(r.URL.Path, ".jpg"),
			strings.HasSuffix(r.URL.Path, ".jpeg"),
			strings.HasSuffix(r.URL.Path, ".ico"),
			strings.HasSuffix(r.URL.Path, ".woff"),
			strings.HasSuffix(r.URL.Path, ".woff2"),
			strings.HasSuffix(r.URL.Path, ".svg"):
			next.ServeHTTP(w, r)
			return
		}

		gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		defer gz.Close()

		w.Header().Set("Content-Encoding", "gzip")
		// Vary tells caches that this response differs by Accept-Encoding, so a
		// client that cannot read gzip is not handed a gzipped body.
		w.Header().Add("Vary", "Accept-Encoding")
		// Content-Length of the uncompressed body would be wrong for the
		// compressed one; removing it lets the client use chunked encoding.
		w.Header().Del("Content-Length")

		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, Writer: gz}, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	io.Writer
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) {
	if g.Header().Get("Content-Type") == "" {
		g.Header().Set("Content-Type", http.DetectContentType(b))
	}
	return g.Writer.Write(b)
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, relying on environment variables")
	}

	dsn, dsnErr := buildDSN()
	if dsnErr != nil {
		log.Printf("%v; starting app without DB connectivity so the server boots", dsnErr)
	} else {
		var err error
		db, err = openPostgresDB(dsn)
		if err != nil {
			log.Printf("database configuration error: %v; starting app without a live database connection", err)
		} else {
			if err := db.Ping(); err != nil {
				log.Printf("database ping failed: %v; starting app without a live database connection", err)
				db.Close()
				db = nil
			} else {
				log.Println("database connection established")
				if err := applyMigrations(context.Background(), db); err != nil {
					log.Printf("migrations failed: %v; some pages will error until the schema is fixed", err)
				}
			}
		}
	}

	if db != nil {
		defer db.Close()
	}

	initSessionStore()
	enableSharedRateLimits()
	startRateLimiterSweeper()

	tmpl = template.Must(template.New("index.html").Funcs(template.FuncMap{
		"tile": tileIndex,
		"mulf": func(a, b float64) float64 { return a * b },
		"divf": func(a, b float64, _ ...float64) float64 {
			if b == 0 {
				return 0
			}
			return a / b
		},
		"subtract": func(a, b float64) float64 { return a - b },
		"money": func(symbol string, v float64) string {
			if v < 0 {
				return "-" + symbol + commaGroupsPrec(-v, 2)
			}
			return symbol + commaGroupsPrec(v, 2)
		},
		"money0": func(symbol string, v float64) string {
			if v < 0 {
				return "-" + symbol + commaGroupsPrec(-v, 0)
			}
			return symbol + commaGroupsPrec(v, 0)
		},
	}).ParseFiles(
		"templates/trends.html",
		"templates/head.html",
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
		"templates/terms.html",
		"templates/export.html",
		"templates/billing.html",
		"templates/support.html",
		"templates/goals.html",
	))
	verifyTemplatesRender()

	mux := http.NewServeMux()
	mux.HandleFunc("/", requireAuth(indexHandler))

	mux.HandleFunc("/trends", requireAuth(trendsHandler))
	mux.HandleFunc("/add", requireAuth(addHandler))
	mux.HandleFunc("/edit/", requireAuth(editHandler))
	mux.HandleFunc("/delete/", requireAuth(deleteHandler))
	mux.HandleFunc("/restore/", requireAuth(restoreHandler))
	mux.HandleFunc("/report", requireAuth(reportHandler))
	mux.HandleFunc("/settings", requireAuth(settingsHandler))
	mux.HandleFunc("/settings/saved", requireAuth(savedSettingsHandler))
	mux.HandleFunc("/settings/reset", requireAuth(resetSettingsHandler))
	mux.HandleFunc("/settings/reset-budget", requireAuth(resetBudgetHandler))
	mux.HandleFunc("/account/change-password", requireAuth(changePasswordHandler))
	mux.HandleFunc("/clear", requireAuth(clearHandler))
	mux.HandleFunc("/archive", requireAuth(archiveHandler))
	mux.HandleFunc("/export", requireAuth(exportPageHandler))
	mux.HandleFunc("/export/csv", requireAuth(exportCSVHandler))
	mux.HandleFunc("/export/pdf", requireAuth(exportPDFHandler))
	mux.HandleFunc("/api/categories", requireAuth(categoriesHandler))
	mux.HandleFunc("/api/expenses-raw", requireAuth(expensesRawHandler))
	mux.HandleFunc("/support", requireAuth(supportHandler))
	mux.HandleFunc("/goals", requireAuth(goalsHandler))
	mux.HandleFunc("/goals/create", requireAuth(goalsCreateHandler))
	mux.HandleFunc("/goals/toggle/", requireAuth(goalsToggleHandler))
	mux.HandleFunc("/goals/delete/", requireAuth(goalsDeleteHandler))

	mux.HandleFunc("/healthz", healthzHandler)
	mux.HandleFunc("/login", loginHandler)
	mux.HandleFunc("/register", registerHandler)
	mux.HandleFunc("/terms", termsHandler)
	mux.HandleFunc("/logout", logoutHandler)
	mux.HandleFunc("/forgot-password", forgotPasswordHandler)
	mux.HandleFunc("/reset-password", resetPasswordHandler)

	setupBilling(mux)

	// Static assets. A request that carries a version query (?v=N) is a
	// content-addressed URL: bumping the N in head.html produces a different
	// URL, so the browser can cache the response forever without ever serving
	// a stale file. Requests without the query are cached for a day, which
	// covers anything not yet on the versioned pattern.
	staticFS := http.StripPrefix("/static/", http.FileServer(http.Dir("static")))
	mux.Handle("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		staticFS.ServeHTTP(w, r)
	}))

	go autoMonthlyReset()

	port := os.Getenv("PORT")
	if port == "" {
		port = "4000"
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           gzipMiddleware(securityHeaders(mux)),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Println("server running at http://localhost:" + port)
	log.Fatal(srv.ListenAndServe())
}

func isTruthy(v string) bool {
	v = strings.ToLower(strings.TrimSpace(v))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func externalBaseURL(r *http.Request) string {
	for _, env := range []string{"APP_BASE_URL", "RENDER_EXTERNAL_URL"} {
		if base := strings.TrimRight(strings.TrimSpace(os.Getenv(env)), "/"); base != "" {
			return base
		}
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	} else if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		if i := strings.Index(proto, ","); i >= 0 {
			proto = proto[:i]
		}
		if p := strings.TrimSpace(proto); p != "" {
			scheme = p
		}
	}
	return scheme + "://" + r.Host
}

func secureCookieMode() bool {
	if v := strings.TrimSpace(os.Getenv("SESSION_COOKIE_SECURE")); v != "" {
		return isTruthy(v)
	}
	if isTruthy(os.Getenv("RENDER")) {
		return true
	}
	if base, ok := os.LookupEnv("APP_BASE_URL"); ok {
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(base)), "https://")
	}
	return false
}

func recaptchaSiteKey() string {
	return strings.TrimSpace(os.Getenv("RECAPTCHA_SITE_KEY"))
}

func recaptchaSecretKey() string {
	return strings.TrimSpace(os.Getenv("RECAPTCHA_SECRET_KEY"))
}

func recaptchaRequired() bool {
	if recaptchaSecretKey() != "" {
		return true
	}
	return secureCookieMode()
}

type recaptchaResponse struct {
	Success bool     `json:"success"`
	Score   float64  `json:"score"`
	Action  string   `json:"action"`
	Errors  []string `json:"error-codes"`
}

var recaptchaClient = &http.Client{Timeout: 10 * time.Second}

func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		parts := strings.Split(fwd, ",")
		if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
			return last
		}
	}
	if ip, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return ip
	}
	return r.RemoteAddr
}

func verifyRecaptcha(r *http.Request, token string) error {
	secret := recaptchaSecretKey()
	if secret == "" {
		return fmt.Errorf("Sign-ups are temporarily unavailable. Please try again later.")
	}
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf(`Please tick the "I'm not a robot" box.`)
	}

	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	form.Set("remoteip", clientIP(r))

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://www.google.com/recaptcha/api/siteverify",
		strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("We couldn't reach the verification service. Please try again.")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := recaptchaClient.Do(req)
	if err != nil {
		log.Printf("recaptcha: siteverify request failed: %v", err)
		return fmt.Errorf("We couldn't reach the verification service. Please try again.")
	}
	defer resp.Body.Close()

	var out recaptchaResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		log.Printf("recaptcha: could not decode siteverify reply: %v", err)
		return fmt.Errorf("We couldn't verify that challenge. Please try again.")
	}

	if !out.Success {
		log.Printf("recaptcha: verification rejected: %v", out.Errors)
		return fmt.Errorf("That verification didn't pass. Please try the box again.")
	}
	return nil
}

const (
	loginWindow     = 15 * time.Minute
	maxFailsPerUser = 5
	maxFailsPerIP   = 20

	maxResetsPerEmail = 3
	maxResetsPerIP    = 10

	maxRateKeys = 10000
)

type failWindow struct {
	count   int
	resetAt time.Time
}

type rateLimiter struct {
	max int

	shared string

	now func() time.Time

	mu      sync.Mutex
	buckets map[string]*failWindow
}

func newRateLimiter(max int) *rateLimiter {
	return &rateLimiter{max: max, now: time.Now, buckets: make(map[string]*failWindow)}
}

func (l *rateLimiter) retryAfter(key string) time.Duration {
	if d, ok := l.sharedRetryAfter(key); ok {
		return d
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.buckets[key]
	if !ok || !now.Before(w.resetAt) || w.count < l.max {
		return 0
	}
	return w.resetAt.Sub(now)
}

func (l *rateLimiter) fail(key string) time.Duration {
	if d, ok := l.sharedFail(key); ok {
		return d
	}
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.buckets[key]
	if !ok || !now.Before(w.resetAt) {
		if len(l.buckets) >= maxRateKeys {
			l.evictLocked(now)
		}
		w = &failWindow{resetAt: now.Add(loginWindow)}
		l.buckets[key] = w
	}
	w.count++
	if w.count < l.max {
		return 0
	}
	return w.resetAt.Sub(now)
}

func (l *rateLimiter) reset(key string) {
	l.sharedReset(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.buckets, key)
}

func (l *rateLimiter) sweep() {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now)
}

func (l *rateLimiter) sweepLocked(now time.Time) {
	for k, w := range l.buckets {
		if !now.Before(w.resetAt) {
			delete(l.buckets, k)
		}
	}
}

func (l *rateLimiter) evictLocked(now time.Time) {
	l.sweepLocked(now)
	if len(l.buckets) < maxRateKeys {
		return
	}
	for k := range l.buckets {
		delete(l.buckets, k)
		if len(l.buckets) <= maxRateKeys/2 {
			return
		}
	}
}

var (
	loginLimiterUser = newRateLimiter(maxFailsPerUser)
	loginLimiterIP   = newRateLimiter(maxFailsPerIP)

	resetLimiterEmail = newRateLimiter(maxResetsPerEmail)
	resetLimiterIP    = newRateLimiter(maxResetsPerIP)
)

var allRateLimiters = []*rateLimiter{
	loginLimiterUser, loginLimiterIP,
	resetLimiterEmail, resetLimiterIP,
}

func userRateKey(username string) string {
	return "user:" + strings.ToLower(strings.TrimSpace(username))
}
func ipRateKey(r *http.Request) string { return "ip:" + clientIP(r) }

func waitingForLogin(r *http.Request, username string) time.Duration {
	if d := loginLimiterUser.retryAfter(userRateKey(username)); d > 0 {
		return d
	}
	return loginLimiterIP.retryAfter(ipRateKey(r))
}

func recordLoginFailure(r *http.Request, username string) time.Duration {
	a := loginLimiterUser.fail(userRateKey(username))
	b := loginLimiterIP.fail(ipRateKey(r))
	if b > a {
		return b
	}
	return a
}

func loginLockoutMessage(wait time.Duration) string {
	minutes := int(math.Ceil(wait.Minutes()))
	if minutes < 1 {
		minutes = 1
	}
	if minutes == 1 {
		return "Too many failed sign-in attempts. Please wait a minute and try again."
	}
	return fmt.Sprintf("Too many failed sign-in attempts. Please wait %d minutes and try again.", minutes)
}

func resetLockoutMessage(wait time.Duration) string {
	minutes := int(math.Ceil(wait.Minutes()))
	if minutes < 1 {
		minutes = 1
	}
	if minutes == 1 {
		return "Too many reset requests. Please wait a minute and try again."
	}
	return fmt.Sprintf("Too many reset requests. Please wait %d minutes and try again.", minutes)
}

func tooManyAttempts(w http.ResponseWriter, wait time.Duration) {
	writeRetryAfter(w, wait)
	tmpl.ExecuteTemplate(w, "login.html", AuthPageData{Error: loginLockoutMessage(wait)})
}

func tooManyResets(w http.ResponseWriter, wait time.Duration) {
	writeRetryAfter(w, wait)
	tmpl.ExecuteTemplate(w, "forgot-password.html", ForgotPasswordPageData{Error: resetLockoutMessage(wait)})
}

func writeRetryAfter(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	w.WriteHeader(http.StatusTooManyRequests)
}

func resetRateKey(email string) string {
	return "reset:" + strings.ToLower(strings.TrimSpace(email))
}
func resetIPRateKey(r *http.Request) string { return "reset-ip:" + clientIP(r) }

func waitingForReset(r *http.Request, email string) time.Duration {
	if d := resetLimiterEmail.retryAfter(resetRateKey(email)); d > 0 {
		return d
	}
	return resetLimiterIP.retryAfter(resetIPRateKey(r))
}

func chargeReset(r *http.Request, email string) time.Duration {
	a := resetLimiterEmail.fail(resetRateKey(email))
	b := resetLimiterIP.fail(resetIPRateKey(r))
	if b > a {
		return b
	}
	return a
}

const sweepInterval = time.Minute

func startRateLimiterSweeper() {
	go func() {
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for range ticker.C {
			for _, l := range allRateLimiters {
				l.sweep()
			}
			sweepSharedRateLimits(time.Now())
		}
	}()
}

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
		Secure:   secureCookieMode(),
	}
	log.Printf("session cookies: secure=%t", secureCookieMode())
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

// initChrome fills in the shared chrome fields that every handler needs and
// none of them should have to remember. Currently just the version.
func initChrome(c *PageChrome) {
	c.Version = appVersion
}

func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, _, ok := currentUser(r)
		if !ok || !sessionStillValid(r, userID) {
			session := getSession(r)
			session.Options.MaxAge = -1
			if err := session.Save(r, w); err != nil {
				log.Printf("failed to clear stale session: %v", err)
			}
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
		accepted := r.FormValue("accept_terms") != ""

		echo := AuthPageData{
			Username:         username,
			Email:            email,
			TermsChecked:     accepted,
			RecaptchaSiteKey: recaptchaSiteKey(),
		}
		reject := func(msg string) {
			echo.Error = msg
			tmpl.ExecuteTemplate(w, "register.html", echo)
		}

		if username == "" {
			reject("Username is required.")
			return
		}
		if msg := validateNewPassword(password); msg != "" {
			reject(msg)
			return
		}
		if email == "" || !strings.Contains(email, "@") {
			reject("A valid email is required for password recovery.")
			return
		}
		if password != confirm {
			reject("Passwords do not match.")
			return
		}
		if !accepted {
			reject("Please accept the Terms and Conditions to create an account.")
			return
		}

		if recaptchaRequired() {
			if err := verifyRecaptcha(r, r.FormValue("g-recaptcha-response")); err != nil {
				reject(err.Error())
				return
			}
		} else {
			log.Println("recaptcha: not configured — signup verification is SKIPPED (development only)")
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			serverError(w, err)
			return
		}

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			serverError(w, err)
			return
		}
		defer tx.Rollback()

		var userID int
		err = tx.QueryRowContext(r.Context(),
			`INSERT INTO users (username, email, password_hash, terms_accepted_at)
			 VALUES ($1, $2, $3, CURRENT_TIMESTAMP) RETURNING id`,
			username, email, string(hash),
		).Scan(&userID)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				reject("That username or email is already taken.")
			} else {
				serverError(w, err)
			}
			return
		}

		if _, err := upsertSettings(tx, userID, defaultSettings()); err != nil {
			serverError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			serverError(w, err)
			return
		}

		session := getSession(r)
		session.Values["user_id"] = userID
		session.Values["username"] = username
		session.Values["sv"] = 0
		if err := session.Save(r, w); err != nil {
			serverError(w, err)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	tmpl.ExecuteTemplate(w, "register.html", AuthPageData{RecaptchaSiteKey: recaptchaSiteKey()})
}

func termsHandler(w http.ResponseWriter, r *http.Request) {
	tmpl.ExecuteTemplate(w, "terms.html", map[string]interface{}{
		"Version": appVersion,
	})
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

		if wait := waitingForLogin(r, username); wait > 0 {
			tooManyAttempts(w, wait)
			return
		}

		var id, sessionVersion int
		var passwordHash string
		err := db.QueryRow(
			"SELECT id, password_hash, session_version FROM users WHERE username = $1", username,
		).Scan(&id, &passwordHash, &sessionVersion)
		if err != nil {
			bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
			if wait := recordLoginFailure(r, username); wait > 0 {
				tooManyAttempts(w, wait)
				return
			}
			tmpl.ExecuteTemplate(w, "login.html", AuthPageData{Error: "Invalid username or password."})
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
			if wait := recordLoginFailure(r, username); wait > 0 {
				tooManyAttempts(w, wait)
				return
			}
			tmpl.ExecuteTemplate(w, "login.html", AuthPageData{Error: "Invalid username or password."})
			return
		}

		loginLimiterUser.reset(userRateKey(username))

		session := getSession(r)
		session.Values["user_id"] = id
		session.Values["username"] = username
		session.Values["sv"] = sessionVersion
		if err := session.Save(r, w); err != nil {
			serverError(w, err)
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

	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}
	render := func(d ChangePasswordPageData) {
		d.Username = username
		d.CurrencySymbol = currencySymbol(settings.Currency)
		initChrome(&d.PageChrome)
		if err := tmpl.ExecuteTemplate(w, "change-password.html", d); err != nil {
			log.Printf("failed to render change-password template: %v", err)
		}
	}

	if r.Method == http.MethodPost {
		current := r.FormValue("current_password")
		newPassword := r.FormValue("new_password")
		confirm := r.FormValue("confirm_password")

		var passwordHash string
		if err := db.QueryRow("SELECT password_hash FROM users WHERE id = $1", userID).Scan(&passwordHash); err != nil {
			serverError(w, err)
			return
		}
		if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(current)); err != nil {
			render(ChangePasswordPageData{Error: "Current password is incorrect."})
			return
		}
		if msg := validateNewPassword(newPassword); msg != "" {
			render(ChangePasswordPageData{Error: msg})
			return
		}
		if newPassword != confirm {
			render(ChangePasswordPageData{Error: "New passwords do not match."})
			return
		}

		newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
		if err != nil {
			serverError(w, err)
			return
		}
		var newVersion int
		if err := db.QueryRow(
			`UPDATE users
				 SET password_hash = $1, reset_token = NULL, reset_token_expires = NULL,
				     session_version = session_version + 1
				 WHERE id = $2
				 RETURNING session_version`,
			string(newHash), userID,
		).Scan(&newVersion); err != nil {
			serverError(w, err)
			return
		}
		session := getSession(r)
		session.Values["sv"] = newVersion
		if err := session.Save(r, w); err != nil {
			log.Printf("failed to re-stamp session: %v", err)
		}

		render(ChangePasswordPageData{Success: "Password updated."})
		return
	}
	render(ChangePasswordPageData{})
}

const resetTokenTTL = 15 * time.Minute

func generateResetToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func forgotPasswordHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}

	if r.Method == http.MethodPost {
		email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))

		if wait := waitingForReset(r, email); wait > 0 {
			tooManyResets(w, wait)
			return
		}
		if wait := chargeReset(r, email); wait > 0 {
			tooManyResets(w, wait)
			return
		}

		successMsg := "If an account exists with that email, a reset link has been sent."

		if email == "" {
			tmpl.ExecuteTemplate(w, "forgot-password.html", ForgotPasswordPageData{Success: successMsg})
			return
		}

		var userID int
		err := db.QueryRow("SELECT id FROM users WHERE email = $1", email).Scan(&userID)
		if err == sql.ErrNoRows {
			tmpl.ExecuteTemplate(w, "forgot-password.html", ForgotPasswordPageData{Success: successMsg})
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}

		token, err := generateResetToken()
		if err != nil {
			serverError(w, err)
			return
		}
		expires := time.Now().Add(resetTokenTTL)

		if _, err := db.Exec(
			"UPDATE users SET reset_token = $1, reset_token_expires = $2 WHERE id = $3",
			hashResetToken(token), expires, userID,
		); err != nil {
			serverError(w, err)
			return
		}

		resetLink := fmt.Sprintf("%s/reset-password?token=%s", externalBaseURL(r), token)

		go func() {
			if err := sendResetEmail(email, resetLink); err != nil {
				log.Printf("failed to send reset email: %v", err)
			}
		}()

		tmpl.ExecuteTemplate(w, "forgot-password.html", ForgotPasswordPageData{Success: successMsg})
		return
	}

	tmpl.ExecuteTemplate(w, "forgot-password.html", ForgotPasswordPageData{})
}

func resetPasswordHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}

	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")

	invalid := func() {
		tmpl.ExecuteTemplate(w, "reset-password.html", ResetPasswordPageData{
			Error: "This reset link is invalid or has expired.", Valid: false,
		})
	}

	if r.Method == http.MethodPost {
		token := r.FormValue("token")
		newPassword := r.FormValue("new_password")
		confirm := r.FormValue("confirm_password")
		tokenHash := hashResetToken(token)

		var userID int
		err := db.QueryRow(
			"SELECT id FROM users WHERE reset_token = $1 AND reset_token_expires > $2",
			tokenHash, time.Now(),
		).Scan(&userID)
		if err == sql.ErrNoRows {
			invalid()
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}

		if msg := validateNewPassword(newPassword); msg != "" {
			tmpl.ExecuteTemplate(w, "reset-password.html", ResetPasswordPageData{
				Error: msg, Valid: true, Token: token,
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
			serverError(w, err)
			return
		}

		res, err := db.Exec(
			`UPDATE users
				 SET password_hash = $1, reset_token = NULL, reset_token_expires = NULL,
				     session_version = session_version + 1
				 WHERE id = $2 AND reset_token = $3 AND reset_token_expires > $4`,
			string(newHash), userID, tokenHash, time.Now(),
		)
		if err != nil {
			serverError(w, err)
			return
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			invalid()
			return
		}

		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	token := r.URL.Query().Get("token")
	if token == "" {
		tmpl.ExecuteTemplate(w, "reset-password.html", ResetPasswordPageData{Valid: false})
		return
	}

	var exists bool
	err := db.QueryRow(
		"SELECT EXISTS (SELECT 1 FROM users WHERE reset_token = $1 AND reset_token_expires > $2)",
		hashResetToken(token), time.Now(),
	).Scan(&exists)
	if err != nil {
		serverError(w, err)
		return
	}
	if !exists {
		invalid()
		return
	}

	tmpl.ExecuteTemplate(w, "reset-password.html", ResetPasswordPageData{Valid: true, Token: token})
}

func defaultSettings() SettingsData {
	return SettingsData{
		BudgetLimit: 0,
		Currency:    "NGN",
		Categories:  []string{"Food", "Transport", "Bills"},
	}
}

func loadSettings(userID int) (SettingsData, error) {
	var s SettingsData
	var categoriesStr string
	err := db.QueryRow(
		"SELECT budget_limit, currency, categories FROM settings WHERE user_id = $1",
		userID,
	).Scan(&s.BudgetLimit, &s.Currency, &categoriesStr)

	if err == sql.ErrNoRows {
		s = defaultSettings()
		if _, err2 := upsertSettings(db, userID, s); err2 != nil {
			return s, err2
		}
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if categoriesStr != "" {
		s.Categories = normalizeCategories(strings.Split(categoriesStr, ","))
	}
	return s, nil
}

type settingsExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func upsertSettings(exec settingsExecer, userID int, s SettingsData) (sql.Result, error) {
	s.Categories = normalizeCategories(s.Categories)
	return exec.Exec(`
		INSERT INTO settings (user_id, budget_limit, currency, categories)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE SET
			budget_limit = EXCLUDED.budget_limit,
			currency = EXCLUDED.currency,
			categories = EXCLUDED.categories
	`, userID, s.BudgetLimit, s.Currency, strings.Join(s.Categories, ","))
}

func saveSettings(userID int, s SettingsData) error {
	_, err := upsertSettings(db, userID, s)
	return err
}

func saveCategoryBudgets(userID int, categories []string, limits []float64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for i, category := range categories {
		var limit float64
		if i < len(limits) {
			limit = limits[i]
		}
		if limit <= 0 {
			if _, err := tx.Exec(
				"DELETE FROM category_budgets WHERE user_id = $1 AND category = $2",
				userID, category,
			); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO category_budgets (user_id, category, limit_amount)
			VALUES ($1, $2, $3)
			ON CONFLICT (user_id, category) DO UPDATE SET limit_amount = EXCLUDED.limit_amount
		`, userID, category, limit); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func clearCategoryBudgets(userID int) error {
	_, err := db.Exec("DELETE FROM category_budgets WHERE user_id = $1", userID)
	return err
}

func currentMonth() string { return time.Now().Format("2006-01") }

func homePath(month, deletedID string) string {
	values := url.Values{}
	if _, err := time.Parse("2006-01", month); err == nil {
		values.Set("month", month)
	}
	if deletedID != "" {
		values.Set("deleted", deletedID)
	}
	if encoded := values.Encode(); encoded != "" {
		return "/?" + encoded
	}
	return "/"
}

func returnPath(r *http.Request, month, deletedID string) string {
	filters := filtersFromForm(r)
	if !filters.Active() {
		return homePath(month, deletedID)
	}
	values := filters.Values()
	if deletedID != "" {
		values.Set("deleted", deletedID)
	}
	return "/?" + values.Encode()
}

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

func normalizeMonth(ym string) string {
	if _, err := time.Parse("2006-01", ym); err != nil {
		return currentMonth()
	}
	return ym
}

func monthRange(ym string) (string, string) {
	start, err := time.Parse("2006-01", ym)
	if err != nil {
		start = time.Now()
	}
	return start.Format("2006-01-02"), start.AddDate(0, 1, 0).Format("2006-01-02")
}

func loadExpensesByMonth(userID int, month string) (PageData, error) {
	start, end := monthRange(month)
	rows, err := db.Query(
		`SELECT id, description, amount, category, kind,
		        to_char(created_at,'YYYY-MM-DD HH24:MI:SS'), to_char(created_at,'YYYY-MM-DD')
		 FROM expenses
		 WHERE user_id = $1 AND deleted_at IS NULL
		   AND created_at >= $2::timestamp AND created_at < $3::timestamp
		 ORDER BY created_at DESC`,
		userID, start, end,
	)
	if err != nil {
		return PageData{}, err
	}
	defer rows.Close()
	var expenses []Expense
	var spent, received float64
	for rows.Next() {
		var e Expense
		if err := rows.Scan(&e.ID, &e.Description, &e.Amount, &e.Category, &e.Kind, &e.CreatedAt, &e.Date); err != nil {
			return PageData{}, err
		}
		expenses = append(expenses, e)
		if e.Income() {
			received += e.Amount
		} else {
			spent += e.Amount
		}
	}
	if err := rows.Err(); err != nil {
		return PageData{}, err
	}

	settings, err := loadSettings(userID)
	if err != nil {
		return PageData{}, err
	}

	budgets, err := categoryBudgetProgress(userID, month, settings.Categories)
	if err != nil {
		return PageData{}, err
	}

	return PageData{
		Expenses: expenses, Spent: spent, Received: received,
		CurrentMonth: currentMonth(), PrevMonth: addMonth(month, -1), NextMonth: addMonth(month, 1),
		BudgetLimit: settings.BudgetLimit, Categories: settings.Categories,
		CategoryBudgets: budgets,
		PageChrome: PageChrome{
			Month:          month,
			CurrencySymbol: currencySymbol(settings.Currency),
		},
	}, nil
}

func categoryLimits(userID int) (map[string]float64, error) {
	rows, err := db.Query(
		`SELECT category, limit_amount FROM category_budgets
		 WHERE user_id = $1 AND limit_amount > 0`,
		userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	limits := map[string]float64{}
	for rows.Next() {
		var name string
		var limit float64
		if err := rows.Scan(&name, &limit); err != nil {
			return nil, err
		}
		limits[name] = limit
	}
	return limits, rows.Err()
}

func categoryBudgetProgress(userID int, month string, order []string) ([]CategoryBudget, error) {
	limits, err := categoryLimits(userID)
	if err != nil {
		return nil, err
	}
	if len(limits) == 0 {
		return nil, nil
	}

	start, end := monthRange(month)
	rows, err := db.Query(
		`SELECT category, SUM(amount) FROM expenses
		 WHERE user_id = $1 AND kind = 'expense' AND deleted_at IS NULL
		   AND created_at >= $2::timestamp AND created_at < $3::timestamp
		 GROUP BY category`,
		userID, start, end,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	spent := map[string]float64{}
	for rows.Next() {
		var name string
		var total float64
		if err := rows.Scan(&name, &total); err != nil {
			return nil, err
		}
		spent[name] = total
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	budgets := make([]CategoryBudget, 0, len(limits))
	for _, name := range order {
		if limit := limits[name]; limit > 0 && !seen[name] {
			seen[name] = true
			budgets = append(budgets, newCategoryBudget(name, limit, spent[name]))
		}
	}

	var rest []string
	for name := range limits {
		if !seen[name] && limits[name] > 0 {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	for _, name := range rest {
		budgets = append(budgets, newCategoryBudget(name, limits[name], spent[name]))
	}

	return budgets, nil
}

const budgetWarnFraction = 0.8

// safeToSpend divides what's left of the monthly budget by the days remaining
// in the current month, including today. It answers "can I afford this
// today?", which a lump sum that only shrinks does not.
//
// Only meaningful for the current month: a past month has no days left to
// divide over, and a future month has nothing spent against it yet. The
// handler checks data.Month == currentMonth() before setting the value, so
// the template only sees a number when it's true.
//
// The divisor never drops below 1. On the last day of the month the whole
// remaining amount is "safe to spend today", which is what someone at the
// end of the month needs to see.
func safeToSpend(budget, spent float64, month string) float64 {
	if budget <= 0 {
		return 0
	}
	remaining := budget - spent
	if remaining <= 0 {
		return 0
	}
	start, err := time.Parse("2006-01", month)
	if err != nil {
		return 0
	}
	// Last day of the month: one month forward, one day back.
	lastDay := start.AddDate(0, 1, -1).Day()
	daysRemaining := lastDay - time.Now().Day() + 1
	if daysRemaining < 1 {
		daysRemaining = 1
	}
	return remaining / float64(daysRemaining)
}

func newCategoryBudget(category string, limit, spent float64) CategoryBudget {
	b := CategoryBudget{
		Category:  category,
		Limit:     limit,
		Spent:     spent,
		Remaining: limit - spent,
	}
	b.PercentUsed = spent / limit * 100
	b.PercentOfLimit = b.PercentUsed
	if b.PercentOfLimit > 100 {
		b.PercentOfLimit = 100
	}
	b.Over = spent >= limit
	b.Near = !b.Over && spent >= limit*budgetWarnFraction
	return b
}

type ExpenseFilters struct {
	Query    string
	Category string
	Kind     string
	From     string
	To       string
}

func (f ExpenseFilters) Active() bool {
	return f.Query != "" || f.Category != "" || f.Kind != "" || f.From != "" || f.To != ""
}

func archiveShouldSearch(month string, filters ExpenseFilters) bool {
	_ = month
	return filters.Active()
}

func (f ExpenseFilters) Values() url.Values {
	values := url.Values{}
	if f.Query != "" {
		values.Set("q", f.Query)
	}
	if f.Category != "" {
		values.Set("category", f.Category)
	}
	if f.Kind != "" {
		values.Set("kind", f.Kind)
	}
	if f.From != "" {
		values.Set("from", f.From)
	}
	if f.To != "" {
		values.Set("to", f.To)
	}
	return values
}

func parseFilters(r *http.Request) ExpenseFilters {
	return parseFiltersFrom(r.URL.Query().Get, true)
}

func filtersFromForm(r *http.Request) ExpenseFilters {
	return parseFiltersFrom(func(key string) string {
		return r.FormValue(key)
	}, false)
}

func parseFiltersFrom(get func(string) string, allowPlainNames bool) ExpenseFilters {
	f := ExpenseFilters{
		Query:    strings.TrimSpace(get("q")),
		Category: strings.TrimSpace(get("category_filter")),
		From:     strings.TrimSpace(get("from")),
		To:       strings.TrimSpace(get("to")),
	}

	kind := strings.TrimSpace(get("kind_filter"))
	if kind == "" && allowPlainNames {
		kind = strings.TrimSpace(get("kind"))
	}
	switch strings.ToLower(kind) {
	case kindExpense:
		f.Kind = kindExpense
	case kindIncome:
		f.Kind = kindIncome
	}

	if f.Category == "" && allowPlainNames {
		f.Category = strings.TrimSpace(get("category"))
	}

	if d, err := time.ParseInLocation("2006-01-02", f.From, time.Local); err == nil {
		f.From = d.Format("2006-01-02")
	} else {
		f.From = ""
	}
	if d, err := time.ParseInLocation("2006-01-02", f.To, time.Local); err == nil {
		f.To = d.Format("2006-01-02")
	} else {
		f.To = ""
	}

	if f.From != "" && f.To != "" && f.From > f.To {
		f.From, f.To = f.To, f.From
	}
	return f
}

const searchResultLimit = 500

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func searchQueryClause(start int, term string) (string, []any) {
	q := "%" + likeEscape(term) + "%"
	return fmt.Sprintf(
		"(description ILIKE $%d ESCAPE '\\' OR category ILIKE $%d ESCAPE '\\' OR kind ILIKE $%d ESCAPE '\\' OR to_char(created_at,'YYYY-MM-DD') ILIKE $%d ESCAPE '\\')",
		start, start+1, start+2, start+3,
	), []any{q, q, q, q}
}

type searchResult struct {
	Expenses  []Expense
	Spent     float64
	Received  float64
	Truncated bool
}

func searchExpenses(userID int, f ExpenseFilters) (searchResult, error) {
	where := []string{"user_id = $1", "deleted_at IS NULL"}
	args := []any{userID}

	add := func(clause string, value any) {
		args = append(args, value)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	if f.Query != "" {
		clause, values := searchQueryClause(len(args)+1, f.Query)
		where = append(where, clause)
		args = append(args, values...)
	}
	if f.Category == noCategorySentinel {
		// An out-of-band value, not a category name. Matching it as a literal
		// string would return nothing; the intent is the rows whose category
		// is empty.
		where = append(where, "category = ''")
	} else if f.Category != "" {
		add("category = $%d", f.Category)
	}
	if f.Kind != "" {
		add("kind = $%d", f.Kind)
	}
	if f.From != "" {
		add("created_at >= $%d::date", f.From)
	}
	if f.To != "" {
		add("created_at < ($%d::date + interval '1 day')", f.To)
	}

	query := fmt.Sprintf(
		`SELECT id, description, amount, category, kind,
		        to_char(created_at,'YYYY-MM-DD HH24:MI:SS'), to_char(created_at,'YYYY-MM-DD')
		 FROM expenses
		 WHERE %s
		 ORDER BY created_at DESC
		 LIMIT %d`,
		strings.Join(where, " AND "), searchResultLimit+1,
	)

	rows, err := db.Query(query, args...)
	if err != nil {
		return searchResult{}, err
	}
	defer rows.Close()

	var result searchResult
	for rows.Next() {
		var e Expense
		if err := rows.Scan(&e.ID, &e.Description, &e.Amount, &e.Category, &e.Kind, &e.CreatedAt, &e.Date); err != nil {
			return searchResult{}, err
		}
		result.Expenses = append(result.Expenses, e)
		if e.Income() {
			result.Received += e.Amount
		} else {
			result.Spent += e.Amount
		}
	}
	if err := rows.Err(); err != nil {
		return searchResult{}, err
	}

	result.Truncated = len(result.Expenses) > searchResultLimit
	if result.Truncated {
		extra := result.Expenses[searchResultLimit]
		if extra.Income() {
			result.Received -= extra.Amount
		} else {
			result.Spent -= extra.Amount
		}
		result.Expenses = result.Expenses[:searchResultLimit]
	}
	return result, nil
}

func normalizeCategories(categories []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(categories))
	for _, raw := range categories {
		name := strings.TrimSpace(strings.ReplaceAll(raw, ",", ""))
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func categoryOptions(configured []string, selected string) []CategoryOption {
	options := make([]CategoryOption, 0, len(configured)+1)
	for _, c := range configured {
		options = append(options, CategoryOption{Value: c, Label: c})
	}
	if selected == "" {
		return options
	}
	for _, o := range options {
		if o.Value == selected {
			return options
		}
	}
	// The selected value is not in the configured list. Two cases: a real
	// category that was removed from settings but still tags some expenses,
	// or the sentinel meaning "the empty category". Both are appended so the
	// select reflects what is actually being filtered.
	label := selected
	if selected == noCategorySentinel {
		label = "Uncategorized"
	}
	return append(options, CategoryOption{Value: selected, Label: label})
}

func parseExpenseDate(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	d, err := time.ParseInLocation("2006-01-02", raw, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	now := time.Now()
	candidate := time.Date(d.Year(), d.Month(), d.Day(), now.Hour(), now.Minute(), now.Second(), 0, time.Local)
	if candidate.After(now) {
		return time.Time{}, false
	}
	return candidate, true
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)
	month := normalizeMonth(r.URL.Query().Get("month"))
	filters := parseFilters(r)

	templateData := map[string]interface{}{
		"Username":     username,
		"Filters":      filters,
		"CurrentMonth": currentMonth(),
		"DeletedID":    r.URL.Query().Get("deleted"),
		"Version":      appVersion,
	}

	if filters.Active() {
		result, err := searchExpenses(userID, filters)
		if err != nil {
			serverError(w, err)
			return
		}
		settings, err := loadSettings(userID)
		if err != nil {
			serverError(w, err)
			return
		}

		templateData["Searching"] = true
		templateData["Expenses"] = result.Expenses
		templateData["Spent"] = result.Spent
		templateData["Received"] = result.Received
		templateData["Net"] = result.Received - result.Spent
		templateData["Truncated"] = result.Truncated
		templateData["ResultLimit"] = searchResultLimit
		templateData["CurrencySymbol"] = currencySymbol(settings.Currency)
		templateData["CategoryOptions"] = categoryOptions(settings.Categories, filters.Category)
		templateData["Month"] = ""

		if err := tmpl.ExecuteTemplate(w, "index.html", templateData); err != nil {
			if !isClientDisconnect(err) {
				log.Printf("failed to render index template: %v", err)
			}
		}
		return
	}

	data, err := loadExpensesByMonth(userID, month)
	if err != nil {
		serverError(w, err)
		return
	}

	templateData["Spent"] = data.Spent
	templateData["Received"] = data.Received
	templateData["Net"] = data.Net()
	templateData["Expenses"] = data.Expenses
	templateData["Month"] = data.Month
	templateData["MonthLabel"] = monthLabel(data.Month)
	templateData["BudgetLimit"] = data.BudgetLimit
	templateData["PrevMonth"] = data.PrevMonth
	templateData["NextMonth"] = data.NextMonth
	templateData["Categories"] = data.Categories
	templateData["CategoryOptions"] = categoryOptions(data.Categories, "")
	templateData["CategoryBudgets"] = data.CategoryBudgets
	templateData["CurrencySymbol"] = data.CurrencySymbol

	if data.BudgetLimit > 0 {
		remaining := data.BudgetLimit - data.Spent
		percent := (data.Spent / data.BudgetLimit) * 100
		if percent > 100 {
			percent = 100
		}
		templateData["HasBudget"] = true
		templateData["Remaining"] = remaining
		templateData["OverAmount"] = math.Abs(remaining)
		templateData["Percent"] = percent
		templateData["WarnAt"] = data.BudgetLimit * budgetWarnFraction

		// Safe-to-spend is a per-day figure, so it only makes sense while the
		// month has days left and money in it. Both conditions are checked
		// here rather than in the template, so a stale value cannot be shown
		// on a past month's view.
		if data.Month == currentMonth() && remaining > 0 {
			templateData["SafeToSpend"] = safeToSpend(data.BudgetLimit, data.Spent, data.Month)
		}
	}

	if err := tmpl.ExecuteTemplate(w, "index.html", templateData); err != nil {
		if !isClientDisconnect(err) {
			log.Printf("failed to render index template: %v", err)
		}
	}
}

func verifyTemplatesRender() {
	sample := Expense{
		ID: 1, Description: "Sample expense", Amount: 12.5, Category: "Food",
		Kind: kindExpense, CreatedAt: "2026-01-01 12:00:00", Date: "2026-01-01",
	}
	sampleIncome := Expense{
		ID: 2, Description: "Sample income", Amount: 250, Category: "Salary",
		Kind: kindIncome, CreatedAt: "2026-01-02 09:00:00", Date: "2026-01-02",
	}
	noFilters := ExpenseFilters{}

	home := func() map[string]interface{} {
		return map[string]interface{}{
			"Username": "sam", "CurrencySymbol": "₦", "Filters": noFilters,
			"CurrentMonth": "2026-01", "Month": "2026-01",
			"MonthLabel": "January 2026", "PrevMonth": "2025-12", "NextMonth": "2026-02",
			"Spent": 12.5, "Received": 250.0, "Net": 237.5,
			"Expenses":    []Expense{sample, sampleIncome},
			"BudgetLimit": 100.0, "Categories": []string{"Food"},
			"CategoryOptions": []CategoryOption{{Value: "Food", Label: "Food"}},
			"CategoryBudgets": []CategoryBudget{
				newCategoryBudget("Food", 100, 12.5),
				newCategoryBudget("Transport", 40, 55),
				newCategoryBudget("Bills", 50, 42),
			},
		}
	}
	withBudget := home()
	withBudget["HasBudget"] = true
	withBudget["Remaining"] = 87.5
	withBudget["OverAmount"] = 87.5
	withBudget["Percent"] = 12.5
	withBudget["WarnAt"] = 80.0
	// Renders the safe-to-spend line, so the {{if .SafeToSpend}} branch is
	// actually executed at startup rather than merely parsed.
	withBudget["SafeToSpend"] = 12.50

	withUndo := home()
	withUndo["DeletedID"] = "1"

	searching := home()
	searching["Searching"] = true
	searching["Truncated"] = true
	searching["ResultLimit"] = searchResultLimit
	searching["Month"] = ""
	searching["Filters"] = ExpenseFilters{Query: "lunch", Category: "Food", Kind: kindIncome, From: "2026-01-01", To: "2026-01-31"}

	type monthSummary struct {
		Month, Label string
		Count        int
		Spent        float64
		Received     float64
	}
	archiveMonths := map[string]interface{}{
		"Username": "sam", "CurrencySymbol": "₦", "Details": nil,
		"Months":  []monthSummary{{Month: "2026-01", Label: "January 2026", Count: 1, Spent: 12.5, Received: 250}},
		"Filters": ExpenseFilters{}, "CategoryOptions": []CategoryOption{{Value: "Food", Label: "Food"}},
	}
	archiveDetail := map[string]interface{}{
		"Username": "sam", "CurrencySymbol": "₦", "Months": nil,
		"Details": PageData{
			Expenses: []Expense{sample, sampleIncome}, Spent: 12.5, Received: 250,
			CurrentMonth: "2026-01", PrevMonth: "2025-12", NextMonth: "2026-02",
			BudgetLimit: 100, Categories: []string{"Food"},
			CategoryBudgets: []CategoryBudget{
				newCategoryBudget("Food", 100, 12.5),
				newCategoryBudget("Transport", 40, 55),
				newCategoryBudget("Bills", 50, 42),
			},
			PageChrome: PageChrome{Month: "2026-01", CurrencySymbol: "₦"},
		},
		"Label": "January 2026", "Month": "2026-01",
		"Filters": ExpenseFilters{}, "CategoryOptions": []CategoryOption{{Value: "Food", Label: "Food"}},
	}

	settings := SettingsData{
		BudgetLimit: 100, Currency: "NGN", Categories: []string{"Food", "Transport", "Bills"},
		PageChrome:     PageChrome{Username: "sam", CurrencySymbol: "₦"},
		CurrencyList:   currencyOptions,
		CategoryLimits: map[string]float64{"Food": 100},
	}

	pages := []struct {
		name string
		data interface{}
	}{
		{"index.html", withBudget},
		{"index.html", home()},
		{"index.html", withUndo},
		{"index.html", searching},
		{"archive.html", archiveMonths},
		{"archive.html", archiveDetail},
		{"report.html", ReportData{
			Count: 1, Total: 12.5, Average: 12.5, Largest: 12.5, BudgetLimit: 100,
			PercentOfBudget: 12.5, ChartExists: true,
			CategoryTotals: []CategoryTotal{{Category: "Food", Total: 12.5}},
			CategoryJSON:   template.JS(`[{"category":"Food","total":12.5}]`),
			PageChrome:     PageChrome{Username: "sam", CurrencySymbol: "₦"},
		}},
		{"settings.html", settings},
		{"saved-settings.html", settings},
		{"export.html", map[string]interface{}{
			"Username": "sam", "CurrencySymbol": "₦", "Filters": noFilters,
			"Month": "2026-01", "Label": "January 2026",
			"PrevMonth": "2025-12", "NextMonth": "2026-02", "CurrentMonth": "2026-01",
		}},
		{"login.html", AuthPageData{Error: "nope"}},
		{"register.html", AuthPageData{RecaptchaSiteKey: "site-key", TermsChecked: true}},
		{"change-password.html", ChangePasswordPageData{
			PageChrome: PageChrome{Username: "sam", CurrencySymbol: "₦"},
		}},
		{"forgot-password.html", ForgotPasswordPageData{Success: "Sent."}},
		{"reset-password.html", ResetPasswordPageData{Valid: true, Token: "token"}},
		{"reset-password.html", ResetPasswordPageData{Valid: false}},
		{"goals.html", GoalsPageData{
			PageChrome:   PageChrome{Username: "sam", CurrencySymbol: "₦"},
			DefaultMonth: "2026-10",
			CurrentMonth: "2026-10",
			CurrentYear:  "2026",
			HasAny:       true,
			ActiveGroups: []GoalGroup{
				{Period: "2026-10", Scope: "month", Label: "October 2026", Goals: []Goal{
					{ID: 1, Title: "Pay rent", Note: "Before the 5th", HasAmount: true, Amount: 80000},
					{ID: 2, Title: "Read a book"},
				}},
				{Period: "2026", Scope: "year", Label: "2026", Goals: []Goal{
					{ID: 3, Title: "Save for a car", HasAmount: true, Amount: 500000},
				}},
			},
			Completed: []Goal{
				{ID: 4, Title: "Set up a budget", Completed: true},
			},
		}},
		{"support.html", SupportPageData{
			PageChrome:   PageChrome{Username: "sam", CurrencySymbol: "₦"},
			Email:        "support@example.com",
			Phone:        "+234 800 000 0000",
			PhoneLink:    "+2348000000000",
			WhatsApp:     "2348000000000",
			WhatsAppText: "Hi%2C%20I%20need%20help%20with%20Budget%20Tracker.",
		}},
		{"terms.html", nil},
	}

	for _, p := range pages {
		var buf bytes.Buffer
		if err := tmpl.ExecuteTemplate(&buf, p.name, p.data); err != nil {
			log.Fatalf("template %s cannot render with its own data shape: %v", p.name, err)
		}
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

	description := strings.TrimSpace(r.FormValue("description"))
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil || amount < 0 {
		http.Error(w, "enter a valid amount", http.StatusBadRequest)
		return
	}
	category := strings.TrimSpace(r.FormValue("category"))
	kind := normalizeKind(r.FormValue("kind"))

	createdAt := time.Now()
	if d, ok := parseExpenseDate(r.FormValue("date")); ok {
		createdAt = d
	}

	_, err = db.Exec(
		"INSERT INTO expenses (user_id, description, amount, category, kind, created_at) VALUES ($1, $2, $3, $4, $5, $6)",
		userID, description, amount, category, kind, createdAt,
	)
	if err != nil {
		serverError(w, err)
		return
	}

	http.Redirect(w, r, homePath(createdAt.Format("2006-01"), ""), http.StatusSeeOther)
}

func editHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)

	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/edit/"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	description := strings.TrimSpace(r.FormValue("description"))
	amount, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("amount")), 64)
	if err != nil || amount < 0 {
		http.Error(w, "enter a valid amount", http.StatusBadRequest)
		return
	}
	category := strings.TrimSpace(r.FormValue("category"))
	kind := normalizeKind(r.FormValue("kind"))

	var newCreatedAt any
	if raw := strings.TrimSpace(r.FormValue("date")); raw != "" && raw != strings.TrimSpace(r.FormValue("orig_date")) {
		if d, ok := parseExpenseDate(raw); ok {
			newCreatedAt = d
		}
	}

	res, err := db.Exec(
		`UPDATE expenses
		 SET description = $1, amount = $2, category = $3, kind = $4,
		     created_at = CASE WHEN $5::timestamp IS NULL THEN created_at ELSE $5::timestamp END
		 WHERE id = $6 AND user_id = $7 AND deleted_at IS NULL`,
		description, amount, category, kind, newCreatedAt, id, userID,
	)
	if err != nil {
		serverError(w, err)
		return
	}

	if n, err := res.RowsAffected(); err == nil && n == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	if filters := filtersFromForm(r); filters.Active() {
		http.Redirect(w, r, "/?"+filters.Values().Encode(), http.StatusSeeOther)
		return
	}

	target := normalizeMonth(r.FormValue("month"))
	if d, ok := newCreatedAt.(time.Time); ok {
		target = d.Format("2006-01")
	}
	http.Redirect(w, r, homePath(target, ""), http.StatusSeeOther)
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

	res, err := db.Exec(
		"UPDATE expenses SET deleted_at = NOW() WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL",
		id, userID,
	)
	if err != nil {
		serverError(w, err)
		return
	}

	if n, err := res.RowsAffected(); err == nil && n == 0 {
		http.Redirect(w, r, returnPath(r, r.FormValue("month"), ""), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, returnPath(r, r.FormValue("month"), strconv.Itoa(id)), http.StatusSeeOther)
}

func restoreHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	userID, _, _ := currentUser(r)

	id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/restore/"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	if _, err := db.Exec(
		"UPDATE expenses SET deleted_at = NULL WHERE id = $1 AND user_id = $2",
		id, userID,
	); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, returnPath(r, r.FormValue("month"), ""), http.StatusSeeOther)
}

func categoriesHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, _, _ := currentUser(r)

	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}

	categories := settings.Categories
	if categories == nil {
		categories = []string{}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(categories)
}

func reportHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)
	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}

	rows, err := db.Query(
		"SELECT amount, category FROM expenses WHERE user_id = $1 AND kind = 'expense' AND deleted_at IS NULL",
		userID,
	)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()
	var amounts []float64
	categoryTotals := map[string]float64{}
	for rows.Next() {
		var a float64
		var cat string
		if err := rows.Scan(&a, &cat); err != nil {
			serverError(w, err)
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
	sort.Slice(breakdown, func(i, j int) bool {
		if breakdown[i].Total == breakdown[j].Total {
			return breakdown[i].Category < breakdown[j].Category
		}
		return breakdown[i].Total > breakdown[j].Total
	})

	chartRows := make([]struct {
		Category string  `json:"category"`
		Total    float64 `json:"total"`
	}, len(breakdown))
	for i, c := range breakdown {
		chartRows[i].Category = c.Category
		chartRows[i].Total = c.Total
	}
	chartJSON, err := json.Marshal(chartRows)
	if err != nil {
		serverError(w, err)
		return
	}

	var percentOfBudget float64
	if settings.BudgetLimit > 0 {
		percentOfBudget = (total / settings.BudgetLimit) * 100
	}

	if err := tmpl.ExecuteTemplate(w, "report.html", ReportData{
		Count: len(amounts), Total: total, Average: average, Largest: largest,
		BudgetLimit: settings.BudgetLimit, PercentOfBudget: percentOfBudget,
		ChartExists: total > 0, CategoryTotals: breakdown,
		CategoryJSON: template.JS(chartJSON),
		PageChrome: PageChrome{
			Username:       username,
			CurrencySymbol: currencySymbol(settings.Currency),
			Version:        appVersion,
		},
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
		serverError(w, err)
		return
	}
	settings.Username = username
	settings.CurrencySymbol = currencySymbol(settings.Currency)
	settings.CurrencyList = currencyOptions
	settings.Version = appVersion

	limits, err := categoryLimits(userID)
	if err != nil {
		serverError(w, err)
		return
	}
	settings.CategoryLimits = limits

	tmpl.ExecuteTemplate(w, "settings.html", settings)
}

func savedSettingsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)

	if r.Method == http.MethodPost {
		r.ParseForm()
		budget, err := strconv.ParseFloat(r.FormValue("budget"), 64)
		if err != nil || budget < 0 {
			budget = 0
		}
		rawNames := r.PostForm["category"]
		rawLimits := r.PostForm["category_limit"]
		categories := make([]string, 0, len(rawNames))
		limits := make([]float64, 0, len(rawNames))
		seen := map[string]struct{}{}
		for i, c := range rawNames {
			c = strings.TrimSpace(strings.ReplaceAll(c, ",", ""))
			if c == "" {
				continue
			}
			if _, exists := seen[c]; exists {
				continue
			}
			seen[c] = struct{}{}
			var limit float64
			if i < len(rawLimits) {
				if v, err := strconv.ParseFloat(strings.TrimSpace(rawLimits[i]), 64); err == nil && v > 0 {
					limit = v
				}
			}
			categories = append(categories, c)
			limits = append(limits, limit)
		}
		existing, err := loadSettings(userID)
		if err != nil {
			serverError(w, err)
			return
		}
		currency := strings.ToUpper(strings.TrimSpace(r.FormValue("currency")))
		if !knownCurrency(currency) {
			currency = existing.Currency
			if !knownCurrency(currency) {
				currency = "NGN"
			}
		}
		newSettings := SettingsData{
			BudgetLimit: budget,
			Currency:    currency,
			Categories:  categories,
		}
		if err := saveSettings(userID, newSettings); err != nil {
			serverError(w, err)
			return
		}
		if err := saveCategoryBudgets(userID, categories, limits); err != nil {
			serverError(w, err)
			return
		}
		http.Redirect(w, r, "/settings/saved", http.StatusSeeOther)
		return
	}

	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}
	settings.Username = username
	settings.CurrencySymbol = currencySymbol(settings.Currency)
	settings.Version = appVersion
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

	month := normalizeMonth(r.FormValue("month"))

	start, end := monthRange(month)
	_, err := db.Exec(
		"DELETE FROM expenses WHERE user_id = $1 AND created_at >= $2::timestamp AND created_at < $3::timestamp",
		userID, start, end,
	)
	if err != nil {
		serverError(w, err)
		return
	}

	existing, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}
	newSettings := SettingsData{
		BudgetLimit: 0,
		Currency:    existing.Currency,
		Categories:  existing.Categories,
	}
	if err := saveSettings(userID, newSettings); err != nil {
		serverError(w, err)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func archiveHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)

	month := r.URL.Query().Get("month")
	filters := parseFilters(r)
	if archiveShouldSearch(month, filters) {
		result, err := searchExpenses(userID, filters)
		if err != nil {
			serverError(w, err)
			return
		}
		settings, err := loadSettings(userID)
		if err != nil {
			serverError(w, err)
			return
		}
		tmpl.ExecuteTemplate(w, "archive.html", map[string]interface{}{
			"Searching":       true,
			"Filters":         filters,
			"Expenses":        result.Expenses,
			"Spent":           result.Spent,
			"Received":        result.Received,
			"Net":             result.Received - result.Spent,
			"Truncated":       result.Truncated,
			"ResultLimit":     searchResultLimit,
			"Username":        username,
			"CurrencySymbol":  currencySymbol(settings.Currency),
			"CategoryOptions": categoryOptions(settings.Categories, filters.Category),
			"Version":         appVersion,
		})
		return
	}
	if month == "" {
		rows, err := db.Query(
			`SELECT to_char(created_at,'YYYY-MM') AS ym,
			        COUNT(*) FILTER (WHERE kind = 'expense'),
			        COALESCE(SUM(amount) FILTER (WHERE kind = 'expense'), 0),
			        COALESCE(SUM(amount) FILTER (WHERE kind = 'income'), 0)
			 FROM expenses
			 WHERE user_id = $1 AND deleted_at IS NULL
			 GROUP BY ym
			 ORDER BY ym DESC`,
			userID,
		)
		if err != nil {
			serverError(w, err)
			return
		}
		defer rows.Close()
		type MonthSummary struct {
			Month, Label string
			Count        int
			Spent        float64
			Received     float64
		}
		var months []MonthSummary
		for rows.Next() {
			var ms MonthSummary
			if err := rows.Scan(&ms.Month, &ms.Count, &ms.Spent, &ms.Received); err != nil {
				serverError(w, err)
				return
			}
			ms.Label = monthLabel(ms.Month)
			months = append(months, ms)
		}
		if err := rows.Err(); err != nil {
			serverError(w, err)
			return
		}
		settings, err := loadSettings(userID)
		if err != nil {
			serverError(w, err)
			return
		}
		categoryOptions := categoryOptions(settings.Categories, filters.Category)
		tmpl.ExecuteTemplate(w, "archive.html", map[string]interface{}{
			"Months": months, "Details": nil, "Username": username,
			"CurrencySymbol": currencySymbol(settings.Currency),
			"Filters":        filters, "CategoryOptions": categoryOptions,
			"Version": appVersion,
		})
		return
	}
	data, err := loadExpensesByMonth(userID, month)
	if err != nil {
		serverError(w, err)
		return
	}
	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}
	tmpl.ExecuteTemplate(w, "archive.html", map[string]interface{}{
		"Months": nil, "Details": data, "Label": monthLabel(month), "Month": month, "Username": username,
		"CurrencySymbol": data.CurrencySymbol,
		"Filters":        filters, "CategoryOptions": categoryOptions(settings.Categories, filters.Category),
		"Version": appVersion,
	})
}

func autoMonthlyReset() {
	if !dbReady() {
		return
	}
	wipe := isTruthy(os.Getenv("AUTO_MONTHLY_RESET"))
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month()+1, 1, 0, 5, 0, 0, now.Location())
		time.Sleep(time.Until(next))

		if wipe {
			prevMonth := addMonth(currentMonth(), -1)
			start, end := monthRange(prevMonth)
			if _, err := db.Exec("DELETE FROM expenses WHERE created_at >= $1::timestamp AND created_at < $2::timestamp", start, end); err != nil {
				log.Printf("auto-reset failed for %s: %v", prevMonth, err)
			} else {
				log.Printf("auto-reset: cleared expenses for %s", prevMonth)
			}
		}

		if _, err := db.Exec("DELETE FROM expenses WHERE deleted_at IS NOT NULL AND deleted_at < NOW() - INTERVAL '30 days'"); err != nil {
			log.Printf("purge of soft-deleted expenses failed: %v", err)
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

	newSettings := SettingsData{
		BudgetLimit: 0,
		Currency:    "NGN",
		Categories:  []string{"Food", "Transport", "Bills"},
	}
	if err := saveSettings(userID, newSettings); err != nil {
		serverError(w, err)
		return
	}
	if err := clearCategoryBudgets(userID); err != nil {
		serverError(w, err)
		return
	}
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
		serverError(w, err)
		return
	}
	existing.BudgetLimit = 0
	if err := saveSettings(userID, existing); err != nil {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/settings/saved", http.StatusSeeOther)
}

// supportHandler serves the contact page. It is behind requireAuth because
// publishing contact details to anonymous visitors invites scraping; users
// reach it from Settings, from Billing, and from the Terms page.
func supportHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)

	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}

	// A pre-filled WhatsApp message saves the user from having to explain
	// what app they are writing about. Spaces are encoded by hand because
	// wa.me rejects the "+"-encoded spaces that url.QueryEscape emits.
	msg := "Hi, I need help with Budget Tracker."
	msg = strings.ReplaceAll(url.QueryEscape(msg), "+", "%20")

	data := SupportPageData{
		PageChrome: PageChrome{
			Username:       username,
			CurrencySymbol: currencySymbol(settings.Currency),
			Version:        appVersion,
		},
		Email:        supportEmail,
		Phone:        supportPhone,
		PhoneLink:    strings.ReplaceAll(supportPhone, " ", ""),
		WhatsApp:     supportWhatsApp,
		WhatsAppText: msg,
	}

	if err := tmpl.ExecuteTemplate(w, "support.html", data); err != nil {
		if !isClientDisconnect(err) {
			log.Printf("failed to render support template: %v", err)
		}
	}
}

func expensesRawHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, _, _ := currentUser(r)

	rows, err := db.Query(
		`SELECT amount, category, to_char(created_at,'YYYY-MM-DD HH24:MI:SS') FROM expenses WHERE user_id = $1 AND kind = 'expense' AND deleted_at IS NULL ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		serverError(w, err)
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
			serverError(w, err)
			return
		}
		data = append(data, e)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func exportPageHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)
	month := normalizeMonth(r.URL.Query().Get("month"))

	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}

	if err := tmpl.ExecuteTemplate(w, "export.html", map[string]interface{}{
		"Username":       username,
		"Month":          month,
		"Label":          monthLabel(month),
		"PrevMonth":      addMonth(month, -1),
		"NextMonth":      addMonth(month, 1),
		"CurrentMonth":   currentMonth(),
		"CurrencySymbol": currencySymbol(settings.Currency),
		"Version":        appVersion,
	}); err != nil {
		log.Printf("failed to render export template: %v", err)
	}
}

func exportCSVHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, _, _ := currentUser(r)
	month := normalizeMonth(r.URL.Query().Get("month"))
	start, end := monthRange(month)

	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}

	rows, err := db.Query(
		`SELECT description, amount, category, kind, to_char(created_at,'YYYY-MM-DD HH24:MI:SS') FROM expenses
		 WHERE user_id = $1 AND deleted_at IS NULL AND created_at >= $2::timestamp AND created_at < $3::timestamp
		 ORDER BY created_at ASC`,
		userID, start, end,
	)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()

	filename := fmt.Sprintf("expenses_%s.csv", month)
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	writer := csv.NewWriter(w)
	defer writer.Flush()

	code := strings.ToUpper(strings.TrimSpace(settings.Currency))
	if !knownCurrency(code) {
		code = "NGN"
	}
	writer.Write([]string{"Kind", "Description", "Amount (" + code + ")", "Category", "Date"})

	var spent, received float64
	count := 0
	for rows.Next() {
		var desc, category, kind, createdAt string
		var amount float64
		if err := rows.Scan(&desc, &amount, &category, &kind, &createdAt); err != nil {
			continue
		}
		if category == "" {
			category = "Uncategorized"
		}
		writer.Write([]string{kind, desc, fmt.Sprintf("%.2f", amount), category, createdAt})
		if kind == kindIncome {
			received += amount
		} else {
			spent += amount
		}
		count++
	}

	writer.Write([]string{})
	writer.Write([]string{"Total spent", fmt.Sprintf("%.2f", spent), "", "", ""})
	writer.Write([]string{"Total received", fmt.Sprintf("%.2f", received), "", "", ""})
	writer.Write([]string{"Count", fmt.Sprintf("%d", count), "", "", ""})
}

func commaGroups(v float64) string {
	return commaGroupsPrec(v, 2)
}

func commaGroupsPrec(v float64, prec int) string {
	s := strconv.FormatFloat(v, 'f', prec, 64)
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	intPart, frac := s, ""
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i:]
	}
	var b strings.Builder
	for i, r := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + b.String() + frac
}

var currencySymbols = map[string]string{
	"NGN": "₦",
	"USD": "$",
	"EUR": "€",
	"GBP": "£",
	"JPY": "¥",
	"CNY": "¥",
	"INR": "₹",
	"ZAR": "R",
	"KES": "KSh ",
	"GHS": "GH₵",
	"CAD": "CA$",
	"AUD": "A$",
}

type CurrencyOption struct {
	Code  string
	Label string
}

var currencyOptions = []CurrencyOption{
	{"NGN", "Nigerian Naira (₦)"},
	{"USD", "US Dollar ($)"},
	{"EUR", "Euro (€)"},
	{"GBP", "Pound Sterling (£)"},
	{"KES", "Kenyan Shilling (KSh)"},
	{"GHS", "Ghanaian Cedi (GH₵)"},
	{"ZAR", "South African Rand (R)"},
	{"INR", "Indian Rupee (₹)"},
	{"CAD", "Canadian Dollar (CA$)"},
	{"AUD", "Australian Dollar (A$)"},
	{"JPY", "Japanese Yen (¥)"},
	{"CNY", "Chinese Yuan (¥)"},
}

func currencySymbol(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return currencySymbols["NGN"]
	}
	if sym, ok := currencySymbols[code]; ok {
		return sym
	}
	return code + " "
}

func knownCurrency(code string) bool {
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, o := range currencyOptions {
		if o.Code == code {
			return true
		}
	}
	return false
}

func exportPDFHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)
	month := normalizeMonth(r.URL.Query().Get("month"))
	start, end := monthRange(month)

	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}
	symbol := template.HTMLEscapeString(currencySymbol(settings.Currency))

	rows, err := db.Query(
		`SELECT description, amount, category, kind, to_char(created_at,'DD Mon YYYY') FROM expenses
		 WHERE user_id = $1 AND deleted_at IS NULL AND created_at >= $2::timestamp AND created_at < $3::timestamp
		 ORDER BY created_at ASC`,
		userID, start, end,
	)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()

	type expRow struct {
		Description string
		Amount      float64
		Category    string
		Kind        string
		CreatedAt   string
	}
	var expenses []expRow
	var spent, received float64
	for rows.Next() {
		var e expRow
		if err := rows.Scan(&e.Description, &e.Amount, &e.Category, &e.Kind, &e.CreatedAt); err != nil {
			continue
		}
		if e.Category == "" {
			e.Category = "Uncategorized"
		}
		expenses = append(expenses, e)
		if e.Kind == kindIncome {
			received += e.Amount
		} else {
			spent += e.Amount
		}
	}

	catMap := map[string]float64{}
	for _, e := range expenses {
		if e.Kind == kindIncome {
			continue
		}
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
		if spent > 0 {
			pct = math.Round((total/spent)*1000) / 10
		}
		cats = append(cats, catRow{cat, total, pct})
	}
	sort.Slice(cats, func(i, j int) bool {
		if cats[i].Total == cats[j].Total {
			return cats[i].Category < cats[j].Category
		}
		return cats[i].Total > cats[j].Total
	})

	label := monthLabel(month)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	receivedKPI := ""
	if received > 0 {
		receivedKPI = fmt.Sprintf(
			"\n    <div class=\"kpi\"><div class=\"kpi-label\">Received</div><div class=\"kpi-value num\">%s%s</div></div>",
			symbol, commaGroups(received))
	}

	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<title>Expenses %s</title>
<style>
  :root{
    color-scheme:light;
    --ink:#0f172a;
    --ink-muted:#5b6b82;
    --line:#e2e8f0;
    --line-strong:#cbd5e1;
    --surface:#ffffff;
    --brand:#185fa5;
    --brand-strong:#0c447c;
    --brand-soft:#eaf3fc;
    print-color-adjust:exact;
    -webkit-print-color-adjust:exact;
  }
  *{box-sizing:border-box;margin:0;padding:0}
  body{font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;font-size:13px;
    line-height:1.5;color:var(--ink);background:var(--surface);-webkit-font-smoothing:antialiased}
  .sheet{max-width:720px;margin:0 auto;padding:40px 32px 48px}
  .num{font-variant-numeric:tabular-nums}
  .print-btn{display:inline-flex;align-items:center;gap:8px;margin-bottom:28px;background:var(--brand);
    color:#fff;border:0;border-radius:8px;padding:10px 18px;font:inherit;font-size:13px;
    font-weight:500;cursor:pointer}
  .print-btn:hover{background:var(--brand-strong)}
  .print-btn:focus-visible{outline:2px solid var(--brand);outline-offset:2px}
  .print-btn svg{width:16px;height:16px}
  .doc-head{display:flex;justify-content:space-between;align-items:flex-start;gap:24px;
    padding-bottom:18px;border-bottom:1px solid var(--line-strong);margin-bottom:26px}
  .doc-title{font-size:20px;font-weight:600;letter-spacing:-0.01em}
  .doc-sub{margin-top:3px;font-size:13px;color:var(--ink-muted)}
  .doc-meta{text-align:right;font-size:12px;color:var(--ink-muted);line-height:1.75;white-space:nowrap}
  .doc-meta strong{color:var(--ink);font-weight:600}
  .kpis{display:grid;grid-template-columns:1.5fr 1fr 1fr;gap:12px;margin-bottom:30px}
  .kpi{border:1px solid var(--line);border-radius:10px;padding:14px 16px}
  .kpi--lead{background:var(--brand-soft);border-color:transparent}
  .kpi-label{font-size:11px;font-weight:600;letter-spacing:.06em;text-transform:uppercase;
    color:var(--ink-muted);margin-bottom:6px}
  .kpi-value{font-size:24px;font-weight:600;letter-spacing:-0.01em}
  .kpi--lead .kpi-value{font-size:30px}
  table{width:100%%;border-collapse:collapse;margin-bottom:30px}
  thead th{font-size:11px;font-weight:600;letter-spacing:.06em;text-transform:uppercase;
    color:var(--ink-muted);text-align:left;padding:0 12px 9px;border-bottom:1px solid var(--line-strong)}
  th.amount,td.amount{text-align:right}
  tbody td{padding:9px 12px;border-bottom:1px solid var(--line);vertical-align:top}
  td.amount{font-weight:500;white-space:nowrap;font-variant-numeric:tabular-nums}
  td.date{color:var(--ink-muted);white-space:nowrap;font-variant-numeric:tabular-nums}
  tbody tr:last-child td{border-bottom:0}
  td.empty{padding:22px 12px;color:var(--ink-muted);text-align:center}
  tfoot td{padding:12px;border-top:1px solid var(--line-strong);font-weight:600;font-size:13.5px}
  tfoot td.amount{font-variant-numeric:tabular-nums}
  .section-title{font-size:11px;font-weight:600;letter-spacing:.06em;text-transform:uppercase;
    color:var(--ink-muted);margin-bottom:14px}
  .cat-row{display:grid;grid-template-columns:136px 1fr 88px 48px;align-items:center;gap:12px;padding:5px 0}
  .cat-name{font-size:12.5px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
  .cat-track{background:var(--brand-soft);border-radius:4px;height:10px}
  .cat-bar{background:var(--brand);height:10px;border-radius:0 4px 4px 0;min-width:3px}
  .cat-total{font-size:12.5px;font-weight:500;text-align:right;white-space:nowrap;
    font-variant-numeric:tabular-nums}
  .cat-pct{font-size:11.5px;color:var(--ink-muted);text-align:right;font-variant-numeric:tabular-nums}
  .doc-foot{margin-top:36px;padding-top:14px;border-top:1px solid var(--line);font-size:11px;
    color:var(--ink-muted);display:flex;justify-content:space-between;gap:16px}
  @media print{
    @page{size:A4;margin:14mm}
    body{font-size:11.5px}
    .sheet{max-width:none;padding:0}
    .no-print{display:none!important}
    thead{display:table-header-group}
    tr,.cat-row,.kpi{break-inside:avoid}
    .cat-track,.cat-bar{print-color-adjust:exact;-webkit-print-color-adjust:exact}
  }
</style>
</head>
<body>
<div class="sheet">
  <button class="print-btn no-print" type="button" onclick="window.print()"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><use href="/static/icons.svg#printer"/></svg> Save as PDF</button>
  <header class="doc-head">
    <div>
      <h1 class="doc-title">Expense report</h1>
      <div class="doc-sub">%s</div>
    </div>
    <div class="doc-meta">
      <div>Prepared for <strong>%s</strong></div>
      <div>Generated %s</div>
      <div>Budget Tracker</div>
    </div>
  </header>
  <section class="kpis">
    <div class="kpi kpi--lead"><div class="kpi-label">Total spent</div><div class="kpi-value num">%s%s</div></div>%s
    <div class="kpi"><div class="kpi-label">Entries</div><div class="kpi-value num">%d</div></div>
    <div class="kpi"><div class="kpi-label">Categories</div><div class="kpi-value num">%d</div></div>
  </section>
  <table>
    <thead><tr><th>Description</th><th>Kind</th><th>Category</th><th>Date</th><th class="amount">Amount (%s)</th></tr></thead>
    <tbody>`,
		template.HTMLEscapeString(label), template.HTMLEscapeString(label),
		template.HTMLEscapeString(username),
		time.Now().Format("2 Jan 2006"),
		symbol, commaGroups(spent), receivedKPI, len(expenses), len(cats),
		symbol,
	)

	for _, e := range expenses {
		fmt.Fprintf(w, `<tr><td>%s</td><td class="kind">%s</td><td>%s</td><td class="date">%s</td><td class="amount">%s</td></tr>`,
			template.HTMLEscapeString(e.Description),
			template.HTMLEscapeString(e.Kind),
			template.HTMLEscapeString(e.Category),
			template.HTMLEscapeString(e.CreatedAt),
			commaGroups(e.Amount),
		)
	}

	if len(expenses) == 0 {
		fmt.Fprintf(w, `<tr><td class="empty" colspan="5">No entries recorded for this month.</td></tr>`)
	}

	fmt.Fprintf(w, `</tbody>
<tfoot><tr><td colspan="4">Total spent</td><td class="amount">%s%s</td></tr>`,
		symbol, commaGroups(spent))
	if received > 0 {
		fmt.Fprintf(w, `<tr><td colspan="4">Total received</td><td class="amount">%s%s</td></tr>`,
			symbol, commaGroups(received))
	}
	fmt.Fprintf(w, `</tfoot>
</table>`)

	if len(cats) > 0 {
		fmt.Fprintf(w, `<section><h2 class="section-title">Category breakdown</h2>`)
		for _, c := range cats {
			fmt.Fprintf(w, `<div class="cat-row">
  <div class="cat-name" title="%s">%s</div>
  <div class="cat-track"><div class="cat-bar" style="width:%.1f%%"></div></div>
  <div class="cat-total">%s%s</div>
  <div class="cat-pct">%.1f%%</div>
</div>`,
				template.HTMLEscapeString(c.Category),
				template.HTMLEscapeString(c.Category), c.Pct,
				symbol, commaGroups(c.Total), c.Pct,
			)
		}
		fmt.Fprintf(w, `</section>`)
	}

	fmt.Fprintf(w, `<footer class="doc-foot"><span>Budget Tracker — monthly expense export</span><span>Generated %s</span></footer>
</div>
</body></html>`, time.Now().Format("2 Jan 2006 15:04"))
}
