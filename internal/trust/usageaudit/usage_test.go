package usageaudit

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/pkg/protocol"
)

func TestAggregatePricesCacheAndLeavesUnknownModelsUnpriced(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "root", session.Meta{Title: "Root"}, []timed{
		at("2026-10-25T10:00:00Z", protocol.ModelSelected{Correlation: protocol.Correlation{SessionID: "root"}, Provider: "openai", Model: "priced"}),
		at("2026-10-25T10:00:01Z", protocol.UsageReported{
			Input: protocol.KnownTokens(1_000_000), Output: protocol.KnownTokens(500_000),
			CacheRead: protocol.KnownTokens(2_000_000), CacheCreation: protocol.KnownTokens(100_000),
			Source: protocol.UsageSourceActual,
		}),
		at("2026-10-26T01:00:00Z", protocol.UsageReported{
			Input: protocol.KnownTokens(0), Output: protocol.KnownTokens(10),
			Source: protocol.UsageSourceActual,
		}),
	})
	writeSession(t, dir, "child", session.Meta{Title: "Child", ParentSessionID: "root"}, []timed{
		at("2026-10-26T02:00:00Z", protocol.ModelSelected{Correlation: protocol.Correlation{SessionID: "child"}, Provider: "echo", Model: "echo"}),
		at("2026-10-26T02:00:01Z", protocol.UsageReported{
			Input: protocol.KnownTokens(40), Output: protocol.KnownTokens(5),
			CacheRead: protocol.KnownTokens(7), Source: protocol.UsageSourceEstimated,
		}),
	})
	writeSession(t, dir, "partial", session.Meta{Title: "Partial"}, []timed{
		at("2026-10-26T03:00:00Z", protocol.ModelSelected{Correlation: protocol.Correlation{SessionID: "partial"}, Provider: "openai", Model: "no-cache-rate"}),
		at("2026-10-26T03:00:01Z", protocol.UsageReported{
			Input: protocol.KnownTokens(1_000_000), Output: protocol.KnownTokens(0),
			CacheRead: protocol.KnownTokens(1_000_000), Source: protocol.UsageSourceActual,
		}),
		at("2026-10-26T03:00:02Z", protocol.SessionBudgetWarning{
			Correlation: protocol.Correlation{SessionID: "partial"},
			Level:       protocol.SessionBudgetLevel80,
			Kind:        protocol.SessionBudgetKindCostUSD,
			CostUSD:     1.5,
			MaxCostUSD:  2,
			Message:     "session cost budget 80%",
		}),
	})

	price := func(provider, model string) (Rates, bool) {
		switch provider + "/" + model {
		case "openai/priced":
			return Rates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 1.25, CacheReadKnown: true, CacheWriteKnown: true}, true
		case "openai/no-cache-rate":
			return Rates{Input: 2, Output: 8}, true
		default:
			return Rates{}, false
		}
	}
	report, err := Aggregate(dir, price)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]SessionUsage{}
	for _, s := range report.Sessions {
		byID[s.ID] = s
	}
	root := byID["root"]
	// 1e6*3 + 5e5*15 + 2e6*0.3 + 1e5*1.25, all per million, plus 10 output tokens.
	wantRoot := 3 + 7.5 + 0.6 + 0.125 + 0.00015
	if !root.CostKnown || root.CostPartial || !near(root.CostUSD, wantRoot) {
		t.Fatalf("root cost known=%v partial=%v usd=%v, want %v", root.CostKnown, root.CostPartial, root.CostUSD, wantRoot)
	}
	if root.CacheRead != 2_000_000 || !root.CacheReadKnown || root.CacheCreation != 100_000 {
		t.Fatalf("root cache read %d/%v write %d/%v", root.CacheRead, root.CacheReadKnown, root.CacheCreation, root.CacheCreateKnown)
	}
	if root.Input != 1_000_000 || root.Output != 500_010 {
		t.Fatalf("root tokens in/out = %d/%d", root.Input, root.Output)
	}
	child := byID["child"]
	if child.ParentID != "root" {
		t.Fatalf("child parent = %q", child.ParentID)
	}
	if child.CostKnown || child.Input != 40 || child.CacheRead != 7 {
		t.Fatalf("child = %+v, want tokens and an unknown cost", child)
	}
	if !strings.Contains(child.CostNote, "pricing unknown") {
		t.Fatalf("child note = %q", child.CostNote)
	}
	partial := byID["partial"]
	if !partial.CostKnown || !partial.CostPartial || !near(partial.CostUSD, 2) {
		t.Fatalf("partial known=%v partial=%v usd=%v, want $2 and unpriced cache", partial.CostKnown, partial.CostPartial, partial.CostUSD)
	}
	if !strings.Contains(partial.CostNote, "cache read") {
		t.Fatalf("partial note = %q", partial.CostNote)
	}

	var day26 DayUsage
	for _, d := range report.Days {
		if d.Day == "2026-10-26" {
			day26 = d
		}
	}
	if day26.Reports != 3 || !day26.CostPartial || day26.UnpricedEvents == 0 {
		t.Fatalf("day 26 = %+v", day26)
	}
	foundAlert := false
	for _, a := range report.Alerts {
		if a.SessionID == "partial" && a.Level == "80" && a.Message == "session cost budget 80%" {
			foundAlert = true
		}
	}
	if !foundAlert {
		t.Fatalf("alerts = %+v", report.Alerts)
	}

	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	alerts := DailyAlerts(DayUsage{Day: "2026-10-26", Reports: 1, CostKnown: true, CostUSD: 1.7}, 2, true, now)
	if len(alerts) != 1 || alerts[0].Level != "80" {
		t.Fatalf("daily alerts = %+v", alerts)
	}
	incomplete := DailyAlerts(day26, 2, true, now)
	if len(incomplete) != 1 || incomplete[0].Level != "incomplete" {
		t.Fatalf("incomplete = %+v", incomplete)
	}
	if got := DailyAlerts(DayUsage{Day: "2026-10-26"}, 2, true, now); got != nil {
		t.Fatalf("empty day raised %+v", got)
	}
}

func TestAggregateIgnoresChildBudgetWarningOnParentLog(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "root", session.Meta{Title: "Root"}, []timed{
		at("2026-10-26T01:00:00Z", protocol.SessionBudgetWarning{
			Correlation: protocol.Correlation{SessionID: "child"},
			Level:       protocol.SessionBudgetLevel80,
			Message:     "child budget",
		}),
		at("2026-10-26T01:00:01Z", protocol.UsageReported{
			Input: protocol.KnownTokens(10), Output: protocol.KnownTokens(1),
			Source: protocol.UsageSourceActual,
		}),
	})
	writeSession(t, dir, "child", session.Meta{Title: "Child", ParentSessionID: "root"}, []timed{
		at("2026-10-26T01:00:00Z", protocol.SessionBudgetWarning{
			Correlation: protocol.Correlation{SessionID: "child"},
			Level:       protocol.SessionBudgetLevel80,
			Message:     "child budget",
		}),
	})
	report, err := Aggregate(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var rootN, childN int
	for _, a := range report.Alerts {
		switch a.SessionID {
		case "root":
			rootN++
		case "child":
			childN++
		}
	}
	if rootN != 0 || childN != 1 {
		t.Fatalf("alerts = %+v", report.Alerts)
	}
}

func TestAggregateNotesMixedModels(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, dir, "mix", session.Meta{Title: "Mix"}, []timed{
		at("2026-10-26T01:00:00Z", protocol.ModelSelected{Correlation: protocol.Correlation{SessionID: "mix"}, Provider: "anthropic", Model: "opus"}),
		at("2026-10-26T01:00:01Z", protocol.UsageReported{Input: protocol.KnownTokens(1_000_000), Output: protocol.KnownTokens(0), Source: protocol.UsageSourceActual}),
		at("2026-10-26T01:00:02Z", protocol.ModelSelected{Correlation: protocol.Correlation{SessionID: "mix"}, Provider: "anthropic", Model: "haiku"}),
		at("2026-10-26T01:00:03Z", protocol.UsageReported{Input: protocol.KnownTokens(1_000_000), Output: protocol.KnownTokens(0), Source: protocol.UsageSourceActual}),
	})
	price := func(provider, model string) (Rates, bool) {
		switch model {
		case "opus":
			return Rates{Input: 15, Output: 75}, true
		case "haiku":
			return Rates{Input: 1, Output: 5}, true
		default:
			return Rates{}, false
		}
	}
	report, err := Aggregate(dir, price)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Sessions) != 1 {
		t.Fatalf("sessions = %+v", report.Sessions)
	}
	got := report.Sessions[0]
	if got.Model != "haiku" || !got.CostPartial || !strings.Contains(got.CostNote, "more than one model") {
		t.Fatalf("mixed row = %+v", got)
	}
	if !near(got.CostUSD, 16) {
		t.Fatalf("cost = %v, want 16", got.CostUSD)
	}
}

func TestAggregateSkipsOversizedLine(t *testing.T) {
	env, err := protocol.Wrap(protocol.UsageReported{
		Input: protocol.KnownTokens(4), Output: protocol.KnownTokens(1),
		Source: protocol.UsageSourceEstimated,
	})
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	old := sessionLineLimit
	sessionLineLimit = len(line) + 8
	t.Cleanup(func() { sessionLineLimit = old })
	dir := t.TempDir()
	path := session.LogPath(dir, "big")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Repeat("x", sessionLineLimit+40) + "\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := Aggregate(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.SkippedLines < 1 || len(report.Sessions) != 1 || report.Sessions[0].Input != 4 {
		t.Fatalf("report = %+v", report)
	}
}

type timed struct {
	at time.Time
	ev protocol.Event
}

func at(stamp string, ev protocol.Event) timed {
	when, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		panic(err)
	}
	return timed{at: when, ev: ev}
}

func writeSession(t *testing.T, dir, id string, meta session.Meta, events []timed) {
	t.Helper()
	f, err := os.Create(session.LogPath(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, item := range events {
		env, err := protocol.Wrap(item.ev)
		if err != nil {
			t.Fatal(err)
		}
		env.Time = item.at.UTC()
		line, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	if meta.Title != "" || meta.ParentSessionID != "" {
		raw, err := json.Marshal(meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(session.MetaPath(dir, id), append(raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func near(got, want float64) bool {
	return math.Abs(got-want) < 1e-9
}
