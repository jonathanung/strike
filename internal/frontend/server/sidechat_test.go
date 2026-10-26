package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/protocol"
)

type scriptedSideChat struct {
	got    SideChatPrompt
	text   string
	err    error
	called bool
}

func (m *scriptedSideChat) Answer(_ context.Context, in SideChatPrompt) (SideChatCompletion, error) {
	m.called = true
	m.got = in
	if m.err != nil {
		return SideChatCompletion{}, m.err
	}
	return SideChatCompletion{Text: m.text, Provider: "echo", Model: "echo"}, nil
}

func appendSessionEvent(t *testing.T, dir, id string, ev protocol.Event) {
	t.Helper()
	env, err := protocol.Wrap(ev)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(session.LogPath(dir, id), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(raw, '\n')); err != nil {
		t.Fatal(err)
	}
}

func postSide(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	return res
}

func TestSideChatRefusesToolApproval(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("sess-side", t.TempDir(), nil, ops)
	defer live.Close()
	model := &scriptedSideChat{text: "should not run"}
	dir := t.TempDir()
	srv, err := New(Options{SessionDir: dir, Live: live, SideChat: model})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		path string
		body string
		want string
	}{
		{
			name: "permission reply on ops channel",
			path: "/v1/sidechat/ops",
			body: `{"type":"permission.reply","data":{"requestId":"r1","decision":"once"}}`,
			want: "cannot approve tools",
		},
		{
			name: "user input on ops channel",
			path: "/v1/sidechat/ops",
			body: `{"type":"user.input","data":{"text":"approve it"}}`,
			want: "cannot post user.input",
		},
		{
			name: "steer on ops channel",
			path: "/v1/sidechat/ops",
			body: `{"type":"steer","data":{"text":"stop and do this instead"}}`,
			want: "cannot steer",
		},
		{
			name: "question reply on ops channel",
			path: "/v1/sidechat/ops",
			body: `{"type":"question.reply","data":{"requestId":"q1","answers":["yes"]}}`,
			want: "cannot answer a pending question",
		},
		{
			name: "permission reply smuggled onto ask",
			path: "/v1/sidechat/ask",
			body: `{"type":"permission.reply","data":{"requestId":"r1","decision":"always"}}`,
			want: "cannot approve tools",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postSide(t, srv, tc.path, tc.body)
			if res.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body %s", res.Code, res.Body.String())
			}
			if !strings.Contains(res.Body.String(), tc.want) || !strings.Contains(res.Body.String(), sideChatReadOnlyCode) {
				t.Fatalf("body = %s", res.Body.String())
			}
			select {
			case op := <-ops:
				t.Fatalf("side chat submitted %T", op)
			default:
			}
		})
	}
	if model.called {
		t.Fatal("approval attempt called the side chat model")
	}

	// The main ops channel still accepts the same approval. The refusal is
	// the side-chat route, not a global block.
	main := postSide(t, srv, "/v1/ops", `{"type":"permission.reply","data":{"requestId":"r1","decision":"once"}}`)
	if main.Code != http.StatusOK {
		t.Fatalf("main ops status = %d %s", main.Code, main.Body.String())
	}
	select {
	case op := <-ops:
		reply, ok := op.(protocol.PermissionReply)
		if !ok || reply.RequestID != "r1" || reply.Decision != protocol.DecisionOnce {
			t.Fatalf("main op = %#v", op)
		}
	default:
		t.Fatal("main channel did not receive permission.reply")
	}
}

func TestSideChatAskUsesTranscriptAndDoesNotSubmit(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	dir := t.TempDir()
	const id = "sess-side"
	live := NewLive(id, dir, nil, ops)
	defer live.Close()
	appendSessionEvent(t, dir, id, protocol.UserMessage{Text: "read the makefile"})
	appendSessionEvent(t, dir, id, protocol.TextDelta{Text: "Opening Makefile."})
	appendSessionEvent(t, dir, id, protocol.ToolCallBegin{Name: "read", CallID: "c1"})
	appendSessionEvent(t, dir, id, protocol.PermissionAsked{RequestID: "r1", Permission: "read", Patterns: []string{"Makefile"}})
	appendSessionEvent(t, dir, "child-1", protocol.TextDelta{Text: "child looked at the recipe"})
	live.Publish(protocol.TeamRoster{
		LeadID: id,
		Members: []protocol.TeamRosterMember{{
			SessionID:       "child-1",
			Name:            "scout",
			State:           "blocked",
			ParentSessionID: id,
		}},
	})
	live.Publish(protocol.ModelSelected{Provider: "echo", Model: "echo-1"})

	before, err := os.ReadFile(session.LogPath(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	model := &scriptedSideChat{text: "The agent is waiting to read Makefile."}
	srv, err := New(Options{SessionDir: dir, Live: live, SideChat: model})
	if err != nil {
		t.Fatal(err)
	}

	res := postSide(t, srv, "/v1/sidechat/ask", `{"text":" what is it doing? ","focusSessionId":"child-1","focusName":"scout"}`)
	if res.Code != http.StatusOK {
		t.Fatalf("ask = %d %s", res.Code, res.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["readOnly"] != true {
		t.Fatalf("readOnly = %#v", got["readOnly"])
	}
	if got["answer"] != model.text {
		t.Fatalf("answer = %#v", got["answer"])
	}
	if !model.called {
		t.Fatal("model was not called")
	}
	if !strings.Contains(model.got.System, "read-only") || !strings.Contains(model.got.System, "approve") {
		t.Fatalf("system = %q", model.got.System)
	}
	for _, want := range []string{"read the makefile", "Opening Makefile.", "Tool: read", "Approval waiting", "Makefile", "scout", "child looked at the recipe"} {
		if !strings.Contains(model.got.Transcript, want) {
			t.Fatalf("transcript missing %q:\n%s", want, model.got.Transcript)
		}
	}
	if model.got.Question != "what is it doing?" {
		t.Fatalf("question = %q", model.got.Question)
	}
	if model.got.Provider != "echo" || model.got.Model != "echo-1" {
		t.Fatalf("live model = %#v", model.got)
	}
	select {
	case op := <-ops:
		t.Fatalf("ask submitted %T", op)
	default:
	}
	after, err := os.ReadFile(session.LogPath(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("side chat wrote into the session log")
	}
	sideRaw, err := os.ReadFile(sideChatPath(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(sideRaw, []byte("what is it doing?")) {
		t.Fatalf("sidecar = %s", sideRaw)
	}
	if bytes.Contains(before, []byte("what is it doing?")) {
		t.Fatal("question landed in the session log snapshot")
	}

	listReq := httptest.NewRequest(http.MethodGet, "/v1/sidechat", nil)
	listRes := httptest.NewRecorder()
	srv.Handler().ServeHTTP(listRes, listReq)
	if listRes.Code != http.StatusOK || !strings.Contains(listRes.Body.String(), `"readOnly":true`) || !strings.Contains(listRes.Body.String(), "what is it doing?") {
		t.Fatalf("list = %d %s", listRes.Code, listRes.Body.String())
	}

	model.text = "Still waiting on the read."
	again := postSide(t, srv, "/v1/sidechat/ask", `{"text":"and now?"}`)
	if again.Code != http.StatusOK {
		t.Fatalf("second ask = %d %s", again.Code, again.Body.String())
	}
	if len(model.got.History) != 1 || model.got.History[0].Question != "what is it doing?" {
		t.Fatalf("history = %#v", model.got.History)
	}
	select {
	case op := <-ops:
		t.Fatalf("second ask submitted %T", op)
	default:
	}
}

func TestSideChatAskRejectsEmptyAndPathTraversal(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("sess-side", t.TempDir(), nil, ops)
	defer live.Close()
	model := &scriptedSideChat{text: "nope"}
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live, SideChat: model})
	if err != nil {
		t.Fatal(err)
	}
	empty := postSide(t, srv, "/v1/sidechat/ask", `{"text":"  "}`)
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("empty = %d %s", empty.Code, empty.Body.String())
	}
	escape := postSide(t, srv, "/v1/sidechat/ask", `{"text":"hi","focusSessionId":"../secret"}`)
	if escape.Code != http.StatusBadRequest {
		t.Fatalf("escape = %d %s", escape.Code, escape.Body.String())
	}
	stranger := postSide(t, srv, "/v1/sidechat/ask", `{"text":"hi","focusSessionId":"other-root"}`)
	if stranger.Code != http.StatusBadRequest || !strings.Contains(stranger.Body.String(), "not part of this session") {
		t.Fatalf("stranger = %d %s", stranger.Code, stranger.Body.String())
	}
	if model.called {
		t.Fatal("rejected ask called the model")
	}
	select {
	case op := <-ops:
		t.Fatalf("rejected ask submitted %T", op)
	default:
	}
}

func TestSideChatTranscriptAllowsImageSizedLine(t *testing.T) {
	dir := t.TempDir()
	const id = "sess-side"
	// Larger than the old 1 MiB scanner cap, smaller than the session log cap.
	appendSessionEvent(t, dir, id, protocol.UserMessage{Text: strings.Repeat("a", 1024*1024+64)})
	appendSessionEvent(t, dir, id, protocol.UserMessage{Text: "after the image"})
	got, err := sessionTranscript(session.LogPath(dir, id), sideChatMaxRunes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "after the image") {
		t.Fatalf("transcript = %q", got)
	}
}

func TestSideChatAskRequiresModel(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("sess-side", t.TempDir(), nil, ops)
	defer live.Close()
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	res := postSide(t, srv, "/v1/sidechat/ask", `{"text":"hello"}`)
	if res.Code != http.StatusServiceUnavailable || !strings.Contains(res.Body.String(), "not configured") {
		t.Fatalf("ask = %d %s", res.Code, res.Body.String())
	}
	// The read-only refusal still holds when no model is wired.
	denied := postSide(t, srv, "/v1/sidechat/ops", `{"type":"permission.reply","data":{"requestId":"r1","decision":"once"}}`)
	if denied.Code != http.StatusForbidden || !strings.Contains(denied.Body.String(), "cannot approve tools") {
		t.Fatalf("ops = %d %s", denied.Code, denied.Body.String())
	}
	select {
	case op := <-ops:
		t.Fatalf("submitted %T", op)
	default:
	}
}

func TestSideChatAuthStillRefusesApproval(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("sess-side", t.TempDir(), nil, ops)
	defer live.Close()
	srv, err := New(Options{Auth: true, Token: "secret", SessionDir: t.TempDir(), Live: live, SideChat: &scriptedSideChat{text: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	anon := httptest.NewRequest(http.MethodPost, "/v1/sidechat/ops", strings.NewReader(`{"type":"permission.reply","data":{"requestId":"r1","decision":"once"}}`))
	anonRes := httptest.NewRecorder()
	srv.Handler().ServeHTTP(anonRes, anon)
	if anonRes.Code != http.StatusUnauthorized {
		t.Fatalf("anon = %d", anonRes.Code)
	}
	authed := httptest.NewRequest(http.MethodPost, "/v1/sidechat/ops", strings.NewReader(`{"type":"permission.reply","data":{"requestId":"r1","decision":"once"}}`))
	authed.Header.Set("Authorization", "Bearer secret")
	authedRes := httptest.NewRecorder()
	srv.Handler().ServeHTTP(authedRes, authed)
	if authedRes.Code != http.StatusForbidden || !strings.Contains(authedRes.Body.String(), sideChatReadOnlyCode) {
		t.Fatalf("authed = %d %s", authedRes.Code, authedRes.Body.String())
	}
	select {
	case op := <-ops:
		t.Fatalf("submitted %T", op)
	default:
	}
}
