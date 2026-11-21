package suggestion

import (
	"errors"
	"testing"
	"time"
)

func producerDraft(key string) Draft {
	return Draft{
		Title:       "Scheduled run failed",
		Why:         "The nightly script exited 1.",
		Prompt:      "Find out why the nightly script failed",
		Kind:        "schedule-failure",
		EvidenceKey: key,
	}
}

func TestDepositRequiresProducerAndKey(t *testing.T) {
	cases := []struct {
		name    string
		source  string
		key     string
		wantErr error
	}{
		{"scheduler", SourceScheduler, "schedule:a:failed", nil},
		{"trigger", SourceTrigger, "trigger:t:start-failed", nil},
		{"child agent", SourceChildAgent, "child:x", nil},
		{"autopilot", SourceAutopilot, "goal:g", nil},
		{"unknown source", "scheduler.heartbeat", "k", ErrInvalid},
		{"empty source", "", "k", ErrInvalid},
		{"no evidence key", SourceScheduler, " ", ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newStore(t)
			d := producerDraft(tc.key)
			d.Source = "ignored"
			sg, created, err := s.Deposit(tc.source, d)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || !created || sg.Source != tc.source {
				t.Fatalf("deposit = %+v created=%v err=%v", sg, created, err)
			}
		})
	}
}

func TestCreateRefusesReservedSources(t *testing.T) {
	for _, src := range Producers() {
		t.Run(src, func(t *testing.T) {
			s, _, _ := newStore(t)
			d := producerDraft("k")
			d.Source = src
			if _, _, err := s.Create(d); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Create(%q) err = %v, want ErrInvalid", src, err)
			}
		})
	}
	if !IsProducer(SourceScheduler) || IsProducer("smoke.producer") {
		t.Fatal("IsProducer mismatch")
	}
}

func TestDepositOnePerEvidenceKey(t *testing.T) {
	s, _, _ := newStore(t)
	first, created, err := s.Deposit(SourceScheduler, producerDraft("schedule:a:failed"))
	if err != nil || !created {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		again, created, err := s.Deposit(SourceScheduler, producerDraft("schedule:a:failed"))
		if err != nil || created || again.ID != first.ID {
			t.Fatalf("refile %d = %+v created=%v err=%v", i, again, created, err)
		}
	}
	list, _, _ := s.List()
	if len(list) != 1 || list[0].Repeats != 3 {
		t.Fatalf("list = %+v", list)
	}
}

func TestDismissedNeverResurfacesAfterExpiry(t *testing.T) {
	s, c, dir := newStore(t)
	d := producerDraft("schedule:a:failed")
	d.TTL = time.Hour
	first, _, err := s.Deposit(SourceScheduler, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Dismiss(first.ID, false); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(2 * time.Hour) // the dismissed row expires and is pruned
	if list, _, _ := s.List(); len(list) != 0 {
		t.Fatalf("expired row still listed: %+v", list)
	}
	if _, _, err := s.Deposit(SourceScheduler, d); !errors.Is(err, ErrDismissed) {
		t.Fatalf("redeposit after expiry err = %v, want ErrDismissed", err)
	}
	// The memory survives a reopen.
	re, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	re.SetClock(c.now)
	if _, _, err := re.Deposit(SourceScheduler, d); !errors.Is(err, ErrDismissed) {
		t.Fatalf("redeposit after reopen err = %v, want ErrDismissed", err)
	}
	// Another producer with the same key is a different idea.
	if _, created, err := re.Deposit(SourceTrigger, d); err != nil || !created {
		t.Fatalf("other producer = created %v err %v", created, err)
	}
}

func TestDismissedMemoryIsBounded(t *testing.T) {
	s, c, _ := newStore(t)
	s.mu.Lock()
	for i := 0; i < MaxDismissed+5; i++ {
		s.rememberDismissedLocked(SourceScheduler, time.Duration(i).String(), c.t)
	}
	n := len(s.doc.Dismissed)
	oldest := s.doc.Dismissed[0].Key
	s.mu.Unlock()
	if n != MaxDismissed || oldest != time.Duration(5).String() {
		t.Fatalf("len = %d oldest = %q", n, oldest)
	}
}

func TestMutedSourceAndKindRefuseDeposit(t *testing.T) {
	cases := []struct {
		name           string
		sources, kinds []string
	}{
		{"muted source", []string{SourceScheduler}, nil},
		{"muted kind", nil, []string{"schedule-failure"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newStore(t)
			if _, err := s.SetMuted(tc.sources, tc.kinds); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Deposit(SourceScheduler, producerDraft("k")); !errors.Is(err, ErrMuted) {
				t.Fatalf("err = %v, want ErrMuted", err)
			}
		})
	}
}

func TestInQuiet(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 20, h, m, 0, 0, time.UTC) }
	overnight := QuietHours{Enabled: true, Start: "22:00", End: "07:00"}
	daytime := QuietHours{Enabled: true, Start: "09:00", End: "17:30"}
	cases := []struct {
		name string
		now  time.Time
		q    QuietHours
		want bool
	}{
		{"disabled", at(23, 0), QuietHours{Start: "22:00", End: "07:00"}, false},
		{"overnight late", at(23, 0), overnight, true},
		{"overnight early", at(6, 59), overnight, true},
		{"overnight end exclusive", at(7, 0), overnight, false},
		{"overnight start inclusive", at(22, 0), overnight, true},
		{"overnight midday", at(12, 0), overnight, false},
		{"daytime inside", at(17, 29), daytime, true},
		{"daytime after", at(17, 30), daytime, false},
		{"same start end is all day", at(3, 0), QuietHours{Enabled: true, Start: "08:00", End: "08:00"}, true},
		{"bad clock never quiet", at(23, 0), QuietHours{Enabled: true, Start: "25:00", End: "07:00"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := InQuiet(tc.now, tc.q); got != tc.want {
				t.Fatalf("InQuiet = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestQuietHoursRefuseNewDeposits(t *testing.T) {
	s, c, _ := newStore(t) // clock at 12:00 UTC
	open, _, err := s.Deposit(SourceScheduler, producerDraft("existing"))
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := s.SetQuietHours(QuietHours{Enabled: true, Start: "11:00", End: "13:00"})
	if err != nil || !prefs.QuietHours.Enabled {
		t.Fatalf("set quiet = %+v %v", prefs, err)
	}
	if _, _, err := s.Deposit(SourceScheduler, producerDraft("new")); !errors.Is(err, ErrQuiet) {
		t.Fatalf("quiet deposit err = %v, want ErrQuiet", err)
	}
	// Tooling is held too: quiet hours are store-enforced.
	tool := producerDraft("tool")
	tool.Source = "smoke.producer"
	if _, _, err := s.Create(tool); !errors.Is(err, ErrQuiet) {
		t.Fatalf("quiet create err = %v, want ErrQuiet", err)
	}
	// A refiling onto a live row is not a new idea: it still dedupes.
	again, created, err := s.Deposit(SourceScheduler, producerDraft("existing"))
	if err != nil || created || again.ID != open.ID {
		t.Fatalf("refile in quiet = %+v created=%v err=%v", again, created, err)
	}
	c.t = c.t.Add(90 * time.Minute) // 13:30, outside the window
	if _, created, err := s.Deposit(SourceScheduler, producerDraft("new")); err != nil || !created {
		t.Fatalf("after quiet = created %v err %v", created, err)
	}
}

func TestSetQuietHoursValidates(t *testing.T) {
	cases := []struct {
		name string
		q    QuietHours
		ok   bool
	}{
		{"valid", QuietHours{Enabled: true, Start: "22:00", End: "07:00"}, true},
		{"valid disabled", QuietHours{Start: "00:00", End: "23:59"}, true},
		{"bad start", QuietHours{Enabled: true, Start: "7:00", End: "08:00"}, false},
		{"bad end", QuietHours{Enabled: true, Start: "07:00", End: "24:00"}, false},
		{"empty", QuietHours{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newStore(t)
			_, err := s.SetQuietHours(tc.q)
			if tc.ok != (err == nil) {
				t.Fatalf("err = %v, ok want %v", err, tc.ok)
			}
			if !tc.ok && !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestQuietHoursDefaultsOff(t *testing.T) {
	s, _, _ := newStore(t)
	_, prefs, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	want := QuietHours{Enabled: false, Start: "22:00", End: "07:00"}
	if prefs.QuietHours != want {
		t.Fatalf("default quiet = %+v", prefs.QuietHours)
	}
}
