package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/protocol"
)

func postOpWithClientID(t *testing.T, srv *Server, text, clientID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/ops", strings.NewReader(`{"type":"user.input","data":{"text":"`+text+`"}}`))
	req.Header.Set("Content-Type", "application/json")
	if clientID != "" {
		req.Header.Set(clientMessageHeader, clientID)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	return res
}

func drainOps(ops chan protocol.Op) []string {
	var got []string
	for {
		select {
		case op := <-ops:
			if ui, ok := op.(protocol.UserInput); ok {
				got = append(got, ui.Text)
			} else {
				got = append(got, fmt.Sprintf("%T", op))
			}
		default:
			return got
		}
	}
}

func TestOpsClientMessageIDDeliversExactlyOnce(t *testing.T) {
	ops := make(chan protocol.Op, 8)
	live := NewLive("sess-outbox", "/cwd", nil, ops)
	defer live.Close()
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live})

	steps := []struct {
		name      string
		text      string
		id        string
		wantCode  int
		wantDup   bool
		wantTexts []string
	}{
		{"first send", "one", "cm-1", http.StatusOK, false, []string{"one"}},
		{"retry after a lost answer", "one", "cm-1", http.StatusOK, true, nil},
		{"second message keeps order", "two", "cm-2", http.StatusOK, false, []string{"two"}},
		{"retry of the second", "two", "cm-2", http.StatusOK, true, nil},
		{"no id keeps the old behaviour", "three", "", http.StatusOK, false, []string{"three"}},
		{"no id twice posts twice", "three", "", http.StatusOK, false, []string{"three"}},
		{"malformed id is refused", "evil", "cm/../x", http.StatusBadRequest, false, nil},
		{"oversized id is refused", "evil", strings.Repeat("a", clientMessageMaxLen+1), http.StatusBadRequest, false, nil},
	}
	for _, step := range steps {
		res := postOpWithClientID(t, srv, step.text, step.id)
		if res.Code != step.wantCode {
			t.Fatalf("%s: code = %d %s", step.name, res.Code, res.Body.String())
		}
		if dup := strings.Contains(res.Body.String(), `"duplicate":true`); dup != step.wantDup {
			t.Fatalf("%s: duplicate = %v body %s", step.name, dup, res.Body.String())
		}
		// Submit is a channel send; give it a moment before draining.
		deadline := time.Now().Add(time.Second)
		var got []string
		for {
			got = append(got, drainOps(ops)...)
			if len(got) >= len(step.wantTexts) || time.Now().After(deadline) {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if strings.Join(got, "|") != strings.Join(step.wantTexts, "|") {
			t.Fatalf("%s: submitted %v, want %v", step.name, got, step.wantTexts)
		}
	}
}

func TestClientMsgDedupeLifecycle(t *testing.T) {
	d := newClientMsgDedupe()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }

	if got := d.reserve("s1", "a"); got != clientMsgNew {
		t.Fatalf("first reserve = %v", got)
	}
	if got := d.reserve("s1", "a"); got != clientMsgInFlight {
		t.Fatalf("concurrent reserve = %v", got)
	}
	// A failed submit forgets the id so the outbox can retry it.
	d.finish("s1", "a", false)
	if got := d.reserve("s1", "a"); got != clientMsgNew {
		t.Fatalf("reserve after failure = %v", got)
	}
	d.finish("s1", "a", true)
	if got := d.reserve("s1", "a"); got != clientMsgDelivered {
		t.Fatalf("reserve after delivery = %v", got)
	}
	// Ids are per live root.
	if got := d.reserve("s2", "a"); got != clientMsgNew {
		t.Fatalf("other session = %v", got)
	}
	// Past the TTL the id is forgotten.
	now = now.Add(clientMessageTTL + time.Second)
	if got := d.reserve("s1", "a"); got != clientMsgNew {
		t.Fatalf("after ttl = %v", got)
	}
}

func TestClientMsgDedupeCapEvictsOldestDelivered(t *testing.T) {
	d := newClientMsgDedupe()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	d.reserve("s", "inflight")
	for i := 0; i < clientMessageMax; i++ {
		now = now.Add(time.Millisecond)
		id := fmt.Sprintf("id%d", i)
		d.reserve("s", id)
		d.finish("s", id, true)
	}
	if n := len(d.entries); n > clientMessageMax {
		t.Fatalf("entries = %d, cap %d", n, clientMessageMax)
	}
	if got := d.reserve("s", "inflight"); got != clientMsgInFlight {
		t.Fatalf("in-flight entry evicted: %v", got)
	}
	if got := d.reserve("s", "id0"); got != clientMsgNew {
		t.Fatalf("oldest delivered should have been evicted: %v", got)
	}
	if got := d.reserve("s", fmt.Sprintf("id%d", clientMessageMax-1)); got != clientMsgDelivered {
		t.Fatalf("newest delivered evicted: %v", got)
	}
}

func TestClientMessageIDHeaderValidation(t *testing.T) {
	cases := []struct {
		raw    string
		wantID string
		wantOK bool
	}{
		{"", "", true},
		{"  ", "", true},
		{"cm-18c2f-3a", "cm-18c2f-3a", true},
		{"A_b.c:d-1", "A_b.c:d-1", true},
		{"has space", "", false},
		{"slash/inside", "", false},
		{"quote\"", "", false},
		{strings.Repeat("x", clientMessageMaxLen), strings.Repeat("x", clientMessageMaxLen), true},
		{strings.Repeat("x", clientMessageMaxLen+1), "", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/v1/ops", nil)
		if tc.raw != "" {
			req.Header.Set(clientMessageHeader, tc.raw)
		}
		id, ok := clientMessageID(req)
		if id != tc.wantID || ok != tc.wantOK {
			t.Errorf("%q: got (%q, %v), want (%q, %v)", tc.raw, id, ok, tc.wantID, tc.wantOK)
		}
	}
}

func TestCORSPreflightAllowsClientMessageHeader(t *testing.T) {
	srv := mustServer(t, Options{SessionDir: t.TempDir()})
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/v1/ops", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", clientMessageHeader)
	srv.Handler().ServeHTTP(res, req)
	if got := res.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(got, clientMessageHeader) {
		t.Fatalf("Allow-Headers = %q", got)
	}
}
