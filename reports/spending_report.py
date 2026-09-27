"""
Spending report for the Budget Tracker app.

Connects to the same MySQL database Go uses, pulls all expenses,
prints a summary, exports a CSV, and generates a spending chart.

Usage:
    python spending_report.py
"""

import csv
import os
import sys
from datetime import datetime

import matplotlib.pyplot as plt
import mysql.connector
import pandas as pd
from dotenv import load_dotenv

load_dotenv()

DB_CONFIG = {
    "host": os.getenv("DB_HOST", "127.0.0.1"),
    "user": os.getenv("DB_USER", "root"),
    "password": os.getenv("DB_PASSWORD", ""),
    "database": os.getenv("DB_NAME", "budget"),
}


def fetch_expenses():
    conn = mysql.connector.connect(**DB_CONFIG)
    cursor = conn.cursor(dictionary=True)
    cursor.execute(
        "SELECT id, description, amount, created_at FROM expenses ORDER BY created_at"
    )
    rows = cursor.fetchall()
    cursor.close()
    conn.close()
    return rows


def print_summary(df):
    print("\n--- Spending Summary ---")
    print(f"Total expenses logged: {len(df)}")
    print(f"Total spent: ₦{df['amount'].sum():,.2f}")
    print(f"Average per entry: ₦{df['amount'].mean():,.2f}")
    print(f"Largest single expense: ₦{df['amount'].max():,.2f}")
    print("------------------------\n")


def export_csv(df, path="reports/expenses_export.csv"):
    df.to_csv(path, index=False, quoting=csv.QUOTE_MINIMAL)
    print(f"CSV exported to {path}")


def plot_monthly_spending(df, path="reports/monthly_spending.png"):
    df["created_at"] = pd.to_datetime(df["created_at"])
    df["month"] = df["created_at"].dt.to_period("M").astype(str)

    monthly = df.groupby("month")["amount"].sum()

    plt.figure(figsize=(8, 5))
    monthly.plot(kind="bar", color="#065f46")
    plt.title("Monthly Spending")
    plt.xlabel("Month")
    plt.ylabel("Amount (₦)")
    plt.tight_layout()
    plt.savefig(path)
    print(f"Chart saved to {path}")


def main():
    rows = fetch_expenses()
    if not rows:
        print("No expenses found in the database.")
        sys.exit(0)

    df = pd.DataFrame(rows)
    df["amount"] = df["amount"].astype(float)  # MySQL DECIMAL -> Python Decimal -> float
    print_summary(df)
    export_csv(df)
    plot_monthly_spending(df)


if __name__ == "__main__":
    main()