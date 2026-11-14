package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live})

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
