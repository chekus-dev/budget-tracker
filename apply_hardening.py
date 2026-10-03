#!/usr/bin/env python3
"""
Applies the security/hardening edits to your Budget Tracker main.go in place.

Usage (run from anywhere; keep this script and hardening.go together):

    python3 apply_hardening.py /path/to/your/project          # apply
    python3 apply_hardening.py /path/to/your/project --dry-run  # check only

What it does:
  * backs up main.go to main.go.bak
  * edits main.go (every edit must match exactly once, otherwise nothing is
    written and you are told which one failed)
  * copies hardening.go next to main.go
  * adds the session_version migration to migrations/ (next free number)
  * changes minlength="6" to "8" in templates/*.html

Matching ignores indentation, so it works whatever your whitespace is.
"""
import re
import shutil
import subprocess
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
args = [a for a in sys.argv[1:] if not a.startswith("--")]
DRY = "--dry-run" in sys.argv
PROJECT = Path(args[0] if args else ".").resolve()
MAIN = PROJECT / "main.go"

if not MAIN.exists():
    sys.exit(f"main.go not found in {PROJECT}. Pass your project folder as the first argument.")

src = MAIN.read_text(encoding="utf-8")

if "sessionStillValid(" in src:
    sys.exit("main.go already contains the hardening edits. Nothing to do.")


def block_pattern(anchor: str) -> re.Pattern:
    """Regex matching `anchor` line by line, ignoring indentation."""
    lines = [l.strip() for l in anchor.strip("\n").splitlines()]
    body = r"[ \t]*\r?\n[ \t]*".join(re.escape(l) for l in lines)
    return re.compile(r"^[ \t]*" + body, re.M)


failures = []


def edit(name: str, anchor: str, replacement: str) -> None:
    global src
    pat = block_pattern(anchor)
    found = list(pat.finditer(src))
    if len(found) != 1:
        failures.append(f"{name}: expected 1 match, found {len(found)}")
        return
    src = pat.sub(lambda m: replacement.strip("\n"), src, count=1)


def edit_func(name: str, start: str, end_marker: str, replacement: str) -> None:
    """Replace from a line starting with `start` up to (not including) end_marker."""
    global src
    pat = re.compile(r"^" + re.escape(start) + r".*?(?=^" + re.escape(end_marker) + r")", re.M | re.S)
    found = list(pat.finditer(src))
    if len(found) != 1:
        failures.append(f"{name}: expected 1 match, found {len(found)}")
        return
    src = pat.sub(lambda m: replacement.strip("\n") + "\n\n", src, count=1)


# ---------------------------------------------------------------- main()

edit(
    "remove public /reports route",
    'mux.Handle("/reports/", http.StripPrefix("/reports/", http.FileServer(http.Dir("reports"))))',
    "\t// /reports/ was served publicly with no login and nothing used it, so it was removed.",
)

edit(
    "server timeouts + security headers",
    """
log.Println("server running at http://localhost:" + port)
log.Fatal(http.ListenAndServe(":"+port, mux))
""",
    """
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Println("server running at http://localhost:" + port)
	log.Fatal(srv.ListenAndServe())
""",
)

# ---------------------------------------------------------------- requireAuth

edit(
    "requireAuth session version check",
    "if _, _, ok := currentUser(r); !ok {",
    """
		userID, _, ok := currentUser(r)
		if !ok || !sessionStillValid(r, userID) {
""",
)

# ---------------------------------------------------------------- register

edit(
    "register password rule",
    """
if username == "" || len(password) < 6 {
reject("Username is required and password must be at least 6 characters.")
return
}
""",
    """
		if username == "" {
			reject("Username is required.")
			return
		}
		if msg := validateNewPassword(password); msg != "" {
			reject(msg)
			return
		}
""",
)

edit(
    "register session stamp",
    """
session.Values["user_id"] = userID
session.Values["username"] = username
""",
    """
		session.Values["user_id"] = userID
		session.Values["username"] = username
		session.Values["sv"] = 0 // new account starts at session version 0
""",
)

# ---------------------------------------------------------------- login

edit(
    "login lookup + timing equaliser",
    """
var id int
var passwordHash string
err := db.QueryRow("SELECT id, password_hash FROM users WHERE username = $1", username).Scan(&id, &passwordHash)
if err != nil {
""",
    """
		var id, sessionVersion int
		var passwordHash string
		err := db.QueryRow(
			"SELECT id, password_hash, session_version FROM users WHERE username = $1", username,
		).Scan(&id, &passwordHash, &sessionVersion)
		if err != nil {
			// Same bcrypt cost as a wrong password, so a missing account is not
			// faster to reject than an existing one.
			bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
""",
)

edit(
    "login session stamp",
    """
session.Values["user_id"] = id
session.Values["username"] = username
""",
    """
		session.Values["user_id"] = id
		session.Values["username"] = username
		session.Values["sv"] = sessionVersion
""",
)

# ---------------------------------------------------------------- change password

edit(
    "change-password length rule",
    """
if len(newPassword) < 6 {
render(ChangePasswordPageData{Error: "New password must be at least 6 characters."})
return
}
""",
    """
		if msg := validateNewPassword(newPassword); msg != "" {
			render(ChangePasswordPageData{Error: msg})
			return
		}
""",
)

edit(
    "change-password update + session re-stamp",
    """
if _, err := db.Exec("UPDATE users SET password_hash = $1 WHERE id = $2", string(newHash), userID); err != nil {
http.Error(w, err.Error(), http.StatusInternalServerError)
return
}

render(ChangePasswordPageData{Success: "Password updated."})
return
""",
    """
		// Bumping session_version signs out every other device. This session is
		// re-stamped below so the user stays signed in here. Any pending reset
		// link is cleared too.
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
""",
)

# ---------------------------------------------------------------- forgot password

edit(
    "forgot-password stores hash",
    "token, expires, userID,",
    "\t\t\thashResetToken(token), expires, userID,",
)

edit(
    "forgot-password sends in background",
    """
if err := sendResetEmail(email, resetLink); err != nil {
log.Printf("failed to send reset email to %s: %v", email, err)
// Don't leak the failure to the client — same generic message either way.
}
""",
    """
		// In the background so a real account and a missing one answer in the
		// same time; otherwise the Resend round trip reveals which emails are
		// registered. The request context is not used because it is cancelled
		// the moment the response is written.
		go func() {
			if err := sendResetEmail(email, resetLink); err != nil {
				log.Printf("failed to send reset email: %v", err)
			}
		}()
""",
)

# ---------------------------------------------------------------- reset password

RESET_FUNC = r'''
func resetPasswordHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}

	// The token is in the URL, so keep it out of Referer headers and caches.
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

		// Look the token up first, without using it up, so a typo in the
		// password fields does not burn the link.
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

		// Check and consume in one statement. If two requests race with the
		// same link, only one of them matches a row; the other sees zero rows
		// affected. Also signs out every existing session.
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

	// GET: validate the token before showing the form.
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
'''

edit_func(
    "reset-password handler rewrite",
    "func resetPasswordHandler(",
    "// ---------- Settings ----------",
    RESET_FUNC,
)

# ---------------------------------------------------------------- monthly reset

MONTHLY_FUNC = r'''
func autoMonthlyReset() {
	// main() deliberately supports booting with no database (db == nil), so
	// there is nothing to do in that case.
	if !dbReady() {
		return
	}
	// Wiping last month's expenses for every user also empties the Archive
	// page, so it is off unless AUTO_MONTHLY_RESET=true is set.
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

		// Soft-deleted rows are invisible to every query, so without this they
		// would accumulate forever. Well past the undo window by now.
		if _, err := db.Exec("DELETE FROM expenses WHERE deleted_at IS NOT NULL AND deleted_at < NOW() - INTERVAL '30 days'"); err != nil {
			log.Printf("purge of soft-deleted expenses failed: %v", err)
		}
	}
}
'''

edit_func(
    "auto monthly reset opt-in",
    "func autoMonthlyReset() {",
    "func resetSettingsHandler(",
    MONTHLY_FUNC,
)

# ---------------------------------------------------------------- raw error leaks (last, so earlier anchors still match)

ERR_RE = re.compile(r"http\.Error\(w, err\.Error\(\), http\.StatusInternalServerError\)")
leaks = len(ERR_RE.findall(src))
src = ERR_RE.sub("serverError(w, err)", src)

# ---------------------------------------------------------------- report

if failures:
    print("Nothing was written. These edits did not match your main.go exactly once:\n")
    for f in failures:
        print("  -", f)
    print("\nThe file probably differs from the version you sent me. Send the current main.go and I will adjust.")
    sys.exit(1)

print(f"All {13} anchored edits matched; {leaks} raw error responses replaced with serverError().")

if DRY:
    print("--dry-run: no files changed.")
    sys.exit(0)

# main.go
shutil.copy2(MAIN, PROJECT / "main.go.bak")
MAIN.write_text(src, encoding="utf-8")
print("main.go updated (backup: main.go.bak)")

# hardening.go
HARD_SRC = HERE / "hardening.go"
HARD_DST = PROJECT / "hardening.go"
if not HARD_SRC.exists():
    print("WARNING: hardening.go not found next to this script; copy it into your project yourself.")
elif HARD_DST.exists() and HARD_DST.read_bytes() == HARD_SRC.read_bytes():
    print("hardening.go already in place")
else:
    shutil.copy2(HARD_SRC, HARD_DST)
    print("hardening.go added")

# migration
SQL_SRC = HERE / "session_version.sql"
mig_dir = PROJECT / "migrations"
if mig_dir.is_dir():
    existing = sorted(p.name for p in mig_dir.glob("*.sql"))
    if any("session_version" in n for n in existing):
        print("session_version migration already present")
    elif SQL_SRC.exists():
        nums = [(int(m.group(1)), len(m.group(1))) for n in existing if (m := re.match(r"(\d+)_", n))]
        if nums:
            top, width = max(nums)
            name = f"{top + 1:0{width}d}_session_version.sql"
        else:
            name = "001_session_version.sql"
        shutil.copy2(SQL_SRC, mig_dir / name)
        print(f"migration added: migrations/{name}  (check it follows your naming and that applyMigrations picks it up)")
else:
    print("NOTE: no migrations/ folder found. Run session_version.sql against your database or add it to your migrations yourself.")

# templates: old 6-character hint
tpl_dir = PROJECT / "templates"
if tpl_dir.is_dir():
    changed = 0
    for p in tpl_dir.glob("*.html"):
        t = p.read_text(encoding="utf-8")
        new = t.replace('minlength="6"', 'minlength="8"')
        if new != t:
            p.write_text(new, encoding="utf-8")
            changed += 1
    print(f'templates: minlength="6" -> "8" in {changed} file(s)')
    hits = subprocess.run(
        ["grep", "-nE", "6 char|six char", *map(str, tpl_dir.glob("*.html"))],
        capture_output=True, text=True,
    ).stdout.strip()
    if hits:
        print("Text that still mentions 6 characters (edit by hand):\n" + hits)

# format + build if Go is installed
if shutil.which("gofmt"):
    subprocess.run(["gofmt", "-w", str(MAIN), str(PROJECT / "hardening.go")])
    print("gofmt applied")
if shutil.which("go"):
    r = subprocess.run(["go", "build", "./..."], cwd=PROJECT, capture_output=True, text=True)
    print("go build: OK" if r.returncode == 0 else "go build FAILED:\n" + r.stderr)
else:
    print("Go not found on PATH; run `go build ./... && go vet ./...` yourself.")
