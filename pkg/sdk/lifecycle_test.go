package sdk_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/sdk"
)

func TestSessionStoreListGetForkRewindPoints(t *testing.T) {
	dir := t.TempDir()
	store := sdk.NewSessionStore(dir)

	// Seed a root session with one completed turn.
	srcID := "src-session-1"
	evs := []protocol.Event{
		protocol.UserMessage{Text: "hello world from test"},
		protocol.TurnCompleted{StopReason: "end_turn"},
	}
	if err := sdk.WriteSession(filepath.Join(dir, srcID+".jsonl"), evs); err != nil {
		t.Fatal(err)
	}

	caps := store.Capabilities()
	if !caps.List || !caps.Fork || caps.Load || caps.EngineRewind {
		t.Fatalf("offline caps = %+v", caps)
	}

	list, err := store.List(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != srcID {
		t.Fatalf("list = %+v", list)
	}

	got, err := store.Get(srcID)
	if err != nil {
		t.Fatal(err)
	}
	if got.EventCount != 2 {
		t.Fatalf("EventCount = %d", got.EventCount)
	}

	points, err := store.RewindPoints(srcID)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 || points[0].KeepEvents != 2 {
		t.Fatalf("points = %+v", points)
	}

	forked, err := store.Fork(srcID)
	if err != nil {
		t.Fatal(err)
	}
	if forked.ID == srcID || forked.ForkedFrom != srcID {
		t.Fatalf("forked = %+v", forked)
	}
	if forked.EventCount != 2 {
		t.Fatalf("fork EventCount = %d", forked.EventCount)
	}

	// ForkAt keep 0 → empty child.
	empty, err := store.ForkAt(srcID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if empty.EventCount != 0 {
		t.Fatalf("empty fork EventCount = %d", empty.EventCount)
	}

	// Missing
	_, err = store.Get("missing")
	if err == nil {
		t.Fatal("expected not found")
	}
	var le *protocol.LifecycleError
	if !asLifecycle(err, &le) || le.Code != protocol.ErrorCodeSessionNotFound {
		t.Fatalf("err = %v", err)
	}

	// Load unsupported offline
	if _, err := store.Load(srcID); err == nil {
		t.Fatal("expected load unsupported")
	}
}

func TestSessionStoreForkRejectsKeepOverflow(t *testing.T) {
	dir := t.TempDir()
	store := sdk.NewSessionStore(dir)
	id := "s"
	if err := sdk.WriteSession(filepath.Join(dir, id+".jsonl"), []protocol.Event{
		protocol.UserMessage{Text: "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForkAt(id, 99); err == nil {
		t.Fatal("expected keep overflow error")
	}
}

func TestLifecycleClientMethods(t *testing.T) {
	calls := make(map[string]int)
	c := sdk.LifecycleClient{
		Call: func(ctx context.Context, method string, params, result any) error {
			calls[method]++
			switch method {
			case protocol.LifecycleMethodCapabilities:
				*(result.(*protocol.LifecycleCapabilities)) = protocol.LifecycleCapabilities{List: true}
			case protocol.LifecycleMethodList:
				*(result.(*protocol.SessionListResult)) = protocol.SessionListResult{
					Sessions: []protocol.SessionSummary{{ID: "a"}},
				}
			case protocol.LifecycleMethodGet:
				*(result.(*protocol.SessionSummary)) = protocol.SessionSummary{ID: "a"}
			case protocol.LifecycleMethodFork, protocol.LifecycleMethodForkAt:
				*(result.(*protocol.SessionSummary)) = protocol.SessionSummary{ID: "b", ForkedFrom: "a"}
			case protocol.LifecycleMethodLoad:
				*(result.(*protocol.SessionLoadResult)) = protocol.SessionLoadResult{ID: "a", Active: true}
			case protocol.LifecycleMethodRewindPoints:
				*(result.(*protocol.SessionRewindPointsResult)) = protocol.SessionRewindPointsResult{
					Points: []protocol.RewindPoint{{KeepEvents: 2, Turn: 1}},
				}
			}
			return nil
		},
	}
	ctx := context.Background()
	if _, err := c.Capabilities(ctx); err != nil {
		t.Fatal(err)
	}
	if list, err := c.List(ctx, true); err != nil || len(list) != 1 {
		t.Fatalf("list = %v %v", list, err)
	}
	if _, err := c.Get(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fork(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ForkAt(ctx, "a", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Load(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if pts, err := c.RewindPoints(ctx, "a"); err != nil || len(pts) != 1 {
		t.Fatalf("points = %v %v", pts, err)
	}
	for _, m := range []string{
		protocol.LifecycleMethodCapabilities,
		protocol.LifecycleMethodList,
		protocol.LifecycleMethodGet,
		protocol.LifecycleMethodFork,
		protocol.LifecycleMethodForkAt,
		protocol.LifecycleMethodLoad,
		protocol.LifecycleMethodRewindPoints,
	} {
		if calls[m] != 1 {
			t.Fatalf("calls[%s] = %d", m, calls[m])
		}
	}
}

func asLifecycle(err error, target **protocol.LifecycleError) bool {
	if err == nil {
		return false
	}
	le, ok := err.(*protocol.LifecycleError)
	if !ok {
		return false
	}
	*target = le
	return true
}
