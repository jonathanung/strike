package main

import (
	"testing"

	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/trust/usageaudit"
)

func TestAppendChildEventRecordsApprovalAndShell(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	log, err := usageaudit.Open(usageaudit.DefaultDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })

	var wrote []string
	events := []protocol.Event{
		protocol.PermissionDecided{
			Correlation: protocol.Correlation{SessionID: "child-1"},
			Permission:  "bash",
			Action:      "run",
			Decision:    protocol.DecisionOnce,
		},
		protocol.ToolCallBegin{
			Correlation: protocol.Correlation{SessionID: "child-1"},
			CallID:      "call-1",
			Name:        "bash",
			Args:        []byte(`{"command":"ls"}`),
		},
	}
	for _, ev := range events {
		if err := appendChildEvent(func(id string, _ protocol.Event) error {
			wrote = append(wrote, id)
			return nil
		}, "child-1", ev); err != nil {
			t.Fatal(err)
		}
	}
	if len(wrote) != 2 || wrote[0] != "child-1" || wrote[1] != "child-1" {
		t.Fatalf("child log writes = %v", wrote)
	}
	entries, err := log.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Action != usageaudit.ActionApproval || entries[1].Action != usageaudit.ActionShell {
		t.Fatalf("entries = %+v", entries)
	}
	for _, e := range entries {
		if e.SessionID != "child-1" {
			t.Fatalf("session = %q", e.SessionID)
		}
	}
	if err := log.Verify(); err != nil {
		t.Fatal(err)
	}
}
