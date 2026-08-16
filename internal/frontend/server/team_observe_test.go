package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestLiveTeamObservationProjection(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("lead-1", t.TempDir(), nil, ops)

	live.Publish(protocol.TeamRoster{
		LeadID: "lead-1",
		Members: []protocol.TeamRosterMember{
			{SessionID: "lead-1", Name: "lead", State: "running", Role: "lead"},
			{SessionID: "c1", Name: "scout", Agent: "explore", State: "running", Role: "member"},
		},
	})
	live.Publish(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "c1"},
		Status:      protocol.ChildStatusCompleted,
		Name:        "scout",
		Summary:     "done",
		Handoff:     protocol.CompletionHandoff{Summary: "done"},
	})
	// Attempt to revive terminal member via roster must fail closed.
	live.Publish(protocol.TeamRoster{
		LeadID: "lead-1",
		Members: []protocol.TeamRosterMember{
			{SessionID: "c1", Name: "scout", State: "running", Role: "member"},
		},
	})
	live.Publish(protocol.DelegationChanged{
		ID: "d1", State: protocol.DelegationWorking, Version: 2, SessionID: "c1",
	})
	live.Publish(protocol.AgentMessage{
		From: "lead-1", To: "c1", Body: "ping", MessageID: "m1",
	})
	live.Publish(protocol.PathOverlap{
		Path: "a.go",
		Holders: []protocol.PathOverlapHolder{
			{SessionID: "c1"}, {SessionID: "c2"},
		},
	})
	live.Publish(protocol.ArtifactUpdated{ID: "art1", Type: "findings", Version: 1, Op: "create"})
	live.Publish(protocol.LedgerUpdated{ID: "L1", Kind: "decision", Status: "active", Op: "append", Statement: "ship it"})

	snap := live.Team()
	if !snap.Available {
		t.Fatal("expected available team snapshot")
	}
	if snap.LeadID != "lead-1" {
		t.Fatalf("leadId=%q", snap.LeadID)
	}
	m := snap.Members["c1"]
	if m.State != "completed" || !m.Terminal {
		t.Fatalf("terminal member not sticky: %+v", m)
	}
	if snap.Delegations["d1"].State != string(protocol.DelegationWorking) {
		t.Fatalf("delegation=%+v", snap.Delegations["d1"])
	}
	if len(snap.Messages) != 1 || snap.Messages[0].Body != "ping" {
		t.Fatalf("messages=%+v", snap.Messages)
	}
	if len(snap.PathOverlaps) != 1 {
		t.Fatalf("path overlaps=%d", len(snap.PathOverlaps))
	}
	if snap.Artifacts["art1"]["type"] != "findings" {
		t.Fatalf("artifact=%v", snap.Artifacts["art1"])
	}
	if snap.Ledger["L1"]["statement"] != "ship it" {
		t.Fatalf("ledger=%v", snap.Ledger["L1"])
	}
}

func TestHandleTeamEndpoint(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("root-a", t.TempDir(), nil, ops)
	live.Publish(protocol.TeamRoster{
		LeadID: "root-a",
		Members: []protocol.TeamRosterMember{
			{SessionID: "root-a", State: "running", Role: "lead"},
		},
	})
	hub := NewLiveHub(nil, nil)
	hub.Add("root-a", live)
	srv, err := New(Options{SessionDir: t.TempDir(), LiveHub: hub})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/team?root=root-a", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var snap TeamSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if snap.LeadID != "root-a" || !snap.Available {
		t.Fatalf("snap=%+v", snap)
	}

	// Missing live root → unavailable, not 5xx.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/team?root=missing", nil)
	rec2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("missing root status=%d", rec2.Code)
	}
	var empty TeamSnapshot
	_ = json.Unmarshal(rec2.Body.Bytes(), &empty)
	if empty.Available {
		t.Fatalf("expected unavailable: %+v", empty)
	}
}

func TestBootstrapTeamCapability(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("root-a", t.TempDir(), nil, ops)
	hub := NewLiveHub(nil, nil)
	hub.Add("root-a", live)
	srv, err := New(Options{SessionDir: t.TempDir(), LiveHub: hub})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var boot bootstrapResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if !boot.Capabilities.Team {
		t.Fatalf("expected team capability: %+v", boot.Capabilities)
	}
}
