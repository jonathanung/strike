package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestManagerCreateAppendReplayList(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	root, err := m.Create(CreateOptions{Title: "root work"})
	if err != nil {
		t.Fatal(err)
	}
	if root.ID == "" || !root.Open || root.Title != "root work" {
		t.Fatalf("root = %+v", root)
	}
	if _, err := os.Stat(root.Path); err != nil {
		t.Fatal(err)
	}
	meta, err := ReadMeta(dir, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "root work" || meta.CreatedAt == "" {
		t.Fatalf("meta = %+v", meta)
	}

	child, err := m.Create(CreateOptions{
		ParentSessionID: root.ID,
		Title:           "child task",
	})
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentSessionID != root.ID {
		t.Fatalf("child parent = %q, want %q", child.ParentSessionID, root.ID)
	}

	if err := m.Append(root.ID, protocol.UserMessage{
		Correlation: protocol.Correlation{SessionID: root.ID},
		Text:        "hello",
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Append(root.ID, protocol.SessionTitled{
		Correlation: protocol.Correlation{SessionID: root.ID},
		Title:       "renamed root",
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Append(child.ID, protocol.TextDelta{
		Correlation: protocol.Correlation{SessionID: child.ID, ParentSessionID: root.ID, Depth: 1},
		Text:        "child-out",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := m.Get(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "renamed root" {
		t.Fatalf("Get title = %q, want renamed root", got.Title)
	}
	meta, err = ReadMeta(dir, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "renamed root" {
		t.Fatalf("persisted title = %q", meta.Title)
	}

	events, err := m.Replay(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("Replay root len = %d, want 2", len(events))
	}

	open := m.ListOpen()
	if len(open) != 2 {
		t.Fatalf("ListOpen = %d, want 2", len(open))
	}
	for _, info := range open {
		if !info.Open {
			t.Fatalf("ListOpen entry not open: %+v", info)
		}
	}

	if err := m.Close(child.ID); err != nil {
		t.Fatal(err)
	}
	if len(m.ListOpen()) != 1 {
		t.Fatalf("ListOpen after close = %d, want 1", len(m.ListOpen()))
	}

	all, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("List = %d, want 2", len(all))
	}
	byID := map[string]Info{}
	for _, info := range all {
		byID[info.ID] = info
	}
	if byID[root.ID].Title != "renamed root" || !byID[root.ID].Open {
		t.Fatalf("list root = %+v", byID[root.ID])
	}
	if byID[child.ID].ParentSessionID != root.ID || byID[child.ID].Open {
		t.Fatalf("list child = %+v", byID[child.ID])
	}

	if err := m.CloseAll(); err != nil {
		t.Fatal(err)
	}
	if len(m.ListOpen()) != 0 {
		t.Fatalf("ListOpen after CloseAll = %d", len(m.ListOpen()))
	}
}

func TestManagerOpenExistingAndBind(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	info, err := m.Create(CreateOptions{ID: "fixed-id"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Append(info.ID, protocol.UserMessage{Text: "persist me"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(info.ID); err != nil {
		t.Fatal(err)
	}

	m2 := NewManager(dir)
	opened, err := m2.Open("fixed-id")
	if err != nil {
		t.Fatal(err)
	}
	if !opened.Open || opened.ID != "fixed-id" {
		t.Fatalf("opened = %+v", opened)
	}
	bound, err := m2.Bind("fixed-id")
	if err != nil {
		t.Fatal(err)
	}
	if bound.ID() != "fixed-id" || bound.Path() == "" {
		t.Fatalf("bound = %+v path=%q", bound, bound.Path())
	}
	if err := bound.Append(protocol.TextDelta{Text: "more"}); err != nil {
		t.Fatal(err)
	}
	if err := bound.Close(); err != nil {
		t.Fatal(err)
	}

	events, err := m2.Replay("fixed-id")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}

	if _, err := m2.Open("missing-session"); err == nil {
		t.Fatal("expected error for missing session")
	}
	if _, err := m2.Create(CreateOptions{ID: "fixed-id"}); err == nil {
		t.Fatal("expected error recreating existing id")
	}
}

func TestManagerRejectsBadIDs(t *testing.T) {
	m := NewManager(t.TempDir())
	// Empty ID mints via NewID; path-like ids must fail.
	if _, err := m.Create(CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../escape", "a/b", `a\b`} {
		if _, err := m.Create(CreateOptions{ID: id}); err == nil {
			t.Fatalf("Create(%q) succeeded", id)
		}
	}
}

func TestManagerConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	const n = 8
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		info, err := m.Create(CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = info.ID
	}

	var wg sync.WaitGroup
	errCh := make(chan error, n*32)
	for _, id := range ids {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 32; j++ {
				ev := protocol.TextDelta{
					Correlation: protocol.Correlation{SessionID: id},
					Text:        fmt.Sprintf("%d", j),
				}
				if err := m.Append(id, ev); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}

	for _, id := range ids {
		events, err := m.Replay(id)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 32 {
			t.Fatalf("session %s events = %d, want 32", id, len(events))
		}
	}
	if err := m.CloseAll(); err != nil {
		t.Fatal(err)
	}
}

func TestMuxTagsBySourceAndCorrelation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := make(chan protocol.Event, 2)
	b := make(chan protocol.Event, 2)
	a <- protocol.TextDelta{Text: "from-a"} // no correlation → source key
	a <- protocol.TextDelta{
		Correlation: protocol.Correlation{SessionID: "corr-a"},
		Text:        "corr",
	}
	b <- protocol.UserMessage{
		Correlation: protocol.Correlation{SessionID: "corr-b"},
		Text:        "hi",
	}
	close(a)
	close(b)

	out := Mux(ctx, map[string]<-chan protocol.Event{
		"src-a": a,
		"src-b": b,
	})

	got := map[string]int{}
	deadline := time.After(2 * time.Second)
	for len(got) < 3 {
		select {
		case te, ok := <-out:
			if !ok {
				if len(got) < 3 {
					t.Fatalf("mux closed early, got %#v", got)
				}
				return
			}
			got[te.SessionID]++
		case <-deadline:
			t.Fatalf("timeout, got %#v", got)
		}
	}
	// drain close
	for range out {
	}
	if got["src-a"] != 1 || got["corr-a"] != 1 || got["corr-b"] != 1 {
		t.Fatalf("tags = %#v", got)
	}
}

func TestMuxEmptyAndCancel(t *testing.T) {
	out := Mux(context.Background(), nil)
	if _, ok := <-out; ok {
		t.Fatal("empty mux should be closed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan protocol.Event)
	out = Mux(ctx, map[string]<-chan protocol.Event{"s": ch})
	cancel()
	select {
	case _, ok := <-out:
		if ok {
			// may or may not deliver; after cancel must eventually close
			for range out {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("mux did not close after cancel")
	}
}

func TestManagerListEmptyDir(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "missing"))
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("list = %#v", list)
	}
}

func TestManagerCreateStoresProjectKey(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	info, err := m.Create(CreateOptions{Title: "a work", ProjectKey: "/repos/a"})
	if err != nil {
		t.Fatal(err)
	}
	if info.ProjectKey != "/repos/a" {
		t.Fatalf("Create Info.ProjectKey = %q", info.ProjectKey)
	}
	meta, err := ReadMeta(dir, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ProjectKey != "/repos/a" {
		t.Fatalf("meta.ProjectKey = %q", meta.ProjectKey)
	}
	if err := m.Close(info.ID); err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProjectKey != "/repos/a" {
		t.Fatalf("Get ProjectKey = %q", got.ProjectKey)
	}
}

func TestManagerLatestRootFiltersByProject(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	writeClosed := func(id, project string, mtime time.Time) {
		t.Helper()
		st, err := Open(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		meta := Meta{
			ProjectKey: project,
			CreatedAt:  mtime.UTC().Format(time.RFC3339Nano),
		}
		if err := WriteMeta(dir, id, meta); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(LogPath(dir, id), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	oldT := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newT := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	writeClosed("root-a", "/proj/a", oldT)
	writeClosed("root-b", "/proj/b", newT) // newer overall
	writeClosed("legacy", "", newT)

	got, err := m.LatestRoot("/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "root-a" {
		t.Fatalf("LatestRoot(/proj/a) = %q, want root-a", got.ID)
	}
	gotB, err := m.LatestRoot("/proj/b")
	if err != nil {
		t.Fatal(err)
	}
	if gotB.ID != "root-b" {
		t.Fatalf("LatestRoot(/proj/b) = %q", gotB.ID)
	}
	// Unfiltered prefers newest root (B over A); legacy is also a root.
	any, err := m.LatestRoot("")
	if err != nil {
		t.Fatal(err)
	}
	if any.ID != "root-b" && any.ID != "legacy" {
		t.Fatalf("LatestRoot(\"\") = %q", any.ID)
	}
	if _, err := m.LatestRoot("/proj/missing"); err == nil {
		t.Fatal("expected error for unknown project")
	}
	if !BelongsToProject(Info{ProjectKey: "/proj/a"}, "/proj/a") {
		t.Error("BelongsToProject same key")
	}
	if BelongsToProject(Info{ProjectKey: ""}, "/proj/a") {
		t.Error("legacy empty key must not match filter")
	}
	if !BelongsToProject(Info{ProjectKey: "x"}, "") {
		t.Error("empty filter matches all")
	}
}

func TestManagerForkPreservesProjectKey(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	root, err := m.Create(CreateOptions{Title: "src", ProjectKey: "/repos/x"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Append(root.ID, protocol.UserMessage{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	fork, err := m.Fork(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fork.ProjectKey != "/repos/x" {
		t.Fatalf("fork ProjectKey = %q", fork.ProjectKey)
	}
	meta, err := ReadMeta(dir, fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ProjectKey != "/repos/x" {
		t.Fatalf("fork meta ProjectKey = %q", meta.ProjectKey)
	}
}

func TestManagerLatestRoot(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	if _, err := m.LatestRoot(""); err == nil {
		t.Fatal("expected error when empty")
	}

	// Fully closed sessions on disk — mtimes control UpdatedAt.
	writeClosed := func(id, parent string, mtime time.Time) {
		t.Helper()
		st, err := Open(dir, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		meta := Meta{ParentSessionID: parent, CreatedAt: mtime.UTC().Format(time.RFC3339Nano)}
		if err := WriteMeta(dir, id, meta); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(LogPath(dir, id), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	oldT := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newT := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	childT := time.Date(2020, 1, 3, 0, 0, 0, 0, time.UTC)
	writeClosed("20200101T000000.000000000Z-old", "", oldT)
	writeClosed("20200102T000000.000000000Z-new", "", newT)
	writeClosed("20200103T000000.000000000Z-child", "20200102T000000.000000000Z-new", childT)

	got, err := m.LatestRoot("")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "20200102T000000.000000000Z-new" {
		t.Fatalf("LatestRoot = %q, want newest root", got.ID)
	}
}

func TestAppendClosedSessionErrors(t *testing.T) {
	m := NewManager(t.TempDir())
	if err := m.Append("nope", protocol.TurnStarted{}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := m.Bind("nope"); err == nil {
		t.Fatal("expected bind error")
	}
}

func TestManagerForkCopiesPrefixAndMeta(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	root, err := m.Create(CreateOptions{Title: "original work"})
	if err != nil {
		t.Fatal(err)
	}
	corr := protocol.Correlation{SessionID: root.ID, TurnID: "t1"}
	for _, ev := range []protocol.Event{
		protocol.ModelSelected{Correlation: protocol.Correlation{SessionID: root.ID}, Provider: "echo", Model: "echo"},
		protocol.UserMessage{Correlation: corr, Text: "hello"},
		protocol.TurnStarted{Correlation: corr},
		protocol.TextDelta{Correlation: corr, Text: "hi"},
		protocol.TurnCompleted{Correlation: corr, StopReason: "end_turn"},
	} {
		if err := m.Append(root.ID, ev); err != nil {
			t.Fatal(err)
		}
	}

	fork, err := m.Fork(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fork.ID == root.ID {
		t.Fatal("fork id must differ from parent")
	}
	if fork.ParentSessionID != "" {
		t.Fatalf("fork ParentSessionID = %q, want empty root", fork.ParentSessionID)
	}
	if fork.Title != "fork of original work" {
		t.Fatalf("title = %q", fork.Title)
	}
	meta, err := ReadMeta(dir, fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ForkedFrom != root.ID {
		t.Fatalf("ForkedFrom = %q, want %q", meta.ForkedFrom, root.ID)
	}
	if meta.Title != "fork of original work" {
		t.Fatalf("meta title = %q", meta.Title)
	}

	parentEvents, err := m.Replay(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	forkEvents, err := m.Replay(fork.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(forkEvents) != len(parentEvents) {
		t.Fatalf("fork events = %d, parent = %d", len(forkEvents), len(parentEvents))
	}

	// Parent still listed as root; fork is also a root for --continue / picker.
	latest, err := m.LatestRoot("")
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != fork.ID && latest.ID != root.ID {
		t.Fatalf("LatestRoot = %q, want parent or fork", latest.ID)
	}
	list, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	var roots int
	for _, info := range list {
		if info.ParentSessionID == "" {
			roots++
		}
	}
	if roots < 2 {
		t.Fatalf("root sessions = %d, want >= 2", roots)
	}

	// Reject forking a subagent child.
	child, err := m.Create(CreateOptions{ParentSessionID: root.ID, Title: "sub"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Fork(child.ID); err == nil {
		t.Fatal("expected error forking subagent session")
	}
}

func TestAppendSessionMetaUpdatesPRInfo(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	root, err := m.Create(CreateOptions{ID: "sess-pr", Title: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Append(root.ID, protocol.SessionMeta{
		Correlation: protocol.Correlation{SessionID: root.ID},
		PRURL:       "https://github.com/acme/repo/pull/21",
		PRNumber:    21,
		PRState:     "OPEN",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PRURL != "https://github.com/acme/repo/pull/21" || got.PRNumber != 21 || got.PRState != PRStateOpen {
		t.Fatalf("Get after SessionMeta = %+v", got)
	}
}

func TestInfoFromDiskIncludesPRMeta(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	root, err := m.Create(CreateOptions{ID: "disk-pr", Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateMeta(dir, root.ID, func(meta *Meta) {
		meta.PRURL = "https://github.com/a/b/pull/4"
		meta.PRNumber = 4
		meta.PRState = PRStateClosed
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(root.ID); err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PRNumber != 4 || got.PRState != PRStateClosed || got.PRURL == "" {
		t.Fatalf("disk info = %+v", got)
	}
}

func TestManagerRenamePersists(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	root, err := m.Create(CreateOptions{ID: "root-ren", Title: "old title"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(root.ID); err != nil {
		t.Fatal(err)
	}
	// Rename while closed.
	got, err := m.Rename(root.ID, "new title")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "new title" {
		t.Fatalf("Rename = %+v", got)
	}
	meta, err := ReadMeta(dir, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "new title" {
		t.Fatalf("meta title = %q", meta.Title)
	}
	// Reopen manager path via Get after "restart".
	m2 := NewManager(dir)
	info, err := m2.Get(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Title != "new title" {
		t.Fatalf("after restart title = %q", info.Title)
	}
	// Rename while open updates live info.
	if _, err := m2.Open(root.ID); err != nil {
		t.Fatal(err)
	}
	got, err = m2.Rename(root.ID, "live")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "live" || !got.Open {
		t.Fatalf("open rename = %+v", got)
	}
	if open := m2.ListOpen(); len(open) != 1 || open[0].Title != "live" {
		t.Fatalf("ListOpen after rename = %+v", open)
	}
}

func TestManagerDeleteRemovesFilesAndRespectsForce(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)
	closed, err := m.Create(CreateOptions{ID: "closed-del", Title: "bye"})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(closed.ID); err != nil {
		t.Fatal(err)
	}
	openSess, err := m.Create(CreateOptions{ID: "open-del", Title: "open"})
	if err != nil {
		t.Fatal(err)
	}

	if err := m.Delete(closed.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LogPath(dir, closed.ID)); !os.IsNotExist(err) {
		t.Fatalf("closed log still present: %v", err)
	}
	if _, err := os.Stat(MetaPath(dir, closed.ID)); !os.IsNotExist(err) {
		t.Fatalf("closed meta still present: %v", err)
	}
	if _, err := m.Get(closed.ID); err == nil {
		t.Fatal("Get closed after delete should fail")
	}

	if err := m.Delete(openSess.ID, false); err == nil {
		t.Fatal("delete open without force should fail")
	}
	if _, err := os.Stat(LogPath(dir, openSess.ID)); err != nil {
		t.Fatalf("open log removed without force: %v", err)
	}
	if err := m.Delete(openSess.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LogPath(dir, openSess.ID)); !os.IsNotExist(err) {
		t.Fatalf("forced open log still present: %v", err)
	}
	if n := m.CountOpenRoots(); n != 0 {
		t.Fatalf("CountOpenRoots after force delete = %d", n)
	}
	if err := m.Delete("missing-id", false); err == nil {
		t.Fatal("delete missing should error")
	}
}

func TestCountOpenRootsSetWorktreeDestroy(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(dir)

	if n := m.CountOpenRoots(); n != 0 {
		t.Fatalf("empty CountOpenRoots = %d", n)
	}
	root, err := m.Create(CreateOptions{Title: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if n := m.CountOpenRoots(); n != 1 {
		t.Fatalf("CountOpenRoots = %d, want 1", n)
	}
	child, err := m.Create(CreateOptions{ParentSessionID: root.ID, Title: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if n := m.CountOpenRoots(); n != 1 {
		t.Fatalf("with child CountOpenRoots = %d, want 1", n)
	}
	root2, err := m.Create(CreateOptions{Title: "r2"})
	if err != nil {
		t.Fatal(err)
	}
	if n := m.CountOpenRoots(); n != 2 {
		t.Fatalf("two roots = %d", n)
	}
	_ = child
	_ = root2

	if err := m.SetWorktree(root.ID, "/tmp/wt-a", "strike/a"); err != nil {
		t.Fatal(err)
	}
	got, err := m.Get(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorktreePath != "/tmp/wt-a" || got.WorktreeBranch != "strike/a" {
		t.Fatalf("Get worktree = %+v", got)
	}
	meta, err := ReadMeta(dir, root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.WorktreePath != "/tmp/wt-a" || meta.WorktreeBranch != "strike/a" {
		t.Fatalf("meta worktree = %+v", meta)
	}

	if err := m.Destroy(root.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LogPath(dir, root.ID)); !os.IsNotExist(err) {
		t.Fatalf("log still present: %v", err)
	}
	if _, err := os.Stat(MetaPath(dir, root.ID)); !os.IsNotExist(err) {
		t.Fatalf("meta still present: %v", err)
	}
	if n := m.CountOpenRoots(); n != 1 {
		t.Fatalf("after destroy CountOpenRoots = %d, want 1", n)
	}
}
