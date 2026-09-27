# Budget Tracker

A personal budget tracking web application built with Go, MySQL, and a
lightweight vanilla JavaScript frontend. The project began as a simple
exercise in connecting Go to a MySQL database, and grew incrementally
into a working full-stack application with a server-rendered interface,
dynamic client-side interactions, and a companion Python reporting tool.

This README documents the project honestly — including the setbacks —
because the process of building it is as much a part of the learning
as the finished code.

---

## What it does

- Log expenses with a description and an amount (displayed in Naira, ₦)
- View all logged expenses with a running total
- Add and delete expenses without a full page reload
- See a live-updating total as an amount is typed, before submitting
- Generate a spending report and monthly chart from the same data,
  using a separate Python script

---

## Tech stack

| Layer              | Technology                                                 |
|--------------------|-------------------------------------------------------------|
| Backend            | Go (`net/http`, `database/sql`)                              |
| Database           | MySQL 8                                                      |
| Templating         | Go's `html/template`                                         |
| Frontend styling   | Tailwind CSS (CDN, prototyping stage)                        |
| Frontend behavior  | Vanilla JavaScript (`fetch`, DOM updates)                    |
| Reporting          | Python (`pandas`, `matplotlib`, `mysql-connector-python`)    |

No frontend framework and no ORM were used, deliberately. The goal was
to understand each layer directly — raw SQL queries, a real HTTP
server, and DOM manipulation without abstraction — before reaching for
tools that hide those details.

---

## How this project was built

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
application. It connects to the same MySQL database independently,
and produces:

- A printed summary (total spent, average expense, largest expense)
- A CSV export of all logged expenses
- A monthly spending bar chart, saved as an image

This was kept as a standalone script rather than folded into the Go
application, to demonstrate that the underlying data is not tied to
any single tool or language — it is a MySQL database that multiple
programs can read independently.

---

## Running the project locally

### Prerequisites

- Go 1.24 or later
- MySQL 8.x, running locally
- Python 3.x (only required for the reporting script)

### Database setup

```sql
CREATE DATABASE budget;

USE budget;

CREATE TABLE expenses (
    id INT AUTO_INCREMENT PRIMARY KEY,
    description VARCHAR(255) NOT NULL,
    amount DECIMAL(10,2) NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

### Running the web application

```bash
go mod tidy
go run main.go
```

Then visit `http://localhost:8000` in a browser.

### Running the spending report

```bash
python3 -m venv venv
source venv/bin/activate
pip install mysql-connector-python matplotlib pandas
python reports/spending_report.py
```

Output is written to the `reports/` directory as a CSV file and a PNG
chart.

---

## Known limitations and next steps

This project is intentionally incremental, and several improvements
are planned rather than already built:

- **Credentials are currently hardcoded** in both `main.go` and
  `spending_report.py`. Moving these to environment variables (and
  excluding them from version control) is the next priority, ahead of
  any further feature work.
- **No automated tests exist yet.** Adding tests for the HTTP handlers
  and database logic is a deliberate next step.
- **No expense categories or date filtering yet** — the application
  currently treats all expenses as a single flat list.
- **The Tailwind CSS build is currently CDN-based**, which is
  appropriate for prototyping but not for a production deployment. A
  proper compiled build is planned once the feature set stabilizes.
- **Server-side validation is currently minimal**, relying partly on
  client-side JavaScript checks. These are not a substitute for
  server-side enforcement and will be hardened.

---

## A note on process

This project was not built in a straight line, and this README does
not pretend otherwise. Long stretches of time went into fixing a local
MySQL installation before a single feature could be built, and several
features were built in a deliberately narrow order — one working piece
at a time — rather than all at once. That pace was a choice: each
layer was confirmed to work before the next was added, which made it
possible to know, at every stage, exactly what was and was not yet
working.
