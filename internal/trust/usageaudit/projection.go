package usageaudit

import (
	"fmt"
	"time"
)

// Projection is a spend-pace estimate computed from the report's own
// Days series (#1388). It is never a claim about a provider account or
// plan reset: Strike only knows what it priced.
type Projection struct {
	// Basis labels the estimate, e.g. "7-day average".
	Basis string `json:"basis"`
	// WindowDays is how many calendar days the average divides by
	// (up to 7, fewer when history is younger than the window).
	WindowDays     int     `json:"windowDays"`
	AvgDailyUSD    float64 `json:"avgDailyUsd"`
	Month          string  `json:"month"`
	MonthToDateUSD float64 `json:"monthToDateUsd"`
	// MonthEndUSD is month-to-date spend plus AvgDailyUSD for every
	// calendar day left in the month after today.
	MonthEndUSD float64          `json:"monthEndUsd"`
	Today       *TodayProjection `json:"today,omitempty"`
}

// TodayProjection extrapolates today's spend-so-far against the daily
// budget. Present only when a budget is set and at least
// minPaceElapsed of the UTC day has passed.
type TodayProjection struct {
	Basis        string  `json:"basis"`
	SpentUSD     float64 `json:"spentUsd"`
	BudgetUSD    float64 `json:"budgetUsd"`
	ProjectedUSD float64 `json:"projectedUsd"`
	// RunOutAt is the RFC 3339 UTC time the budget is exhausted at
	// today's pace; empty when the pace stays within budget.
	RunOutAt     string `json:"runOutAt,omitempty"`
	Exhausted    bool   `json:"exhausted"`
	WithinBudget bool   `json:"withinBudget"`
	// Level is "info" under 80% of budget projected, "warning" above.
	Level string `json:"level"`
}

const (
	projectionWindow  = 7
	minPricedDays     = 3
	minPaceElapsed    = time.Hour
	projectionWarnPct = 0.8
)

// Project returns the pace projection, or nil plus a one-line reason
// when an honest estimate is not possible: prices incomplete, or fewer
// than minPricedDays days of priced history. An absent projection is
// never rendered as zero.
func Project(r Report, budget float64, budgetSet bool, now time.Time) (*Projection, string) {
	if !r.CostKnown || r.CostPartial || r.Unpriced > 0 {
		return nil, "no projection: prices are incomplete, so any pace estimate would be a guess"
	}
	byDay := map[string]DayUsage{}
	first := ""
	priced := 0
	for _, d := range r.Days {
		if d.Day == "" {
			continue
		}
		if d.CostPartial || d.UnpricedEvents > 0 {
			return nil, "no projection: prices are incomplete, so any pace estimate would be a guess"
		}
		byDay[d.Day] = d
		if d.CostKnown {
			priced++
		}
		if first == "" || d.Day < first {
			first = d.Day
		}
	}
	if priced < minPricedDays {
		return nil, fmt.Sprintf("no projection yet: needs %d days of priced history", minPricedDays)
	}

	now = now.UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	today := midnight.Format("2006-01-02")

	// Trailing window: the complete days before today, clipped to the
	// first recorded day so young history is not diluted by days that
	// predate it. Days inside the window with no events are real $0 days.
	window, sum := 0, 0.0
	for i := 1; i <= projectionWindow; i++ {
		day := midnight.AddDate(0, 0, -i).Format("2006-01-02")
		if day < first {
			break
		}
		window++
		sum += byDay[day].CostUSD
	}
	if window == 0 {
		return nil, fmt.Sprintf("no projection yet: needs %d days of priced history", minPricedDays)
	}
	avg := sum / float64(window)

	month := midnight.Format("2006-01")
	mtd := 0.0
	for day, d := range byDay {
		if len(day) >= 7 && day[:7] == month && day <= today {
			mtd += d.CostUSD
		}
	}
	nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	left := int(nextMonth.Sub(midnight).Hours()/24) - 1

	p := &Projection{
		Basis:          fmt.Sprintf("%d-day average", window),
		WindowDays:     window,
		AvgDailyUSD:    avg,
		Month:          month,
		MonthToDateUSD: mtd,
		MonthEndUSD:    mtd + avg*float64(left),
	}
	if budgetSet && budget > 0 {
		p.Today = projectToday(byDay[today].CostUSD, budget, midnight, now)
	}
	return p, ""
}

func projectToday(spent, budget float64, midnight, now time.Time) *TodayProjection {
	elapsed := now.Sub(midnight)
	if elapsed < minPaceElapsed {
		return nil
	}
	day := 24 * time.Hour
	t := &TodayProjection{
		Basis:        "today's pace",
		SpentUSD:     spent,
		BudgetUSD:    budget,
		ProjectedUSD: spent * float64(day) / float64(elapsed),
	}
	switch {
	case spent >= budget:
		t.Exhausted = true
	case t.ProjectedUSD < budget:
		t.WithinBudget = true
	default:
		runOut := midnight.Add(time.Duration(budget / spent * float64(elapsed)))
		t.RunOutAt = runOut.Format(time.RFC3339)
	}
	if t.ProjectedUSD >= projectionWarnPct*budget {
		t.Level = "warning"
	} else {
		t.Level = "info"
	}
	return t
}
