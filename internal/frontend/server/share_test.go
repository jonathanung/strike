package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/protocol"
)

const (
	shareMarkerA = "SHARED_SESSION_MARKER_A"
	shareMarkerB = "OTHER_SESSION_MARKER_B"
)

type shareFixture struct {
	srv   *Server
	dir   string
	audit *memAuditor
}

func newShareFixture(t *testing.T) shareFixture {
	t.Helper()
	dir := t.TempDir()
	writeFixtureSession(t, dir, "sess-a", protocol.UserMessage{Text: shareMarkerA})
	writeFixtureSession(t, dir, "sess-b", protocol.UserMessage{Text: shareMarkerB})
	aud := &memAuditor{}
	srv, err := New(Options{Auth: true, Token: "secret", SessionDir: dir, Audit: aud, PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return shareFixture{srv: srv, dir: dir, audit: aud}
}

func (f shareFixture) call(t *testing.T, method, target, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(res, req)
	return res
}

func (f shareFixture) mint(t *testing.T, sessionID, body string) shareMintResponse {
	t.Helper()
	res := f.call(t, http.MethodPost, "/v1/sessions/"+sessionID+"/shares", body, "secret")
	if res.Code != http.StatusCreated {
		t.Fatalf("mint = %d %s", res.Code, res.Body.String())
	}
	var out shareMintResponse
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f shareFixture) redeem(t *testing.T, token string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"token": token, "after": 0})
	return f.call(t, http.MethodPost, "/share/v1/transcript", string(b), "")
}

func TestShareMintRedeemRevokeLifecycle(t *testing.T) {
	f := newShareFixture(t)
	minted := f.mint(t, "sess-a", `{"label":"for review","ttlSeconds":86400}`)
	if !strings.HasPrefix(minted.Token, shareTokenPrefix) || minted.Path != "/#share="+minted.Token {
		t.Fatalf("mint = %+v", minted)
	}
	if minted.Label != "for review" || minted.ID == "" {
		t.Fatalf("mint view = %+v", minted.ShareView)
	}

	// Hash at rest: the token itself is never stored.
	stored, err := os.ReadFile(filepath.Join(f.dir, "shares.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), minted.Token) {
		t.Fatalf("token stored in plain text: %s", stored)
	}
	if info, _ := os.Stat(filepath.Join(f.dir, "shares.json")); info.Mode().Perm() != 0o600 {
		t.Fatalf("shares.json mode = %o", info.Mode().Perm())
	}

	// The token reads its session, and only that one.
	res := f.redeem(t, minted.Token)
	if res.Code != http.StatusOK {
		t.Fatalf("redeem = %d %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), shareMarkerA) || strings.Contains(res.Body.String(), shareMarkerB) {
		t.Fatalf("redeem body = %s", res.Body.String())
	}
	if strings.Contains(res.Body.String(), `"session.header"`) {
		t.Fatalf("session header leaked into share: %s", res.Body.String())
	}
	if got := res.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}

	// The list shows the grant without the token or hash.
	list := f.call(t, http.MethodGet, "/v1/sessions/sess-a/shares", "", "secret")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), minted.ID) {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), minted.Token) || strings.Contains(list.Body.String(), `"hash"`) {
		t.Fatalf("list leaks secret: %s", list.Body.String())
	}
	if other := f.call(t, http.MethodGet, "/v1/sessions/sess-b/shares", "", "secret"); strings.Contains(other.Body.String(), minted.ID) {
		t.Fatalf("grant listed on another session: %s", other.Body.String())
	}

	// Revoking from the wrong session is a 404; from the right one, gone.
	if res := f.call(t, http.MethodDelete, "/v1/sessions/sess-b/shares/"+minted.ID, "", "secret"); res.Code != http.StatusNotFound {
		t.Fatalf("cross-session revoke = %d", res.Code)
	}
	if res := f.call(t, http.MethodDelete, "/v1/sessions/sess-a/shares/"+minted.ID, "", "secret"); res.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", res.Code, res.Body.String())
	}
	if res := f.redeem(t, minted.Token); res.Code != http.StatusNotFound {
		t.Fatalf("revoked redeem = %d", res.Code)
	}

	// Audit: mint, redeem, revoke, and the denied redeem; no token anywhere.
	var ops []string
	for _, p := range f.audit.payloads() {
		ops = append(ops, p.OpType+":"+p.Outcome)
		blob, _ := json.Marshal(p)
		if strings.Contains(string(blob), minted.Token) {
			t.Fatalf("audit holds the token: %s", blob)
		}
	}
	want := "share.mint:ok,share.redeem:ok,share.revoke:ok,share.redeem:denied"
	if strings.Join(ops, ",") != want {
		t.Fatalf("audit = %v, want %s", ops, want)
	}
}

func TestShareTokenIsNotAGatewayCredential(t *testing.T) {
	f := newShareFixture(t)
	token := f.mint(t, "sess-a", "").Token
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v1/sessions"},
		{http.MethodGet, "/v1/sessions/sess-a/events"},
		{http.MethodGet, "/v1/sessions/sess-b/events"},
		{http.MethodGet, "/v1/sessions/sess-a/timeline"},
		{http.MethodGet, "/v1/sessions/sess-a/timeline/export"},
		{http.MethodGet, "/v1/sessions/sess-a/shares"},
		{http.MethodPost, "/v1/sessions/sess-a/shares"},
		{http.MethodGet, "/v1/notifications"},
		{http.MethodPut, "/v1/notifications/prefs"},
		{http.MethodGet, "/v1/settings"},
		{http.MethodGet, "/v1/status"},
		{http.MethodGet, "/v1/workspaces"},
		{http.MethodGet, "/v1/bootstrap"},
		{http.MethodPost, "/v1/ops"},
		{http.MethodGet, "/v1/live/events"},
		{http.MethodGet, "/v1/pair/devices"},
		{http.MethodPost, "/v1/pair"},
		{http.MethodGet, "/v1/audit"},
	}
	for _, rt := range routes {
		for _, via := range []string{"bearer", "cookie"} {
			req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(`{}`))
			if via == "bearer" {
				req.Header.Set("Authorization", "Bearer "+token)
			} else {
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
			}
			res := httptest.NewRecorder()
			f.srv.Handler().ServeHTTP(res, req)
			if res.Code != http.StatusUnauthorized {
				t.Errorf("%s %s via %s = %d, want 401", rt.method, rt.path, via, res.Code)
			}
			if strings.Contains(res.Body.String(), shareMarkerA) || strings.Contains(res.Body.String(), shareMarkerB) {
				t.Errorf("%s %s via %s leaked transcript", rt.method, rt.path, via)
			}
		}
	}
}

func TestShareDeadEndsAreIndistinguishable(t *testing.T) {
	f := newShareFixture(t)
	live := f.mint(t, "sess-a", "")
	expiring := f.mint(t, "sess-a", `{"ttlSeconds":60}`)
	revoked := f.mint(t, "sess-a", "")
	deleted := f.mint(t, "sess-b", "")

	if res := f.call(t, http.MethodDelete, "/v1/sessions/sess-a/shares/"+revoked.ID, "", "secret"); res.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d", res.Code)
	}
	if err := os.Remove(filepath.Join(f.dir, "sess-b.jsonl")); err != nil {
		t.Fatal(err)
	}
	st := f.srv.shares()
	st.mu.Lock()
	st.clock = func() time.Time { return time.Now().Add(2 * time.Minute) }
	st.mu.Unlock()

	if res := f.redeem(t, live.Token); res.Code != http.StatusOK {
		t.Fatalf("live share = %d", res.Code)
	}
	queryReq := httptest.NewRequest(http.MethodPost, "/share/v1/transcript?token="+live.Token, strings.NewReader(`{"token":"`+live.Token+`"}`))
	queryRes := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(queryRes, queryReq)

	cases := map[string]*httptest.ResponseRecorder{
		"expired":           f.redeem(t, expiring.Token),
		"revoked":           f.redeem(t, revoked.Token),
		"session deleted":   f.redeem(t, deleted.Token),
		"unknown":           f.redeem(t, shareTokenPrefix+"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		"empty":             f.redeem(t, ""),
		"gateway token":     f.redeem(t, "secret"),
		"token in query":    queryRes,
		"session parameter": f.call(t, http.MethodPost, "/share/v1/transcript", `{"token":"`+live.Token+`","sessionId":"sess-b"}`, ""),
		"negative offset":   f.call(t, http.MethodPost, "/share/v1/transcript", `{"token":"`+live.Token+`","after":-1}`, ""),
	}
	want := `{"error":"share_unavailable"}`
	for name, res := range cases {
		if res.Code != http.StatusNotFound || strings.TrimSpace(res.Body.String()) != want {
			t.Errorf("%s = %d %q, want 404 %s", name, res.Code, res.Body.String(), want)
		}
	}
}

func TestShareMintValidation(t *testing.T) {
	f := newShareFixture(t)
	cases := []struct {
		name, path, body string
		want             int
	}{
		{"unknown session", "/v1/sessions/nope/shares", "", http.StatusNotFound},
		{"backslash in id", "/v1/sessions/a%5Cb/shares", "", http.StatusBadRequest},
		{"expiry too short", "/v1/sessions/sess-a/shares", `{"ttlSeconds":30}`, http.StatusBadRequest},
		{"expiry too long", "/v1/sessions/sess-a/shares", `{"ttlSeconds":2678400}`, http.StatusBadRequest},
		{"negative expiry", "/v1/sessions/sess-a/shares", `{"ttlSeconds":-5}`, http.StatusBadRequest},
		{"unknown field", "/v1/sessions/sess-a/shares", `{"scope":"all"}`, http.StatusBadRequest},
		{"one hour", "/v1/sessions/sess-a/shares", `{"ttlSeconds":3600}`, http.StatusCreated},
		{"default", "/v1/sessions/sess-a/shares", ``, http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := f.call(t, http.MethodPost, tc.path, tc.body, "secret")
			if res.Code != tc.want {
				t.Fatalf("code = %d %s", res.Code, res.Body.String())
			}
		})
	}
	if res := f.call(t, http.MethodPost, "/v1/sessions/sess-a/shares", "", ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("mint without auth = %d", res.Code)
	}
	if res := f.call(t, http.MethodDelete, "/v1/sessions/sess-a/shares/shr_x", "", ""); res.Code != http.StatusUnauthorized {
		t.Fatalf("revoke without auth = %d", res.Code)
	}

	def := f.mint(t, "sess-a", "")
	created, _ := time.Parse(time.RFC3339, def.CreatedAt)
	expires, _ := time.Parse(time.RFC3339, def.ExpiresAt)
	if got := expires.Sub(created); got != shareDefaultTTL {
		t.Fatalf("default expiry = %v", got)
	}
}

func TestShareMintCapPerSession(t *testing.T) {
	f := newShareFixture(t)
	for i := 0; i < shareMaxPerSession; i++ {
		f.mint(t, "sess-a", "")
	}
	if res := f.call(t, http.MethodPost, "/v1/sessions/sess-a/shares", "", "secret"); res.Code != http.StatusTooManyRequests {
		t.Fatalf("over cap = %d", res.Code)
	}
	// Another session has its own budget.
	f.mint(t, "sess-b", "")
}

func TestShareRedeemRateLimited(t *testing.T) {
	f := newShareFixture(t)
	token := f.mint(t, "sess-a", "").Token
	for i := 0; i < shareRedeemPerMin; i++ {
		f.redeem(t, token)
	}
	if res := f.redeem(t, token); res.Code != http.StatusTooManyRequests {
		t.Fatalf("over limit = %d", res.Code)
	}
}

func TestReadShareChunkPagesCompleteLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	content := `{"type":"session.header","data":{}}` + "\n" +
		`{"type":"user.message","data":{"text":"one"}}` + "\n" +
		"not json\n" +
		`{"type":"user.message","data":{"text":"two"}}` + "\n" +
		`{"type":"user.message","data":{"text":"partial"`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	events, next, more, err := readShareChunk(path, 0, 1<<20)
	if err != nil || more {
		t.Fatalf("err=%v more=%v", err, more)
	}
	if len(events) != 2 || !strings.Contains(string(events[1]), "two") {
		t.Fatalf("events = %s", events)
	}
	if want := int64(strings.LastIndex(content, "\n") + 1); next != want {
		t.Fatalf("next = %d, want %d (stop before the partial line)", next, want)
	}
	// A tiny budget pages one line at a time.
	first, off, more, _ := readShareChunk(path, 0, 1)
	if !more || len(first) != 0 || off == 0 {
		t.Fatalf("page 1 = %s off=%d more=%v", first, off, more)
	}
	second, _, _, _ := readShareChunk(path, off, 1)
	if len(second) != 1 || !strings.Contains(string(second[0]), "one") {
		t.Fatalf("page 2 = %s", second)
	}
	// An offset past the end (rewritten log) starts again.
	again, _, _, _ := readShareChunk(path, 1<<30, 1<<20)
	if len(again) != 2 {
		t.Fatalf("restart = %s", again)
	}
}
