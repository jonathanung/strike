package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/persist/memory"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

type lessonsFixture struct {
	srv     *Server
	project *memory.Store
	global  *memory.Store
	mode    *string
	repo    string
	trust   string
}

// newLessonsFixture wires real project/global stores, a live session with a
// settable memory mode, and (when withTrust) the workspace trust gate.
func newLessonsFixture(t *testing.T, withTrust, trusted bool) lessonsFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	project, err := memory.Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	global, err := memory.Open(root, memory.GlobalKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = project.Close(); _ = global.Close() })
	repo := t.TempDir()
	live := NewLive("sess-lessons", repo, nil, make(chan protocol.Op))
	t.Cleanup(live.Close)
	mode := memory.ModeNormal
	live.BindMemoryMode(func() string { return mode }, func(next string) error { mode = next; return nil })
	opts := Options{
		SessionDir: t.TempDir(),
		Live:       live,
		Services:   &host.Services{Memory: local.ScopeMemory(local.AdaptMemory(project), global)},
	}
	trustPath := ""
	if withTrust {
		trustPath = filepath.Join(t.TempDir(), "workspace-trust.json")
		opts.WorkspaceTrustPath = trustPath
		opts.WorkspaceTrustAssembled = &WorkspaceTrustAssembly{Dir: repo, Root: repo, Trusted: trusted}
		if trusted {
			store, err := workspacetrust.Open(trustPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Trust(repo); err != nil {
				t.Fatal(err)
			}
		}
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return lessonsFixture{srv: srv, project: project, global: global, mode: &mode, repo: repo, trust: trustPath}
}

func (f lessonsFixture) do(t *testing.T, method, path, body string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	f.srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestLessonProposalApproveFlowRedacts(t *testing.T) {
	f := newLessonsFixture(t, true, true)
	secret := "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"

	code, body := f.do(t, http.MethodPost, "/v1/memory/proposals",
		`{"key":"test-cmd","value":"use make test-fast `+secret+`","session":"spoofed"}`)
	if code != http.StatusCreated || strings.Contains(body, secret) {
		t.Fatalf("propose = %d %s", code, body)
	}
	var p host.MemoryProposal
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.Scope != "project" || p.Session != "sess-lessons" {
		t.Fatalf("proposal = %+v", p)
	}
	raw, err := os.ReadFile(f.project.ProposalsPath())
	if err != nil || strings.Contains(string(raw), secret) {
		t.Fatalf("proposal file leaks secret (err=%v)", err)
	}

	code, body = f.do(t, http.MethodGet, "/v1/memory/proposals", "")
	if code != http.StatusOK || !strings.Contains(body, p.ID) {
		t.Fatalf("list = %d %s", code, body)
	}
	// Pending is not stored memory.
	if entries, _ := f.project.List(""); len(entries) != 0 {
		t.Fatalf("pending proposal stored: %+v", entries)
	}

	code, body = f.do(t, http.MethodPatch, "/v1/memory/proposals/"+p.ID, `{"key":"test-fast","value":"run make test-fast","scope":"project"}`)
	if code != http.StatusOK || !strings.Contains(body, `"key":"test-fast"`) {
		t.Fatalf("edit = %d %s", code, body)
	}

	// A key collision is refused unless replace is set; the proposal stays.
	if err := f.project.Put("test-fast", "old", nil); err != nil {
		t.Fatal(err)
	}
	code, body = f.do(t, http.MethodPost, "/v1/memory/proposals/"+p.ID+"/approve", "")
	if code != http.StatusConflict {
		t.Fatalf("approve collision = %d %s", code, body)
	}
	if list, _ := f.project.Proposals(); len(list) != 1 {
		t.Fatalf("proposal lost on refused approve: %+v", list)
	}
	code, body = f.do(t, http.MethodPost, "/v1/memory/proposals/"+p.ID+"/approve", `{"replace":true}`)
	if code != http.StatusOK {
		t.Fatalf("approve = %d %s", code, body)
	}
	got, ok, err := f.project.Get("test-fast")
	if err != nil || !ok || got.Value != "run make test-fast" || len(got.Tags) != 1 || got.Tags[0] != memory.TagLesson {
		t.Fatalf("approved entry = %+v ok=%v err=%v", got, ok, err)
	}
	if list, _ := f.project.Proposals(); len(list) != 0 {
		t.Fatalf("approved proposal still pending: %+v", list)
	}
	if code, _ := f.do(t, http.MethodPost, "/v1/memory/proposals/"+p.ID+"/approve", ""); code != http.StatusNotFound {
		t.Fatalf("approve twice = %d", code)
	}
}

func TestLessonProposalGlobalAndDismiss(t *testing.T) {
	f := newLessonsFixture(t, false, false)
	code, body := f.do(t, http.MethodPost, "/v1/memory/proposals", `{"key":"tabs","value":"prefer tabs","scope":"global"}`)
	if code != http.StatusCreated {
		t.Fatalf("propose = %d %s", code, body)
	}
	var p host.MemoryProposal
	_ = json.Unmarshal([]byte(body), &p)
	if code, body := f.do(t, http.MethodPost, "/v1/memory/proposals/"+p.ID+"/approve", ""); code != http.StatusOK {
		t.Fatalf("approve = %d %s", code, body)
	}
	if _, ok, _ := f.global.Get("tabs"); !ok {
		t.Fatal("global approve did not store in the global scope")
	}

	code, body = f.do(t, http.MethodPost, "/v1/memory/proposals", `{"key":"x","value":"dismiss me"}`)
	if code != http.StatusCreated {
		t.Fatalf("propose = %d %s", code, body)
	}
	_ = json.Unmarshal([]byte(body), &p)
	if code, _ := f.do(t, http.MethodDelete, "/v1/memory/proposals/"+p.ID, ""); code != http.StatusNoContent {
		t.Fatalf("dismiss = %d", code)
	}
	if _, ok, _ := f.project.Get("x"); ok {
		t.Fatal("dismiss stored the proposal")
	}
	if code, _ := f.do(t, http.MethodDelete, "/v1/memory/proposals/"+p.ID, ""); code != http.StatusNotFound {
		t.Fatalf("dismiss twice = %d", code)
	}
	for _, bad := range []string{`{"key":"","value":"v"}`, `{"key":"k","value":""}`, `{"key":"k","value":"v","scope":"team"}`} {
		if code, body := f.do(t, http.MethodPost, "/v1/memory/proposals", bad); code != http.StatusBadRequest {
			t.Fatalf("propose %s = %d %s", bad, code, body)
		}
	}
}

func TestLessonProposeRefusedByModeAndTrust(t *testing.T) {
	for _, mode := range []string{memory.ModeIncognito, memory.ModeTemporary} {
		t.Run(mode, func(t *testing.T) {
			f := newLessonsFixture(t, false, false)
			*f.mode = mode
			code, body := f.do(t, http.MethodPost, "/v1/memory/proposals", `{"key":"k","value":"v"}`)
			if code != http.StatusForbidden || !strings.Contains(body, mode) {
				t.Fatalf("propose in %s = %d %s", mode, code, body)
			}
			if list, _ := f.project.Proposals(); len(list) != 0 {
				t.Fatalf("refused proposal stored: %+v", list)
			}
		})
	}
	t.Run("untrusted", func(t *testing.T) {
		f := newLessonsFixture(t, true, false)
		code, body := f.do(t, http.MethodPost, "/v1/memory/proposals", `{"key":"k","value":"v"}`)
		if code != http.StatusForbidden || !strings.Contains(body, "untrusted workspace") {
			t.Fatalf("propose untrusted = %d %s", code, body)
		}
		if list, _ := f.project.Proposals(); len(list) != 0 {
			t.Fatalf("refused proposal stored: %+v", list)
		}
		// A fresh grant counts without a restart.
		store, err := workspacetrust.Open(f.trust)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.Trust(f.repo); err != nil {
			t.Fatal(err)
		}
		if code, body := f.do(t, http.MethodPost, "/v1/memory/proposals", `{"key":"k","value":"v"}`); code != http.StatusCreated {
			t.Fatalf("propose after grant = %d %s", code, body)
		}
	})
}

func TestSessionLessonsUsed(t *testing.T) {
	f := newLessonsFixture(t, false, false)
	if err := f.project.RecordInjected("sess-a", []memory.Entry{{Key: "test-fast", Value: "run make test-fast", Tags: []string{memory.TagLesson}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.global.RecordInjected("sess-a", []memory.Entry{{Key: "tabs", Value: "prefer tabs"}}); err != nil {
		t.Fatal(err)
	}
	code, body := f.do(t, http.MethodGet, "/v1/sessions/sess-a/lessons-used", "")
	if code != http.StatusOK {
		t.Fatalf("lessons-used = %d %s", code, body)
	}
	var out struct {
		SessionID string                 `json:"sessionId"`
		Entries   []host.MemoryInjection `json:"entries"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.SessionID != "sess-a" || len(out.Entries) != 2 || out.Entries[0].Key != "test-fast" || out.Entries[0].Scope != "project" || out.Entries[1].Scope != "global" {
		t.Fatalf("lessons-used = %+v", out)
	}
	code, body = f.do(t, http.MethodGet, "/v1/sessions/other/lessons-used", "")
	if code != http.StatusOK || !strings.Contains(body, `"entries":[]`) {
		t.Fatalf("unknown session = %d %s", code, body)
	}
}

func TestLessonRoutesWithoutMemory(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/memory/proposals"},
		{http.MethodGet, "/v1/sessions/s/lessons-used"},
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s = %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}
