package main

import (
	"math"
	"strings"
	"testing"
	"time"
)

func trendNear(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// A past year with a few months of spending.
func sampleYear() [12]monthTotals {
	var m [12]monthTotals
	m[0].Spent = 100
	m[1].Spent = 200
	m[7].Spent = 922
	m[9].Spent = 1822.94
	return m
}

func TestSplitAmount(t *testing.T) {
	cases := []struct {
		in         float64
		main, frac string
	}{
		{3044.94, "₦3,044", ".94"},
		{0, "₦0", ".00"},
		{0.999, "₦1", ".00"}, // rounds up into the whole part
		{-120.5, "-₦120", ".50"},
	}
	for _, c := range cases {
		m, f := splitAmount("₦", c.in)
		if m != c.main || f != c.frac {
			t.Errorf("splitAmount(%v) = %q %q, want %q %q", c.in, m, f, c.main, c.frac)
		}
	}
}

func TestBuildTrendsSpending(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	d := buildTrends("spending", 2025, now, sampleYear(), 0, "₦")
	if !d.HasData || len(d.Bars) != 12 {
		t.Fatalf("want data and 12 bars, got %v and %d", d.HasData, len(d.Bars))
	}
	if d.AvgDivisor != 12 {
		t.Errorf("past year divides by 12, got %d", d.AvgDivisor)
	}
	if d.HeadMain != "₦3,044" || d.HeadFrac != ".94" {
		t.Errorf("headline = %s%s", d.HeadMain, d.HeadFrac)
	}
	if !trendNear(d.PosPct, 100) || !trendNear(d.NegPct, 0) {
		t.Errorf("spending has no negative half: pos %v neg %v", d.PosPct, d.NegPct)
	}
	// The tallest bar fills the plot; the others are proportional.
	if !trendNear(d.Bars[9].Pct, 100) || !trendNear(d.Bars[7].Pct, 922/1822.94*100) {
		t.Errorf("bar heights wrong: %v %v", d.Bars[9].Pct, d.Bars[7].Pct)
	}
	if !d.Bars[3].Zero {
		t.Error("an empty month should be marked Zero")
	}
	wantAvg := 3044.94 / 12
	if !d.ShowLine || !trendNear(d.LineValue, wantAvg) || d.LineLabel != "AVG" {
		t.Errorf("average line = %v %v", d.ShowLine, d.LineValue)
	}
	if !trendNear(d.LineBottom, wantAvg/1822.94*100) {
		t.Errorf("average line position = %v", d.LineBottom)
	}
}

func TestBuildTrendsCurrentYearAveragesElapsedMonths(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	var m [12]monthTotals
	m[9].Spent = 1000
	d := buildTrends("spending", 2026, now, m, 0, "₦")
	if d.AvgDivisor != 10 || !trendNear(d.LineValue, 100) {
		t.Errorf("divisor %d, average %v; want 10 and 100", d.AvgDivisor, d.LineValue)
	}
}

func TestBuildTrendsBalanceGoesBelowZero(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	var m [12]monthTotals
	m[0] = monthTotals{Spent: 100, Received: 400} // +300
	m[1] = monthTotals{Spent: 500, Received: 0}   // -500
	d := buildTrends("balance", 2025, now, m, 0, "₦")
	if !d.Bars[0].Gain || d.Bars[0].Negative {
		t.Error("January is a gain")
	}
	if !d.Bars[1].Negative || !trendNear(d.Bars[1].Pct, 100) {
		t.Errorf("February is the deepest dip: %+v", d.Bars[1])
	}
	if !trendNear(d.PosPct+d.NegPct, 100) || !trendNear(d.PosPct, 300.0/800*100) {
		t.Errorf("split = %v / %v", d.PosPct, d.NegPct)
	}
	if !trendNear(d.ZeroBottom, d.NegPct) {
		t.Errorf("zero line should sit at the negative share, got %v", d.ZeroBottom)
	}
	if d.HeadMain != "-₦200" || d.HeadCaption != "Net" {
		t.Errorf("headline = %s %s", d.HeadMain, d.HeadCaption)
	}
	if d.ShowLine {
		t.Error("balance view has no reference line")
	}
}

func TestBuildTrendsTarget(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	var m [12]monthTotals
	m[0].Spent = 800  // within
	m[1].Spent = 1500 // over
	m[2].Spent = 1000 // exactly on the limit counts as within
	d := buildTrends("target", 2025, now, m, 1000, "₦")
	if d.HeadMain != "2 of 3" || d.HeadCaption != "months within budget" {
		t.Errorf("headline = %q %q", d.HeadMain, d.HeadCaption)
	}
	if !d.Bars[0].Ok || d.Bars[0].Over || !d.Bars[1].Over || d.Bars[1].Ok || !d.Bars[2].Ok {
		t.Errorf("ok/over flags wrong: %+v %+v %+v", d.Bars[0], d.Bars[1], d.Bars[2])
	}
	if len(d.Months) != 3 || !d.Months[1].Over || !trendNear(d.Months[1].Diff, 500) {
		t.Errorf("month rows = %+v", d.Months)
	}
	if d.Months[0].Href != "/?month=2025-01" {
		t.Errorf("month link = %s", d.Months[0].Href)
	}
	if !d.ShowLine || d.LineLabel != "BUDGET" || !trendNear(d.LineValue, 1000) {
		t.Error("budget line missing")
	}
}

func TestBuildTrendsTargetKeepsLimitInsidePlot(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	var m [12]monthTotals
	m[0].Spent = 100
	d := buildTrends("target", 2025, now, m, 1000, "₦")
	if d.LineBottom > 100.001 || !trendNear(d.LineBottom, 100) {
		t.Errorf("limit above every bar should sit at the top, got %v", d.LineBottom)
	}
	if !trendNear(d.Bars[0].Pct, 10) {
		t.Errorf("bar should be 10%% of the limit-sized plot, got %v", d.Bars[0].Pct)
	}
}

func TestBuildTrendsTargetWithoutBudget(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	d := buildTrends("target", 2025, now, sampleYear(), 0, "₦")
	if !d.NoBudget || len(d.Bars) != 0 {
		t.Errorf("no limit means no chart: %v, %d bars", d.NoBudget, len(d.Bars))
	}
}

func TestBuildTrendsEmptyYear(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	d := buildTrends("spending", 2024, now, [12]monthTotals{}, 0, "₦")
	if d.HasData || len(d.Bars) != 0 {
		t.Error("an empty year has no bars")
	}
	if d.HeadMain != "₦0" || d.HeadFrac != ".00" {
		t.Errorf("headline = %s%s", d.HeadMain, d.HeadFrac)
	}
}
func TestBuildTrendRows(t *testing.T) {
	// A real category: link carries its name.
	cats := []catTotal{
		{Name: "Food", Count: 3, Total: 150.00},
	}
	rows, _ := buildTrendRows(cats, "₦", 2025)
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d", len(rows))
	}
	if !strings.Contains(rows[0].Href, "category=Food") {
		t.Errorf("named category should filter by name, got %q", rows[0].Href)
	}
	if strings.Contains(rows[0].Href, noCategorySentinel) {
		t.Errorf("named category must not carry the sentinel, got %q", rows[0].Href)
	}

	// An uncategorized row (blank category): link carries the sentinel so it
	// lands on just the blank-category entries. Before this was fixed, the
	// link dropped the category filter entirely and showed everything in the
	// year — the count in the row and the count on the destination page did
	// not match.
	cats = []catTotal{
		{Name: "Uncategorized", Blank: true, Count: 2, Total: 40.00},
	}
	rows, _ = buildTrendRows(cats, "₦", 2025)
	if len(rows) != 1 {
		t.Fatalf("expected one row, got %d", len(rows))
	}
	if !strings.Contains(rows[0].Href, "category="+noCategorySentinel) {
		t.Errorf("uncategorized row should filter to the sentinel, got %q", rows[0].Href)
	}

	// The link must still scope to the year and to spending only.
	for _, want := range []string{"kind=expense", "from=2025-01-01", "to=2025-12-31"} {
		if !strings.Contains(rows[0].Href, want) {
			t.Errorf("uncategorized href missing %q: %q", want, rows[0].Href)
		}
	}
}

func TestTrendAllCategoriesHref(t *testing.T) {
	// "See entries" is unfiltered by category. It is deliberately not the
	// same call as the blank-catTotal path, which now means "only the
	// uncategorised rows" — the opposite of what this link promises.
	href := trendAllCategoriesHref(2025)
	if strings.Contains(href, "category=") {
		t.Errorf("see-all link should carry no category filter, got %q", href)
	}
	for _, want := range []string{"kind=expense", "from=2025-01-01", "to=2025-12-31"} {
		if !strings.Contains(href, want) {
			t.Errorf("see-all href missing %q: %q", want, href)
		}
	}
}