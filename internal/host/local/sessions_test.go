package local

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/session"
)

func TestSessionsAdapterChildrenAndReplay(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	root, err := mgr.Create(session.CreateOptions{ID: "root-a", Title: "root"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := mgr.Create(session.CreateOptions{
		ID:              "child-a",
		ParentSessionID: root.ID,
		Title:           "sub task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Append(child.ID, protocol.UserMessage{
		Correlation: protocol.Correlation{SessionID: child.ID, ParentSessionID: root.ID, Depth: 1},
		Text:        "hello child",
	}); err != nil {
		t.Fatal(err)
	}

	svc := NewSessions(mgr)
	if svc == nil {
		t.Fatal("NewSessions nil")
	}
	got, ok, err := svc.Get(child.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.ParentID != root.ID || got.Title != "sub task" {
		t.Errorf("Get = %+v", got)
	}
	kids, err := svc.Children(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(kids) != 1 || kids[0].ID != child.ID {
		t.Fatalf("Children = %+v", kids)
	}
	data, err := svc.ReplayJSONL(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hello child") {
		t.Errorf("ReplayJSONL missing payload: %s", data)
	}
	if _, ok, err := svc.Get("missing"); err != nil || ok {
		t.Errorf("missing Get: ok=%v err=%v", ok, err)
	}
}

func TestNewSessionsNil(t *testing.T) {
	if NewSessions(nil) != nil {
		t.Error("want nil adapter for nil manager")
	}
}
