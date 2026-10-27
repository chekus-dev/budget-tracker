package main

import (
	"math"
	"time"
)

// WeekInsight is what the "last 7 days" card on Home shows.
type WeekInsight struct {
	Show        bool
	WeekSpent   float64
	LastSpent   float64
	HasLast     bool
	Less, Same  bool
	ChangePct   float64
	TopCategory string
	TopAmount   float64
	Streak      int
	LoggedToday bool
}

// weekInsights compares the last 7 days with the 7 before and counts the
// current logging streak, from one query over the last 60 days.
func weekInsights(userID int, now time.Time) (WeekInsight, error) {
	var ins WeekInsight
	today := utcDate(now)

	rows, err := db.Query(
		`SELECT created_at::date, category, kind, SUM(amount)
		 FROM expenses
		 WHERE user_id = $1 AND deleted_at IS NULL
		   AND created_at >= $2::date - 60 AND created_at < $2::date + 1
		 GROUP BY 1, 2, 3`,
		userID, today.Format("2006-01-02"),
	)
	if err != nil {
		return ins, err
	}
	defer rows.Close()

	have := map[string]bool{}
	cats := map[string]float64{}
	for rows.Next() {
		var d time.Time
		var cat, kind string
		var sum float64
		if err := rows.Scan(&d, &cat, &kind, &sum); err != nil {
			return ins, err
		}
		day := utcDate(d)
		have[day.Format("2006-01-02")] = true
		if kind != kindExpense {
			continue
		}
		age := int(today.Sub(day) / (24 * time.Hour))
		switch {
		case age >= 0 && age <= 6:
			ins.WeekSpent += sum
			if cat == "" {
				cat = "Uncategorized"
			}
			cats[cat] += sum
		case age >= 7 && age <= 13:
			ins.LastSpent += sum
		}
	}
	if err := rows.Err(); err != nil {
		return ins, err
	}

	for name, total := range cats {
		if total > ins.TopAmount || (total == ins.TopAmount && name < ins.TopCategory) {
			ins.TopCategory, ins.TopAmount = name, total
		}
	}

	if ins.LastSpent > 0 {
		ins.HasLast = true
		change := (ins.WeekSpent - ins.LastSpent) / ins.LastSpent * 100
		ins.ChangePct = math.Abs(change)
		ins.Same = ins.ChangePct < 1
		ins.Less = change < 0
	}

	// A streak stays alive until the end of today, so start from yesterday
	// when nothing has been logged yet today.
	ins.LoggedToday = have[today.Format("2006-01-02")]
	day := today
	if !ins.LoggedToday {
		day = day.AddDate(0, 0, -1)
	}
	for have[day.Format("2006-01-02")] {
		ins.Streak++
		day = day.AddDate(0, 0, -1)
	}

	ins.Show = ins.WeekSpent > 0 || ins.Streak > 0
	return ins, nil
}
