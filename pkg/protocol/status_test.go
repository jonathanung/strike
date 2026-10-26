package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestClassifyLivePriority(t *testing.T) {
	cases := []struct {
		name string
		in   LiveStatusInput
		want string
	}{
		{name: "quiet", in: LiveStatusInput{}, want: StatusIdle},
		{name: "end turn stays idle", in: LiveStatusInput{StopReason: "end_turn"}, want: StatusIdle},
		{name: "busy", in: LiveStatusInput{Busy: true}, want: StatusRunning},
		{name: "ask wins over busy", in: LiveStatusInput{Busy: true, PermissionPending: true}, want: StatusWaiting},
		{name: "question", in: LiveStatusInput{QuestionPending: true}, want: StatusWaiting},
		{name: "child running", in: LiveStatusInput{ChildRunning: true}, want: StatusRunning},
		{name: "child waiting wins over running", in: LiveStatusInput{ChildRunning: true, ChildWaiting: true}, want: StatusWaiting},
		{name: "blocked child", in: LiveStatusInput{ChildBlocked: true}, want: StatusBlocked},
		{name: "running wins over blocked", in: LiveStatusInput{Busy: true, ChildBlocked: true}, want: StatusRunning},
		{name: "failed stop", in: LiveStatusInput{StopReason: "provider_error"}, want: StatusFailed},
		{name: "budget", in: LiveStatusInput{StopReason: "budget_exhausted"}, want: StatusFailed},
		{name: "canceled", in: LiveStatusInput{StopReason: "canceled"}, want: StatusStopped},
		{name: "aborted", in: LiveStatusInput{StopReason: "aborted"}, want: StatusStopped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyLive(tc.in); got != tc.want {
				t.Fatalf("ClassifyLive(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeStatusAliases(t *testing.T) {
	if got, ok := KnownStatus("tool"); ok || got != "" {
		t.Fatalf("KnownStatus(tool) = %q, %v", got, ok)
	}
	if got := NormalizeStatus("nope"); got != StatusIdle {
		t.Fatalf("unknown = %q", got)
	}
	if got := NormalizeStatus(" Thinking "); got != StatusRunning {
		t.Fatalf("thinking = %q", got)
	}
	if got := NormalizeStatus("completed"); got != StatusDone {
		t.Fatalf("completed = %q", got)
	}
}

func TestAggregateGateway(t *testing.T) {
	rows := []SessionStatus{
		{SessionID: "a", Status: StatusDone},
		{SessionID: "b", Status: StatusBlocked},
		{SessionID: "c", Status: StatusIdle},
	}
	if got := AggregateGateway(rows); got != StatusBlocked {
		t.Fatalf("blocked aggregate = %q", got)
	}
	rows = append(rows, SessionStatus{SessionID: "d", Status: StatusRunning})
	if got := AggregateGateway(rows); got != StatusRunning {
		t.Fatalf("running aggregate = %q", got)
	}
	rows = append(rows, SessionStatus{SessionID: "e", Status: StatusWaiting})
	if got := AggregateGateway(rows); got != StatusWaiting {
		t.Fatalf("waiting aggregate = %q", got)
	}
}

func TestSessionStatusSchema(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "schemas", "session-status.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		IDs                 []string          `json:"ids"`
		Labels              map[string]string `json:"labels"`
		Tones               map[string]string `json:"tones"`
		ChipIdleTone        string            `json:"chipIdleTone"`
		Aliases             map[string]string `json:"aliases"`
		HeartbeatIntervalMs int64             `json:"heartbeatIntervalMs"`
		HeartbeatStaleMs    int64             `json:"heartbeatStaleMs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ids := StatusIDs()
	if len(doc.IDs) != len(ids) {
		t.Fatalf("schema ids %v, code %v", doc.IDs, ids)
	}
	for i, id := range ids {
		if doc.IDs[i] != id {
			t.Fatalf("schema ids %v, code %v", doc.IDs, ids)
		}
		if doc.Labels[id] != StatusLabel(id) {
			t.Fatalf("label %s: schema %q code %q", id, doc.Labels[id], StatusLabel(id))
		}
		if doc.Tones[id] != StatusTone(id) {
			t.Fatalf("tone %s: schema %q code %q", id, doc.Tones[id], StatusTone(id))
		}
		if got := NormalizeStatus(id); got != id {
			t.Fatalf("id %s normalized to %s", id, got)
		}
	}
	if doc.ChipIdleTone != StatusChipIdleTone {
		t.Fatalf("chip idle tone %q", doc.ChipIdleTone)
	}
	if doc.HeartbeatIntervalMs != HeartbeatIntervalMs || doc.HeartbeatStaleMs != HeartbeatStaleMs {
		t.Fatalf("heartbeat schema %d/%d code %d/%d", doc.HeartbeatIntervalMs, doc.HeartbeatStaleMs, HeartbeatIntervalMs, HeartbeatStaleMs)
	}
	for alias, want := range doc.Aliases {
		got, ok := KnownStatus(alias)
		if !ok || got != want {
			t.Fatalf("alias %s → %q, %v; schema wants %s", alias, got, ok, want)
		}
	}
}
