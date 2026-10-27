package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"rsc.io/qr"
)

const pairDaemonToken = "daemon-secret-do-not-leak"

func newPairSrv(t *testing.T, tweak func(*Options)) *Server {
	t.Helper()
	opts := Options{
		Auth:             true,
		Token:            pairDaemonToken,
		Addr:             "127.0.0.1:8787",
		SessionDir:       t.TempDir(),
		PairRedeemLimit:  30,
		PairRedeemGlobal: 100,
	}
	if tweak != nil {
		tweak(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func pairDo(srv *Server, method, target, token string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, target, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	return res
}

func mintCode(t *testing.T, srv *Server) PairMint {
	t.Helper()
	res := pairDo(srv, http.MethodPost, "/v1/pair", pairDaemonToken, map[string]string{"label": "phone"})
	if res.Code != http.StatusCreated {
		t.Fatalf("mint status = %d body=%s", res.Code, res.Body.String())
	}
	var mint PairMint
	if err := json.Unmarshal(res.Body.Bytes(), &mint); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Body.String(), pairDaemonToken) {
		t.Fatalf("mint response contains the daemon token: %s", res.Body.String())
	}
	return mint
}

func TestPairURLIsFragmentAndNotDaemonToken(t *testing.T) {
	srv := newPairSrv(t, nil)
	mint := mintCode(t, srv)
	codeRe := regexp.MustCompile(`^p1\.[A-Za-z0-9_-]{22}$`)
	if !codeRe.MatchString(mint.Code) {
		t.Fatalf("code = %q", mint.Code)
	}
	if strings.Contains(mint.URL, "?") || strings.Contains(mint.URL, pairDaemonToken) {
		t.Fatalf("url = %q", mint.URL)
	}
	if mint.URL != "http://127.0.0.1:8787/#pair="+mint.Code {
		t.Fatalf("url = %q", mint.URL)
	}
	if mint.Gateway.PhoneReachable || !mint.Gateway.Loopback {
		t.Fatalf("gateway = %+v", mint.Gateway)
	}
	if !strings.Contains(mint.Gateway.OptIn, "--allow-private-bind") || !strings.Contains(mint.Gateway.OptIn, "will not bind 0.0.0.0") {
		t.Fatalf("optIn = %q", mint.Gateway.OptIn)
	}
	if strings.Contains(mint.Gateway.OptIn, "0.0.0.0:8787") {
		t.Fatalf("optIn suggests an unspecified bind: %q", mint.Gateway.OptIn)
	}
}

func TestPairRedeemCookieIsSingleUseAndNotDaemonToken(t *testing.T) {
	srv := newPairSrv(t, nil)
	mint := mintCode(t, srv)
	res := pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": mint.Code, "label": "Pixel"})
	if res.Code != http.StatusOK {
		t.Fatalf("redeem status = %d body=%s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	if strings.Contains(body, "sessionToken") || strings.Contains(body, pairDaemonToken) || strings.Contains(body, mint.Code) {
		t.Fatalf("cookie redeem leaked a secret: %s", body)
	}
	var cookie *http.Cookie
	for _, c := range res.Result().Cookies() {
		if c.Name == authCookieName {
			cookie = c
		}
	}
	if cookie == nil || !strings.HasPrefix(cookie.Value, "s1.") || cookie.Value == pairDaemonToken {
		t.Fatalf("cookie = %#v", cookie)
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge <= 0 {
		t.Fatalf("cookie flags HttpOnly=%v SameSite=%v Path=%q MaxAge=%d", cookie.HttpOnly, cookie.SameSite, cookie.Path, cookie.MaxAge)
	}
	if strings.Contains(body, cookie.Value) {
		t.Fatal("response body contains the session token")
	}
	api := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
	req.AddCookie(cookie)
	srv.Handler().ServeHTTP(api, req)
	if api.Code != http.StatusOK {
		t.Fatalf("session cookie status = %d", api.Code)
	}

	again := pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": mint.Code})
	if again.Code != http.StatusGone || !strings.Contains(again.Body.String(), "code_used") {
		t.Fatalf("replay status = %d body=%s", again.Code, again.Body.String())
	}
	if strings.Contains(again.Body.String(), mint.Code) || strings.Contains(again.Body.String(), cookie.Value) {
		t.Fatal("replay echoed a secret")
	}
}

func TestPairDeliveryTokenAndRevoke(t *testing.T) {
	srv := newPairSrv(t, nil)
	mint := mintCode(t, srv)
	res := pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{
		"code": mint.Code, "delivery": "token", "label": "native",
	})
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	var body struct {
		DeviceID     string `json:"deviceId"`
		SessionToken string `json:"sessionToken"`
		Delivery     string `json:"delivery"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Delivery != "token" || !strings.HasPrefix(body.SessionToken, "s1.") || body.SessionToken == pairDaemonToken {
		t.Fatalf("body = %+v", body)
	}
	if strings.Contains(res.Body.String(), pairDaemonToken) {
		t.Fatal("daemon token in redeem body")
	}
	if pairDo(srv, http.MethodGet, "/v1/bootstrap", body.SessionToken, nil).Code != http.StatusOK {
		t.Fatal("session bearer rejected")
	}
	if res := pairDo(srv, http.MethodPost, "/v1/pair", body.SessionToken, map[string]string{}); res.Code != http.StatusForbidden {
		t.Fatalf("session mint status = %d", res.Code)
	}
	listed := pairDo(srv, http.MethodGet, "/v1/pair/devices", pairDaemonToken, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d", listed.Code)
	}
	if strings.Contains(listed.Body.String(), body.SessionToken) || strings.Contains(listed.Body.String(), mint.Code) || strings.Contains(listed.Body.String(), pairDaemonToken) {
		t.Fatalf("device list leaked a secret: %s", listed.Body.String())
	}
	if !strings.Contains(listed.Body.String(), body.DeviceID) || !strings.Contains(listed.Body.String(), "native") {
		t.Fatalf("list = %s", listed.Body.String())
	}
	if pairDo(srv, http.MethodGet, "/v1/pair/devices", body.SessionToken, nil).Code != http.StatusUnauthorized {
		t.Fatal("session must not list devices")
	}
	gone := pairDo(srv, http.MethodDelete, "/v1/pair/devices/"+body.DeviceID, pairDaemonToken, nil)
	if gone.Code != http.StatusOK {
		t.Fatalf("revoke status = %d", gone.Code)
	}
	if pairDo(srv, http.MethodGet, "/v1/bootstrap", body.SessionToken, nil).Code != http.StatusUnauthorized {
		t.Fatal("revoked session still accepted")
	}
	if pairDo(srv, http.MethodGet, "/v1/bootstrap", pairDaemonToken, nil).Code != http.StatusOK {
		t.Fatal("daemon token rejected after revoke")
	}
}

func TestPairCodeExpires(t *testing.T) {
	cur := time.Date(2026, 10, 26, 7, 0, 0, 0, time.UTC)
	srv := newPairSrv(t, func(o *Options) {
		o.PairClock = func() time.Time { return cur }
		o.PairTTL = 5 * time.Minute
	})
	mint := mintCode(t, srv)
	cur = cur.Add(6 * time.Minute)
	res := pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": mint.Code})
	if res.Code != http.StatusGone || !strings.Contains(res.Body.String(), "code_expired") {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), mint.Code) {
		t.Fatal("expiry echoed the code")
	}
}

func TestPairRedeemRateLimit(t *testing.T) {
	cur := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	srv := newPairSrv(t, func(o *Options) {
		o.PairClock = func() time.Time { return cur }
		o.PairRedeemLimit = 2
		o.PairRedeemGlobal = 50
	})
	code := "p1.not-a-real-code-value"
	for i := 0; i < 2; i++ {
		res := pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": code})
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d body=%s", i, res.Code, res.Body.String())
		}
		if strings.Contains(res.Body.String(), code) {
			t.Fatal("error echoed the code")
		}
	}
	res := pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": code})
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if res.Header().Get("Retry-After") != "60" || strings.Contains(res.Body.String(), code) {
		t.Fatalf("limited body/header: %s %q", res.Header().Get("Retry-After"), res.Body.String())
	}
}

func TestPairQuerySecretDoesNotConsumeCode(t *testing.T) {
	srv := newPairSrv(t, nil)
	mint := mintCode(t, srv)
	res := pairDo(srv, http.MethodPost, "/v1/pair/redeem?code="+mint.Code, "", map[string]string{"code": mint.Code})
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "query_secret") {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), mint.Code) {
		t.Fatal("query rejection echoed the code")
	}
	ok := pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": mint.Code})
	if ok.Code != http.StatusOK {
		t.Fatalf("body redeem after query reject = %d body=%s", ok.Code, ok.Body.String())
	}
	q := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/bootstrap?code="+mint.Code, nil)
	req.Header.Set("Authorization", "Bearer "+pairDaemonToken)
	srv.Handler().ServeHTTP(q, req)
	if q.Code != http.StatusUnauthorized {
		t.Fatalf("query code on /v1 status = %d", q.Code)
	}
}

func TestPairDaemonTokenIsNotACode(t *testing.T) {
	srv := newPairSrv(t, nil)
	res := pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": pairDaemonToken})
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.Code)
	}
	if len(res.Result().Cookies()) != 0 {
		t.Fatal("daemon token must not set a session cookie")
	}
	if strings.Contains(res.Body.String(), pairDaemonToken) {
		t.Fatal("response echoed the daemon token")
	}
}

func TestPairTokenFileRotationRevokesSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.token")
	if err := os.WriteFile(path, []byte("old-daemon-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := New(Options{
		Auth:             true,
		Token:            "old-daemon-token",
		TokenFile:        path,
		Addr:             "127.0.0.1:8787",
		SessionDir:       t.TempDir(),
		PairRedeemLimit:  20,
		PairRedeemGlobal: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := pairDo(srv, http.MethodPost, "/v1/pair", "old-daemon-token", map[string]string{"label": "phone"})
	if res.Code != http.StatusCreated {
		t.Fatalf("mint = %d %s", res.Code, res.Body.String())
	}
	var mint PairMint
	if err := json.Unmarshal(res.Body.Bytes(), &mint); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(mint.URL, "old-daemon-token") {
		t.Fatal(mint.URL)
	}
	redeemed := pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": mint.Code, "delivery": "token"})
	var session struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.Unmarshal(redeemed.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new-daemon-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pairDo(srv, http.MethodGet, "/v1/bootstrap", session.SessionToken, nil).Code != http.StatusUnauthorized {
		t.Fatal("session survived token rotation")
	}
	if pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": mint.Code}).Code == http.StatusOK {
		t.Fatal("code survived token rotation")
	}
	if pairDo(srv, http.MethodGet, "/v1/bootstrap", "old-daemon-token", nil).Code != http.StatusUnauthorized {
		t.Fatal("old daemon token still accepted")
	}
	if pairDo(srv, http.MethodGet, "/v1/bootstrap", "new-daemon-token", nil).Code != http.StatusOK {
		t.Fatal("rotated daemon token rejected")
	}
}

func TestPairParallelRedeemIsSingleUse(t *testing.T) {
	srv := newPairSrv(t, nil)
	mint := mintCode(t, srv)
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res := pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": mint.Code, "delivery": "token"})
			codes[i] = res.Code
		}(i)
	}
	close(start)
	wg.Wait()
	got := map[int]int{}
	for _, c := range codes {
		got[c]++
	}
	if got[http.StatusOK] != 1 || got[http.StatusGone] != 1 {
		t.Fatalf("statuses = %v", codes)
	}
}

func TestPairReachability(t *testing.T) {
	lan := newPairSrv(t, func(o *Options) { o.Addr = "192.168.1.20:8787" })
	mint := mintCode(t, lan)
	if !mint.Gateway.PhoneReachable || !mint.Gateway.LAN || mint.Gateway.LanWarning == "" {
		t.Fatalf("lan gateway = %+v", mint.Gateway)
	}
	if !strings.HasPrefix(mint.URL, "http://192.168.1.20:8787/#pair=") || strings.Contains(mint.URL, pairDaemonToken) {
		t.Fatalf("url = %q", mint.URL)
	}

	tun := newPairSrv(t, func(o *Options) { o.AdvertiseURL = "https://gw.example.test/strike" })
	mint = mintCode(t, tun)
	if !strings.HasPrefix(mint.URL, "https://gw.example.test/strike/#pair=") {
		t.Fatalf("tunnel url = %q", mint.URL)
	}
	if mint.Gateway.AdvertiseWarning == "" || !mint.Gateway.Loopback {
		t.Fatalf("gateway = %+v", mint.Gateway)
	}
	var bindURL string
	for _, u := range mint.Gateway.URLs {
		if u.Kind == "bind" {
			bindURL = u.URL
		}
	}
	if bindURL != "http://127.0.0.1:8787/" {
		t.Fatalf("bind url = %q", bindURL)
	}

	for _, raw := range []string{"javascript:alert(1)", "http://user:pass@host", "https://host/?token=abc", "https://host/#pair=x"} {
		_, err := New(Options{Auth: true, Token: "t", SessionDir: t.TempDir(), AdvertiseURL: raw})
		if err == nil {
			t.Fatalf("accepted advertise %q", raw)
		}
	}
}

func TestPairAuthDisabled(t *testing.T) {
	srv, err := New(Options{Addr: "127.0.0.1:8787", SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	res := pairDo(srv, http.MethodPost, "/v1/pair", "", map[string]string{})
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "auth_disabled") {
		t.Fatalf("status = %d body=%s", res.Code, res.Body.String())
	}
	if strings.Contains(res.Body.String(), "p1.") || strings.Contains(res.Body.String(), "?token=") {
		t.Fatalf("unauth mint leaked a code: %s", res.Body.String())
	}
	gw := pairDo(srv, http.MethodGet, "/v1/gateway", "", nil)
	if gw.Code != http.StatusOK || !strings.Contains(gw.Body.String(), "A phone cannot open") {
		t.Fatalf("gateway = %d %s", gw.Code, gw.Body.String())
	}
}

func TestPairRevokeSelfAndAll(t *testing.T) {
	srv := newPairSrv(t, nil)
	mint := mintCode(t, srv)
	res := pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": mint.Code, "delivery": "token"})
	var body struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	self := pairDo(srv, http.MethodPost, "/v1/pair/revoke-self", body.SessionToken, map[string]string{})
	if self.Code != http.StatusOK {
		t.Fatalf("self status = %d %s", self.Code, self.Body.String())
	}
	if pairDo(srv, http.MethodGet, "/v1/bootstrap", body.SessionToken, nil).Code != http.StatusUnauthorized {
		t.Fatal("self-revoked session still works")
	}
	if pairDo(srv, http.MethodPost, "/v1/pair/revoke-self", pairDaemonToken, map[string]string{}).Code != http.StatusBadRequest {
		t.Fatal("daemon token revoke-self should ask for device revoke")
	}

	mint = mintCode(t, srv)
	res = pairDo(srv, http.MethodPost, "/v1/pair/redeem", "", map[string]string{"code": mint.Code, "delivery": "token"})
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	all := pairDo(srv, http.MethodPost, "/v1/pair/revoke-all", pairDaemonToken, map[string]string{})
	if all.Code != http.StatusOK {
		t.Fatalf("revoke-all = %d", all.Code)
	}
	if pairDo(srv, http.MethodGet, "/v1/bootstrap", body.SessionToken, nil).Code != http.StatusUnauthorized {
		t.Fatal("revoke-all left a session")
	}
}

func TestMatchHashScansWholeSlice(t *testing.T) {
	hashes := [][32]byte{
		sha256Bytes("p1.aaaaaaaaaaaaaaaaaaaaaa"),
		sha256Bytes("p1.bbbbbbbbbbbbbbbbbbbbbb"),
	}
	idx, ok := matchHash("p1.bbbbbbbbbbbbbbbbbbbbbb", hashes)
	if !ok || idx != 1 {
		t.Fatalf("idx=%d ok=%v", idx, ok)
	}
	if _, ok := matchHash("p1.bbbbbbbbbbbbbbbbbbbbb", hashes); ok {
		t.Fatal("prefix matched")
	}
	if _, ok := matchHash("p1.bbbbbbbbbbbbbbbbbbbbbbEXTRA", hashes); ok {
		t.Fatal("longer string matched")
	}
	if _, ok := matchHash("", hashes); ok {
		t.Fatal("empty matched")
	}
	if _, ok := matchHash("p1.bbbbbbbbbbbbbbbbbbbbbb", nil); ok {
		t.Fatal("empty store matched")
	}
}

func sha256Bytes(s string) [32]byte {
	return sha256.Sum256([]byte(s))
}

func TestQRBlocksDrawsFinder(t *testing.T) {
	text := "http://127.0.0.1:8787/#pair=p1.examplecode"
	blocks := QRBlocks(text)
	if blocks == "" || (!strings.Contains(blocks, "█") && !strings.Contains(blocks, "▀") && !strings.Contains(blocks, "▄")) {
		t.Fatalf("qr blocks missing: %q", blocks)
	}
	if strings.Contains(blocks, pairDaemonToken) || strings.Contains(blocks, "?token=") {
		t.Fatalf("qr rendered a token: %q", blocks)
	}
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		t.Fatal(err)
	}
	if !code.Black(0, 0) {
		t.Fatal("expected a dark finder module at 0,0")
	}
}
