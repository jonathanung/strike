package sdk_test

import (
	"path/filepath"
	"testing"

	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/sdk"
)

// TestLifecycleConformanceOffline exercises the public session lifecycle
// scenarios that every frontend must support equivalently (list → get →
// rewind_points → fork_at → fork). HTTP/RPC/ACP wire the same types; this
// suite locks the offline SDK store as the reference implementation.
func TestLifecycleConformanceOffline(t *testing.T) {
	dir := t.TempDir()
	store := sdk.NewSessionStore(dir)

	id := "conf-root"
	evs := []protocol.Event{
		protocol.UserMessage{Text: "turn one"},
		protocol.TurnCompleted{StopReason: "end_turn"},
		protocol.UserMessage{Text: "turn two"},
		protocol.TurnCompleted{StopReason: "end_turn"},
	}
	if err := sdk.WriteSession(filepath.Join(dir, id+".jsonl"), evs); err != nil {
		t.Fatal(err)
	}

	// 1. capabilities discovery
	caps := store.Capabilities()
	requireCap := func(name string, v bool) {
		t.Helper()
		if !v {
			t.Fatalf("capability %s missing", name)
		}
	}
	requireCap("list", caps.List)
	requireCap("get", caps.Get)
	requireCap("fork", caps.Fork)
	requireCap("forkAt", caps.ForkAt)
	requireCap("rewindPoints", caps.RewindPoints)
	requireCap("replay", caps.Replay)
	if caps.Load {
		t.Fatal("offline store must not claim load")
	}

	// 2. list
	list, err := store.List(true)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatalf("list = %+v err=%v", list, err)
	}

	// 3. get / inspect
	got, err := store.Get(id)
	if err != nil || got.EventCount != 4 {
		t.Fatalf("get = %+v err=%v", got, err)
	}

	// 4. rewind points
	points, err := store.RewindPoints(id)
	if err != nil || len(points) != 2 {
		t.Fatalf("points = %+v err=%v", points, err)
	}
	if points[0].KeepEvents != 2 || points[1].KeepEvents != 4 {
		t.Fatalf("keepEvents = %v", points)
	}

	// 5. fork_at first turn
	forked, err := store.ForkAt(id, points[0].KeepEvents)
	if err != nil {
		t.Fatal(err)
	}
	if forked.ForkedFrom != id || forked.EventCount != 2 {
		t.Fatalf("fork_at = %+v", forked)
	}
	childEvs, err := sdk.ReadSession(filepath.Join(dir, forked.ID+".jsonl"))
	if err != nil || len(childEvs) != 2 {
		t.Fatalf("child events = %d err=%v", len(childEvs), err)
	}

	// 6. full fork
	full, err := store.Fork(id)
	if err != nil || full.EventCount != 4 {
		t.Fatalf("fork = %+v err=%v", full, err)
	}

	// 7. replay
	raw, err := store.ReplayJSONL(id)
	if err != nil || len(raw) == 0 {
		t.Fatalf("replay err=%v len=%d", err, len(raw))
	}

	// 8. structured errors
	_, err = store.Get("no-such")
	assertCode(t, err, protocol.ErrorCodeSessionNotFound)
	_, err = store.ForkAt(id, 99)
	assertCode(t, err, protocol.ErrorCodeInvalidSession)
	_, err = store.Load(id)
	assertCode(t, err, protocol.ErrorCodeUnsupported)
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error code %s", code)
	}
	le, ok := err.(*protocol.LifecycleError)
	if !ok {
		t.Fatalf("err type %T: %v", err, err)
	}
	if le.Code != code {
		t.Fatalf("code = %q, want %q (%v)", le.Code, code, err)
	}
}
