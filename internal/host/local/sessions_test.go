package local

import (
	"context"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host"
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
	roots, err := svc.List(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0].ID != root.ID || roots[0].Title != "root" {
		t.Fatalf("List(rootsOnly) = %+v", roots)
	}
	all, err := svc.List(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("List(all) len = %d, want 2", len(all))
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

func TestSessionsAdapterExposesPRMetadata(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	root, err := mgr.Create(session.CreateOptions{ID: "root-pr", Title: "ship"})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.WriteMeta(dir, root.ID, session.Meta{
		Title:    "ship",
		PRURL:    "https://github.com/acme/repo/pull/12",
		PRNumber: 12,
		PRState:  session.PRStateOpen,
	}); err != nil {
		t.Fatal(err)
	}
	// Close so List reads from disk meta.
	if err := mgr.Close(root.ID); err != nil {
		t.Fatal(err)
	}
	svc := NewSessions(mgr)
	got, ok, err := svc.Get(root.ID)
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.PRURL != "https://github.com/acme/repo/pull/12" || got.PRNumber != 12 || got.PRState != "open" {
		t.Fatalf("Get PR fields = %+v", got)
	}
	list, err := svc.List(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].PRNumber != 12 {
		t.Fatalf("List = %+v", list)
	}
}

func TestRefreshPRStatesUpdatesSidecar(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	root, err := mgr.Create(session.CreateOptions{ID: "root-refresh", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.UpdateMeta(dir, root.ID, func(m *session.Meta) {
		m.PRURL = "https://github.com/acme/repo/pull/5"
		m.PRNumber = 5
		m.PRState = session.PRStateOpen
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Close(root.ID); err != nil {
		t.Fatal(err)
	}
	ad := sessionsAdapter{
		m: mgr,
		viewPR: func(ctx context.Context, number int, url string) (string, error) {
			if number != 5 {
				t.Fatalf("number = %d", number)
			}
			return "MERGED", nil
		},
	}
	in := []host.Session{{
		ID: root.ID, PRURL: "https://github.com/acme/repo/pull/5", PRNumber: 5, PRState: "open",
	}}
	out := ad.RefreshPRStates(in)
	if len(out) != 1 || out[0].PRState != "merged" {
		t.Fatalf("RefreshPRStates = %+v", out)
	}
	meta, err := session.ReadMeta(dir, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.PRState != session.PRStateMerged || meta.PRUpdatedAt == "" {
		t.Fatalf("sidecar after refresh = %+v", meta)
	}
}

func TestRefreshPRStatesLeavesCacheOnFailure(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	root, err := mgr.Create(session.CreateOptions{ID: "root-offline", Title: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.UpdateMeta(dir, root.ID, func(m *session.Meta) {
		m.PRURL = "https://github.com/acme/repo/pull/8"
		m.PRNumber = 8
		m.PRState = session.PRStateOpen
	}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Close(root.ID); err != nil {
		t.Fatal(err)
	}
	ad := sessionsAdapter{
		m: mgr,
		viewPR: func(context.Context, int, string) (string, error) {
			return "", context.DeadlineExceeded
		},
	}
	in := []host.Session{{
		ID: root.ID, PRURL: "https://github.com/acme/repo/pull/8", PRNumber: 8, PRState: "open",
	}}
	out := ad.RefreshPRStates(in)
	if out[0].PRState != "open" {
		t.Fatalf("want unchanged open, got %+v", out[0])
	}
	meta, err := session.ReadMeta(dir, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.PRState != session.PRStateOpen {
		t.Fatalf("sidecar changed on failure: %+v", meta)
	}
}
