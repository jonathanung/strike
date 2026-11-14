package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/pkg/protocol"
)

const reportSecret = "sk-ant-api03-REPORTSECRETVALUE1234"

// CreateSessionReport lets the shared fakeArtifacts satisfy host.SessionReportWriter.
func (f *fakeArtifacts) CreateSessionReport(sessionID, ownerRoot, title, content string) (host.Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.arts == nil {
		f.arts = map[string]host.Artifact{}
	}
	now := time.Now().UTC()
	a := host.Artifact{
		ID: fmt.Sprintf("art-%d", len(f.arts)+1), Type: "session_report", Title: title, Content: content,
		Version: 1, Scope: "session", SessionID: sessionID, Access: "team",
		OwnerSession: sessionID, OwnerRoot: ownerRoot, CreatedAt: now, UpdatedAt: now,
		Versions: []int{1},
	}
	f.arts[a.ID] = a
	return a, nil
}

// reportSessions is a Sessions stub serving one seeded record.
type reportSessions struct {
	childSessions
	sess host.Session
	raw  []byte
}

func (r reportSessions) Get(id string) (host.Session, bool, error) {
	if id != r.sess.ID {
		return host.Session{}, false, nil
	}
	return r.sess, true, nil
}
func (r reportSessions) ReplayJSONL(string) ([]byte, error) { return r.raw, nil }

// seededSessionRecord builds a JSONL record with two turns, three approvals
// (allowed, denied, never resolved), a policy allow, a child session whose
// own tool calls are interleaved, file changes, usage, and a secret-shaped
// user message and tool argument.
func seededSessionRecord(t *testing.T, sid string) []byte {
	t.Helper()
	c := protocol.Correlation{SessionID: sid, TurnID: "t1"}
	child := protocol.Correlation{SessionID: "child-1", ParentSessionID: sid, Depth: 1}
	evs := []protocol.Event{
		protocol.ModelSelected{Correlation: c, Provider: "anthropic", Model: "claude-x"},
		protocol.UserMessage{Correlation: c, Text: "Fix the login bug\nwith key " + reportSecret},
		protocol.TurnStarted{Correlation: c},
		protocol.ToolCallBegin{Correlation: c, CallID: "1", Name: "bash", Args: json.RawMessage(`{"command":"echo SUPERSECRETARG"}`)},
		protocol.ToolCallEnd{Correlation: c, CallID: "1", IsError: true},
		protocol.ToolCallBegin{Correlation: c, CallID: "2", Name: "edit", Args: json.RawMessage(`{}`)},
		protocol.ToolCallBegin{Correlation: c, CallID: "3", Name: "edit", Args: json.RawMessage(`{}`)},
		protocol.PermissionAsked{Correlation: c, RequestID: "r1", Permission: "bash"},
		protocol.PermissionResolved{Correlation: c, RequestID: "r1", Decision: protocol.DecisionOnce},
		protocol.PermissionAsked{Correlation: c, RequestID: "r2", Permission: "edit"},
		protocol.PermissionDecided{Correlation: c, RequestID: "r2", Permission: "edit", Action: "deny", Decision: protocol.DecisionReject},
		protocol.PermissionAsked{Correlation: c, RequestID: "r3", Permission: "bash"}, // expired: never resolved
		protocol.PermissionDecided{Correlation: c, Permission: "read", Action: "allow"},
		protocol.ChildStarted{Correlation: child, Name: "scribe", Agent: "general"},
		protocol.ToolCallBegin{Correlation: child, CallID: "c1", Name: "grep"},
		protocol.TurnStarted{Correlation: child},
		protocol.ChildStarted{Correlation: protocol.Correlation{SessionID: "grand-1", ParentSessionID: "child-1", Depth: 2}, Name: "nested"},
		protocol.ChildCompleted{Correlation: child, Status: protocol.ChildStatusCompleted, Name: "scribe"},
		protocol.UsageReported{Correlation: c, Input: protocol.TokenCount{N: 100, Known: true}, Output: protocol.TokenCount{N: 40, Known: true}},
		protocol.TurnCompleted{Correlation: c, StopReason: "end_turn", Files: []protocol.TurnFileChange{
			{Path: "auth/login.go", Kind: "update"}, {Path: "auth/login_test.go", Kind: "create"},
		}},
		protocol.UserMessage{Correlation: c, Text: "Now remove the old helper"},
		protocol.TurnStarted{Correlation: c},
		protocol.UsageReported{Correlation: c, Input: protocol.TokenCount{N: 20, Known: true}, Output: protocol.TokenCount{N: 5, Known: true}},
		protocol.TurnCompleted{Correlation: c, StopReason: "end_turn", Files: []protocol.TurnFileChange{
			{Path: "auth/helper.go", Kind: "delete"},
		}},
	}
	start := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	var b strings.Builder
	b.WriteString(`{"type":"session.header","data":{"schemaVersion":1}}` + "\n")
	for i, ev := range evs {
		env, err := protocol.Wrap(ev)
		if err != nil {
			t.Fatal(err)
		}
		env.Time = start.Add(time.Duration(i) * time.Second)
		line, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func TestBuildSessionReportFactsSeeded(t *testing.T) {
	f, err := buildSessionReportFacts("root-1", seededSessionRecord(t, "root-1"))
	if err != nil {
		t.Fatal(err)
	}
	if f.TurnsStarted != 2 || f.TurnsCompleted != 2 || f.FinalStatus != "end_turn" {
		t.Fatalf("turns = %d/%d status %q", f.TurnsStarted, f.TurnsCompleted, f.FinalStatus)
	}
	if len(f.Asks) != 2 || !strings.HasPrefix(f.Asks[0], "Fix the login bug") || strings.Contains(f.Asks[0], "with key") {
		t.Fatalf("asks = %#v", f.Asks)
	}
	if f.ToolCalls["bash"] != 1 || f.ToolCalls["edit"] != 2 || f.ToolCalls["grep"] != 0 || f.ToolErrors != 1 {
		t.Fatalf("tools = %#v errors %d (child grep must not count)", f.ToolCalls, f.ToolErrors)
	}
	if len(f.Files) != 3 || f.Files["auth/helper.go"] != "delete" || f.Files["auth/login_test.go"] != "create" {
		t.Fatalf("files = %#v", f.Files)
	}
	if f.ApprovalsAsked != 3 || f.ApprovalsAllowed != 1 || f.ApprovalsDenied != 1 || f.ApprovalsUnresolved != 1 {
		t.Fatalf("approvals asked=%d allowed=%d denied=%d unresolved=%d",
			f.ApprovalsAsked, f.ApprovalsAllowed, f.ApprovalsDenied, f.ApprovalsUnresolved)
	}
	if f.PolicyAllowed != 1 || f.PolicyDenied != 0 {
		t.Fatalf("policy = %d/%d", f.PolicyAllowed, f.PolicyDenied)
	}
	if len(f.Children) != 1 || f.Children[0].SessionID != "child-1" || f.Children[0].Status != "completed" || f.Children[0].Name != "scribe" {
		t.Fatalf("children = %#v", f.Children)
	}
	if f.Usage["input"] != 120 || f.Usage["output"] != 45 {
		t.Fatalf("usage = %#v", f.Usage)
	}
	if len(f.Models) != 1 || f.Models[0] != "anthropic/claude-x" {
		t.Fatalf("models = %#v", f.Models)
	}
	if !f.Start.Equal(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)) || !f.End.After(f.Start) {
		t.Fatalf("start/end = %v %v", f.Start, f.End)
	}
}

func TestBuildSessionReportFactsInterruptedAndError(t *testing.T) {
	c := protocol.Correlation{SessionID: "s"}
	var b strings.Builder
	for _, ev := range []protocol.Event{protocol.TurnStarted{Correlation: c}} {
		env, _ := protocol.Wrap(ev)
		line, _ := json.Marshal(env)
		b.Write(line)
		b.WriteByte('\n')
	}
	f, err := buildSessionReportFacts("s", []byte(b.String()))
	if err != nil || f.FinalStatus != "interrupted" {
		t.Fatalf("status = %q err %v", f.FinalStatus, err)
	}
	env, _ := protocol.Wrap(protocol.EngineError{Correlation: c, Message: "boom"})
	line, _ := json.Marshal(env)
	b.Write(line)
	b.WriteByte('\n')
	f, err = buildSessionReportFacts("s", []byte(b.String()))
	if err != nil || f.FinalStatus != "error" || f.Errors != 1 || f.LastError != "boom" {
		t.Fatalf("facts = %+v err %v", f, err)
	}
	if _, err := buildSessionReportFacts("s", []byte("{not json\n")); err == nil {
		t.Fatal("corrupt record should fail")
	}
}

func TestSessionReportRouteStoresRedactedArtifact(t *testing.T) {
	arts := &fakeArtifacts{arts: map[string]host.Artifact{}}
	sessions := reportSessions{
		sess: host.Session{ID: "root-1", Title: "login fix", ProjectKey: "/work/app"},
		raw:  seededSessionRecord(t, "root-1"),
	}
	srv, err := New(Options{Services: &host.Services{Artifacts: arts, Sessions: sessions}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/root-1/report?actorRoot=root-1", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	var dto artifactDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Type != "session_report" || dto.Scope != "session" || dto.SessionID != "root-1" || dto.OwnerRoot != "root-1" {
		t.Fatalf("dto = %+v", dto.artifactMetaDTO)
	}
	if dto.Title != "Session report: login fix" {
		t.Fatalf("title = %q", dto.Title)
	}
	for _, want := range []string{
		"- Turns: 2 started, 2 completed",
		"- Final status: end_turn",
		"- Workspace: /work/app",
		"- Models: anthropic/claude-x",
		"1. Fix the login bug",
		"- Calls: 3 (1 errored)",
		"- By tool: edit 2, bash 1",
		"## Files changed (3)",
		"- delete `auth/helper.go`",
		"- Asked: 3 — allowed 1, denied 1, unresolved 1",
		"- Decided by policy without asking: allowed 1, denied 0",
		"- `child-1` scribe — completed",
		"- Input: 120\n- Output: 45",
	} {
		if !strings.Contains(dto.Content, want) {
			t.Errorf("report missing %q\n%s", want, dto.Content)
		}
	}
	for _, leak := range []string{reportSecret, "SUPERSECRETARG", "grep", "grand-1"} {
		if strings.Contains(dto.Content, leak) {
			t.Errorf("report leaked %q\n%s", leak, dto.Content)
		}
	}
	if strings.Contains(dto.Content, "## Errors") {
		t.Errorf("errors section should be omitted when none recorded\n%s", dto.Content)
	}
}

func TestSessionReportRouteRedactsSecretInAsk(t *testing.T) {
	c := protocol.Correlation{SessionID: "s"}
	env, _ := protocol.Wrap(protocol.UserMessage{Correlation: c, Text: "deploy with " + reportSecret})
	line, _ := json.Marshal(env)
	arts := &fakeArtifacts{arts: map[string]host.Artifact{}}
	srv, err := New(Options{Services: &host.Services{
		Artifacts: arts,
		Sessions:  reportSessions{sess: host.Session{ID: "s"}, raw: append(line, '\n')},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/s/report", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), reportSecret) || !strings.Contains(rec.Body.String(), "deploy with") {
		t.Fatalf("ask not redacted: %s", rec.Body.String())
	}
}

func TestSessionReportRouteLongTitleFitsStore(t *testing.T) {
	arts := &fakeArtifacts{arts: map[string]host.Artifact{}}
	srv, err := New(Options{Services: &host.Services{
		Artifacts: arts,
		Sessions:  reportSessions{sess: host.Session{ID: "s", Title: strings.Repeat("界", 400)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/s/report", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("report: %d %s", rec.Code, rec.Body.String())
	}
	var dto artifactDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if len(dto.Title) > 512 {
		t.Fatalf("title %d bytes exceeds store cap", len(dto.Title))
	}
}

func TestSessionReportRouteErrors(t *testing.T) {
	sessions := reportSessions{sess: host.Session{ID: "s"}}
	// Unknown session → 404.
	srv, err := New(Options{Services: &host.Services{Artifacts: &fakeArtifacts{}, Sessions: sessions}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/missing/report", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing session: %d %s", rec.Code, rec.Body.String())
	}
	// No artifact store → 501.
	srv, err = New(Options{Services: &host.Services{Sessions: sessions}})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/sessions/s/report", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("no artifacts: %d %s", rec.Code, rec.Body.String())
	}
}
