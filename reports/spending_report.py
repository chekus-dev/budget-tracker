"""
Spending report for the Budget Tracker app.

Connects to the same PostgreSQL database Go uses, pulls all expenses,
prints a summary, exports a CSV, and generates a spending chart.

Usage:
    python spending_report.py
"""

import csv
import os
import sys
from datetime import datetime

import matplotlib.pyplot as plt
import pandas as pd
import psycopg
from dotenv import load_dotenv
from psycopg.rows import dict_row

load_dotenv()

# Mirrors currencySymbols in main.go. Kept deliberately short for the same
# reason it is short there: every entry is one somebody has to keep correct,
# and an unknown code falls back to printing the code itself ("CHF 1,200.00")
# rather than guessing at a symbol and labelling someone's money wrongly.
CURRENCY_SYMBOLS = {
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


def currency_symbol(code):
    """Glyph for an ISO code, or the code plus a space if it is not known."""
    code = (code or "").strip().upper()
    if not code:
        return CURRENCY_SYMBOLS["NGN"]
    return CURRENCY_SYMBOLS.get(code, code + " ")


def database_url():
    """Read the same DATABASE_URL the Go server uses, from the environment/.env."""
    url = os.getenv("DATABASE_URL")
    if not url:
        sys.exit(
            "DATABASE_URL is not set. Put your Supabase connection string in .env as\n"
            '  DATABASE_URL=postgresql://postgres.<ref>:<password>@<host>:5432/postgres'
        )
    return url


def fetch_expenses():
    """Return (rows, symbol), where symbol is the currency the app is set to.

    This script reports across every expense in the database — it has never
    filtered by user — so with more than one account there is no single
    correct currency to use. It reads the first settings row and goes with
    that. The web app is per-user and is the accurate view; this is a
    companion for inspecting the data.
    """
    with psycopg.connect(database_url(), row_factory=dict_row) as conn:
        with conn.cursor() as cursor:
            cursor.execute("SELECT currency FROM settings ORDER BY user_id LIMIT 1")
            row = cursor.fetchone()
            symbol = currency_symbol(row["currency"] if row else "")

            # deleted_at IS NULL matches the app: a "deleted" expense is soft
            # deleted and stays in the table until the 30-day purge, so without
            # this the report would count rows the app no longer shows.
            #
            # kind = 'expense' matches it too. The table holds income as well,
            # and every figure below is a spending figure — leaving income in
            # would inflate the total and, worse, put a salary at the top of a
            # "biggest expenses" list.
            cursor.execute(
                "SELECT id, description, amount, created_at FROM expenses"
                " WHERE deleted_at IS NULL AND kind = 'expense'"
                " ORDER BY created_at"
            )
            return cursor.fetchall(), symbol


def print_summary(df, symbol):
    print("\n--- Spending Summary ---")
    print(f"Total expenses logged: {len(df)}")
    print(f"Total spent: {symbol}{df['amount'].sum():,.2f}")
    print(f"Average per entry: {symbol}{df['amount'].mean():,.2f}")
    print(f"Largest single expense: {symbol}{df['amount'].max():,.2f}")
    print("------------------------\n")


def export_csv(df, path="reports/expenses_export.csv"):
    df.to_csv(path, index=False, quoting=csv.QUOTE_MINIMAL)
    print(f"CSV exported to {path}")


def plot_monthly_spending(df, symbol, path="reports/monthly_spending.png"):
    df["created_at"] = pd.to_datetime(df["created_at"])
    df["month"] = df["created_at"].dt.to_period("M").astype(str)

    monthly = df.groupby("month")["amount"].sum()

    plt.figure(figsize=(8, 5))
    monthly.plot(kind="bar", color="#065f46")
    plt.title("Monthly Spending")
    plt.xlabel("Month")
    plt.ylabel(f"Amount ({symbol})")
    plt.tight_layout()
    plt.savefig(path)
    print(f"Chart saved to {path}")


def main():
    rows, symbol = fetch_expenses()
    if not rows:
        print("No expenses found in the database.")
        sys.exit(0)

    df = pd.DataFrame(rows)
    df["amount"] = df["amount"].astype(float)  # NUMERIC -> Python Decimal -> float
    print_summary(df, symbol)
    export_csv(df)
    plot_monthly_spending(df, symbol)


if __name__ == "__main__":
    main()