package local

import (
	"context"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/persist/session"
	"github.com/jonathanung/strike-cli/internal/protocol"
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

	svc := NewSessions(mgr, "")
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
	if NewSessions(nil, "") != nil {
		t.Error("want nil adapter for nil manager")
	}
}

func TestReplayJSONLSyncsOpenSession(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	root, err := mgr.Create(session.CreateOptions{ID: "root-sync", Title: "root"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := mgr.Create(session.CreateOptions{
		ID:              "child-sync",
		ParentSessionID: root.ID,
		Title:           "live",
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewSessions(mgr, "")
	// Append after adapter creation; ReplayJSONL must flush before read.
	if err := mgr.Append(child.ID, protocol.TextDelta{
		Correlation: protocol.Correlation{SessionID: child.ID, ParentSessionID: root.ID, Depth: 1},
		Text:        "synced-live-delta",
	}); err != nil {
		t.Fatal(err)
	}
	data, err := svc.ReplayJSONL(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "synced-live-delta") {
		t.Fatalf("ReplayJSONL missed unsynced append: %s", data)
	}
}

func TestSessionsAdapterFork(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	root, err := mgr.Create(session.CreateOptions{ID: "root-f", Title: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Append(root.ID, protocol.UserMessage{
		Correlation: protocol.Correlation{SessionID: root.ID},
		Text:        "hi",
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewSessions(mgr, "")
	child, err := svc.Fork(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if child.ID == root.ID || child.ParentID != "" {
		t.Fatalf("child = %+v", child)
	}
	if child.Title != "fork of work" {
		t.Fatalf("title = %q", child.Title)
	}
	data, err := svc.ReplayJSONL(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "hi") {
		t.Errorf("fork log missing prefix: %s", data)
	}
	// Both roots listed.
	roots, err := svc.List(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 {
		t.Fatalf("roots = %+v", roots)
	}
}

func TestSessionsAdapterForkAt(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	root, err := mgr.Create(session.CreateOptions{ID: "root-at", Title: "work"})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", "two"} {
		if err := mgr.Append(root.ID, protocol.UserMessage{
			Correlation: protocol.Correlation{SessionID: root.ID},
			Text:        text,
		}); err != nil {
			t.Fatal(err)
		}
		if err := mgr.Append(root.ID, protocol.TurnCompleted{
			Correlation: protocol.Correlation{SessionID: root.ID},
			StopReason:  "end_turn",
		}); err != nil {
			t.Fatal(err)
		}
	}
	svc := NewSessions(mgr, "")
	child, err := svc.ForkAt(root.ID, 2) // first user + turn.completed
	if err != nil {
		t.Fatal(err)
	}
	data, err := svc.ReplayJSONL(child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "one") {
		t.Fatalf("missing one: %s", data)
	}
	if strings.Contains(string(data), "two") {
		t.Fatalf("should not include two: %s", data)
	}
	src, err := svc.ReplayJSONL(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "two") {
		t.Fatal("source should still have two")
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
	svc := NewSessions(mgr, "")
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

func TestSessionsListFiltersByProject(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	projA := "/tmp/proj-a"
	projB := "/tmp/proj-b"
	a, err := mgr.Create(session.CreateOptions{ID: "root-a", Title: "work in A", ProjectKey: projA})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Create(session.CreateOptions{ID: "root-b", Title: "work in B", ProjectKey: projB}); err != nil {
		t.Fatal(err)
	}
	// Legacy session without project key.
	if _, err := mgr.Create(session.CreateOptions{ID: "root-legacy", Title: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.CloseAll(); err != nil {
		t.Fatal(err)
	}

	scoped := NewSessions(mgr, projA)
	list, err := scoped.List(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != a.ID || list[0].ProjectKey != projA {
		t.Fatalf("List(project A) = %+v, want only root-a", list)
	}

	// Get is unfiltered so /session <id> can open another workspace's root.
	other, ok, err := scoped.Get("root-b")
	if err != nil || !ok || other.ID != "root-b" || other.ProjectKey != projB {
		t.Fatalf("Get(cross-workspace) ok=%v err=%v got=%+v", ok, err, other)
	}

	allLister, ok := scoped.(host.AllProjectsSessions)
	if !ok {
		t.Fatal("adapter should implement AllProjectsSessions")
	}
	all, err := allLister.ListAllProjects(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("ListAllProjects = %+v, want 3 roots", all)
	}
}

func TestSessionsAdapterRenameAndDelete(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(dir)
	root, err := mgr.Create(session.CreateOptions{ID: "root-rd", Title: "before", ProjectKey: "/proj"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.Close(root.ID); err != nil {
		t.Fatal(err)
	}
	svc := NewSessions(mgr, "/proj")

	renamed, err := svc.Rename(root.ID, "after")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "after" {
		t.Fatalf("Rename = %+v", renamed)
	}
	// Survives new manager (restart).
	mgr2 := session.NewManager(dir)
	svc2 := NewSessions(mgr2, "/proj")
	got, ok, err := svc2.Get(root.ID)
	if err != nil || !ok || got.Title != "after" {
		t.Fatalf("Get after restart: ok=%v err=%v got=%+v", ok, err, got)
	}

	other, err := mgr2.Create(session.CreateOptions{ID: "root-other", Title: "x", ProjectKey: "/proj"})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr2.Close(other.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc2.Delete(other.ID, false); err != nil {
		t.Fatal(err)
	}
	list, err := svc2.List(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != root.ID {
		t.Fatalf("List after delete = %+v", list)
	}

	// Open session requires force.
	if _, err := mgr2.Open(root.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc2.Delete(root.ID, false); err == nil {
		t.Fatal("expected force required")
	}
	if err := svc2.Delete(root.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := svc2.Get(root.ID); err != nil || ok {
		t.Fatalf("Get after force delete: ok=%v err=%v", ok, err)
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
