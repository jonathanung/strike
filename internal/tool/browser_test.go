package tool

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/sandbox"
)

func browserTC(t *testing.T, session string) *Context {
	t.Helper()
	tc := allowAll(t.TempDir())
	tc.SessionID = session
	tc.SessionTempDir = t.TempDir()
	t.Cleanup(resetBrowserProfilesForTest)
	return tc
}

func TestBrowserNavigateSnapshot(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><head><title>Demo</title></head>
<body><header></header><main>
<h1>Hello World</h1>
<a href="https://example.com/page">link</a>
<button>Save</button>
<img src="x.png">
<input type="password" name="secret">
</main></body></html>`))
	})
	tc := browserTC(t, "sess-nav")
	res, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate",
		"url":    srv.URL,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Hello World", "heading1", "link", "button", `missing=alt`, "textbox"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("navigate missing %q\n%s", want, res.Output)
		}
	}
	if !strings.Contains(string(res.Metadata), `"action":"navigate"`) {
		t.Errorf("metadata = %s", res.Metadata)
	}

	snap, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "snapshot",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap.Output, "Hello World") {
		t.Fatalf("snapshot = %s", snap.Output)
	}
}

func TestBrowserProfilesIsolated(t *testing.T) {
	var sawA, sawB string
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("sid"); err == nil {
			switch c.Value {
			case "a":
				sawA = c.Value
			case "b":
				sawB = c.Value
			}
		}
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: r.URL.Query().Get("v")})
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>ok</body></html>"))
	})
	a := browserTC(t, "task-a")
	b := browserTC(t, "task-b")
	if _, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL + "/?v=a",
	}), a); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL + "/?v=b",
	}), b); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL + "/again",
	}), a); err != nil {
		t.Fatal(err)
	}
	if _, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL + "/again",
	}), b); err != nil {
		t.Fatal(err)
	}
	if sawA != "a" || sawB != "b" {
		t.Fatalf("cookie leak: a=%q b=%q", sawA, sawB)
	}
}

func TestBrowserRedirectToPrivateDenied(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.1/secret", http.StatusFound)
	})
	_, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL,
	}), browserTC(t, "redir"))
	if err == nil {
		t.Fatal("expected redirect deny")
	}
	if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "local") && !strings.Contains(err.Error(), "redirect") {
		t.Errorf("got %v", err)
	}
}

func TestBrowserNetworkAllowDenied(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	tc := browserTC(t, "allow")
	tc.NetworkAllow = []string{"api.github.com"}
	_, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL,
	}), tc)
	if err == nil {
		t.Fatal("expected network deny")
	}
	if CodeOf(err) != string(CodeNetworkDenied) && !strings.Contains(err.Error(), "network") {
		t.Errorf("got %v", err)
	}
}

func TestBrowserAirGapDenied(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	tc := browserTC(t, "air")
	tc.Sandbox = sandbox.Policy{NoNetwork: true}
	_, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL,
	}), tc)
	if err == nil {
		t.Fatal("expected air-gap deny")
	}
	if CodeOf(err) != string(CodeNetworkDenied) {
		t.Errorf("code=%q err=%v", CodeOf(err), err)
	}
}

func TestBrowserCredentialURLDenied(t *testing.T) {
	_, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate",
		"url":    "https://user:hunter2@example.com/",
	}), browserTC(t, "cred"))
	if err == nil {
		t.Fatal("expected credential url deny")
	}
	if CodeOf(err) != string(CodeBlocked) {
		t.Errorf("code=%q err=%v", CodeOf(err), err)
	}
}

func TestBrowserComputerUseDenied(t *testing.T) {
	tc := browserTC(t, "cu")
	for _, action := range []string{"click", "type", "fill", "upload", "download", "evaluate", "clipboard"} {
		_, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
			"action": action,
		}), tc)
		if err == nil {
			t.Fatalf("%s: expected deny", action)
		}
		if CodeOf(err) != string(CodeBlocked) {
			t.Errorf("%s code=%q err=%v", action, CodeOf(err), err)
		}
	}
}

func TestBrowserDownloadBlocked(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", "attachment; filename=secret.zip")
		_, _ = w.Write([]byte("PK\x03\x04secret"))
	})
	tc := browserTC(t, "dl")
	res, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "download: blocked") {
		t.Fatalf("output=%s", res.Output)
	}
	net, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{"action": "network"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(net.Output, "download blocked") {
		t.Fatalf("network=%s", net.Output)
	}
	con, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{"action": "console"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(con.Output, "download blocked") {
		t.Fatalf("console=%s", con.Output)
	}
}

func TestBrowserRedactsCredentials(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body>token sk-ant-abcdefghijklmnopqrstuvwxyz</body></html>`))
	})
	res, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL,
	}), browserTC(t, "redact"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Output, "sk-ant-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("secret leaked: %s", res.Output)
	}
	if !strings.Contains(res.Output, "REDACTED") {
		t.Fatalf("expected redaction marker: %s", res.Output)
	}
}

func TestBrowserPermissionDenied(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	tc := browserTC(t, "perm")
	tc.Ask = func(context.Context, AskRequest) error { return errors.New("denied") }
	_, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL,
	}), tc)
	if err == nil {
		t.Fatal("expected permission deny")
	}
}

func TestBrowserAskPatterns(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	})
	var saw AskRequest
	tc := browserTC(t, "ask")
	tc.Ask = func(_ context.Context, req AskRequest) error {
		saw = req
		return nil
	}
	_, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if saw.Permission != "browser" || len(saw.Patterns) != 1 {
		t.Fatalf("ask=%+v", saw)
	}
}

func TestBrowserCloseCleansProfile(t *testing.T) {
	srv := webfetchServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body>ok</body></html>"))
	})
	tc := browserTC(t, "close-me")
	if _, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "navigate", "url": srv.URL,
	}), tc); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tc.SessionTempDir, "browser", "close-me")
	if _, err := os.Stat(filepath.Join(dir, "actions.jsonl")); err != nil {
		t.Fatalf("action log missing: %v", err)
	}
	res, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{"action": "close"}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "removed=true") {
		t.Fatalf("close=%s", res.Output)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("profile dir still exists: %v", err)
	}
	_, err = NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{"action": "snapshot"}), tc)
	if err == nil {
		t.Fatal("expected snapshot after close to fail")
	}
}

func TestBrowserScreenshotUnavailable(t *testing.T) {
	res, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{
		"action": "screenshot",
	}), browserTC(t, "shot"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "unavailable") {
		t.Fatalf("output=%s", res.Output)
	}
}

func TestBrowserInvalidArgs(t *testing.T) {
	tc := browserTC(t, "bad")
	if _, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{}), tc); err == nil {
		t.Fatal("expected missing action")
	}
	if _, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{"action": "navigate"}), tc); err == nil {
		t.Fatal("expected missing url")
	}
	if _, err := NewBrowser().Execute(context.Background(), mustJSON(t, map[string]any{"action": "warp"}), tc); err == nil {
		t.Fatal("expected unknown action")
	}
}

func TestBrowserContract(t *testing.T) {
	c := NewBrowser().(interface{ Contract() Contract }).Contract()
	if c.SideEffect != SideEffectNetwork || c.Idempotency != IdempotencySafeRetry {
		t.Fatalf("contract=%+v", c)
	}
}
