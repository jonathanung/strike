package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/schedule/sessionloop"
)

func loopReq(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func decodeLoops(t *testing.T, rr *httptest.ResponseRecorder) sessionLoopsResponse {
	t.Helper()
	var out sessionLoopsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return out
}

func TestSessionLoopsCRUD(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("s1", "/cwd", nil, ops)
	defer live.Close()
	aud := &memAuditor{}
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live, Audit: aud})

	rr := loopReq(t, srv, http.MethodGet, "/v1/sessions/s1/loops", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list %d %s", rr.Code, rr.Body.String())
	}
	got := decodeLoops(t, rr)
	if len(got.Loops) != 0 || got.Max != 16 || got.MinIntervalMs != 1000 || got.MaxIntervalMs != (168*time.Hour).Milliseconds() {
		t.Fatalf("empty list = %+v", got)
	}
	if !strings.Contains(rr.Body.String(), `"loops":[]`) {
		t.Fatalf("empty loops must encode as [], got %s", rr.Body.String())
	}

	rr = loopReq(t, srv, http.MethodPost, "/v1/sessions/s1/loops", `{"interval":"15m","job":"check the pipeline"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("start %d %s", rr.Code, rr.Body.String())
	}
	var started sessionLoopStartResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.Loop.ID != "l1" || started.Loop.Interval != "15m" || started.Loop.Job != "check the pipeline" || started.Loop.NextAt.IsZero() {
		t.Fatalf("started = %+v", started.Loop)
	}
	if _, err := live.Loops().Start("2h", "second"); err != nil {
		t.Fatal(err)
	}
	if st := live.Status(); st.Loops != 2 {
		t.Fatalf("status loops = %d, want 2", st.Loops)
	}

	got = decodeLoops(t, loopReq(t, srv, http.MethodGet, "/v1/sessions/s1/loops", ""))
	if len(got.Loops) != 2 || got.Loops[0].ID != "l1" || got.Loops[1].ID != "l2" {
		t.Fatalf("list = %+v", got.Loops)
	}

	rr = loopReq(t, srv, http.MethodDelete, "/v1/sessions/s1/loops/l1", "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"stopped":1`) {
		t.Fatalf("stop %d %s", rr.Code, rr.Body.String())
	}
	rr = loopReq(t, srv, http.MethodDelete, "/v1/sessions/s1/loops/l1", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("stop unknown %d %s", rr.Code, rr.Body.String())
	}
	rr = loopReq(t, srv, http.MethodDelete, "/v1/sessions/s1/loops", "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"stopped":1`) {
		t.Fatalf("stop all %d %s", rr.Code, rr.Body.String())
	}
	if live.Loops().Len() != 0 || live.Status().Loops != 0 {
		t.Fatal("loops left after stop all")
	}

	var oks, errs int
	for _, p := range aud.payloads() {
		switch p.Outcome {
		case opOutcomeOK:
			oks++
		case opOutcomeError:
			errs++
		}
		if p.OpType != opLoopStart && p.OpType != opLoopStop {
			t.Fatalf("audit op = %q", p.OpType)
		}
	}
	if oks != 3 || errs != 1 {
		t.Fatalf("audit ok=%d err=%d (%+v)", oks, errs, aud.payloads())
	}
	select {
	case op := <-ops:
		t.Fatalf("no firing expected yet, got %#v", op)
	default:
	}
}

func TestSessionLoopStartRejects(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("s1", "/cwd", nil, ops)
	defer live.Close()
	// Loop CRUD shares the per-IP /v1/ops limiter (burst 20); this test sends
	// 23 mutating requests, so give it headroom rather than exempt the routes.
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live, OpsBurst: 100})

	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"interval":"500ms","job":"x"}`, http.StatusBadRequest},
		{`{"interval":"169h","job":"x"}`, http.StatusBadRequest},
		{`{"interval":"15m","job":"   "}`, http.StatusBadRequest},
		{`{"interval":"15m"}`, http.StatusBadRequest},
		{`{"interval":"15m","job":"x","extra":1}`, http.StatusBadRequest},
		{`not json`, http.StatusBadRequest},
	} {
		rr := loopReq(t, srv, http.MethodPost, "/v1/sessions/s1/loops", tc.body)
		if rr.Code != tc.code {
			t.Errorf("%s → %d %s, want %d", tc.body, rr.Code, rr.Body.String(), tc.code)
		}
	}
	for i := 0; i < 16; i++ {
		rr := loopReq(t, srv, http.MethodPost, "/v1/sessions/s1/loops", fmt.Sprintf(`{"interval":"1h","job":"job %d"}`, i))
		if rr.Code != http.StatusCreated {
			t.Fatalf("start %d → %d %s", i, rr.Code, rr.Body.String())
		}
	}
	rr := loopReq(t, srv, http.MethodPost, "/v1/sessions/s1/loops", `{"interval":"1h","job":"17th"}`)
	if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "16") {
		t.Fatalf("17th → %d %s", rr.Code, rr.Body.String())
	}
}

func TestSessionLoopsUnknownOrDeadSession(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("s1", "/cwd", nil, ops)
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live})

	if rr := loopReq(t, srv, http.MethodGet, "/v1/sessions/other/loops", ""); rr.Code != http.StatusNotFound || !strings.Contains(rr.Body.String(), "session_not_live") {
		t.Fatalf("unknown session → %d %s", rr.Code, rr.Body.String())
	}
	if rr := loopReq(t, srv, http.MethodGet, "/v1/sessions/a..b/loops", ""); rr.Code != http.StatusBadRequest {
		t.Fatalf("bad id → %d", rr.Code)
	}
	if _, err := live.Loops().Start("1m", "x"); err != nil {
		t.Fatal(err)
	}
	live.Close()
	if live.Loops().Len() != 0 {
		t.Fatal("session close must stop loops")
	}
	rr := loopReq(t, srv, http.MethodPost, "/v1/sessions/s1/loops", `{"interval":"1m","job":"x"}`)
	if rr.Code != http.StatusGone {
		t.Fatalf("start on closed session → %d %s", rr.Code, rr.Body.String())
	}

	// No live engine at all (attach-only).
	attach := mustServer(t, Options{SessionDir: t.TempDir()})
	if rr := loopReq(t, attach, http.MethodGet, "/v1/sessions/s1/loops", ""); rr.Code != http.StatusNotFound {
		t.Fatalf("attach-only → %d", rr.Code)
	}
}

func TestSessionLoopsReadOnly(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("s1", "/cwd", nil, ops)
	defer live.Close()
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live, ReadOnly: true})

	if rr := loopReq(t, srv, http.MethodGet, "/v1/sessions/s1/loops", ""); rr.Code != http.StatusOK {
		t.Fatalf("read-only list → %d", rr.Code)
	}
	if rr := loopReq(t, srv, http.MethodPost, "/v1/sessions/s1/loops", `{"interval":"1m","job":"x"}`); rr.Code != http.StatusForbidden {
		t.Fatalf("read-only start → %d", rr.Code)
	}
	if rr := loopReq(t, srv, http.MethodDelete, "/v1/sessions/s1/loops", ""); rr.Code != http.StatusForbidden {
		t.Fatalf("read-only stop → %d", rr.Code)
	}
	if live.Loops().Len() != 0 {
		t.Fatal("read-only start created a loop")
	}
}

func TestSessionLoopsCapability(t *testing.T) {
	boot := func(opts Options) capabilities {
		t.Helper()
		srv := mustServer(t, opts)
		rr := loopReq(t, srv, http.MethodGet, "/v1/bootstrap", "")
		var b bootstrapResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		return b.Capabilities
	}
	live := NewLive("s1", "/cwd", nil, make(chan protocol.Op, 1))
	defer live.Close()
	if !boot(Options{SessionDir: t.TempDir(), Live: live}).Loops {
		t.Fatal("live gateway must advertise loops")
	}
	if boot(Options{SessionDir: t.TempDir(), Live: live, ReadOnly: true}).Loops {
		t.Fatal("read-only must not advertise loops")
	}
	if boot(Options{SessionDir: t.TempDir()}).Loops {
		t.Fatal("attach-only must not advertise loops")
	}
}

// A firing is an ordinary user.input on the session's op channel, so the
// engine queues it behind a busy turn exactly like a queued send.
func TestSessionLoopFiresUserInput(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("s1", "/cwd", nil, ops)
	defer live.Close()
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live})
	rr := loopReq(t, srv, http.MethodPost, "/v1/sessions/s1/loops", `{"interval":"1s","job":"tick job"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("start %d %s", rr.Code, rr.Body.String())
	}
	select {
	case op := <-ops:
		in, ok := op.(protocol.UserInput)
		if !ok || in.Text != "tick job" {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loop never fired")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		l := live.Loops().List()
		if len(l) == 1 && l[0].Runs >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("runs not recorded: %+v", l)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLiveHubLiveForSession(t *testing.T) {
	hub := NewLiveHub(nil, nil)
	a := NewLive("sa", "/cwd", nil, make(chan protocol.Op, 1))
	b := NewLive("sb", "/cwd", nil, make(chan protocol.Op, 1))
	hub.Add("sa", a)
	hub.Add("root-b", b)
	defer hub.Close()

	if hub.LiveForSession("sa") != a {
		t.Fatal("keyed lookup")
	}
	if hub.LiveForSession("sb") != b {
		t.Fatal("lookup by Live session id")
	}
	if hub.LiveForSession("") != nil || hub.LiveForSession("  ") != nil {
		t.Fatal("empty id must not fall back to the active root")
	}
	if hub.LiveForSession("nope") != nil {
		t.Fatal("unknown id")
	}
	var nilHub *LiveHub
	if nilHub.LiveForSession("sa") != nil {
		t.Fatal("nil hub")
	}

	srv := mustServer(t, Options{SessionDir: t.TempDir(), LiveHub: hub})
	if rr := loopReq(t, srv, http.MethodPost, "/v1/sessions/sb/loops", `{"interval":"1h","job":"x"}`); rr.Code != http.StatusCreated {
		t.Fatalf("hub start → %d %s", rr.Code, rr.Body.String())
	}
	if b.Loops().Len() != 1 || a.Loops().Len() != 0 {
		t.Fatal("loop landed on the wrong session")
	}
	hub.Remove("root-b")
	if b.Loops().Len() != 0 {
		t.Fatal("removing the root must stop its loops")
	}
}

func TestLoopErrorStatus(t *testing.T) {
	cases := map[error]int{
		sessionloop.ErrInvalid:  http.StatusBadRequest,
		sessionloop.ErrNotFound: http.StatusNotFound,
		sessionloop.ErrLimit:    http.StatusConflict,
		sessionloop.ErrClosed:   http.StatusGone,
		errors.New("other"):     http.StatusInternalServerError,
	}
	for err, want := range cases {
		if got := loopErrorStatus(fmt.Errorf("wrap: %w", err)); got != want {
			t.Errorf("%v → %d want %d", err, got, want)
		}
	}
}

func TestZeroLiveLoopsNil(t *testing.T) {
	var l *Live
	if l.Loops() != nil {
		t.Fatal("nil Live")
	}
	z := &Live{}
	if z.Loops() != nil || z.Status().Loops != 0 {
		t.Fatal("zero Live")
	}
}

// TestLiveEventsCarryLoopsChanged (#1944): a start and a stop through the
// loops routes each push loops.changed on GET /v1/live/events with the
// whole list, and nothing is written to the session log.
func TestLiveEventsCarryLoopsChanged(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("s1", "/cwd", nil, ops)
	defer live.Close()
	dir := t.TempDir()
	srv := mustServer(t, Options{SessionDir: dir, Live: live, PollInterval: 20 * time.Millisecond})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/v1/live/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}

	steps := []struct {
		method, path, body string
		code               int
		reason, loopID     string
		ids                []any
	}{
		{http.MethodPost, "/v1/sessions/s1/loops", `{"interval":"1h","job":"check ci"}`, http.StatusCreated, "start", "l1", []any{"l1"}},
		{http.MethodPost, "/v1/sessions/s1/loops", `{"interval":"2h","job":"triage"}`, http.StatusCreated, "start", "l2", []any{"l1", "l2"}},
		{http.MethodDelete, "/v1/sessions/s1/loops/l1", "", http.StatusOK, "stop", "l1", []any{"l2"}},
		{http.MethodDelete, "/v1/sessions/s1/loops", "", http.StatusOK, "stop", "", []any{}},
	}
	for _, st := range steps {
		if rr := loopReq(t, srv, st.method, st.path, st.body); rr.Code != st.code {
			t.Fatalf("%s %s = %d %s", st.method, st.path, rr.Code, rr.Body.String())
		}
		ev := readSSEData(t, res.Body, 1, 2*time.Second)[0]
		if ev["type"] != "loops.changed" {
			t.Fatalf("%s %s: event %v", st.method, st.path, ev)
		}
		data, _ := ev["data"].(map[string]any)
		if data["sessionId"] != "s1" || data["reason"] != st.reason || (data["loopId"] != nil && data["loopId"] != st.loopID) || (data["loopId"] == nil && st.loopID != "") {
			t.Fatalf("%s %s: data %v", st.method, st.path, data)
		}
		loops, ok := data["loops"].([]any)
		if !ok {
			t.Fatalf("loops not a list: %v", data["loops"])
		}
		ids := make([]any, 0, len(loops))
		for _, l := range loops {
			ids = append(ids, l.(map[string]any)["id"])
		}
		if fmt.Sprint(ids) != fmt.Sprint(st.ids) {
			t.Fatalf("%s %s: ids %v, want %v", st.method, st.path, ids, st.ids)
		}
	}
	if _, err := os.Stat(session.LogPath(dir, "s1")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loops.changed must not be persisted; log stat err = %v", err)
	}
}

// TestLiveEphemeralFanout: PublishEphemeral reaches SubscribeEphemeral
// readers only, never the persisted-event subscribers, and Close ends it.
func TestLiveEphemeralFanout(t *testing.T) {
	live := NewLive("s1", "/cwd", nil, make(chan protocol.Op, 1))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eph := live.SubscribeEphemeral(ctx)
	sub := live.Subscribe(ctx)
	if _, err := live.Loops().Start("1h", "x"); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-eph:
		lc, ok := ev.(protocol.LoopsChanged)
		if !ok || lc.Reason != protocol.LoopsChangedStart || lc.LoopID != "l1" || len(lc.Loops) != 1 || lc.Loops[0].Job != "x" {
			t.Fatalf("ephemeral = %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no loops.changed on the ephemeral stream")
	}
	select {
	case ev := <-sub:
		t.Fatalf("persisted subscribers must not see stream-only events: %#v", ev)
	default:
	}
	live.Close()
	select {
	case ev, ok := <-eph:
		// Close stops the loop (reason close) before the bridge closes.
		if ok {
			if lc, isLC := ev.(protocol.LoopsChanged); !isLC || lc.Reason != protocol.LoopsChangedClose || len(lc.Loops) != 0 {
				t.Fatalf("close event = %#v", ev)
			}
			if _, open := <-eph; open {
				t.Fatal("ephemeral channel still open after Close")
			}
		}
	case <-time.After(time.Second):
		t.Fatal("ephemeral channel not closed by Close")
	}
	if ch := live.SubscribeEphemeral(context.Background()); ch != nil {
		if _, open := <-ch; open {
			t.Fatal("subscribe after Close must return a closed channel")
		}
	}
}

// TestWriteEphemeralEventHasNoID: a stream-only event must not move the
// client's Last-Event-ID, which is a byte offset into the session log.
func TestWriteEphemeralEventHasNoID(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := writeEphemeralEvent(rec, rec, protocol.LoopsChanged{Reason: protocol.LoopsChangedStop, Loops: []protocol.LoopState{}}); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "data: ") || !strings.HasSuffix(body, "\n\n") || strings.Contains(body, "id:") {
		t.Fatalf("frame = %q", body)
	}
	if !strings.Contains(body, `"type":"loops.changed"`) || !strings.Contains(body, `"loops":[]`) {
		t.Fatalf("frame = %q", body)
	}
}
