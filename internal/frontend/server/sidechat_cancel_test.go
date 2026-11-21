package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/protocol"
)

// blockingSideChat holds the answer until its context ends, like a slow
// provider stream.
type blockingSideChat struct {
	started chan struct{}
	err     chan error
}

func (m *blockingSideChat) Answer(ctx context.Context, _ SideChatPrompt) (SideChatCompletion, error) {
	close(m.started)
	<-ctx.Done()
	m.err <- ctx.Err()
	return SideChatCompletion{}, ctx.Err()
}

// TestSideChatAskStopsWhenClientAborts covers the cockpit's side-turn Stop
// (#1363): the browser aborts the fetch, the request context ends, the
// provider call is canceled, and no turn is saved.
func TestSideChatAskStopsWhenClientAborts(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	dir := t.TempDir()
	const id = "sess-stop"
	live := NewLive(id, dir, nil, ops)
	defer live.Close()
	appendSessionEvent(t, dir, id, protocol.UserMessage{Text: "long task"})
	model := &blockingSideChat{started: make(chan struct{}), err: make(chan error, 1)}
	srv, err := New(Options{SessionDir: dir, Live: live, SideChat: model})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/v1/sidechat/ask", strings.NewReader(`{"text":"why so slow?"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		srv.Handler().ServeHTTP(res, req)
		close(done)
	}()

	select {
	case <-model.started:
	case <-time.After(5 * time.Second):
		t.Fatal("side chat model was not called")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after the client aborted")
	}
	if got := <-model.err; !errors.Is(got, context.Canceled) {
		t.Fatalf("model ctx err = %v, want context.Canceled", got)
	}
	if res.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d %s", res.Code, res.Body.String())
	}
	if raw, err := os.ReadFile(sideChatPath(dir, id)); err == nil && strings.Contains(string(raw), "why so slow?") {
		t.Fatalf("a stopped side turn was saved: %s", raw)
	}
	select {
	case op := <-ops:
		t.Fatalf("stopped ask submitted %T", op)
	default:
	}
}
