package usageaudit

import (
	"strings"
	"testing"
	"time"
)

func pricedDay(day string, usd float64) DayUsage {
	return DayUsage{Day: day, Reports: 1, CostUSD: usd, CostKnown: true}
}

func pricedReport(days ...DayUsage) Report {
	r := Report{Days: days, CostKnown: true}
	for _, d := range days {
		r.CostUSD += d.CostUSD
	}
	return r
}

func TestProjectSteadyPace(t *testing.T) {
	// 2026-11-14 12:00 UTC; seven complete $2 days before today, $1 so far today.
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	var days []DayUsage
	for d := 7; d <= 13; d++ {
		days = append(days, pricedDay(time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), 2))
	}
	days = append(days, pricedDay("2026-11-14", 1), pricedDay("2026-10-31", 50))
	p, note := Project(pricedReport(days...), 10, true, now)
	if p == nil {
		t.Fatalf("projection absent: %q", note)
	}
	if p.WindowDays != 7 || p.Basis != "7-day average" || !near(p.AvgDailyUSD, 2) {
		t.Fatalf("average = %+v", p)
	}
	// Month-to-date counts November only: 7*$2 + $1 = $15; 16 days left after the 14th.
	if p.Month != "2026-11" || !near(p.MonthToDateUSD, 15) || !near(p.MonthEndUSD, 15+16*2) {
		t.Fatalf("month = %+v", p)
	}
	tp := p.Today
	if tp == nil || !tp.WithinBudget || tp.Exhausted || tp.RunOutAt != "" || !near(tp.ProjectedUSD, 2) || tp.Level != "info" {
		t.Fatalf("today = %+v, want within budget at $2 projected", tp)
	}
}

func TestProjectFrontLoadedDayRunsOut(t *testing.T) {
	// $6 spent by 06:00 against a $10 budget: $24/day pace, runs out at 10:00.
	now := time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC)
	p, note := Project(pricedReport(pricedDay("2026-11-12", 1), pricedDay("2026-11-13", 1), pricedDay("2026-11-14", 6)), 10, true, now)
	if p == nil {
		t.Fatalf("projection absent: %q", note)
	}
	// History starts on the 12th, so the window clips to two complete days.
	if p.WindowDays != 2 || p.Basis != "2-day average" || !near(p.AvgDailyUSD, 1) {
		t.Fatalf("window = %+v", p)
	}
	tp := p.Today
	if tp == nil || tp.WithinBudget || tp.Exhausted || tp.RunOutAt != "2026-11-14T10:00:00Z" || tp.Level != "warning" {
		t.Fatalf("today = %+v, want run-out at 10:00", tp)
	}
}

func TestProjectExhaustedAndEarlyDay(t *testing.T) {
	base := []DayUsage{pricedDay("2026-11-11", 1), pricedDay("2026-11-12", 1), pricedDay("2026-11-13", 1)}
	over := append(append([]DayUsage{}, base...), pricedDay("2026-11-14", 12))
	p, _ := Project(pricedReport(over...), 10, true, time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC))
	if p == nil || p.Today == nil || !p.Today.Exhausted || p.Today.Level != "warning" {
		t.Fatalf("over budget = %+v", p)
	}
	// Under an hour into the UTC day there is no honest pace yet.
	p, _ = Project(pricedReport(over...), 10, true, time.Date(2026, 11, 14, 0, 30, 0, 0, time.UTC))
	if p == nil || p.Today != nil {
		t.Fatalf("early day = %+v, want month projection without today", p)
	}
	// No budget: averages only.
	p, _ = Project(pricedReport(base...), 0, false, time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC))
	if p == nil || p.Today != nil {
		t.Fatalf("no budget = %+v", p)
	}
}

func TestProjectSuppressed(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	partialDay := pricedDay("2026-11-13", 1)
	partialDay.CostPartial = true
	unpricedDay := pricedDay("2026-11-13", 1)
	unpricedDay.UnpricedEvents = 2
	cases := []struct {
		name string
		r    Report
		want string
	}{
		{"under three priced days", pricedReport(pricedDay("2026-11-12", 1), pricedDay("2026-11-13", 1)), "priced history"},
		{"nothing priced", Report{}, "incomplete"},
		{"report partial", func() Report {
			r := pricedReport(pricedDay("2026-11-11", 1), pricedDay("2026-11-12", 1), pricedDay("2026-11-13", 1))
			r.CostPartial = true
			return r
		}(), "incomplete"},
		{"partial day", pricedReport(pricedDay("2026-11-11", 1), pricedDay("2026-11-12", 1), partialDay), "incomplete"},
		{"unpriced day", pricedReport(pricedDay("2026-11-11", 1), pricedDay("2026-11-12", 1), unpricedDay), "incomplete"},
		{"history only today", pricedReport(pricedDay("2026-11-14", 1), pricedDay("2026-11-14", 1), pricedDay("2026-11-14", 1)), "priced history"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, note := Project(tc.r, 10, true, now)
			if p != nil || !strings.Contains(note, tc.want) {
				t.Fatalf("Project = %+v, %q; want nil and note containing %q", p, note, tc.want)
			}
		})
	}
}
