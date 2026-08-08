package server

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/session"
)

func TestLiveStatusTracksUsageAndFitWarning(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("sess-ctx", "/tmp/work", nil, ops)
	defer live.Close()

	live.Publish(protocol.UsageReported{Used: protocol.KnownTokens(1500), Source: protocol.UsageSourceActual})
	st := live.Status()
	if st.ContextUsed != 1500 {
		t.Fatalf("ContextUsed = %d, want 1500", st.ContextUsed)
	}
	if st.ContextLimit != 0 {
		t.Fatalf("ContextLimit = %d, want 0 until fit warning", st.ContextLimit)
	}

	live.Publish(protocol.ContextFitWarning{
		EstimatedTokens: 180_000,
		ContextLimit:    200_000,
		Level:           protocol.ContextFitWarn,
		Message:         "hot",
	})
	st = live.Status()
	if st.ContextLimit != 200_000 {
		t.Fatalf("ContextLimit = %d, want 200000", st.ContextLimit)
	}
	// Known usage is not overwritten by the estimate.
	if st.ContextUsed != 1500 {
		t.Fatalf("ContextUsed = %d, want 1500 (keep measured)", st.ContextUsed)
	}
}

func TestLiveOpsPOSTAndStatus(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("sess-live", "/tmp/work", []AgentInfo{{Name: "build"}}, ops)
	defer live.Close()

	live.Publish(protocol.ModelSelected{Provider: "echo", Model: "echo"})
	live.Publish(protocol.AgentSelected{Name: "build"})
	live.Publish(protocol.PermissionModeSelected{Mode: protocol.PermissionModeDefault})
	live.Publish(protocol.AutonomySelected{Mode: protocol.AutonomySupervised})

	dir := t.TempDir()
	srv, err := New(Options{Auth: true, Token: "secret", SessionDir: dir, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Unauthorized
	res, err := http.Get(ts.URL + "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status unauth = %d", res.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var st StatusSnapshot
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.SessionID != "sess-live" || st.Provider != "echo" || st.Agent != "build" {
		t.Fatalf("status = %+v", st)
	}

	body := `{"type":"user.input","data":{"text":"hello"}}`
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/ops", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("ops status = %d", res.StatusCode)
	}
	select {
	case op := <-ops:
		ui, ok := op.(protocol.UserInput)
		if !ok || ui.Text != "hello" {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for op")
	}

	// Permission reply
	body = `{"type":"permission.reply","data":{"requestId":"r1","decision":"once"}}`
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/ops", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("perm ops = %d", res.StatusCode)
	}
	select {
	case op := <-ops:
		pr, ok := op.(protocol.PermissionReply)
		if !ok || pr.RequestID != "r1" || pr.Decision != protocol.DecisionOnce {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout permission op")
	}
}

func TestOpsRejectsUntrustedOriginBeforeSubmit(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("live", "/cwd", nil, ops)
	defer live.Close()
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live})

	req := httptest.NewRequest(http.MethodPost, "/v1/ops", strings.NewReader(`{"type":"user.input","data":{"text":"evil"}}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", "https://evil.example")
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("POST status = %d, want %d", res.Code, http.StatusForbidden)
	}
	select {
	case op := <-ops:
		t.Fatalf("unexpected submitted op: %#v", op)
	default:
	}
}

func TestLiveAgentsAndSessions(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("L1", "/cwd", []AgentInfo{{Name: "build"}, {Name: "plan"}}, ops)
	defer live.Close()
	dir := t.TempDir()
	writeFixtureSession(t, dir, "other", protocol.UserMessage{Text: "x"})
	srv := mustServer(t, Options{Auth: true, Token: "t", SessionDir: dir, Live: live})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var ar agentsResponse
	if err := json.NewDecoder(res.Body).Decode(&ar); err != nil {
		t.Fatal(err)
	}
	if len(ar.Agents) != 2 || ar.Agents[0].Name != "build" {
		t.Fatalf("agents = %+v", ar.Agents)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var sr sessionsResponse
	if err := json.NewDecoder(res.Body).Decode(&sr); err != nil {
		t.Fatal(err)
	}
	if sr.LiveID != "L1" {
		t.Fatalf("liveId = %q", sr.LiveID)
	}
	found := false
	for _, s := range sr.Sessions {
		if s.ID == "other" {
			found = true
		}
	}
	if !found {
		t.Fatalf("sessions = %+v", sr.Sessions)
	}
}

func TestLivePublishDisconnectsLaggardWithoutBlockingOthers(t *testing.T) {
	live := NewLive("live", "/cwd", nil, make(chan protocol.Op))
	defer live.Close()
	lagCtx, lagCancel := context.WithCancel(context.Background())
	defer lagCancel()
	laggard := live.Subscribe(lagCtx)
	for range 256 {
		live.Publish(protocol.TextDelta{Text: "fill"})
	}
	healthyCtx, healthyCancel := context.WithCancel(context.Background())
	defer healthyCancel()
	healthy := live.Subscribe(healthyCtx)

	done := make(chan struct{})
	go func() {
		live.Publish(protocol.TextDelta{Text: "next"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Publish blocked on lagging subscriber")
	}
	select {
	case ev := <-healthy:
		if ev.(protocol.TextDelta).Text != "next" {
			t.Fatalf("healthy event = %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("healthy subscriber did not receive event")
	}
	for range laggard {
	}
}

func TestLiveEventsSSE(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("live1", "/cwd", nil, ops)
	defer live.Close()
	dir := t.TempDir()
	// Backlog on disk
	store, err := session.Open(dir, "live1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Append(protocol.UserMessage{Text: "backlog"}); err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	srv := mustServer(t, Options{Auth: true, Token: "t", SessionDir: dir, Live: live, PollInterval: 20 * time.Millisecond})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/live/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}

	got := readSSEData(t, res.Body, 1, 2*time.Second)
	if got[0]["type"] != "user.message" {
		t.Fatalf("backlog = %v", got[0])
	}

	if err := store.Append(protocol.TextDelta{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	live.Publish(protocol.TextDelta{Text: "hi"})
	got2 := readSSEData(t, res.Body, 1, 2*time.Second)
	if got2[0]["type"] != "text.delta" {
		t.Fatalf("live = %v", got2[0])
	}
	cancel()
}

func TestLiveReplayBoundaryDeliversEachEventOnce(t *testing.T) {
	dir := t.TempDir()
	store, err := session.Open(dir, "boundary")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Append(protocol.UserMessage{Text: "before"}); err != nil {
		t.Fatal(err)
	}
	path := session.LogPath(dir, "boundary")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	boundary := st.Size()
	if err := store.Append(protocol.UserMessage{Text: "after"}); err != nil {
		t.Fatal(err)
	}

	srv := mustServer(t, Options{SessionDir: dir})
	res := httptest.NewRecorder()
	offset, err := srv.writeEventsRange(context.Background(), res, res, path, 0, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if offset != boundary {
		t.Fatalf("replay offset = %d, want boundary %d", offset, boundary)
	}
	if _, err := srv.writeEventsFrom(context.Background(), res, res, path, offset); err != nil {
		t.Fatal(err)
	}
	body := res.Body.String()
	if strings.Count(body, `"text":"before"`) != 1 || strings.Count(body, `"text":"after"`) != 1 {
		t.Fatalf("boundary replay duplicated or lost events: %s", body)
	}
}

type recordingWSWriter struct {
	messages []string
}

func (w *recordingWSWriter) WriteText(message string) error {
	w.messages = append(w.messages, message)
	return nil
}

func TestWriteWSRangeBoundaries(t *testing.T) {
	lines := []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}
	content := strings.Join(lines, "\n") + "\n"
	path := t.TempDir() + "/events.jsonl"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	firstBoundary := int64(len(lines[0]) + 1)
	secondBoundary := firstBoundary + int64(len(lines[1])+1)
	tests := []struct {
		name       string
		boundary   int64
		wantOffset int64
		want       []string
	}{
		{name: "complete line", boundary: firstBoundary, wantOffset: firstBoundary, want: lines[:1]},
		{name: "partial trailing line", boundary: secondBoundary - 1, wantOffset: firstBoundary, want: lines[:1]},
		{name: "fixed boundary excludes later lines", boundary: secondBoundary, wantOffset: secondBoundary, want: lines[:2]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := &recordingWSWriter{}
			offset, err := mustServer(t, Options{SessionDir: t.TempDir()}).writeWSRange(context.Background(), writer, path, 0, tt.boundary)
			if err != nil {
				t.Fatal(err)
			}
			if offset != tt.wantOffset {
				t.Fatalf("offset = %d, want %d", offset, tt.wantOffset)
			}
			if strings.Join(writer.messages, "|") != strings.Join(tt.want, "|") {
				t.Fatalf("messages = %q, want %q", writer.messages, tt.want)
			}
		})
	}
}

func TestWebSocketOpsAndEvents(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("ws1", "/cwd", []AgentInfo{{Name: "build"}}, ops)
	defer live.Close()

	dir := t.TempDir()
	store, err := session.Open(dir, "ws1")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := mustServer(t, Options{Auth: true, Token: "secret", SessionDir: dir, Live: live})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/v1/ws"
	conn, err := dialWS(t, wsURL, "Authorization: Bearer secret")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Drain hello/status (and any early frames) until we can send ops.
	deadline := time.Now().Add(2 * time.Second)
	sawStatus := false
	for time.Now().Before(deadline) && !sawStatus {
		msg, err := conn.readText(200 * time.Millisecond)
		if err != nil {
			continue
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(msg), &env); err != nil {
			continue
		}
		if env["type"] == "status" {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Fatal("did not receive status hello")
	}

	// Send user.input over WS (primary acceptance path).
	if err := conn.writeText(`{"type":"user.input","data":{"text":"ping"}}`); err != nil {
		t.Fatal(err)
	}
	select {
	case op := <-ops:
		if ui, ok := op.(protocol.UserInput); !ok || ui.Text != "ping" {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no op from ws")
	}

	// Permission reply via WS
	if err := conn.writeText(`{"type":"permission.reply","data":{"requestId":"p9","decision":"reject"}}`); err != nil {
		t.Fatal(err)
	}
	select {
	case op := <-ops:
		pr, ok := op.(protocol.PermissionReply)
		if !ok || pr.Decision != protocol.DecisionReject {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no perm op")
	}

	// Event fan-out: publish after client is connected; read until text.delta.
	if err := store.Append(protocol.TextDelta{Text: "stream"}); err != nil {
		t.Fatal(err)
	}
	live.Publish(protocol.TextDelta{Text: "stream"})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msg, err := conn.readText(200 * time.Millisecond)
		if err != nil {
			continue
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(msg), &env); err != nil {
			continue
		}
		if env["type"] == "text.delta" {
			return
		}
	}
	t.Fatal("timeout waiting for text.delta")
}

func TestOpsUnavailableWithoutLive(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/ops", strings.NewReader(`{"type":"interrupt"}`))
	req.Header.Set("Authorization", "Bearer secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestAttachPageLoadsViteAssets(t *testing.T) {
	srv := testServer(t, t.TempDir(), "secret")
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	srv.Handler().ServeHTTP(res, req)
	body := res.Body.String()
	for _, want := range []string{"id=\"root\"", "type=\"module\"", "/assets/"} {
		if !strings.Contains(body, want) {
			t.Fatalf("page missing %q", want)
		}
	}
}

func mustServer(t *testing.T, opts Options) *Server {
	t.Helper()
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// --- minimal WS client for tests ---

type testWS struct {
	c    net.Conn
	bufr *bufio.Reader
}

func dialWS(t *testing.T, url string, extraHeaders ...string) (*testWS, error) {
	t.Helper()
	// url like ws://127.0.0.1:port/v1/ws
	u := strings.TrimPrefix(url, "ws://")
	hostPath := u
	path := "/"
	if i := strings.Index(u, "/"); i >= 0 {
		hostPath = u[:i]
		path = u[i:]
	}
	c, err := net.Dial("tcp", hostPath)
	if err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString([]byte("dGhlIHNhbXBsZSBub25jZQ==")[:16])
	// Fixed test key
	key = "dGhlIHNhbXBsZSBub25jZQ=="
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + hostPath + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n"
	for _, h := range extraHeaders {
		h = strings.TrimSpace(h)
		if h != "" {
			req += h + "\r\n"
		}
	}
	req += "\r\n"
	if _, err := c.Write([]byte(req)); err != nil {
		_ = c.Close()
		return nil, err
	}
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	if !strings.Contains(status, "101") {
		_ = c.Close()
		return nil, io.EOF
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		if line == "\r\n" {
			break
		}
	}
	// Verify accept optional
	_ = wsAcceptKey(key)
	_ = sha1.New
	return &testWS{c: c, bufr: br}, nil
}

func (w *testWS) Close() error { return w.c.Close() }

func (w *testWS) writeText(s string) error {
	payload := []byte(s)
	mask := [4]byte{1, 2, 3, 4}
	n := len(payload)
	var hdr []byte
	hdr = append(hdr, 0x81) // fin+text
	if n < 126 {
		hdr = append(hdr, byte(0x80|n))
	} else {
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	}
	hdr = append(hdr, mask[:]...)
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := w.c.Write(hdr); err != nil {
		return err
	}
	_, err := w.c.Write(masked)
	return err
}

func (w *testWS) readText(timeout time.Duration) (string, error) {
	_ = w.c.SetReadDeadline(time.Now().Add(timeout))
	h := make([]byte, 2)
	if _, err := io.ReadFull(w.bufr, h); err != nil {
		return "", err
	}
	n := int(h[1] & 0x7f)
	if n == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(w.bufr, ext[:]); err != nil {
			return "", err
		}
		n = int(binary.BigEndian.Uint16(ext[:]))
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(w.bufr, payload); err != nil {
		return "", err
	}
	return string(payload), nil
}

func TestLiveHubAddAndActive(t *testing.T) {
	ops1 := make(chan protocol.Op, 1)
	ops2 := make(chan protocol.Op, 1)
	live1 := NewLive("root1", "/a", nil, ops1)
	live2 := NewLive("root2", "/b", nil, ops2)
	defer live1.Close()
	defer live2.Close()

	hub := NewLiveHub(nil, nil)
	hub.Add("root1", live1)
	if got := hub.ActiveID(); got != "root1" {
		t.Fatalf("active = %q, want root1", got)
	}
	if got := hub.Active(); got == nil || got.SessionID() != "root1" {
		t.Fatal("Active() missing or wrong")
	}

	hub.Add("root2", live2)
	if got := hub.ActiveID(); got != "root1" {
		t.Fatalf("active = %q after add 2, want root1", got)
	}
	if err := hub.Activate("root2"); err != nil {
		t.Fatal(err)
	}
	if got := hub.ActiveID(); got != "root2" {
		t.Fatalf("active = %q after activate, want root2", got)
	}
	if got := hub.LiveFor("root1"); got == nil || got.SessionID() != "root1" {
		t.Fatal("LiveFor root1 missing")
	}
}

func TestLiveHubList(t *testing.T) {
	live1 := NewLive("r1", "/a", []AgentInfo{{Name: "build"}}, make(chan protocol.Op))
	live2 := NewLive("r2", "/b", nil, make(chan protocol.Op))
	defer live1.Close()
	defer live2.Close()
	live1.Publish(protocol.AgentSelected{Name: "build"})
	live2.Publish(protocol.TurnStarted{})
	live2.Publish(protocol.TurnCompleted{})

	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live1)
	hub.Add("r2", live2)

	list := hub.List()
	if len(list) != 2 {
		t.Fatalf("list = %d, want 2", len(list))
	}
	if list[0].ID != "r1" {
		t.Fatalf("list[0] = %q, want r1 (first added)", list[0].ID)
	}
	if list[0].Agent != "build" {
		t.Fatalf("list[0].Agent = %q, want build", list[0].Agent)
	}
	if list[1].Busy != false {
		t.Fatalf("list[1] (r2) Busy = %v, want false (TurnCompleted)", list[1].Busy)
	}
}

func TestLiveHubClose(t *testing.T) {
	live := NewLive("r1", "/a", nil, make(chan protocol.Op))
	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live)
	hub.Close()
	if got := hub.Active(); got != nil {
		t.Fatal("Active() after close should be nil")
	}
	if got := hub.List(); len(got) != 0 {
		t.Fatalf("List after close = %d, want 0", len(got))
	}
}

func TestLiveHubActivateRejectsUnknown(t *testing.T) {
	hub := NewLiveHub(nil, nil)
	if err := hub.Activate("missing"); err == nil {
		t.Fatal("expected error for missing root")
	}
}

func TestLiveHubLiveForEmptyDelegatesToActive(t *testing.T) {
	live := NewLive("r1", "/a", nil, make(chan protocol.Op))
	defer live.Close()
	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live)
	if got := hub.LiveFor(""); got == nil || got.SessionID() != "r1" {
		t.Fatal("LiveFor(\"\") should return active")
	}
}

func TestLiveHubCreateAndRemove(t *testing.T) {
	live1 := NewLive("r1", "/a", nil, make(chan protocol.Op))
	live2 := NewLive("r2", "/b", nil, make(chan protocol.Op))
	defer live1.Close()
	defer live2.Close()
	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live1)
	hub.Add("r2", live2)
	hub.Remove("r1")
	if hub.ActiveID() != "r2" {
		t.Fatalf("active should fall back to r2, got %q", hub.ActiveID())
	}
	list := hub.List()
	if len(list) != 1 || list[0].ID != "r2" {
		t.Fatalf("list after remove = %v", list)
	}
}

func TestLiveHubResolveLiveBackwardCompat(t *testing.T) {
	dir := t.TempDir()
	ops := make(chan protocol.Op, 1)
	singleLive := NewLive("single", "/", nil, ops)
	defer singleLive.Close()

	srv, err := New(Options{Auth: true, Token: "t", SessionDir: dir, Live: singleLive})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/status?root=anything", nil)
	req.Header.Set("Authorization", "Bearer t")
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	var st StatusSnapshot
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.SessionID != "single" {
		t.Fatalf("SessionID = %q", st.SessionID)
	}
}
func TestLivePermissionQuestionPendingOnStatus(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("s1", "/tmp", nil, ops)
	if live.Status().PermissionPending || live.Status().QuestionPending {
		t.Fatal("expected no pending at start")
	}
	live.Publish(protocol.PermissionAsked{RequestID: "p1", Permission: "bash"})
	if !live.Status().PermissionPending {
		t.Fatal("permission pending after asked")
	}
	live.Publish(protocol.PermissionResolved{RequestID: "p1", Decision: protocol.DecisionOnce})
	if live.Status().PermissionPending {
		t.Fatal("permission cleared after resolved")
	}
	live.Publish(protocol.QuestionAsked{RequestID: "q1"})
	if !live.Status().QuestionPending {
		t.Fatal("question pending after asked")
	}
	live.Publish(protocol.QuestionResolved{RequestID: "q1"})
	if live.Status().QuestionPending {
		t.Fatal("question cleared after resolved")
	}
}

func TestLiveHubListExposesAttentionFlags(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("r1", "/tmp", nil, ops)
	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live)
	live.Publish(protocol.PermissionAsked{RequestID: "p1", Permission: "bash"})
	live.Publish(protocol.TurnStarted{})
	list := hub.List()
	if len(list) != 1 {
		t.Fatalf("list = %d", len(list))
	}
	if !list[0].PermissionPending || !list[0].Busy {
		t.Fatalf("summary = %+v, want permissionPending+busy", list[0])
	}
}
