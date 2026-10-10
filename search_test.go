package main

import (
	"bytes"
	"html/template"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func values(pairs ...string) url.Values {
	v := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		v.Set(pairs[i], pairs[i+1])
	}
	return v
}

// urlFilters parses the way a query string is parsed: the GET form posts the
// plain field names, and nothing else in a URL uses them.
func urlFilters(v url.Values) ExpenseFilters {
	return parseFiltersFrom(v.Get, true)
}

// formFilters parses the way one of our own POST forms is parsed, where the
// plain names belong to the entry being saved rather than to a search.
func formFilters(v url.Values) ExpenseFilters {
	return parseFiltersFrom(v.Get, false)
}

func TestLikeEscape(t *testing.T) {
	// A user searching for "50%" means the text "50%", not "anything starting
	// with 50". Each of these would otherwise be a wildcard.
	cases := map[string]string{
		"lunch":   "lunch",
		"50%":     `50\%`,
		"a_b":     `a\_b`,
		`back\`:   `back\\`,
		`\%_`:     `\\\%\_`,
		"":        "",
		"a%b_c\\": `a\%b\_c\\`,
	}
	for in, want := range cases {
		if got := likeEscape(in); got != want {
			t.Errorf("likeEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSearchQueryClauseMatchesDescriptionAndCategory(t *testing.T) {
	clause, values := searchQueryClause(1, "Food")
	if len(values) != 4 {
		t.Fatalf("expected four placeholders for query search, got %d", len(values))
	}
	if !strings.Contains(clause, "description ILIKE") || !strings.Contains(clause, "category ILIKE") {
		t.Fatalf("query clause should match description and category, got %q", clause)
	}
	for _, v := range values {
		if v != "%Food%" {
			t.Fatalf("query clause should escape the term as a loose match, got %#v", values)
		}
	}
}

func TestParseFiltersSwapsReversedRange(t *testing.T) {
	f := urlFilters(values("from", "2026-06-01", "to", "2026-03-01"))
	if f.From != "2026-03-01" || f.To != "2026-06-01" {
		t.Errorf("reversed range not swapped: from=%q to=%q", f.From, f.To)
	}
}

func TestParseFiltersDropsUnusableDates(t *testing.T) {
	// A half-typed date should narrow the search less, not fail the request.
	for _, raw := range []string{"", "not-a-date", "2026-13-45", "01/02/2026"} {
		f := urlFilters(values("from", raw, "to", raw))
		if f.From != "" || f.To != "" {
			t.Errorf("date %q was kept: from=%q to=%q", raw, f.From, f.To)
		}
	}
}

func TestParseFiltersTrimsAndNormalises(t *testing.T) {
	f := urlFilters(values("q", "  lunch  ", "from", "2026-03-01T00:00:00Z"))
	if f.Query != "lunch" {
		t.Errorf("query not trimmed: %q", f.Query)
	}
	// The trailing time component makes this unparseable as a plain date, so it
	// is dropped rather than half-understood.
	if f.From != "" {
		t.Errorf("expected a malformed date to be dropped, got %q", f.From)
	}
}

func TestParseFiltersPrefersTheHiddenFieldName(t *testing.T) {
	// When both are present the POST-only name wins, because that is the one
	// only our own forms can set.
	f := urlFilters(values("category_filter", "Food", "category", "Transport"))
	if f.Category != "Food" {
		t.Errorf("category_filter not preferred: got %q", f.Category)
	}
	// The GET form has no category_filter, so the plain name must still work.
	f = urlFilters(values("category", "Transport"))
	if f.Category != "Transport" {
		t.Errorf("plain category not honoured: got %q", f.Category)
	}
}

func TestFormFiltersIgnoreTheEntrysOwnFields(t *testing.T) {
	// The add/edit modal posts "category" and "kind" to mean the entry being
	// saved, and carries any active search separately under the _filter names.
	// Reading the plain names here made saving an edit redirect into a search
	// nobody asked for — an edit landed on "/?category=Food&kind=expense".
	posted := values(
		"description", "Rent", "amount", "12.00",
		"category", "Food", "kind", "expense",
	)
	if f := formFilters(posted); f.Active() {
		t.Errorf("the entry's own fields were read as a search: %+v", f)
	}

	// The filter fields themselves still have to work, or a delete made from a
	// search would throw the results away.
	posted = values(
		"category", "Food", "kind", "expense",
		"q", "lunch", "category_filter", "Transport", "kind_filter", "income",
	)
	f := formFilters(posted)
	if f.Query != "lunch" || f.Category != "Transport" || f.Kind != kindIncome {
		t.Errorf("posted filters not read: %+v", f)
	}
}

func TestParseFiltersKind(t *testing.T) {
	// Only the two real kinds narrow anything. Junk narrows nothing rather than
	// emptying the results, which is what a hand-typed URL should get.
	for _, raw := range []string{"expense", "EXPENSE", " income "} {
		if got := urlFilters(values("kind", raw)).Kind; got == "" {
			t.Errorf("kind %q was not accepted", raw)
		}
	}
	for _, raw := range []string{"", "transfer", "expenses"} {
		if got := urlFilters(values("kind", raw)).Kind; got != "" {
			t.Errorf("kind %q should have narrowed nothing, got %q", raw, got)
		}
	}
}

func TestFiltersKindRoundTrip(t *testing.T) {
	original := ExpenseFilters{Kind: kindIncome}
	if back := urlFilters(original.Values()); back != original {
		t.Errorf("kind did not survive the round trip: %+v -> %+v", original, back)
	}
}

func TestNormalizeKind(t *testing.T) {
	// Anything unrecognised becomes an expense: every row that existed before
	// income did is an expense, so that is the direction that cannot misfile
	// a new entry as money coming in.
	cases := map[string]string{
		"":          kindExpense,
		"expense":   kindExpense,
		"income":    kindIncome,
		"INCOME":    kindIncome,
		" Income ":  kindIncome,
		"transfer":  kindExpense,
		"incoming":  kindExpense,
		"expenses)": kindExpense,
	}
	for in, want := range cases {
		if got := normalizeKind(in); got != want {
			t.Errorf("normalizeKind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseFiltersActive(t *testing.T) {
	if (ExpenseFilters{}).Active() {
		t.Error("an empty filter set should not be active")
	}
	for _, f := range []ExpenseFilters{
		{Query: "x"}, {Category: "Food"}, {Kind: kindIncome},
		{From: "2026-01-01"}, {To: "2026-01-01"},
	} {
		if !f.Active() {
			t.Errorf("filter %+v should be active", f)
		}
	}
}

func TestParseFiltersValuesRoundTrip(t *testing.T) {
	// The filters are echoed into the form and into redirect targets, so what
	// Values emits has to parse back to the same thing.
	original := ExpenseFilters{Query: "lunch & co", Category: "Food", From: "2026-01-01", To: "2026-01-31"}
	back := urlFilters(original.Values())
	if back != original {
		t.Errorf("round trip changed the filters: %+v -> %+v", original, back)
	}
}

func TestCategoryOptionsAppendsAMissingSelection(t *testing.T) {
	configured := []string{"Food", "Transport"}

	// A category can be dropped from settings while expenses still carry it.
	// Without this the select would read "All categories" while a filter was
	// in fact being applied, leaving the user no way to see or clear it.
	got := categoryOptions(configured, "Legacy")
    if len(got) != 3 || got[2].Value != "Legacy" || got[2].Label != "Legacy" {		t.Errorf("missing selection not appended: %v", got)
	}

	// No duplicate when it is already there.
	got = categoryOptions(configured, "Food")
	if len(got) != 2 {
		t.Errorf("selection duplicated: %v", got)
	}

	// No mutation of the caller's slice.
	if len(configured) != 2 || configured[0] != "Food" {
		t.Errorf("input slice was mutated: %v", configured)
	}

	// Nothing selected means nothing appended.
	if got := categoryOptions(configured, ""); len(got) != 2 {
		t.Errorf("unexpected options for an empty selection: %v", got)
	}
}

func TestNormalizeCategoriesTrimsBlanksAndDuplicates(t *testing.T) {
	got := normalizeCategories([]string{" Food ", "Food", "", ",Transport", "Transport", "Bills,", "Bills"})
	want := []string{"Food", "Transport", "Bills"}
	if len(got) != len(want) {
		t.Fatalf("normalizeCategories() len = %d, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeCategories()[%d] = %q, want %q: %#v", i, got[i], want[i], got)
		}
	}
}

func TestDateTriggerMarkupAndPickerFallback(t *testing.T) {
	localTmpl := template.Must(template.New("nav.html").ParseFiles(
		"templates/head.html",
		"templates/nav.html",
	))

	var buf bytes.Buffer
	if err := localTmpl.ExecuteTemplate(&buf, "nav.html", map[string]interface{}{
		"Username":       "sam",
		"CurrencySymbol": "₦",
		"Filters":        ExpenseFilters{},
	}); err != nil {
		t.Fatalf("nav template should render: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `data-date-target`) {
		t.Fatal("nav.html should include a calendar trigger button for the date field")
	}

	js, err := os.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("app.js should be readable: %v", err)
	}
	if !strings.Contains(string(js), `showPicker`) {
		t.Fatal("static/app.js should include the native picker fallback for the calendar trigger")
	}
	if !strings.Contains(string(js), `requestSubmit`) || !strings.Contains(string(js), `form.submit`) {
		t.Fatal("static/app.js should include a browser-safe form submission fallback for archive auto-search")
	}
}

func TestArchiveSearchRunsBeforeMonthListWhenFiltersAreActive(t *testing.T) {
	if archiveShouldSearch("", ExpenseFilters{Query: "Groceries"}) == false {
		t.Fatal("a query filter should trigger the archive results view even when no month is selected")
	}
	if archiveShouldSearch("", ExpenseFilters{}) {
		t.Fatal("an empty filter set should not trigger the archive results view")
	}
}

func TestParseExpenseDate(t *testing.T) {
	today := time.Now().Format("2006-01-02")
	d, ok := parseExpenseDate(today)
	if !ok {
		t.Fatal("today should parse")
	}
	// The time of day comes from now, so a backfilled expense sorts among that
	// day's entries the way its neighbours do.
	if d.Format("2006-01-02") != today {
		t.Errorf("date drifted: %s", d.Format("2006-01-02"))
	}
	if d.Hour() != time.Now().Hour() {
		t.Errorf("expected the current hour, got %d", d.Hour())
	}

	for _, raw := range []string{"", "nonsense", "2026-13-01", time.Now().AddDate(0, 0, 2).Format("2006-01-02")} {
		if _, ok := parseExpenseDate(raw); ok {
			t.Errorf("%q should not be accepted", raw)
		}
	}
}
