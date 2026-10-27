package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/frontend/host/local"
	"github.com/jonathanung/strike/internal/persist/knowledge"
	"github.com/jonathanung/strike/internal/persist/memory"
	"github.com/jonathanung/strike/internal/protocol"
)

func TestStorageHTTPRedactsRestartsAndSetsMode(t *testing.T) {
	root := t.TempDir()
	project, err := memory.Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	global, err := memory.Open(root, memory.GlobalKey)
	if err != nil {
		t.Fatal(err)
	}
	kProject, err := knowledge.Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	kGlobal, err := knowledge.Open(root, knowledge.GlobalKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = project.Close()
		_ = global.Close()
		_ = kProject.Close()
		_ = kGlobal.Close()
	})

	mem := local.ScopeMemory(local.AdaptMemory(project), global)
	know := local.NewKnowledge(kProject, kGlobal)
	live := NewLive("live", t.TempDir(), nil, make(chan protocol.Op))
	mode := memory.ModeNormal
	live.BindMemoryMode(
		func() string { return mode },
		func(next string) error {
			norm, err := memory.NormalizeMode(next)
			if err != nil {
				return err
			}
			mode = norm
			return nil
		},
	)
	srv, err := New(Options{
		SessionDir: t.TempDir(),
		Live:       live,
		Services:   &host.Services{Memory: mem, Knowledge: know},
	})
	if err != nil {
		t.Fatal(err)
	}

	secret := "sk-ant-api03-abcdefghijklmnopqrstuvwxyz"
	put := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/memory/lesson", strings.NewReader(
		`{"value":"`+secret+`","tags":["preference"],"author":"operator","scope":"project"}`,
	))
	srv.Handler().ServeHTTP(put, req)
	if put.Code != http.StatusOK || strings.Contains(put.Body.String(), secret) {
		t.Fatalf("put = %d %s", put.Code, put.Body.String())
	}

	list := httptest.NewRecorder()
	srv.Handler().ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/memory?scope=project", nil))
	body := list.Body.String()
	if list.Code != http.StatusOK || !strings.Contains(body, "lesson") || strings.Contains(body, secret) {
		t.Fatalf("list = %d %s", list.Code, body)
	}
	if !strings.Contains(body, "markdownFile") || !strings.Contains(body, ".md") {
		t.Fatalf("list missing plain file: %s", body)
	}

	gput := httptest.NewRecorder()
	srv.Handler().ServeHTTP(gput, httptest.NewRequest(http.MethodPut, "/v1/memory/global-note?scope=global", strings.NewReader(
		`{"value":"across projects","author":"operator"}`,
	)))
	if gput.Code != http.StatusOK {
		t.Fatalf("global put = %d %s", gput.Code, gput.Body.String())
	}
	plist := httptest.NewRecorder()
	srv.Handler().ServeHTTP(plist, httptest.NewRequest(http.MethodGet, "/v1/memory?scope=project", nil))
	if strings.Contains(plist.Body.String(), "global-note") {
		t.Fatalf("project list included global entry: %s", plist.Body.String())
	}

	kput := httptest.NewRecorder()
	srv.Handler().ServeHTTP(kput, httptest.NewRequest(http.MethodPut, "/v1/knowledge", strings.NewReader(
		`{"path":"docs/theme.md","note":"see `+secret+`","author":"operator","scope":"project"}`,
	)))
	if kput.Code != http.StatusOK || strings.Contains(kput.Body.String(), secret) {
		t.Fatalf("pin = %d %s", kput.Code, kput.Body.String())
	}

	bad := httptest.NewRecorder()
	srv.Handler().ServeHTTP(bad, httptest.NewRequest(http.MethodPut, "/v1/memory/mode?root=live", strings.NewReader(`{"mode":"nope"}`)))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad mode = %d %s", bad.Code, bad.Body.String())
	}
	set := httptest.NewRecorder()
	srv.Handler().ServeHTTP(set, httptest.NewRequest(http.MethodPut, "/v1/memory/mode?root=live", strings.NewReader(`{"mode":"incognito"}`)))
	if set.Code != http.StatusOK || !strings.Contains(set.Body.String(), `"incognito"`) {
		t.Fatalf("set mode = %d %s", set.Code, set.Body.String())
	}
	got := httptest.NewRecorder()
	srv.Handler().ServeHTTP(got, httptest.NewRequest(http.MethodGet, "/v1/memory/mode?root=live", nil))
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"wired":true`) || !strings.Contains(got.Body.String(), "incognito") {
		t.Fatalf("get mode = %d %s", got.Code, got.Body.String())
	}

	if err := project.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := memory.Open(root, "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	entry, ok, err := reopened.Get("lesson")
	if err != nil || !ok {
		t.Fatalf("reopen ok=%v err=%v", ok, err)
	}
	if strings.Contains(entry.Value, secret) || entry.Author != "operator" {
		t.Fatalf("reopened entry = %+v", entry)
	}
}
