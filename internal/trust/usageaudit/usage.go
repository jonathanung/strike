package usageaudit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/pkg/protocol"
)

// Rates is USD per million tokens from a catalog. Cache rates are Known
// only when the catalog published a non-zero cache price. A zero cache
// field cannot be told apart from an omitted one, so cache tokens stay
// unpriced instead of being billed at the input rate.
type Rates struct {
	Input           float64
	Output          float64
	CacheRead       float64
	CacheWrite      float64
	CacheReadKnown  bool
	CacheWriteKnown bool
}

// Pricer returns catalog rates. The bool is false when the model has no
// published price. Callers must not substitute a guess.
type Pricer func(provider, model string) (Rates, bool)

// Alert is a budget or engine warning. Messages quote recorded figures only.
type Alert struct {
	Level     string `json:"level"`
	Scope     string `json:"scope,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Message   string `json:"message"`
}

// TokenTotals is a sum of known token parts. A false *Known flag means no
// event reported that part — it is not a measured zero.
type TokenTotals struct {
	Input            int  `json:"input"`
	Output           int  `json:"output"`
	CacheRead        int  `json:"cacheRead"`
	CacheCreation    int  `json:"cacheCreation"`
	InputKnown       bool `json:"inputKnown"`
	OutputKnown      bool `json:"outputKnown"`
	CacheReadKnown   bool `json:"cacheReadKnown"`
	CacheCreateKnown bool `json:"cacheCreationKnown"`
	Reports          int  `json:"reports"`
}

func (t *TokenTotals) add(ev protocol.UsageReported) {
	t.Reports++
	addPart(&t.InputKnown, &t.Input, ev.Input)
	addPart(&t.OutputKnown, &t.Output, ev.Output)
	addPart(&t.CacheReadKnown, &t.CacheRead, ev.CacheRead)
	addPart(&t.CacheCreateKnown, &t.CacheCreation, ev.CacheCreation)
}

func addPart(ok *bool, sum *int, part protocol.TokenCount) {
	if !part.Known {
		return
	}
	if !*ok {
		*ok = true
		*sum = part.N
		return
	}
	*sum += part.N
}

// SessionUsage is one session, root or delegated child.
type SessionUsage struct {
	ID               string  `json:"id"`
	Title            string  `json:"title,omitempty"`
	ParentID         string  `json:"parentId,omitempty"`
	Provider         string  `json:"provider,omitempty"`
	Model            string  `json:"model,omitempty"`
	FirstDay         string  `json:"firstDay,omitempty"`
	LastDay          string  `json:"lastDay,omitempty"`
	Source           string  `json:"source"`
	Input            int     `json:"input"`
	Output           int     `json:"output"`
	CacheRead        int     `json:"cacheRead"`
	CacheCreation    int     `json:"cacheCreation"`
	InputKnown       bool    `json:"inputKnown"`
	OutputKnown      bool    `json:"outputKnown"`
	CacheReadKnown   bool    `json:"cacheReadKnown"`
	CacheCreateKnown bool    `json:"cacheCreationKnown"`
	Reports          int     `json:"reports"`
	CostUSD          float64 `json:"costUsd,omitempty"`
	CostKnown        bool    `json:"costKnown"`
	CostPartial      bool    `json:"costPartial"`
	CostNote         string  `json:"costNote,omitempty"`
}

// DayUsage is every usage.reported event whose timestamp falls on that UTC day.
type DayUsage struct {
	Day              string  `json:"day"`
	Sessions         int     `json:"sessions"`
	Input            int     `json:"input"`
	Output           int     `json:"output"`
	CacheRead        int     `json:"cacheRead"`
	CacheCreation    int     `json:"cacheCreation"`
	InputKnown       bool    `json:"inputKnown"`
	OutputKnown      bool    `json:"outputKnown"`
	CacheReadKnown   bool    `json:"cacheReadKnown"`
	CacheCreateKnown bool    `json:"cacheCreationKnown"`
	Reports          int     `json:"reports"`
	CostUSD          float64 `json:"costUsd,omitempty"`
	CostKnown        bool    `json:"costKnown"`
	CostPartial      bool    `json:"costPartial"`
	UnpricedEvents   int     `json:"unpricedEvents"`
	CostNote         string  `json:"costNote,omitempty"`
}

// Report is the dashboard payload before the daily budget is applied.
type Report struct {
	Sessions     []SessionUsage `json:"sessions"`
	Days         []DayUsage     `json:"days"`
	Totals       TokenTotals    `json:"totals"`
	CostUSD      float64        `json:"costUsd,omitempty"`
	CostKnown    bool           `json:"costKnown"`
	CostPartial  bool           `json:"costPartial"`
	CostNote     string         `json:"costNote,omitempty"`
	Unpriced     int            `json:"unpricedSessions"`
	SkippedLines int            `json:"skippedLines,omitempty"`
	Alerts       []Alert        `json:"alerts,omitempty"`
}

type acc struct {
	tokens    TokenTotals
	cost      float64
	costKnown bool
	partial   bool
	unpriced  int
	notes     map[string]struct{}
	anyActual bool
	anyEst    bool
	provider  string
	model     string
	seenModel map[string]struct{}
	firstDay  string
	lastDay   string
	title     string
	parent    string
}

func (a *acc) note(n string) {
	n = strings.TrimSpace(n)
	if n == "" {
		return
	}
	if a.notes == nil {
		a.notes = map[string]struct{}{}
	}
	a.notes[n] = struct{}{}
}

func (a *acc) noteJoin() string {
	if len(a.notes) == 0 {
		return ""
	}
	out := make([]string, 0, len(a.notes))
	for n := range a.notes {
		out = append(out, n)
	}
	sort.Strings(out)
	return strings.Join(out, "; ")
}

func (a *acc) addCost(usd float64, known, partial bool, note string) {
	if known {
		a.cost += usd
		a.costKnown = true
	} else {
		a.unpriced++
	}
	if partial || (!known && a.tokens.Reports > 0) {
		a.partial = true
	}
	a.note(note)
}

// Aggregate reads every session JSONL under dir. price may be nil, which
// leaves every cost unknown.
func Aggregate(dir string, price Pricer) (Report, error) {
	var report Report
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return report, nil
		}
		return report, err
	}
	dayIndex := map[string]*dayAcc{}
	var sessions []SessionUsage
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if id == "" || strings.Contains(id, string(filepath.Separator)) {
			continue
		}
		su, days, alerts, skipped, err := scanSession(filepath.Join(dir, name), id, price)
		report.SkippedLines += skipped
		if err != nil {
			return report, err
		}
		report.Alerts = append(report.Alerts, alerts...)
		if su.Reports == 0 && len(alerts) == 0 {
			continue
		}
		if su.Reports > 0 {
			sessions = append(sessions, su)
			for day, part := range days {
				bucket := dayIndex[day]
				if bucket == nil {
					bucket = &dayAcc{day: day, seen: map[string]struct{}{}}
					dayIndex[day] = bucket
				}
				bucket.add(id, part)
			}
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		if sessions[i].LastDay != sessions[j].LastDay {
			return sessions[i].LastDay > sessions[j].LastDay
		}
		return sessions[i].ID > sessions[j].ID
	})
	report.Sessions = sessions
	for _, su := range sessions {
		report.Totals.Reports += su.Reports
		addKnown(&report.Totals.InputKnown, &report.Totals.Input, su.InputKnown, su.Input)
		addKnown(&report.Totals.OutputKnown, &report.Totals.Output, su.OutputKnown, su.Output)
		addKnown(&report.Totals.CacheReadKnown, &report.Totals.CacheRead, su.CacheReadKnown, su.CacheRead)
		addKnown(&report.Totals.CacheCreateKnown, &report.Totals.CacheCreation, su.CacheCreateKnown, su.CacheCreation)
		if su.CostKnown {
			report.CostUSD += su.CostUSD
			report.CostKnown = true
		} else {
			report.Unpriced++
		}
		if su.CostPartial || !su.CostKnown {
			report.CostPartial = true
		}
		if su.CostNote != "" {
			if report.CostNote == "" {
				report.CostNote = su.CostNote
			} else if !strings.Contains(report.CostNote, su.CostNote) {
				report.CostNote += "; " + su.CostNote
			}
		}
	}
	if report.Unpriced > 0 {
		note := fmt.Sprintf("%d session(s) have no published price; their tokens are not converted to dollars", report.Unpriced)
		if report.CostNote == "" {
			report.CostNote = note
		} else if !strings.Contains(report.CostNote, "no published price") {
			report.CostNote = note + "; " + report.CostNote
		}
	}
	days := make([]DayUsage, 0, len(dayIndex))
	for _, bucket := range dayIndex {
		days = append(days, bucket.usage())
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Day > days[j].Day })
	report.Days = days
	if report.Sessions == nil {
		report.Sessions = []SessionUsage{}
	}
	if report.Days == nil {
		report.Days = []DayUsage{}
	}
	return report, nil
}

func addKnown(ok *bool, sum *int, partOK bool, part int) {
	if !partOK {
		return
	}
	if !*ok {
		*ok = true
		*sum = part
		return
	}
	*sum += part
}

type dayAcc struct {
	day      string
	seen     map[string]struct{}
	tokens   TokenTotals
	cost     float64
	known    bool
	partial  bool
	unpriced int
	notes    map[string]struct{}
}

type dayPart struct {
	tokens   TokenTotals
	cost     float64
	known    bool
	partial  bool
	unpriced int
	note     string
}

func (d *dayAcc) add(sessionID string, part dayPart) {
	d.seen[sessionID] = struct{}{}
	d.tokens.Reports += part.tokens.Reports
	addKnown(&d.tokens.InputKnown, &d.tokens.Input, part.tokens.InputKnown, part.tokens.Input)
	addKnown(&d.tokens.OutputKnown, &d.tokens.Output, part.tokens.OutputKnown, part.tokens.Output)
	addKnown(&d.tokens.CacheReadKnown, &d.tokens.CacheRead, part.tokens.CacheReadKnown, part.tokens.CacheRead)
	addKnown(&d.tokens.CacheCreateKnown, &d.tokens.CacheCreation, part.tokens.CacheCreateKnown, part.tokens.CacheCreation)
	if part.known {
		d.cost += part.cost
		d.known = true
	}
	d.unpriced += part.unpriced
	if part.partial || part.unpriced > 0 {
		d.partial = true
	}
	if part.note != "" {
		if d.notes == nil {
			d.notes = map[string]struct{}{}
		}
		d.notes[part.note] = struct{}{}
	}
}

func (d *dayAcc) usage() DayUsage {
	notes := make([]string, 0, len(d.notes))
	for n := range d.notes {
		notes = append(notes, n)
	}
	sort.Strings(notes)
	return DayUsage{
		Day:              d.day,
		Sessions:         len(d.seen),
		Input:            d.tokens.Input,
		Output:           d.tokens.Output,
		CacheRead:        d.tokens.CacheRead,
		CacheCreation:    d.tokens.CacheCreation,
		InputKnown:       d.tokens.InputKnown,
		OutputKnown:      d.tokens.OutputKnown,
		CacheReadKnown:   d.tokens.CacheReadKnown,
		CacheCreateKnown: d.tokens.CacheCreateKnown,
		Reports:          d.tokens.Reports,
		CostUSD:          d.cost,
		CostKnown:        d.known,
		CostPartial:      d.partial,
		UnpricedEvents:   d.unpriced,
		CostNote:         strings.Join(notes, "; "),
	}
}

func scanSession(path, id string, price Pricer) (SessionUsage, map[string]dayPart, []Alert, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionUsage{}, nil, nil, 0, err
	}
	defer f.Close()
	meta := readMeta(filepath.Dir(path), id)
	var (
		acc      acc
		provider string
		model    string
		days     = map[string]dayPart{}
		alerts   []Alert
		skipped  int
	)
	acc.title = meta.Title
	acc.parent = meta.ParentSessionID
	// Same ceiling as session.readLogLines. A larger tool argument or image
	// line is skipped so one session cannot blank the whole dashboard.
	br := bufio.NewReaderSize(f, 64*1024)
	for {
		raw, tooLong, err := readCappedLine(br, sessionLineLimit)
		if tooLong {
			skipped++
		}
		if err == io.EOF {
			if !tooLong {
				if line := strings.TrimSpace(raw); line != "" {
					skipped += applyUsageLine(line, id, price, &acc, &provider, &model, days, &alerts)
				}
			}
			break
		}
		if err != nil {
			return SessionUsage{}, nil, nil, skipped, err
		}
		if tooLong {
			continue
		}
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		skipped += applyUsageLine(line, id, price, &acc, &provider, &model, days, &alerts)
	}
	if len(acc.seenModel) > 1 {
		acc.note("priced across more than one model; the label is the latest")
		acc.partial = true
	}
	if acc.tokens.Reports == 0 {
		return SessionUsage{ID: id, Title: acc.title, ParentID: acc.parent}, nil, alerts, skipped, nil
	}
	su := SessionUsage{
		ID:               id,
		Title:            acc.title,
		ParentID:         acc.parent,
		Provider:         acc.provider,
		Model:            acc.model,
		FirstDay:         acc.firstDay,
		LastDay:          acc.lastDay,
		Source:           sourceLabel(acc.anyActual, acc.anyEst),
		Input:            acc.tokens.Input,
		Output:           acc.tokens.Output,
		CacheRead:        acc.tokens.CacheRead,
		CacheCreation:    acc.tokens.CacheCreation,
		InputKnown:       acc.tokens.InputKnown,
		OutputKnown:      acc.tokens.OutputKnown,
		CacheReadKnown:   acc.tokens.CacheReadKnown,
		CacheCreateKnown: acc.tokens.CacheCreateKnown,
		Reports:          acc.tokens.Reports,
		CostUSD:          acc.cost,
		CostKnown:        acc.costKnown,
		CostPartial:      acc.partial,
		CostNote:         acc.noteJoin(),
	}
	return su, days, alerts, skipped, nil
}

// sessionLineLimit matches session.readLogLines. Tests lower it.
var sessionLineLimit = 32 << 20

func applyUsageLine(line, id string, price Pricer, acc *acc, provider, model *string, days map[string]dayPart, alerts *[]Alert) int {
	var env protocol.Envelope
	if err := json.Unmarshal([]byte(line), &env); err != nil {
		return 1
	}
	switch env.Type {
	case "model.selected":
		var ev protocol.ModelSelected
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return 1
		}
		if ev.Provider != "" {
			*provider = ev.Provider
		}
		if ev.Model != "" {
			*model = ev.Model
		}
	case "session.titled":
		var ev protocol.SessionTitled
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return 1
		}
		if strings.TrimSpace(ev.Title) != "" && acc.title == "" {
			acc.title = ev.Title
		}
	case "session.budget_warning":
		var ev protocol.SessionBudgetWarning
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return 1
		}
		// A child warning re-emitted onto the parent log names the child.
		owner := strings.TrimSpace(ev.SessionID)
		if owner != "" && owner != id {
			return 0
		}
		*alerts = append(*alerts, budgetAlert(id, ev))
	case "usage.reported":
		var ev protocol.UsageReported
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return 1
		}
		day := ""
		if !env.Time.IsZero() {
			day = env.Time.UTC().Format("2006-01-02")
		}
		acc.tokens.add(ev)
		switch ev.Source {
		case protocol.UsageSourceActual:
			acc.anyActual = true
		case protocol.UsageSourceEstimated:
			acc.anyEst = true
		}
		if *provider != "" {
			acc.provider = *provider
		}
		if *model != "" {
			acc.model = *model
		}
		if *provider != "" && *model != "" {
			if acc.seenModel == nil {
				acc.seenModel = map[string]struct{}{}
			}
			acc.seenModel[*provider+"/"+*model] = struct{}{}
		}
		if day != "" {
			if acc.firstDay == "" || day < acc.firstDay {
				acc.firstDay = day
			}
			if day > acc.lastDay {
				acc.lastDay = day
			}
		}
		usd, known, partial, note := priceUsage(price, *provider, *model, ev)
		acc.addCost(usd, known, partial, note)
		part := days[day]
		part.tokens.add(ev)
		if known {
			part.cost += usd
			part.known = true
		} else {
			part.unpriced++
		}
		if partial || !known {
			part.partial = true
		}
		if note != "" {
			part.note = note
		}
		days[day] = part
	default:
	}
	return 0
}

func readCappedLine(r *bufio.Reader, max int) (string, bool, error) {
	if max <= 0 {
		max = 32 << 20
	}
	var b strings.Builder
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if b.Len()+len(chunk) > max {
				tooLong = true
				b.Reset()
			} else {
				b.Write(chunk)
			}
		}
		if err == nil {
			if tooLong {
				return "", true, nil
			}
			return b.String(), false, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if tooLong {
				return "", true, io.EOF
			}
			return b.String(), false, io.EOF
		}
		return "", false, err
	}
}

func priceUsage(price Pricer, provider, model string, ev protocol.UsageReported) (usd float64, known, partial bool, note string) {
	if price == nil || provider == "" || model == "" {
		return 0, false, false, "pricing unknown for this model"
	}
	rates, ok := price(provider, model)
	if !ok {
		return 0, false, false, "pricing unknown for this model"
	}
	var notes []string
	if ev.Input.Known {
		usd += float64(ev.Input.N) * rates.Input / 1_000_000
		known = true
	}
	if ev.Output.Known {
		usd += float64(ev.Output.N) * rates.Output / 1_000_000
		known = true
	}
	if ev.CacheRead.Known && ev.CacheRead.N != 0 {
		if rates.CacheReadKnown {
			usd += float64(ev.CacheRead.N) * rates.CacheRead / 1_000_000
			known = true
		} else {
			partial = true
			notes = append(notes, "cache read tokens are not priced for this model")
		}
	}
	if ev.CacheCreation.Known && ev.CacheCreation.N != 0 {
		if rates.CacheWriteKnown {
			usd += float64(ev.CacheCreation.N) * rates.CacheWrite / 1_000_000
			known = true
		} else {
			partial = true
			notes = append(notes, "cache write tokens are not priced for this model")
		}
	}
	if !ev.Input.Known || !ev.Output.Known {
		if known {
			partial = true
		}
	}
	if !known {
		notes = append(notes, "token split was not reported")
	}
	return usd, known, partial, strings.Join(notes, "; ")
}

func sourceLabel(actual, estimated bool) string {
	switch {
	case actual && estimated:
		return "mixed"
	case estimated:
		return protocol.UsageSourceEstimated
	case actual:
		return protocol.UsageSourceActual
	default:
		return "unknown"
	}
}

func budgetAlert(sessionID string, ev protocol.SessionBudgetWarning) Alert {
	msg := strings.TrimSpace(ev.Message)
	if msg == "" {
		switch ev.Kind {
		case protocol.SessionBudgetKindTurnTokens:
			msg = fmt.Sprintf("engine turn-token budget %s%%: %d / %d", ev.Level, ev.TokensUsed, ev.MaxTokens)
		case protocol.SessionBudgetKindCostUSD, "":
			if ev.MaxCostUSD > 0 {
				msg = fmt.Sprintf("engine session cost budget %s%%: %.4f / %.4f USD", ev.Level, ev.CostUSD, ev.MaxCostUSD)
			} else {
				msg = fmt.Sprintf("engine session budget %s%%", ev.Level)
			}
		default:
			msg = fmt.Sprintf("engine session budget %s%% (%s)", ev.Level, ev.Kind)
		}
	}
	return Alert{
		Level:     ev.Level,
		Scope:     "session",
		SessionID: sessionID,
		Message:   msg,
	}
}

func readMeta(dir, id string) session.Meta {
	data, err := os.ReadFile(session.MetaPath(dir, id))
	if err != nil {
		return session.Meta{}
	}
	var meta session.Meta
	if err := json.Unmarshal(data, &meta); err != nil {
		return session.Meta{}
	}
	return meta
}

// DailyAlerts turns today's priced total into 50/80/100 warnings.
// now selects the UTC day. Incomplete prices never count as "within budget".
func DailyAlerts(day DayUsage, budget float64, budgetSet bool, now time.Time) []Alert {
	if !budgetSet || budget <= 0 {
		return nil
	}
	today := now.UTC().Format("2006-01-02")
	if day.Day != "" && day.Day != today {
		return nil
	}
	if day.Day == "" {
		day.Day = today
	}
	if day.Reports == 0 && day.UnpricedEvents == 0 && !day.CostKnown {
		return nil
	}
	if day.CostPartial || day.UnpricedEvents > 0 || !day.CostKnown {
		return []Alert{{
			Level:   "incomplete",
			Scope:   "day",
			Message: fmt.Sprintf("daily budget is $%g but today's price is incomplete, so no spend alert is raised. %s", budget, strings.TrimSpace(day.CostNote)),
		}}
	}
	if !day.CostKnown {
		return []Alert{{
			Level:   "incomplete",
			Scope:   "day",
			Message: fmt.Sprintf("daily budget is $%g. No priced usage is recorded for %s.", budget, today),
		}}
	}
	ratio := day.CostUSD / budget
	level := ""
	switch {
	case ratio >= 1:
		level = "100"
	case ratio >= 0.8:
		level = "80"
	case ratio >= 0.5:
		level = "50"
	default:
		return nil
	}
	return []Alert{{
		Level:   level,
		Scope:   "day",
		Message: fmt.Sprintf("priced usage today is $%.4f of a $%g daily budget (%s%%)", day.CostUSD, budget, level),
	}}
}
