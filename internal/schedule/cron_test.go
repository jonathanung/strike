package schedule

import (
	"testing"
	"time"
)

func TestParseCronRejects(t *testing.T) {
	for _, expr := range []string{"", "* * *", "* * * *", "60 * * * *", "* * * *", "*/0 * * * *", "1-0 * * * *"} {
		if _, err := parseCron(expr); err == nil {
			t.Fatalf("parse %q succeeded", expr)
		}
	}
}

func TestNextCronUTC(t *testing.T) {
	from := time.Date(2026, 10, 26, 8, 30, 0, 0, time.UTC) // Monday
	next, err := nextCron("0 9 * * MON", from, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next %s, want %s", next, want)
	}
	again, err := nextCron("0 9 * * 1", next, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Equal(want.AddDate(0, 0, 7)) {
		t.Fatalf("following %s", again)
	}
}

func TestNextCronStepAndNames(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next, err := nextCron("*/15 0 1 JAN *", from, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 1, 1, 0, 15, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next %s, want %s", next, want)
	}
}

func TestNextCronDayOfMonthOrWeek(t *testing.T) {
	// 15th or Monday. 2026-06-01 is a Monday; 2026-06-15 is a Monday too.
	from := time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC)
	next, err := nextCron("0 12 15 * MON", from, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC) // next Monday
	if !next.Equal(want) {
		t.Fatalf("next %s, want %s", next, want)
	}
}

func TestCronDSTSpringForward(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-03-08 02:00 EST does not exist. A 02:30 daily slot skips to the 9th.
	from := time.Date(2026, 3, 8, 1, 0, 0, 0, loc)
	next, err := nextCron("30 2 * * *", from, loc)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 3, 9, 2, 30, 0, 0, loc)
	if !next.Equal(want) {
		t.Fatalf("02:30 slot %s, want %s", next.In(loc), want.In(loc))
	}

	// Every minute across the gap: 01:59 EST is followed by 03:00 EDT.
	gapFrom := time.Date(2026, 3, 8, 1, 59, 0, 0, loc)
	minute, err := nextCron("* * * * *", gapFrom, loc)
	if err != nil {
		t.Fatal(err)
	}
	wantMin := time.Date(2026, 3, 8, 3, 0, 0, 0, loc)
	if !minute.Equal(wantMin) {
		t.Fatalf("minute across gap %s, want %s", minute.UTC(), wantMin.UTC())
	}
}

func TestCronDSTFallBackFiresOnce(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// 2026-11-01 01:30 happens twice. The slot fires at the earlier offset
	// and the next fire is the next calendar day, not the repeated hour.
	from := time.Date(2026, 11, 1, 0, 30, 0, 0, loc)
	first, err := nextCron("30 1 * * *", from, loc)
	if err != nil {
		t.Fatal(err)
	}
	earlier := time.Date(2026, 11, 1, 1, 30, 0, 0, loc)
	if !first.Equal(earlier) {
		t.Fatalf("first %s, want %s", first.UTC(), earlier.UTC())
	}
	second, err := nextCron("30 1 * * *", first, loc)
	if err != nil {
		t.Fatal(err)
	}
	nextDay := time.Date(2026, 11, 2, 1, 30, 0, 0, loc)
	if !second.Equal(nextDay) {
		t.Fatalf("second %s, want %s (not the repeated 01:30)", second.UTC(), nextDay.UTC())
	}
	if !second.After(first.Add(2 * time.Hour)) {
		t.Fatalf("fall-back slot fired again inside the repeated hour: %s then %s", first, second)
	}
}
