package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/protocol"
)

func TestSeenStoreEpochAndForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status-seen.json")
	store := OpenSeenStore(path)
	if store.Unread("old", store.epoch-1) {
		t.Fatal("activity before epoch should not be unread")
	}
	if store.Unread("quiet", 0) {
		t.Fatal("no activity should not be unread")
	}
	later := store.epoch + 5
	if !store.Unread("fresh", later) {
		t.Fatal("activity after epoch should be unread")
	}
	if err := store.MarkSeen("fresh", later); err != nil {
		t.Fatal(err)
	}
	if store.Unread("fresh", later) {
		t.Fatal("acked activity should be read")
	}
	if !store.Unread("fresh", later+1) {
		t.Fatal("newer activity should be unread again")
	}
	if err := store.MarkUnread("quiet"); err != nil {
		t.Fatal(err)
	}
	if !store.Unread("quiet", 0) {
		t.Fatal("forced unread should stay set with no activity")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("seen file mode = %o, want 600", info.Mode().Perm())
	}
	reloaded := OpenSeenStore(path)
	if !reloaded.Unread("quiet", 0) {
		t.Fatal("forced unread did not survive reload")
	}
	if reloaded.Unread("fresh", later) {
		t.Fatal("ack did not survive reload")
	}

	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	fresh := OpenSeenStore(path)
	if fresh.epoch <= 0 {
		t.Fatal("corrupt store should start a new epoch")
	}
}

func TestSessionStatusBoardSeenAndEvents(t *testing.T) {
	dir := t.TempDir()
	ops := make(chan protocol.Op, 1)
	live := NewLive("root-1", dir, nil, ops)
	defer live.Close()
	hub := NewLiveHub(nil, nil)
	hub.Add("root-1", live)
	srv, err := New(Options{SessionDir: dir, LiveHub: hub})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	healthReq, err := http.NewRequest(http.MethodGet, ts.URL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	healthRes, err := http.DefaultClient.Do(healthReq)
	if err != nil {
		t.Fatal(err)
	}
	var health healthResponse
	if err := json.NewDecoder(healthRes.Body).Decode(&health); err != nil {
		healthRes.Body.Close()
		t.Fatal(err)
	}
	healthRes.Body.Close()
	if !health.OK || health.Now <= 0 {
		t.Fatalf("health = %+v", health)
	}

	board := getBoard(t, ts.URL)
	if board.HeartbeatIntervalMs != protocol.HeartbeatIntervalMs || board.HeartbeatStaleMs != protocol.HeartbeatStaleMs {
		t.Fatalf("heartbeat fields = %d/%d", board.HeartbeatIntervalMs, board.HeartbeatStaleMs)
	}
	if len(board.Sessions) != 1 || board.Sessions[0].Status != protocol.StatusIdle || board.Sessions[0].Unread {
		t.Fatalf("initial board = %+v", board.Sessions)
	}
	if board.Gateway != protocol.StatusIdle {
		t.Fatalf("gateway = %q", board.Gateway)
	}

	time.Sleep(5 * time.Millisecond)
	live.Publish(protocol.TurnStarted{})
	board = getBoard(t, ts.URL)
	if board.Sessions[0].Status != protocol.StatusRunning {
		t.Fatalf("status = %q, want running", board.Sessions[0].Status)
	}
	if !board.Sessions[0].Unread {
		t.Fatal("turn start should mark the root unread")
	}
	if board.Gateway != protocol.StatusRunning {
		t.Fatalf("gateway = %q, want running", board.Gateway)
	}

	rootsReq, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/roots", nil)
	if err != nil {
		t.Fatal(err)
	}
	rootsRes, err := http.DefaultClient.Do(rootsReq)
	if err != nil {
		t.Fatal(err)
	}
	var roots rootsResponse
	if err := json.NewDecoder(rootsRes.Body).Decode(&roots); err != nil {
		rootsRes.Body.Close()
		t.Fatal(err)
	}
	rootsRes.Body.Close()
	if len(roots.Roots) != 1 || roots.Roots[0].Status != protocol.StatusRunning || !roots.Roots[0].Unread {
		t.Fatalf("roots = %+v", roots.Roots)
	}

	seenReq, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions/root-1/seen", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	seenReq.Header.Set("Content-Type", "application/json")
	seenRes, err := http.DefaultClient.Do(seenReq)
	if err != nil {
		t.Fatal(err)
	}
	seenRes.Body.Close()
	if seenRes.StatusCode != http.StatusOK {
		t.Fatalf("seen status = %d", seenRes.StatusCode)
	}
	board = getBoard(t, ts.URL)
	if board.Sessions[0].Unread {
		t.Fatal("seen should clear unread")
	}

	time.Sleep(5 * time.Millisecond)
	live.Publish(protocol.PermissionAsked{RequestID: "p1", Permission: "bash"})
	st := live.Status()
	if st.TurnState != protocol.StatusWaiting {
		t.Fatalf("TurnState = %q, want waiting", st.TurnState)
	}
	board = getBoard(t, ts.URL)
	if board.Sessions[0].Status != protocol.StatusWaiting || !board.Sessions[0].Unread {
		t.Fatalf("after ask = %+v", board.Sessions[0])
	}

	bad, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/sessions/no..pe/seen", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	bad.Header.Set("Content-Type", "application/json")
	badRes, err := http.DefaultClient.Do(bad)
	if err != nil {
		t.Fatal(err)
	}
	badRes.Body.Close()
	if badRes.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad id status = %d", badRes.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	evReq, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/status/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	evRes, err := http.DefaultClient.Do(evReq)
	if err != nil {
		t.Fatal(err)
	}
	defer evRes.Body.Close()
	reader := bufio.NewReader(evRes.Body)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "data: ") || !strings.Contains(line, `"snapshot"`) {
		t.Fatalf("first event = %q", line)
	}
}

func TestLiveTurnStateFailed(t *testing.T) {
	live := NewLive("s", t.TempDir(), nil, make(chan protocol.Op, 1))
	defer live.Close()
	live.Publish(protocol.TurnStarted{})
	live.Publish(protocol.TurnCompleted{StopReason: "error"})
	st := live.Status()
	if st.Busy {
		t.Fatal("completed turn still busy")
	}
	if st.TurnState != protocol.StatusFailed {
		t.Fatalf("TurnState = %q, want failed", st.TurnState)
	}
	if st.LastEventAt <= 0 {
		t.Fatal("LastEventAt not set")
	}
}

func getBoard(t *testing.T, base string) statusBoard {
	t.Helper()
	res, err := http.Get(base + "/v1/status/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("board status = %d", res.StatusCode)
	}
	var board statusBoard
	if err := json.NewDecoder(res.Body).Decode(&board); err != nil {
		t.Fatal(err)
	}
	return board
}
