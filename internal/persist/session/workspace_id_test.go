package session

import (
	"testing"
)

func TestCreateAndForkKeepWorkspaceID(t *testing.T) {
	m := NewManager(t.TempDir())
	info, err := m.Create(CreateOptions{Title: "root", ProjectKey: "/p", WorkspaceID: "ws-1"})
	if err != nil {
		t.Fatal(err)
	}
	if info.WorkspaceID != "ws-1" {
		t.Fatalf("create id = %q", info.WorkspaceID)
	}
	got, err := m.Get(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspaceID != "ws-1" {
		t.Fatalf("get id = %q", got.WorkspaceID)
	}
	forked, err := m.Fork(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if forked.WorkspaceID != "ws-1" {
		t.Fatalf("fork id = %q", forked.WorkspaceID)
	}
	meta, err := ReadMeta(m.Dir(), forked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.WorkspaceID != "ws-1" {
		t.Fatalf("fork meta = %+v", meta)
	}
}
