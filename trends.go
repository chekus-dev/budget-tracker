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
	"unicode"
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
	Icon    string
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
	// HeadMoney marks the headline as a real figure rather than the "2 of 3"
	// and "No budget" captions that share the same fields. Only a money
	// headline gets the masked twin the screenshot deterrents swap in.
	HeadMoney bool

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

/*
---- Category icons ----

	A category is free text: whatever the user types in Settings is the string
	stored against the expense. There is no icon column and no picker, so the
	icon is worked out from the name here instead — which also means the
	categories a user already has pick up an icon without re-entering them.

	categoryRuleIcons is a keyword table, and its order is the part that
	matters, because the first rule listing a word keeps that word:

	  - Money concepts come first. "Savings for school fees" is a savings
	    category, and both halves of it are in the table.
	  - Concrete things follow, and vague ones come last, so "Transport to
	    work" reads as transport rather than as work.
	  - "Bills" and "Fees" sit well down, because those are the words people
	    fall back on when nothing more specific fits.

	Words match whole, so "car" does not match "scarves", and a trailing "s"
	or "es" is folded, so "Snacks" finds "snack" and "Taxes" finds "tax".
	A name with several known words takes the one that comes first in it.
*/
var categoryRuleIcons = []struct {
	icon  string
	words []string
}{
	// Money.
	{"piggy", []string{"saving", "save", "ajo", "esusu", "susu", "adashe", "thrift", "emergency", "sinking", "pension"}},
	{"trending-up", []string{"investment", "invest", "stock", "share", "crypto", "bitcoin", "forex", "trading", "trade", "dividend", "bond", "mutual"}},
	{"coins", []string{"loan", "debt", "borrow", "repay", "repayment", "owe", "owed", "instalment", "installment", "hire"}},
	{"wallet", []string{"allowance", "pocket", "personal", "cash", "petty", "upkeep"}},
	{"banknote", []string{"salary", "wage", "wages", "payroll", "bonus", "commission", "freelance", "stipend", "income", "revenue", "profit", "refund", "cashback", "earning", "earnings", "gig"}},

	// Food and drink.
	{"utensils", []string{"food", "feeding", "feed", "eat", "eating", "lunch", "dinner", "breakfast", "brunch", "restaurant", "eatery", "takeaway", "takeout", "chop", "suya", "shawarma", "pizza", "burger", "snack", "canteen", "cafeteria", "kitchen", "meal"}},
	{"cart", []string{"grocery", "groceries", "market", "provision", "supermarket", "foodstuff", "vegetable", "meat", "fish", "pepper", "rice", "store"}},
	{"coffee", []string{"coffee", "cafe", "café", "tea", "starbucks", "cappuccino"}},
	{"cup", []string{"drink", "beer", "wine", "alcohol", "juice", "soda", "beverage", "bar", "shisha"}},
	{"cake", []string{"cake", "birthday", "anniversary", "celebration"}},

	// Getting around.
	{"car", []string{"transport", "transportation", "car", "uber", "bolt", "taxi", "cab", "keke", "okada", "ride", "vehicle", "parking", "toll", "wash", "driving"}},
	{"bus", []string{"bus", "danfo", "brt", "train", "rail", "metro", "shuttle", "ferry"}},
	{"flame", []string{"gas", "cylinder", "firewood", "charcoal"}},
	{"fuel", []string{"fuel", "petrol", "diesel", "kerosene", "pms"}},
	{"plane", []string{"flight", "travel", "trip", "holiday", "vacation", "airfare", "aeroplane", "airplane"}},
	{"bed", []string{"hotel", "lodge", "lodging", "airbnb", "guesthouse", "shortlet"}},

	// Home and the bills that come with it.
	{"home", []string{"rent", "house", "home", "mortgage", "housing", "landlord", "tenancy", "tenement", "accommodation", "property"}},
	{"bolt", []string{"electricity", "electric", "power", "nepa", "light", "utility", "prepaid", "token", "meter", "generator", "gen"}},
	{"droplet", []string{"water", "borehole"}},
	{"wifi", []string{"data", "internet", "wifi", "broadband", "modem", "router", "browsing", "network"}},
	{"smartphone", []string{"airtime", "recharge", "phone", "call", "mtn", "glo", "airtel", "9mobile", "etisalat", "sim", "topup", "telecom", "sms"}},
	{"trash", []string{"waste", "refuse", "sanitation", "garbage", "trash", "disposal", "cleaning", "fumigation", "pest", "environment"}},
	{"wrench", []string{"repair", "maintenance", "plumber", "electrician", "mechanic", "servicing", "fix", "handyman", "carpenter", "welder", "painter", "installation"}},
	{"shield", []string{"insurance", "insure", "security", "guard", "safety", "policy"}},

	// Health, appearance, and the people around you.
	{"heart", []string{"health", "medical", "hospital", "clinic", "doctor", "treatment", "surgery", "hmo", "nhis", "sick", "illness", "checkup", "lab", "laboratory", "scan", "test"}},
	{"pill", []string{"medicine", "medication", "drug", "pharmacy", "chemist", "pill", "prescription", "vitamin"}},
	{"dumbbell", []string{"gym", "fitness", "workout", "exercise", "sport", "training", "yoga", "football", "ball", "coach"}},
	{"scissors", []string{"haircut", "hair", "barber", "barbing", "salon", "beauty", "cosmetic", "makeup", "spa", "manicure", "pedicure", "nail", "wig", "weave", "braid", "cream", "lotion"}},
	{"shirt", []string{"cloth", "clothes", "clothing", "fashion", "shoe", "wears", "outfit", "tailor", "laundry", "fabric", "ankara", "lace", "jewellery", "jewelry", "watch"}},
	{"graduation", []string{"school", "tuition", "education", "academic", "university", "polytechnic", "college", "lesson", "lecture", "exam", "waec", "jamb", "neco", "nursery"}},
	{"book", []string{"book", "stationery", "textbook", "notebook", "pen", "learning", "course", "study", "studies", "library", "research", "printing", "photocopy"}},
	{"baby", []string{"baby", "babies", "kid", "child", "children", "diaper", "nappy", "pampers", "childcare", "nanny", "maternity", "antenatal", "toy", "creche"}},
	{"paw", []string{"pet", "dog", "cat", "vet", "veterinary", "animal"}},
	{"gift", []string{"gift", "present", "donation", "charity", "tithe", "offering", "church", "mosque", "zakat", "sadaka", "alms", "welfare", "thanksgiving"}},

	// Leisure.
	{"film", []string{"movie", "cinema", "film", "netflix", "showmax", "prime", "entertainment", "dstv", "gotv", "startimes", "tv", "cable", "streaming"}},
	{"music", []string{"music", "spotify", "audiomack", "boomplay", "song", "album"}},
	{"gamepad", []string{"game", "gaming", "playstation", "xbox", "ps4", "ps5", "console"}},
	{"dice", []string{"bet", "betting", "gamble", "gambling", "lottery", "lotto", "pool", "stake", "sporty", "bet9ja", "casino", "jackpot"}},
	{"ticket", []string{"ticket", "event", "concert", "party", "parties", "outing", "owambe", "show"}},

	// Everything else, vaguest last.
	{"laptop", []string{"laptop", "computer", "electronics", "gadget", "accessories", "charger"}},
	{"bag", []string{"shopping", "shop", "mall", "boutique", "purchase", "amazon", "jumia", "konga"}},
	{"receipt", []string{"bill", "fee", "charge", "subscription", "due", "levy", "tax", "vat"}},
	{"receipt", []string{"charges", "bank", "transfer", "stamp", "atm", "penalty"}},
	{"briefcase", []string{"work", "business", "office", "job", "company", "staff", "employee", "professional", "consulting"}},
	{"tag", []string{"other", "misc", "miscellaneous", "uncategorised", "uncategorized", "general", "random", "tag", "various", "stuff"}},
}

/*
categoryWordIcons is the table above turned inside out, built once at

	startup. The first rule to list a word keeps it, which is what makes the
	order of categoryRuleIcons decide a name that fits two of them.
*/
var categoryWordIcons = func() map[string]string {
	m := make(map[string]string, 512)
	for _, rule := range categoryRuleIcons {
		for _, w := range rule.words {
			if _, taken := m[w]; !taken {
				m[w] = rule.icon
			}
		}
	}
	return m
}()

/*
categoryFallbackIcons are the shapes an unrecognised category hashes into.

	None of them means anything, which is the point: a name the table does not
	know gets a marker of its own rather than a wrong guess at what it is. Two
	unknown names can still land on the same shape, but not all of them will.
*/
var categoryFallbackIcons = []string{
	"tag", "wallet", "receipt", "banknote", "piggy", "target", "calendar",
	"inbox", "bookmark", "star", "flag", "layers", "package", "grid",
	"sparkle", "folder",
}

/*
categoryIcon returns the sprite id to draw for a category name. It returns

	"" for an empty name, which is how an entry logged without a category asks
	for the generic receipt/banknote instead.
*/
func categoryIcon(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return ""
	}
	for _, w := range categoryWords(name) {
		/* Exact first, then the singular, so a word that genuinely ends in
		   "s" — "gas", "bus" — is not folded into something else. */
		for _, form := range []string{w, strings.TrimSuffix(w, "s"), strings.TrimSuffix(w, "es")} {
			if icon, ok := categoryWordIcons[form]; ok {
				return icon
			}
		}
	}
	return categoryIconFallback(name)
}

/*
categoryWords splits a name into the words the table is matched against.

	Everything that is not a letter or a digit separates words, so "Car wash",
	"car-wash" and "car/wash" all give the same two.
*/
func categoryWords(name string) []string {
	return strings.FieldsFunc(name, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

/*
categoryIconFallback gives an unrecognised name a stable icon of its own:

	FNV-1a over the name, so the same category draws the same icon on every
	page and after every restart, and different names spread over the pool.
*/
func categoryIconFallback(name string) string {
	h := uint32(2166136261)
	for _, r := range name {
		h = (h ^ uint32(r)) * 16777619
	}
	return categoryFallbackIcons[h%uint32(len(categoryFallbackIcons))]
}

/*
catIcon is what the templates ask for wherever a category is drawn as a

	category — a tile, a budget row, beside a name. It differs from
	categoryIcon in one way that matters: it never returns "".

	An empty id would make the markup <use href="/static/icons.svg?v=7#"/>,
	and an empty fragment selects the sprite's root <svg>, so the browser
	would draw all eighty-six symbols at once inside a 20px box. Names are
	meant to be non-empty by the time they get here — Settings drops blank
	ones and the report and trends queries relabel them "Uncategorized" — but
	categoryBudgetProgress also builds rows from the keys in the limits
	table, which predate that rule, so this does not rely on it.

	rowIcon is the one caller that does need the difference, and it asks
	categoryIcon directly: an entry logged with no category should fall back
	to the generic receipt or banknote, not to a meaningless shape.
*/
func catIcon(name string) string {
	if icon := categoryIcon(name); icon != "" {
		return icon
	}
	return "tag"
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
	d := TrendsPageData{View: view, Year: year, BudgetLimit: limit, AvgDivisor: 1}
	monthsElapsed := 12
	if year == now.Year() {
		monthsElapsed = int(now.Month())
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

	// Average over the months since the first entry, not over the whole year
	// (or every month so far): one month of data means one month averaged.
	first := -1
	for i, m := range months {
		if m.Spent > 0 || m.Received > 0 {
			first = i
			break
		}
	}
	if first >= 0 && monthsElapsed > first {
		d.AvgDivisor = monthsElapsed - first
	}
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
		d.HeadMoney = true
		if d.Net > 0 {
			d.HeadMain = "+" + d.HeadMain
		}
		d.HeadCaption = "Net for the year"
	case "target":
		if d.NoBudget {
			d.HeadMain, d.HeadCaption = "No budget", "Set one in Settings"
		} else {
			d.HeadMain, d.HeadFrac = splitAmount(symbol, d.Spent)
			d.HeadMoney = true
			d.HeadCaption = "Total spent"
		}
	default:
		d.HeadMain, d.HeadFrac = splitAmount(symbol, d.Spent)
		d.HeadMoney = true
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
		for i := 0; i < monthsElapsed; i++ {
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
			Name: c.Name, Icon: catIcon(c.Name), Tile: tileIndex(c.Name),
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
			Color string  `json:"color"`
		}
		const maxSlices = 8
		var slices []donutSlice
		var otherSum float64
		for i, c := range cats {
			if i < maxSlices {
				slices = append(slices, donutSlice{Name: c.Name, Value: c.Total, Color: catColor(c.Name)})
			} else {
				otherSum += c.Total
			}
		}
		if otherSum > 0 {
			slices = append(slices, donutSlice{Name: "Other", Value: otherSum, Color: "#94A3B8"})
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
