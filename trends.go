package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	trendsMinYear  = 2000
	trendsTopRows  = 5
	trendsInitials = "JFMAMJJASOND"
)

type monthTotals struct {
	Spent    float64
	Received float64
	Count    int
}

type catTotal struct {
	Name  string
	Blank bool
	Count int
	Total float64
}

type TrendBar struct {
	Label    string
	Month    string
	Value    float64
	Pct      float64
	Negative bool
	Zero     bool
	Gain     bool
	Ok       bool
	Over     bool
	Title    string
}

type TrendRow struct {
	Name    string
	Initial string
	Tile    int
	Count   int
	AmtMain string
	AmtFrac string
	Href    string
}

type TrendMonth struct {
	Short   string
	Name    string
	Href    string
	AmtMain string
	AmtFrac string
	Over    bool
	Diff    float64
}

type TrendTab struct {
	Key, Label, Icon, Href string
	Active                 bool
}

type TrendsPageData struct {
	PageChrome

	Initial                  string
	Year, PrevYear, NextYear int
	HasPrev, HasNext         bool
	PrevHref, NextHref       string
	View                     string
	Tabs                     []TrendTab

	HeadMain, HeadFrac, HeadCaption string

	HasData     bool
	NoBudget    bool
	BudgetLimit float64
	AvgDivisor  int

	Bars       []TrendBar
	PosPct     float64
	NegPct     float64
	ZeroBottom float64
	TopLabel   float64
	ShowLine   bool
	LineLabel  string
	LineValue  float64
	LineBottom float64

	Rows, MoreRows []TrendRow
	SeeAllHref     string

	Received, Spent, Net float64
	Months               []TrendMonth

	// Summary figures — one row of three cards above the charts.
	AvgIncome   float64
	AvgExpense  float64
	SavingsRate float64
	HasIncome   bool

	// ChartJSON is the line chart's data (one point per month).
	ChartJSON template.JS

	// DonutJSON is the category breakdown as a donut chart. Only populated
	// on the Spending view, where categories are already computed.
	DonutJSON template.JS
}

func normalizeTrendsView(raw string) string {
	switch raw {
	case "balance", "target":
		return raw
	}
	return "spending"
}

func trendsHref(view string, year int) string {
	v := url.Values{}
	v.Set("view", view)
	v.Set("year", strconv.Itoa(year))
	return "/trends?" + v.Encode()
}

func trendMoney(symbol string, v float64) string {
	if v < 0 {
		return "-" + symbol + commaGroupsPrec(-v, 0)
	}
	return symbol + commaGroupsPrec(v, 0)
}

func splitAmount(symbol string, v float64) (string, string) {
	cents := int64(math.Round(math.Abs(v) * 100))
	main := symbol + commaGroupsPrec(float64(cents/100), 0)
	if v < 0 && cents != 0 {
		main = "-" + main
	}
	return main, fmt.Sprintf(".%02d", cents%100)
}

func initialOf(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return "?"
	}
	return strings.ToUpper(string(r[0]))
}

func tileIndex(name string) int {
	h := 0
	for _, r := range name {
		h = (h*31 + int(r)) % 8
	}
	return h
}

func trendYearRange(v url.Values, year int) {
	v.Set("kind", kindExpense)
	v.Set("from", fmt.Sprintf("%04d-01-01", year))
	v.Set("to", fmt.Sprintf("%04d-12-31", year))
}

func trendCategoryHref(c catTotal, year int) string {
	v := url.Values{}
	trendYearRange(v, year)
	if c.Blank {
		v.Set("category", noCategorySentinel)
	} else {
		v.Set("category", c.Name)
	}
	return "/archive?" + v.Encode()
}

func trendAllCategoriesHref(year int) string {
	v := url.Values{}
	trendYearRange(v, year)
	return "/archive?" + v.Encode()
}

func buildTrends(view string, year int, now time.Time, months [12]monthTotals, limit float64, symbol string) TrendsPageData {
	d := TrendsPageData{View: view, Year: year, BudgetLimit: limit, AvgDivisor: 12}
	if year == now.Year() {
		d.AvgDivisor = int(now.Month())
	}

	vals := make([]float64, 12)
	var maxPos, maxNeg float64
	for i, m := range months {
		d.Spent += m.Spent
		d.Received += m.Received
		if view == "balance" {
			vals[i] = m.Received - m.Spent
			if m.Spent > 0 || m.Received > 0 {
				d.HasData = true
			}
		} else {
			vals[i] = m.Spent
			if m.Spent > 0 {
				d.HasData = true
			}
		}
		if vals[i] > maxPos {
			maxPos = vals[i]
		}
		if -vals[i] > maxNeg {
			maxNeg = -vals[i]
		}
	}
	d.Net = d.Received - d.Spent

	divisor := float64(d.AvgDivisor)
	if divisor > 0 {
		d.AvgIncome = d.Received / divisor
		d.AvgExpense = d.Spent / divisor
	}
	d.HasIncome = d.Received > 0
	if d.Received > 0 {
		d.SavingsRate = (d.Received - d.Spent) / d.Received * 100
	}

	if view == "target" {
		if limit <= 0 {
			d.NoBudget = true
			// A target view without a budget has nothing to chart. Turning
			// HasData off here keeps the template from rendering an empty
			// canvas beside the "no budget" empty state.
			d.HasData = false
		} else if limit > maxPos {
			maxPos = limit
		}
	}

	switch view {
	case "balance":
		d.HeadMain, d.HeadFrac = splitAmount(symbol, d.Net)
		if d.Net > 0 {
			d.HeadMain = "+" + d.HeadMain
		}
		d.HeadCaption = "Net for the year"
	case "target":
		if d.NoBudget {
			d.HeadMain, d.HeadCaption = "No budget", "Set one in Settings"
		} else {
			d.HeadMain, d.HeadFrac = splitAmount(symbol, d.Spent)
			d.HeadCaption = "Total spent"
		}
	default:
		d.HeadMain, d.HeadFrac = splitAmount(symbol, d.Spent)
		d.HeadCaption = "Total spent"
	}

	total := maxPos + maxNeg
	if !d.HasData || total <= 0 || d.NoBudget {
		return d
	}

	d.PosPct = maxPos / total * 100
	d.NegPct = 100 - d.PosPct
	d.ZeroBottom = d.NegPct
	d.TopLabel = maxPos

	for i, v := range vals {
		name := time.Month(i + 1).String()
		b := TrendBar{
			Label: string(trendsInitials[i]),
			Month: name,
			Value: v,
			Zero:  v == 0,
			Title: name + ": " + trendMoney(symbol, v),
		}
		if v >= 0 {
			if maxPos > 0 {
				b.Pct = v / maxPos * 100
			}
		} else {
			b.Negative = true
			b.Pct = -v / maxNeg * 100
		}
		switch view {
		case "balance":
			b.Gain = v > 0
		case "target":
			b.Over = v > limit
			b.Ok = v > 0 && v <= limit
		}
		d.Bars = append(d.Bars, b)
	}

	switch view {
	case "spending":
		if avg := d.Spent / float64(d.AvgDivisor); avg > 0 {
			d.ShowLine, d.LineLabel, d.LineValue = true, "AVG", avg
		}
	case "target":
		d.ShowLine, d.LineLabel, d.LineValue = true, "BUDGET", limit
	}
	if d.ShowLine {
		d.LineBottom = d.NegPct + d.LineValue/maxPos*d.PosPct
	}

	if view == "target" {
		counted, within := 0, 0
		for i := 0; i < d.AvgDivisor; i++ {
			if months[i].Spent <= 0 {
				continue
			}
			counted++
			over := months[i].Spent > limit
			if !over {
				within++
			}
			name := time.Month(i + 1).String()
			main, frac := splitAmount(symbol, months[i].Spent)
			d.Months = append(d.Months, TrendMonth{
				Short: name[:3], Name: name, Over: over,
				Href:    fmt.Sprintf("/?month=%04d-%02d", year, i+1),
				AmtMain: main, AmtFrac: frac,
				Diff: math.Abs(limit - months[i].Spent),
			})
		}
		d.HeadMain = fmt.Sprintf("%d of %d", within, counted)
		d.HeadCaption = "months within budget"
	}
	return d
}

func buildTrendRows(cats []catTotal, symbol string, year int) (top, more []TrendRow) {
	for i, c := range cats {
		main, frac := splitAmount(symbol, c.Total)
		row := TrendRow{
			Name: c.Name, Initial: initialOf(c.Name), Tile: tileIndex(c.Name),
			Count: c.Count, AmtMain: main, AmtFrac: frac,
			Href: trendCategoryHref(c, year),
		}
		if i < trendsTopRows {
			top = append(top, row)
		} else {
			more = append(more, row)
		}
	}
	return top, more
}

func trendsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireDB(w) {
		return
	}
	userID, username, _ := currentUser(r)

	settings, err := loadSettings(userID)
	if err != nil {
		serverError(w, err)
		return
	}
	symbol := currencySymbol(settings.Currency)

	now := time.Now()
	year := now.Year()
	if y, err := strconv.Atoi(r.URL.Query().Get("year")); err == nil && y >= trendsMinYear && y <= now.Year() {
		year = y
	}
	view := normalizeTrendsView(r.URL.Query().Get("view"))
	start := fmt.Sprintf("%04d-01-01", year)
	end := fmt.Sprintf("%04d-01-01", year+1)

	var months [12]monthTotals
	rows, err := db.QueryContext(r.Context(),
		`SELECT EXTRACT(MONTH FROM created_at)::int,
		        COALESCE(SUM(amount) FILTER (WHERE kind = 'expense'), 0),
		        COALESCE(SUM(amount) FILTER (WHERE kind = 'income'), 0),
		        COUNT(*) FILTER (WHERE kind = 'expense')
		 FROM expenses
		 WHERE user_id = $1 AND deleted_at IS NULL
		   AND created_at >= $2::timestamp AND created_at < $3::timestamp
		 GROUP BY 1`,
		userID, start, end)
	if err != nil {
		serverError(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var m int
		var t monthTotals
		if err := rows.Scan(&m, &t.Spent, &t.Received, &t.Count); err != nil {
			serverError(w, err)
			return
		}
		if m >= 1 && m <= 12 {
			months[m-1] = t
		}
	}
	if err := rows.Err(); err != nil {
		serverError(w, err)
		return
	}

	data := buildTrends(view, year, now, months, settings.BudgetLimit, symbol)

	type chartPoint struct {
		Month    string  `json:"month"`
		Spent    float64 `json:"spent"`
		Received float64 `json:"received"`
		Net      float64 `json:"net"`
	}
	points := make([]chartPoint, 12)
	for i, m := range months {
		points[i] = chartPoint{
			Month:    time.Month(i + 1).String(),
			Spent:    m.Spent,
			Received: m.Received,
			Net:      m.Received - m.Spent,
		}
	}
	chartPayload := map[string]any{
		"points":    points,
		"view":      view,
		"showLine":  data.ShowLine,
		"lineValue": data.LineValue,
		"lineLabel": data.LineLabel,
		"symbol":    symbol,
	}
	if raw, err := json.Marshal(chartPayload); err == nil {
		data.ChartJSON = template.JS(raw)
	}

	if view == "spending" && data.HasData {
		crows, err := db.QueryContext(r.Context(),
			`SELECT category, COUNT(*), SUM(amount)
			 FROM expenses
			 WHERE user_id = $1 AND kind = 'expense' AND deleted_at IS NULL
			   AND created_at >= $2::timestamp AND created_at < $3::timestamp
			 GROUP BY category
			 ORDER BY SUM(amount) DESC, category`,
			userID, start, end)
		if err != nil {
			serverError(w, err)
			return
		}
		defer crows.Close()
		var cats []catTotal
		for crows.Next() {
			var c catTotal
			var raw string
			if err := crows.Scan(&raw, &c.Count, &c.Total); err != nil {
				serverError(w, err)
				return
			}
			c.Name, c.Blank = raw, raw == ""
			if c.Blank {
				c.Name = "Uncategorized"
			}
			cats = append(cats, c)
		}
		if err := crows.Err(); err != nil {
			serverError(w, err)
			return
		}
		data.Rows, data.MoreRows = buildTrendRows(cats, symbol, year)
		data.SeeAllHref = trendAllCategoriesHref(year)

		type donutSlice struct {
			Name  string  `json:"name"`
			Value float64 `json:"value"`
		}
		const maxSlices = 8
		var slices []donutSlice
		var otherSum float64
		for i, c := range cats {
			if i < maxSlices {
				slices = append(slices, donutSlice{Name: c.Name, Value: c.Total})
			} else {
				otherSum += c.Total
			}
		}
		if otherSum > 0 {
			slices = append(slices, donutSlice{Name: "Other", Value: otherSum})
		}
		if raw, err := json.Marshal(slices); err == nil {
			data.DonutJSON = template.JS(raw)
		}
	}

	data.PageChrome = PageChrome{Username: username, CurrencySymbol: symbol}
	data.Initial = initialOf(username)
	data.PrevYear, data.NextYear = year-1, year+1
	data.HasPrev = year > trendsMinYear
	data.HasNext = year < now.Year()
	data.PrevHref = trendsHref(view, data.PrevYear)
	data.NextHref = trendsHref(view, data.NextYear)
	data.Tabs = []TrendTab{
		{Key: "balance", Label: "Balance", Icon: "banknote"},
		{Key: "spending", Label: "Spending", Icon: "chart"},
		{Key: "target", Label: "Target", Icon: "check"},
	}
	for i := range data.Tabs {
		data.Tabs[i].Href = trendsHref(data.Tabs[i].Key, year)
		data.Tabs[i].Active = data.Tabs[i].Key == view
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "trends.html", data); err != nil {
		log.Printf("trends: render: %v (is templates/trends.html in ParseFiles?)", err)
		http.Error(w, "Something went wrong. Please try again.", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}