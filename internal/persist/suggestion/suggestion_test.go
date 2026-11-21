package suggestion

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newStore(t *testing.T) (*Store, *clock, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	s.SetClock(c.now)
	return s, c, dir
}

func draft(title string) Draft {
	return Draft{
		Title:  title,
		Why:    "because the heartbeat saw it",
		Prompt: "Investigate " + title,
		Source: "scheduler.heartbeat",
		Kind:   "flaky-test",
		Evidence: []Evidence{
			{Label: "Session", SessionID: "sess-1"},
			{Label: "CI run", URL: "https://example.com/run/1"},
		},
	}
}

func TestCreateValidates(t *testing.T) {
	s, _, _ := newStore(t)
	long := strings.Repeat("x", 121)
	cases := []struct {
		name string
		mut  func(*Draft)
	}{
		{"empty title", func(d *Draft) { d.Title = " " }},
		{"long title", func(d *Draft) { d.Title = long }},
		{"empty prompt", func(d *Draft) { d.Prompt = "" }},
		{"bad source", func(d *Draft) { d.Source = "Scheduler Heartbeat" }},
		{"empty source", func(d *Draft) { d.Source = "" }},
		{"bad kind", func(d *Draft) { d.Kind = "a/b" }},
		{"evidence without label", func(d *Draft) { d.Evidence = []Evidence{{SessionID: "s"}} }},
		{"evidence js url", func(d *Draft) { d.Evidence = []Evidence{{Label: "x", URL: "javascript:alert(1)"}} }},
		{"evidence url with userinfo", func(d *Draft) { d.Evidence = []Evidence{{Label: "x", URL: "https://u:p@example.com"}} }},
		{"evidence session with slash", func(d *Draft) { d.Evidence = []Evidence{{Label: "x", SessionID: "../etc"}} }},
		{"too much evidence", func(d *Draft) { d.Evidence = make([]Evidence, 11) }},
	}
	for _, tc := range cases {
		d := draft("valid")
		tc.mut(&d)
		if _, _, err := s.Create(d); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: err = %v, want ErrInvalid", tc.name, err)
		}
	}
	list, _, _ := s.List()
	if len(list) != 0 {
		t.Fatalf("invalid drafts stored: %+v", list)
	}
}

func TestCreateDefaultsAndRedacts(t *testing.T) {
	s, c, _ := newStore(t)
	d := draft("Fix flaky test")
	d.Kind = ""
	d.Why = "token sk-ant-api03-" + strings.Repeat("a", 40) + " leaked"
	sg, created, err := s.Create(d)
	if err != nil || !created {
		t.Fatalf("create = %v %v", created, err)
	}
	if sg.Kind != "general" || sg.State != StateOpen || sg.EvidenceKey != "general:fix flaky test" {
		t.Fatalf("defaults = %+v", sg)
	}
	if !sg.ExpiresAt.Equal(c.t.Add(DefaultTTL)) {
		t.Fatalf("expiry = %v", sg.ExpiresAt)
	}
	if strings.Contains(sg.Why, strings.Repeat("a", 40)) {
		t.Fatalf("why not redacted: %q", sg.Why)
	}
	d2 := draft("long ttl")
	d2.TTL = 365 * 24 * time.Hour
	sg2, _, _ := s.Create(d2)
	if !sg2.ExpiresAt.Equal(c.t.Add(MaxTTL)) {
		t.Fatalf("ttl not clamped: %v", sg2.ExpiresAt)
	}
}

func TestDedupeByEvidenceKeyAcrossStates(t *testing.T) {
	s, _, _ := newStore(t)
	d := draft("Fix flaky test")
	d.EvidenceKey = "test:TestFoo"
	first, created, err := s.Create(d)
	if err != nil || !created {
		t.Fatal(err)
	}
	again, created, err := s.Create(d)
	if err != nil || created || again.ID != first.ID || again.Repeats != 1 {
		t.Fatalf("refile = %+v created=%v err=%v", again, created, err)
	}
	if _, err := s.Dismiss(first.ID, false); err != nil {
		t.Fatal(err)
	}
	after, created, err := s.Create(d)
	if err != nil || created || after.State != StateDismissed {
		t.Fatalf("dismissed idea refiled: %+v created=%v err=%v", after, created, err)
	}
	other := d
	other.Source = "goal.autopilot"
	if _, created, _ := s.Create(other); !created {
		t.Fatal("same key from another source must be a new suggestion")
	}
}

func TestExpiryPrunesAndFreesKey(t *testing.T) {
	s, c, _ := newStore(t)
	d := draft("Fix flaky test")
	d.TTL = time.Hour
	first, _, _ := s.Create(d)
	c.t = c.t.Add(2 * time.Hour)
	list, _, err := s.List()
	if err != nil || len(list) != 0 {
		t.Fatalf("expired still listed: %+v %v", list, err)
	}
	again, created, err := s.Create(d)
	if err != nil || !created || again.ID == first.ID {
		t.Fatalf("expired key not freed: %+v created=%v", again, created)
	}
}

func TestTransitions(t *testing.T) {
	s, c, _ := newStore(t)
	sg, _, _ := s.Create(draft("a"))
	if _, err := s.Snooze(sg.ID, 30*time.Second); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short snooze err = %v", err)
	}
	if _, err := s.Snooze(sg.ID, 31*24*time.Hour); !errors.Is(err, ErrInvalid) {
		t.Fatalf("long snooze err = %v", err)
	}
	snoozed, err := s.Snooze(sg.ID, time.Hour)
	if err != nil || snoozed.State != StateSnoozed || snoozed.SnoozeUntil == nil {
		t.Fatalf("snooze = %+v %v", snoozed, err)
	}
	c.t = c.t.Add(61 * time.Minute)
	list, _, _ := s.List()
	if list[0].State != StateOpen || list[0].SnoozeUntil != nil {
		t.Fatalf("due snooze not reopened: %+v", list[0])
	}
	acc, err := s.Accept(sg.ID)
	if err != nil || acc.State != StateAccepted {
		t.Fatalf("accept = %+v %v", acc, err)
	}
	if _, err := s.Accept(sg.ID); !errors.Is(err, ErrState) {
		t.Fatalf("second accept err = %v", err)
	}
	if _, err := s.Dismiss("missing", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestDismissSignalsAndMute(t *testing.T) {
	s, _, _ := newStore(t)
	a, _, _ := s.Create(draft("a"))
	b, _, _ := s.Create(draft("b"))
	if _, err := s.Dismiss(a.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dismiss(b.ID, true); err != nil {
		t.Fatal(err)
	}
	_, prefs, _ := s.List()
	if prefs.Dismissals["scheduler.heartbeat/flaky-test"] != 2 {
		t.Fatalf("dismissals = %+v", prefs.Dismissals)
	}
	if len(prefs.MutedKinds) != 1 || prefs.MutedKinds[0] != "flaky-test" {
		t.Fatalf("never did not mute kind: %+v", prefs.MutedKinds)
	}
	if _, _, err := s.Create(draft("c")); !errors.Is(err, ErrMuted) {
		t.Fatalf("muted kind err = %v", err)
	}
	if _, err := s.SetMuted([]string{"scheduler.heartbeat", "scheduler.heartbeat"}, nil); err != nil {
		t.Fatal(err)
	}
	other := draft("d")
	other.Kind = "docs"
	if _, _, err := s.Create(other); !errors.Is(err, ErrMuted) {
		t.Fatalf("muted source err = %v", err)
	}
	_, prefs, _ = s.List()
	if len(prefs.MutedSources) != 1 || len(prefs.MutedKinds) != 0 {
		t.Fatalf("SetMuted = %+v", prefs)
	}
	if _, err := s.SetMuted([]string{"Bad Token"}, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad mute err = %v", err)
	}
}

func TestPersistsWith0600AndSurvivesReopen(t *testing.T) {
	s, _, dir := newStore(t)
	sg, _, _ := s.Create(draft("a"))
	info, err := os.Stat(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, _, _ := again.List()
	if len(list) != 1 || list[0].ID != sg.ID {
		t.Fatalf("reopen = %+v", list)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if list, _, _ := corrupt.List(); len(list) != 0 {
		t.Fatalf("corrupt file = %+v", list)
	}
}

func TestCapDropsSettledFirst(t *testing.T) {
	s, c, _ := newStore(t)
	first, _, _ := s.Create(draft("keep-open-oldest"))
	settled, _, _ := s.Create(draft("settled"))
	if _, err := s.Dismiss(settled.ID, false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxStored-1; i++ {
		c.t = c.t.Add(time.Second)
		d := draft("n")
		d.EvidenceKey = "k" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + time.Duration(i).String()
		if _, _, err := s.Create(d); err != nil {
			t.Fatal(err)
		}
	}
	list, _, _ := s.List()
	if len(list) != MaxStored {
		t.Fatalf("len = %d", len(list))
	}
	var hasFirst, hasSettled bool
	for _, sg := range list {
		hasFirst = hasFirst || sg.ID == first.ID
		hasSettled = hasSettled || sg.ID == settled.ID
	}
	if !hasFirst || hasSettled {
		t.Fatalf("cap dropped wrong row: first=%v settled=%v", hasFirst, hasSettled)
	}
}

func TestValidToken(t *testing.T) {
	cases := map[string]bool{
		"scheduler.heartbeat":   true,
		"goal-autopilot_2":      true,
		"":                      false,
		"Upper":                 false,
		"has space":             false,
		strings.Repeat("a", 65): false,
	}
	for in, want := range cases {
		if got := ValidToken(in); got != want {
			t.Fatalf("ValidToken(%q) = %v", in, got)
		}
	}
}
