package rpc

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

type memLifecycle struct {
	caps     protocol.LifecycleCapabilities
	sessions map[string]protocol.SessionSummary
	events   map[string][]protocol.Event
}

func (m *memLifecycle) Capabilities() protocol.LifecycleCapabilities { return m.caps }
func (m *memLifecycle) List(ctx context.Context, rootsOnly bool) ([]protocol.SessionSummary, error) {
	var out []protocol.SessionSummary
	for _, s := range m.sessions {
		if rootsOnly && s.ParentID != "" {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}
func (m *memLifecycle) Get(ctx context.Context, id string) (protocol.SessionSummary, error) {
	s, ok := m.sessions[id]
	if !ok {
		return protocol.SessionSummary{}, protocol.NewLifecycleError(protocol.ErrorCodeSessionNotFound, "not found", id)
	}
	return s, nil
}
func (m *memLifecycle) Fork(ctx context.Context, id string) (protocol.SessionSummary, error) {
	return m.ForkAt(ctx, id, -1)
}
func (m *memLifecycle) ForkAt(ctx context.Context, id string, keepEvents int) (protocol.SessionSummary, error) {
	src, err := m.Get(ctx, id)
	if err != nil {
		return protocol.SessionSummary{}, err
	}
	child := protocol.SessionSummary{ID: "fork-1", ForkedFrom: src.ID, Title: "fork of " + src.Title}
	m.sessions[child.ID] = child
	return child, nil
}
func (m *memLifecycle) Load(ctx context.Context, id string) (protocol.SessionLoadResult, error) {
	if id != m.caps.ActiveSessionID {
		return protocol.SessionLoadResult{}, protocol.NewLifecycleError(protocol.ErrorCodeSessionBusy, "not active", id)
	}
	return protocol.SessionLoadResult{ID: id, Active: true}, nil
}
func (m *memLifecycle) RewindPoints(ctx context.Context, id string) ([]protocol.RewindPoint, error) {
	evs := m.events[id]
	return protocol.RewindPoints(evs), nil
}
func (m *memLifecycle) Replay(ctx context.Context, id string) ([]byte, error) {
	if _, ok := m.sessions[id]; !ok {
		return nil, protocol.NewLifecycleError(protocol.ErrorCodeSessionNotFound, "not found", id)
	}
	return []byte(`{"type":"user.message","data":{"text":"hi"}}` + "\n"), nil
}

func TestLifecycleMethodsAndCapabilities(t *testing.T) {
	lc := &memLifecycle{
		caps: protocol.LifecycleCapabilities{
			List: true, Get: true, Fork: true, ForkAt: true, Load: true,
			RewindPoints: true, Replay: true, EngineRewind: true,
			ActiveSessionID: "live-1",
		},
		sessions: map[string]protocol.SessionSummary{
			"live-1": {ID: "live-1", Title: "live"},
			"other":  {ID: "other", Title: "other"},
		},
		events: map[string][]protocol.Event{
			"live-1": {
				protocol.UserMessage{Text: "hi"},
				protocol.TurnCompleted{StopReason: "end_turn"},
			},
		},
	}

	pr, pw := io.Pipe()
	out := &safeBuffer{}
	srv := New(pr, out, func(ctx context.Context, op protocol.Op) error { return nil }, Options{
		SessionID: "live-1",
		Lifecycle: lc,
	})
	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background(), nil) }()

	waitForMethod(t, out, "rpc.ready", time.Second)

	// initialize advertises lifecycle
	writeLine(t, pw, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	initMsg := waitForID(t, out, 1, time.Second)
	res, _ := initMsg["result"].(map[string]any)
	life, _ := res["lifecycle"].(map[string]any)
	if life["list"] != true || life["activeSessionId"] != "live-1" {
		t.Fatalf("lifecycle caps = %#v", life)
	}

	// session.list
	writeLine(t, pw, `{"jsonrpc":"2.0","id":2,"method":"session.list","params":{"rootsOnly":true}}`)
	listMsg := waitForID(t, out, 2, time.Second)
	listRes, _ := listMsg["result"].(map[string]any)
	sessions, _ := listRes["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("list = %#v", listRes)
	}

	// session.fork
	writeLine(t, pw, `{"jsonrpc":"2.0","id":3,"method":"session.fork","params":{"id":"live-1"}}`)
	forkMsg := waitForID(t, out, 3, time.Second)
	forkRes, _ := forkMsg["result"].(map[string]any)
	if forkRes["id"] != "fork-1" || forkRes["forkedFrom"] != "live-1" {
		t.Fatalf("fork = %#v", forkRes)
	}

	// session.load active ok
	writeLine(t, pw, `{"jsonrpc":"2.0","id":4,"method":"session.load","params":{"id":"live-1"}}`)
	loadMsg := waitForID(t, out, 4, time.Second)
	loadRes, _ := loadMsg["result"].(map[string]any)
	if loadRes["active"] != true {
		t.Fatalf("load = %#v", loadRes)
	}

	// session.load other → session_busy
	writeLine(t, pw, `{"jsonrpc":"2.0","id":5,"method":"session.load","params":{"id":"other"}}`)
	busyMsg := waitForID(t, out, 5, time.Second)
	errObj, _ := busyMsg["error"].(map[string]any)
	data, _ := errObj["data"].(map[string]any)
	if data["code"] != protocol.ErrorCodeSessionBusy {
		t.Fatalf("busy error = %#v", busyMsg)
	}

	// session.get missing
	writeLine(t, pw, `{"jsonrpc":"2.0","id":6,"method":"session.get","params":{"id":"nope"}}`)
	missMsg := waitForID(t, out, 6, time.Second)
	errObj, _ = missMsg["error"].(map[string]any)
	data, _ = errObj["data"].(map[string]any)
	if data["code"] != protocol.ErrorCodeSessionNotFound {
		t.Fatalf("missing error = %#v", missMsg)
	}

	// session.rewind_points
	writeLine(t, pw, `{"jsonrpc":"2.0","id":7,"method":"session.rewind_points","params":{"id":"live-1"}}`)
	ptsMsg := waitForID(t, out, 7, time.Second)
	ptsRes, _ := ptsMsg["result"].(map[string]any)
	points, _ := ptsRes["points"].([]any)
	if len(points) != 1 {
		t.Fatalf("points = %#v", ptsRes)
	}

	writeLine(t, pw, `{"jsonrpc":"2.0","id":99,"method":"shutdown"}`)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestLifecycleUnsupportedWithoutHandler(t *testing.T) {
	pr, pw := io.Pipe()
	out := &safeBuffer{}
	srv := New(pr, out, func(ctx context.Context, op protocol.Op) error { return nil }, Options{SessionID: "s"})
	done := make(chan error, 1)
	go func() { done <- srv.Run(context.Background(), nil) }()
	waitForMethod(t, out, "rpc.ready", time.Second)

	writeLine(t, pw, `{"jsonrpc":"2.0","id":1,"method":"session.list"}`)
	msg := waitForID(t, out, 1, time.Second)
	errObj, _ := msg["error"].(map[string]any)
	data, _ := errObj["data"].(map[string]any)
	if data["code"] != protocol.ErrorCodeUnsupported {
		t.Fatalf("error = %#v", msg)
	}

	// initialize still advertises lifecycle bits (engine rewind + empty host)
	writeLine(t, pw, `{"jsonrpc":"2.0","id":2,"method":"initialize"}`)
	initMsg := waitForID(t, out, 2, time.Second)
	res, _ := initMsg["result"].(map[string]any)
	life, _ := res["lifecycle"].(map[string]any)
	if life["engineRewind"] != true {
		t.Fatalf("lifecycle = %#v", life)
	}

	writeLine(t, pw, `{"jsonrpc":"2.0","id":3,"method":"shutdown"}`)
	<-done
}

func writeLine(t *testing.T, w io.Writer, line string) {
	t.Helper()
	if _, err := io.WriteString(w, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

func waitForID(t *testing.T, out *safeBuffer, id float64, d time.Duration) map[string]any {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, msg := range decodeLines(t, out.Bytes()) {
			if v, ok := msg["id"].(float64); ok && v == id {
				return msg
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for id %v\n%s", id, out.String())
	return nil
}
