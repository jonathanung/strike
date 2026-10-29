package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

// cronExpr is a five-field cron in a named timezone.
// domStar and dowStar are true only for a literal "*", which is what
// selects Vixie OR semantics when both day fields are constrained.
type cronExpr struct {
	min     [60]bool
	hour    [24]bool
	dom     [32]bool
	month   [13]bool
	dow     [7]bool
	domStar bool
	dowStar bool
}

func parseCron(expr string) (cronExpr, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cronExpr{}, fmt.Errorf("cron: want 5 fields, got %d", len(fields))
	}
	var out cronExpr
	var err error
	if err = fill(&out.min, fields[0], 0, 59, nil); err != nil {
		return cronExpr{}, fmt.Errorf("cron minute: %w", err)
	}
	if err = fill(&out.hour, fields[1], 0, 23, nil); err != nil {
		return cronExpr{}, fmt.Errorf("cron hour: %w", err)
	}
	out.domStar = fields[2] == "*"
	if err = fill(&out.dom, fields[2], 1, 31, nil); err != nil {
		return cronExpr{}, fmt.Errorf("cron day: %w", err)
	}
	if err = fill(&out.month, fields[3], 1, 12, monthNames); err != nil {
		return cronExpr{}, fmt.Errorf("cron month: %w", err)
	}
	out.dowStar = fields[4] == "*"
	var dow [8]bool
	if err = fill(&dow, fields[4], 0, 7, dowNames); err != nil {
		return cronExpr{}, fmt.Errorf("cron weekday: %w", err)
	}
	for i := 0; i <= 6; i++ {
		out.dow[i] = dow[i]
	}
	if dow[7] {
		out.dow[0] = true
	}
	return out, nil
}

func (e cronExpr) matches(t time.Time) bool {
	if !e.month[int(t.Month())] || !e.hour[t.Hour()] || !e.min[t.Minute()] {
		return false
	}
	domOK := e.dom[t.Day()]
	dowOK := e.dow[int(t.Weekday())]
	switch {
	case e.domStar && e.dowStar:
		return true
	case e.domStar:
		return dowOK
	case e.dowStar:
		return domOK
	default:
		// Vixie: a constrained day-of-month and day-of-week match either.
		return domOK || dowOK
	}
}

// nextCron returns the earliest instant strictly after from whose wall
// clock in loc matches expr. A spring-forward gap is skipped. A fall-back
// hour is matched once, at the earlier offset, so a daily slot does not
// fire twice.
func nextCron(expr string, from time.Time, loc *time.Location) (time.Time, error) {
	e, err := parseCron(expr)
	if err != nil {
		return time.Time{}, err
	}
	if loc == nil {
		loc = time.UTC
	}
	wall := from.In(loc)
	y, m, d := wall.Date()
	h, min, _ := wall.Clock()
	y, m, d, h, min = addWallMinute(y, m, d, h, min)
	end := from.AddDate(4, 0, 0)
	for {
		cand, ok := civil(loc, y, m, d, h, min)
		if ok && cand.After(from) && e.matches(cand.In(loc)) {
			return cand, nil
		}
		if !cand.IsZero() && cand.After(end) {
			break
		}
		// A gap minute has no civil instant. Bound the search by wall
		// steps as well so a missing hour cannot spin.
		y, m, d, h, min = addWallMinute(y, m, d, h, min)
		if time.Date(y, m, d, 0, 0, 0, 0, time.UTC).After(end.AddDate(0, 0, 2)) {
			break
		}
	}
	return time.Time{}, fmt.Errorf("cron: no match within 4 years")
}

func civil(loc *time.Location, y int, m time.Month, d, h, min int) (time.Time, bool) {
	t := time.Date(y, m, d, h, min, 0, 0, loc)
	if t.Year() != y || t.Month() != m || t.Day() != d || t.Hour() != h || t.Minute() != min {
		return time.Time{}, false
	}
	return t, true
}

func addWallMinute(y int, m time.Month, d, h, min int) (int, time.Month, int, int, int) {
	min++
	if min <= 59 {
		return y, m, d, h, min
	}
	min = 0
	h++
	if h <= 23 {
		return y, m, d, h, min
	}
	h = 0
	next := time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	return next.Year(), next.Month(), next.Day(), h, min
}

// fill sets bits in dst. dst must be addressable and longer than hi.
func fill(dst any, raw string, lo, hi int, names map[string]int) error {
	set := func(n int) {
		switch b := dst.(type) {
		case *[60]bool:
			b[n] = true
		case *[24]bool:
			b[n] = true
		case *[32]bool:
			b[n] = true
		case *[13]bool:
			b[n] = true
		case *[8]bool:
			b[n] = true
		default:
			panic("cron: bad bitset")
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("empty")
	}
	anySet := false
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return fmt.Errorf("empty list entry")
		}
		step := 1
		body := part
		if i := strings.IndexByte(part, '/'); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return fmt.Errorf("bad step %q", part[i+1:])
			}
			step = n
			body = part[:i]
		}
		start, end := lo, hi
		switch {
		case body == "*":
		case strings.Contains(body, "-"):
			a, b, ok := strings.Cut(body, "-")
			if !ok {
				return fmt.Errorf("bad range %q", body)
			}
			var err error
			start, err = parseToken(a, lo, hi, names)
			if err != nil {
				return err
			}
			end, err = parseToken(b, lo, hi, names)
			if err != nil {
				return err
			}
			if start > end {
				return fmt.Errorf("range %d-%d reversed", start, end)
			}
		default:
			n, err := parseToken(body, lo, hi, names)
			if err != nil {
				return err
			}
			start, end = n, n
		}
		for n := start; n <= end; n += step {
			set(n)
			anySet = true
		}
	}
	if !anySet {
		return fmt.Errorf("matches nothing")
	}
	return nil
}

func parseToken(raw string, lo, hi int, names map[string]int) (int, error) {
	raw = strings.TrimSpace(raw)
	if names != nil {
		if n, ok := names[strings.ToUpper(raw)]; ok {
			return n, nil
		}
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", raw)
	}
	if n < lo || n > hi {
		return 0, fmt.Errorf("%d outside %d-%d", n, lo, hi)
	}
	return n, nil
}

var monthNames = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

var dowNames = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

func loadLoc(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "UTC") {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("timezone %q: %w", name, err)
	}
	return loc, nil
}
