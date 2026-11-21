package skillpropose

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDueWeekly(t *testing.T) {
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		ws   Workspace
		want bool
	}{
		{"off never scans", Workspace{Enabled: false, Dir: "/w", LastScan: now.Add(-30 * 24 * time.Hour)}, false},
		{"off and never scanned", Workspace{Dir: "/w"}, false},
		{"on without dir", Workspace{Enabled: true}, false},
		{"on never scanned", Workspace{Enabled: true, Dir: "/w"}, true},
		{"on scanned yesterday", Workspace{Enabled: true, Dir: "/w", LastScan: now.Add(-24 * time.Hour)}, false},
		{"on scanned just under a week", Workspace{Enabled: true, Dir: "/w", LastScan: now.Add(-WeeklyCadence + time.Minute)}, false},
		{"on scanned exactly a week ago", Workspace{Enabled: true, Dir: "/w", LastScan: now.Add(-WeeklyCadence)}, true},
		{"on scanned a month ago", Workspace{Enabled: true, Dir: "/w", LastScan: now.Add(-30 * 24 * time.Hour)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ws.DueWeekly(now); got != tc.want {
				t.Fatalf("DueWeekly = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStoreDueAndSetDir(t *testing.T) {
	st := NewStore(filepath.Join(t.TempDir(), "p.json"))
	now := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	on, off := true, false
	if _, err := st.Settings("b", &on, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Settings("a", &on, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Settings("c", &off, nil); err != nil {
		t.Fatal(err)
	}
	// No directory yet: nothing is due.
	if due, err := st.Due(now); err != nil || len(due) != 0 {
		t.Fatalf("due without dirs = %+v %v", due, err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if err := st.SetDir(k, "/work/"+k); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetDir("a", "  "); err != nil {
		t.Fatal(err)
	}
	due, err := st.Due(now)
	if err != nil {
		t.Fatal(err)
	}
	want := []DueWorkspace{{Key: "a", Dir: "/work/a"}, {Key: "b", Dir: "/work/b"}}
	if !reflect.DeepEqual(due, want) {
		t.Fatalf("due = %+v, want %+v", due, want)
	}
	// A scan resets the clock; a week later it is due again.
	if _, err := st.Scan("a", nil, nil, now); err != nil {
		t.Fatal(err)
	}
	due, _ = st.Due(now.Add(time.Hour))
	if len(due) != 1 || due[0].Key != "b" {
		t.Fatalf("due after scan = %+v", due)
	}
	due, _ = st.Due(now.Add(WeeklyCadence))
	if len(due) != 2 {
		t.Fatalf("due a week later = %+v", due)
	}
	// Turning the opt-in off stops the cadence.
	if _, err := st.Settings("b", &off, nil); err != nil {
		t.Fatal(err)
	}
	due, _ = st.Due(now.Add(WeeklyCadence))
	if len(due) != 1 || due[0].Key != "a" {
		t.Fatalf("due after opt-out = %+v", due)
	}
}
