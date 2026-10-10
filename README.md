<div align="center">

# 💰 Budget Tracker

**A personal finance app built from the ground up — Go, PostgreSQL, and a vanilla JS frontend, with no framework hiding the hard parts.**

<img width="1300" height="884" alt="Budget Tracker screenshot" src="https://github.com/user-attachments/assets/65d3a5c3-cd5e-448b-8515-a0c9923dc326" />

<br/><br/>

![Go](https://img.shields.io/badge/Go-00ADD8?style=for-the-badge&logo=go&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-4169E1?style=for-the-badge&logo=postgresql&logoColor=white)
![Supabase](https://img.shields.io/badge/Supabase-3ECF8E?style=for-the-badge&logo=supabase&logoColor=white)
![Tailwind](https://img.shields.io/badge/Tailwind_v4-06B6D4?style=for-the-badge&logo=tailwindcss&logoColor=white)
![Python](https://img.shields.io/badge/Python-3776AB?style=for-the-badge&logo=python&logoColor=white)

[![Live Demo](https://img.shields.io/badge/▶_Live_Demo-budget--tracker-f97316?style=for-the-badge)](https://budget-tracker-1-svws.onrender.com/)

</div>

<br/>

A personal budget tracking web application built with Go, PostgreSQL, and a lightweight vanilla JavaScript frontend. The project began as a simple exercise in connecting Go to a MySQL database, and grew incrementally into a working full-stack application with a server-rendered interface, dynamic client-side interactions, and a companion Python reporting tool. It now runs against a hosted PostgreSQL database on Supabase.

This README documents the project honestly — including the setbacks — because the process of building it is as much a part of the learning as the finished code.

---

## ✨ What It Does

- Log expenses with a description, an amount and a date (displayed in Naira, ₦)
- Choose the currency from Settings — twelve are offered, and the symbol follows
  the amounts everywhere, including the CSV and PDF exports
- Set a monthly budget and see what is **left to spend** at the top of the home
  screen, with the amount already spent shown separately
- Get a warning at 80% of the budget and again when it is exceeded — but never
  be blocked from logging an expense
- **Search and filter** every expense you have ever logged — by text, category
  or date range — without leaving the home screen
- **Edit an expense in place**, reusing the same form the add button opens
- Browse previous months, switch between them, and reset a month (with a
  confirmation step)
- See a report of spending by category, with summary statistics
- Export a month as a CSV spreadsheet or a print-ready PDF
- Delete an expense and undo it from a toast that stays on screen for a few
  seconds
- Sign in, register, and reset a forgotten password by email
- Light and dark themes across every screen; the preference is remembered per browser
- Generate a spending report and monthly chart from the same data, using a
  separate Python script

---

## 🧰 Tech Stack

| Layer              | Technology                                                 |
|--------------------|-------------------------------------------------------------|
| Backend            | Go (`net/http`, `database/sql`)                              |
| Database           | PostgreSQL 16, hosted on Supabase                            |
| Templating         | Go's `html/template`                                         |
| Frontend styling   | Tailwind CSS v4, compiled by a build step (see below)        |
| Frontend behavior  | Server-rendered form POSTs, with vanilla JS for enhancement  |
| Reporting          | Python (`pandas`, `matplotlib`, `psycopg`)                   |

No frontend framework and no ORM were used, deliberately. The goal was
to understand each layer directly — raw SQL queries, a real HTTP
server, and DOM manipulation without abstraction — before reaching for
tools that hide those details.

The interface is server-rendered first: adding, deleting and restoring an
expense are ordinary `<form>` POSTs that reload the page, so the database is
always the single source of truth and every screen works with JavaScript
disabled. JavaScript only adds to that — the modals, the undo toast, the
navigation highlight and the loading skeleton.

---

## 🛠️ How This Project Was Built

### 1. Establishing the connection

The project started with the smallest possible piece: a single Go file
that opened a connection to a local MySQL database and printed
`connected to database`. Nothing else. Before writing any application
logic, it mattered to confirm that Go and MySQL could actually talk to
each other.

This step turned out to be the hardest part of the entire project —
not because of Go, but because of the local MySQL installation itself.

### 2. A detour through MySQL installation trouble

Setting up MySQL locally did not go smoothly, and it is worth recording
what happened, since working through it was itself a real exercise in
systems debugging:

- The initial installation left `mysql-common` in a "removed but not
  purged" state, and `systemctl` could not find a `mysql.service` unit
  at all.
- After reinstalling, root authentication repeatedly failed. MySQL 8.4
  had dropped support for the `mysql_native_password` plugin by
  default, which the original setup instructions assumed, requiring a
  switch to `caching_sha2_password` instead.
- A stuck `mysqld` process, started in `--skip-grant-tables` recovery
  mode, could not be killed — not even by root. Investigation via
  `dmesg` revealed that AppArmor was actively denying the kill signal
  at the kernel level, a subtlety that is easy to overlook when a
  process simply "won't die."
- The eventual fix was a full reinstall: purging MySQL entirely,
  removing its data directory, and reinitializing from a clean state
  with `mysqld --initialize-insecure`, followed by carefully resetting
  the root password and confirming each step before moving to the
  next.

This stretch of the project produced no application code at all, but
it was where most of the real learning happened — about systemd, about
Linux process signals, and about the discipline of verifying each step
before building on top of it.

### 3. First working connection and first query

Once MySQL was stable, the original goal was reached: a Go program
that connected successfully, inserted a row into an `expenses` table,
and read it back. This was deliberately tested from the command line
before any web server was introduced, to isolate database logic from
HTTP logic.

### 4. Building the web server

With the database layer proven, an HTTP server was introduced using
Go's standard library alone. The first route rendered an HTML page
listing expenses from the database. A second route accepted form
submissions and inserted new expenses. Each route was tested
individually — the list page first, with a non-functional form, before
the insert logic was added — rather than writing both at once.

### 5. Interface and usability

Once the core functionality worked, attention moved to the interface:

- A styled layout was added using Tailwind CSS, with a defined color
  direction (a muted emerald-green palette, chosen deliberately to
  suit a finance-oriented application) rather than default styling.
- Responsive breakpoints were added so the layout adapts across
  mobile, tablet, and desktop widths.
- Naira (₦) was used as the currency throughout, in place of the
  default dollar formatting.

### 6. Client-side interactivity

A vanilla JavaScript layer was added in stages, each verified before
the next was introduced:

1. **Client-side validation** — preventing empty descriptions or
   non-positive amounts from being submitted at all.
2. **No-reload submission** — intercepting the form with `fetch()` so
   new expenses appear instantly, without a full page reload. This
   required the Go backend to support two response modes: a full HTML
   page for a normal request, and just the updated expense list for a
   JavaScript-originated one, distinguished by a custom request
   header.
3. **A live-updating total** — as an amount is typed, the total shown
   updates in real time to preview the effect of the pending entry,
   before it is submitted.
4. **Deletion** — a delete endpoint was added on the Go side, and a
   delete control on each expense row, both using the same no-reload
   `fetch()` pattern established for adding expenses.

### 7. A Python reporting companion

Finally, a separate Python script was added alongside the Go
application. It connects to the same PostgreSQL database independently,
and produces:

- A printed summary (total spent, average expense, largest expense)
- A CSV export of all logged expenses
- A monthly spending bar chart, saved as an image

This was kept as a standalone script rather than folded into the Go
application, to demonstrate that the underlying data is not tied to
any single tool or language — it is a PostgreSQL database that multiple
programs can read independently.

### 8. Moving the database off the machine

The original MySQL database lived on the same computer as the server,
which meant the app could only be run from that one machine. The
database was moved to a hosted PostgreSQL instance on Supabase so the
app is no longer tied to a local install.

The migration touched more than the connection string. MySQL-specific
SQL had to be translated:

- `?` placeholders became PostgreSQL's numbered `$1, $2, …` form
- `DATE_FORMAT(created_at, '%Y-%m')` became `to_char(created_at, 'YYYY-MM')`
- `ON DUPLICATE KEY UPDATE` became `ON CONFLICT (user_id) DO UPDATE`
- `res.LastInsertId()` had to be replaced entirely, because PostgreSQL
  does not return one — the insert now ends in `RETURNING id`. This was
  the one that would have failed silently rather than loudly, since the
  original code discarded the error and would have gone on to save every
  new user's settings against user id `0`.

The Go driver changed from `go-sql-driver/mysql` to `pgx`, and the
Python report moved from `mysql-connector-python` to `psycopg`.

### 9. Unifying the interface

By this point the app worked, but it did not look like one product. Each of
the thirteen templates carried its own `<style>` block — roughly 2,500 lines
of CSS in total — and they had drifted apart in ways that were only obvious
once compared side by side:

- `export.html` used `--surface` / `--bg` / `--text`; every other page used
  `--surface-light` / `--bg-light` / `--text-primary`
- `--success` was `#10b981` on one page and `#16a34a` on two others
- dark mode was applied as a class on `body` in some templates and on `html`
  in others, so the auth pages never went dark at all
- `manifest.json` and the app icons were linked as `/static/…` but actually
  lived in `templates/`, so every one of them returned 404
- the report chart plotted spending by *day* while the server was already
  computing category totals, and a 5-second polling loop overwrote the
  server-rendered statistics with its own

The fix was to replace all thirteen stylesheets with one compiled Tailwind v4
build plus a small component layer, and to give every page a shared `<head>`
partial and a shared navigation partial. Two rules kept the result coherent:
colour is expressed as semantic tokens (`surface`, `ink`, `brand`, `safe`,
`near`, `over`) rather than literal colour names, so dark mode is a token swap
and not a second set of rules; and no status is ever signalled by colour
alone — the progress bar and the alerts always carry text as well.

Deleting an expense also changed. It used to be a hard `DELETE` with no
confirmation. It is now a soft delete (`deleted_at`) that raises an **Undo**
toast, with a nightly purge for rows deleted more than 30 days ago. That in
turn meant auditing every query that reads `expenses` to filter on
`deleted_at IS NULL` — including the ones that were easy to miss, like the
month list in the archive and the two export handlers.

One behaviour deliberately changed: the old code returned **403 Forbidden**
when an expense would push the user past their monthly budget. That is now a
warning banner instead. Refusing the entry does not stop the money being
spent; it only stops it being recorded.

---

## 🚀 Running the Project Locally

### Prerequisites

- Go 1.24 or later
- Node.js 22 or later (only to build the stylesheet)
- A PostgreSQL database — this project uses a free Supabase project
- Python 3.x (only required for the reporting script)

### Database setup

Create a project at [supabase.com](https://supabase.com). The app applies
[`migrations/`](migrations/) itself on startup — six short files, embedded in
the binary and run in order before it serves the first request — so on a fresh
database there is nothing to do here. Applied in sequence they create the
`users`, `settings`, and `expenses` tables the app expects, and since every
statement is idempotent, restarts and redeploys are no-ops.

To run them by hand instead — to see the SQL in front of you, or to repair a
database — open the **SQL Editor** and run the files in numeric order. See
[`migrations/README.md`](migrations/README.md) for the full list and for
running them from the command line.

Alternatively, [`schema_postgres.sql`](schema_postgres.sql) is the same schema
in a single file, for a database that has no data in it yet. Use one or the
other, not both.

There is no `CREATE DATABASE` step: Supabase already provides a `postgres`
database, and PostgreSQL selects the database through the connection string
rather than through a `USE` statement.

### Environment variables

Copy the example file and fill it in:

```bash
cp .env.example .env
```

The one required value is `DATABASE_URL` — copy the connection string from
your Supabase project under **Project Settings → Database**. The `.env` file
is gitignored and is read automatically by both the Go server and the Python
script.

`SESSION_KEY` must also be set to at least 32 characters, or the server will
refuse to start:

```bash
openssl rand -base64 32
```

> Direct and pooler connection strings are supported. The app disables named
> prepared statements so Supabase's Transaction pooler can be used as well.

#### Optional: signup bot protection

New accounts must accept the Terms and Conditions and pass a Google reCAPTCHA
check. Create a free **v2 "I'm not a robot" checkbox** key pair at
<https://www.google.com/recaptcha/admin/create>, then add every domain the app
is served from — including `localhost` — to that key's allowed-domains list.
The widget refuses to render on a domain that is not listed.

```
RECAPTCHA_SITE_KEY=...
RECAPTCHA_SECRET_KEY=...
```

The site key is public and is rendered into the page. The secret key is used
only in the server-side `siteverify` call and must never be committed.

Leaving both unset is fine for local development: the widget is omitted and the
check is skipped, with a warning in the log. On a **deployed** (HTTPS) instance
a missing secret key makes signup **refuse**, rather than silently accepting
everyone — see `recaptchaRequired()` in [`main.go`](main.go) for why failing
closed was chosen over failing open.

### Building the stylesheet

The app's CSS is compiled from [`static/css/input.css`](static/css/input.css)
into `static/css/app.css`. Run this **before** starting the server, and again
after editing any template — Tailwind scans the templates to decide which
classes to emit, so a class that appears only in a template you have not
saved yet will be missing from the output.

```bash
npm install
npm run build:css     # one-off build
npm run watch:css     # rebuild on every save, while developing
```

`static/css/app.css` is committed, so a deploy that forgets the build step
still serves a working stylesheet — just a possibly stale one. `input.css` is
the only file to edit; never edit `app.css` directly.

If a class you added to a template has no effect, that is almost always this
build step rather than a typo. Note also that a class name cannot be assembled
at runtime by concatenation (`'progress-fill is-' + status`) — Tailwind's
scanner only ever sees complete literal strings, so such a class must be
written out in full somewhere it can be scanned.

### Running the web application

```bash
npm install && npm run build:css
go mod tidy
go run main.go
```

Then visit `http://localhost:4000` in a browser.

### A page that cannot render stops the server

On startup, every page template is executed once against a representative
value, and the process exits if any of them fails.

This is worth having because `html/template` fails at *execution* time, not
parse time. A page handed a value that lacks a field the shared navigation
reads renders correctly right up to that point and then stops — serving a
truncated document with a `200` status. Nothing surfaces it: `go build` is
happy, and the browser shows a half-page without complaining, so the symptom
reads like broken markup rather than a missing field. That is exactly how
adding a currency label to `nav.html` silently cut the change-password page in
half.

The fix for that class of bug is structural: every full-page data type embeds
a `PageChrome` struct holding what the shared chrome needs, so a new chrome
field is present on every page the moment it is declared. The startup check is
the backstop that proves it.

### Running the spending report

```bash
python3 -m venv venv
source venv/bin/activate
pip install -r reports/requirements.txt
python reports/spending_report.py
```

The script reads the same `DATABASE_URL` from `.env`. Output is written to
the `reports/` directory as a CSV file and a PNG chart.

---

## ☁️ Deploying to Render (Always On)

The app deploys to [Render](https://render.com) from this repo. The
[`render.yaml`](render.yaml) blueprint describes the service, so Render
picks up the build and start commands automatically.

### Steps

1. Run `go mod tidy` locally and commit the resulting `go.mod` and `go.sum`.
   The build needs pgx's checksums in `go.sum`, and fetching them during the
   Render build would not be reproducible.
2. Run `npm install` and commit the resulting `package-lock.json`. The
   `npm ci` in the build command installs exactly this lockfile and fails
   without it.
3. Push this repo to GitHub.
4. In Render: **New → Blueprint**, pick the repo. Render reads `render.yaml`.
5. When prompted, fill in the secrets (`DATABASE_URL` from Supabase; the
   `RESEND_*` values are optional). `SESSION_KEY` is generated for you.
6. Deploy. Render gives you a URL like `https://budget-tracker.onrender.com`.

Every push to `main` then redeploys automatically. The build runs
`npm ci && npm run build:css && go build -o budget-tracker .`, so the
stylesheet is rebuilt from `input.css` on every deploy rather than trusted
from the committed copy.

### Staying up 24/7

Render's **free** web services spin down after about 15 minutes without
traffic, then cold-start on the next visit. To be genuinely always on:

- **Paid instance (what `render.yaml` is set to).** `plan: starter` keeps
  the service running continuously. Change it to `plan: free` in
  [`render.yaml`](render.yaml) if you would rather not pay.
- **Free instance plus a keep-awake ping.** Leave the plan as `free` and
  point a free external monitor — [UptimeRobot](https://uptimerobot.com)
  or [cron-job.org](https://cron-job.org) — at
  `https://<your-app>.onrender.com/healthz` every 10 minutes. The endpoint
  is public, returns 200, and does not require a login.

### What the app does differently in production

Two things that work locally but break behind a proxy on a real domain
were fixed for deployment:

- **Password-reset links** are now built from the app's public URL rather
  than from `r.TLS`. A hosting proxy terminates HTTPS in front of the app,
  so the request itself looks like plain HTTP and the old code generated
  `http://` links.
- **Session cookies** are marked `Secure` when the app is served over
  HTTPS, so browsers will not send them over a plaintext connection. This
  is detected automatically on Render and stays off for local development.
- **Signup is guarded.** Registering requires accepting the Terms and
  Conditions and passing a reCAPTCHA challenge. Both are checked on the
  server, not just in the browser — the HTML `required` attribute and the
  widget are conveniences that `curl` ignores. A deployed instance with no
  reCAPTCHA secret configured refuses signups outright instead of letting
  bots through.
- **Sign-in is rate limited.** See "Brute-force protection" below.

### Brute-force protection

The login form counts failures against two independent budgets, both on a
rolling 15-minute window:

| Budget | Limit | What it stops |
| --- | --- | --- |
| Per username | 5 failures | Someone sitting on one account guessing passwords |
| Per client IP | 20 failures | One password tried across many accounts — a spray that never trips the per-username budget, because each name is only tried once |

Once a budget is spent the request is answered with `429 Too Many Requests`,
a `Retry-After` header, and a message saying how long to wait. The counters
are checked *before* the database lookup and the bcrypt comparison, which is
the point: bcrypt is deliberately slow, and an unlimited caller would
otherwise be getting that work for free.

The password-reset form is limited the same way (3 per address, 10 per IP),
because it sends mail to an address the caller chooses. It charges the budget
*before* looking the address up, so a registered address and an unknown one
behave identically — charging only on a hit would turn the form into a way to
discover which emails have accounts, undoing the equal-response design it was
written with.

Two deliberate choices worth knowing about:

- **The window is short.** A permanent lockout would be a denial-of-service
  handed to the attacker: they could shut a real person out of their own
  account just by failing logins on that name. Fifteen minutes is a nuisance
  rather than a lockout, and still cuts an online guessing rate from
  thousands per minute to a handful per quarter hour.
- **State is in memory.** It is per-process and resets on deploy. For one
  always-on instance that is the right trade — a database round trip on every
  failed login would cost more than the protection is worth — but running two
  instances would need the counters moved to shared storage to stay correct.

Because the per-IP budget buckets by address, and a client can send its own
`X-Forwarded-For` header, `clientIP()` reads the **last** entry of that
header, not the first. A proxy appends the address it actually saw to
whatever arrived, so the first entry is attacker-chosen and the last is not.
Reading the first would have let a caller mint an unlimited supply of fresh
buckets.

There is also a new `GET /healthz` endpoint returning
`{"status":"ok","database":"ok"}`, used by Render's health checks and by
the keep-awake monitor.

### Note on the database

The Supabase free tier pauses a project after about a week with no
activity. Since `/healthz` pings the database, an always-on app keeps it
warm. If the app is allowed to sleep for long enough, the first request
afterwards may fail until Supabase wakes the project up again.

---

## 🔍 Searching, Filtering and Editing

The home screen carries a filter bar above the list: free text, a category,
and a from/to date range. Fill in any combination and the page switches from
the month view to a results view.

**A search covers every month, not the one on screen.** That was a deliberate
choice. "Where did I put that ₦80,000?" is a question about all of your
history, and answering it only within the month you happen to be looking at
would make the feature useless for its main purpose. Because the results
cross months, the month-scoped furniture — the budget hero, the month
switcher, the progress bar — is not shown on a results page. A monthly budget
cannot say anything meaningful about an arbitrary slice of history.

A few smaller decisions worth knowing about:

- **User input is never concatenated into SQL.** The `WHERE` clause is
  assembled from a fixed set of fragments, each contributing its own
  placeholder. The clauses are joined in a stable order, so the same search
  always produces the same statement and Postgres can reuse a prepared plan.
- **Wildcards typed by the user are treated as text.** Searching for `50%`
  looks for the literal characters `50%`, not "anything starting with 50".
  `likeEscape` neutralises `%`, `_` and the backslash itself.
- **A result set is capped at 500 rows.** The query asks for one row more than
  the cap, which is how the page knows the difference between "exactly this
  many" and "there are more". When it cuts off, the page says so and the total
  shown is the sum of the rows you can actually see — a headline figure that
  disagreed with the list under it would be worse than no figure.
- **An unusable date is dropped, not rejected.** A half-typed date narrows the
  search less; it does not fail the request. A backwards range (June to March)
  is swapped, because that is almost always a slip and an empty result would
  just look broken.
- **The search lives in the URL.** It is a GET form, so a search can be
  bookmarked, shared, and survived by a refresh.

Each row has a pencil button that opens the *same* modal the add button does,
pre-filled with that row. Sharing one form means the two paths cannot drift
apart: a field added for adding is a field added for editing, and the server
sees an identical submission either way, differing only in the URL it posts
to. Everything the modal needs rides on the button's `data-*` attributes, so
opening the editor is a local operation with no round trip.

Two details in the edit path are easy to get wrong:

- **The date is only rewritten when it actually changed.** The date input
  carries a day and no time, so writing it unconditionally would stamp today's
  clock time onto an expense you only wanted to re-price — quietly reordering
  it among that day's entries. The form carries the date it was rendered with,
  and the server compares the two.
- **Editing and deleting return you to your search.** Both rebuild the active
  filters from hidden form fields rather than replaying a stored URL, so a
  hand-edited value cannot turn the redirect into a way off the site.

---

## 📌 Known Limitations and Next Steps

This project is intentionally incremental, and several improvements
are planned rather than already built:

- **Credentials are read from the environment** in both `main.go` and
  `spending_report.py`, via the gitignored `.env` file. See `.env.example`
  for the full list.
- **Test coverage is thin.** The rate limiter and `clientIP` have unit
  tests (`ratelimit_test.go`), as do the filter parsing, the `LIKE`
  escaping and the date handling behind search (`search_test.go`); run
  them with `go test ./...`. The HTTP handlers and the database logic
  still have none. Adding those is a deliberate next step.
- **The theme preference is per-browser.** Light or dark is applied from
  `localStorage` before the first paint, and new browsers default to dark.
  Appearance is intentionally not stored in account settings.
- **Soft-deleted rows are only purged nightly.** An expense deleted in
  the last 30 days is still present in the `expenses` table with
  `deleted_at` set. It is invisible everywhere in the app, but it is
  there — worth knowing before inspecting the table directly.
- **Server-side validation is currently minimal.** The add-expense
  handler now rejects a non-numeric or negative amount and ignores a
  future date, but validation is still thinner than it should be.

---

## 📝 A Note on Process

This project was not built in a straight line, and this README does
not pretend otherwise. Long stretches of time went into fixing a local
MySQL installation before a single feature could be built, and several
features were built in a deliberately narrow order — one working piece
at a time — rather than all at once. That pace was a choice: each
layer was confirmed to work before the next was added, which made it
possible to know, at every stage, exactly what was and was not yet
working.

<div align="center">
<br/>

[![Live Demo](https://img.shields.io/badge/▶_Try_the_Live_Demo-f97316?style=for-the-badge)](https://budget-tracker-1-svws.onrender.com/)

</div>
