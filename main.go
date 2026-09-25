package main

import (
	"database/sql"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"

	_ "github.com/go-sql-driver/mysql"
)

type Expense struct {
	ID          int
	Description string
	Amount      float64
	CreatedAt   string
}

type PageData struct {
	Expenses []Expense
	Total    float64
}

var db *sql.DB
var tmpl *template.Template

func main() {
	dsn := "root:Budget2026!@tcp(127.0.0.1:3306)/budget"

	var err error
	db, err = sql.Open("mysql", dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatal(err)
	}

	tmpl = template.Must(template.ParseFiles("templates/index.html", "templates/expenses_list.html"))

	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/add", addHandler)
	http.HandleFunc("/delete/", deleteHandler)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))

	log.Println("server running at http://localhost:8000")
	log.Fatal(http.ListenAndServe(":8000", nil))
}

func indexHandler(w http.ResponseWriter, r *http.Request) {
	data, err := loadExpenses()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpl.ExecuteTemplate(w, "index.html", data)
}

func addHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	description := r.FormValue("description")
	amount := r.FormValue("amount")

	_, err := db.Exec(
		"INSERT INTO expenses (description, amount) VALUES (?, ?)",
		description, amount,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// If the request came from fetch(), return just the updated list/total HTML.
	if r.Header.Get("X-Requested-With") == "fetch" {
		data, err := loadExpenses()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmpl.ExecuteTemplate(w, "expenses_list.html", data)
		return
	}

	// Fallback for non-JS clients.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func deleteHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	idStr := strings.TrimPrefix(r.URL.Path, "/delete/")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	_, err = db.Exec("DELETE FROM expenses WHERE id = ?", id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("X-Requested-With") == "fetch" {
		data, err := loadExpenses()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		tmpl.ExecuteTemplate(w, "expenses_list.html", data)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func loadExpenses() (PageData, error) {
	rows, err := db.Query("SELECT id, description, amount, created_at FROM expenses ORDER BY created_at DESC")
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

	return PageData{Expenses: expenses, Total: total}, nil
}