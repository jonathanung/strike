package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// RFC 8291 Section 5 / Appendix A.
const (
	rfcPlaintext = "When I grow up, I want to be a watermelon"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcASPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcBody      = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decodeB64Any(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

func TestEncryptWebPushMatchesRFC8291Vector(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(asPriv.PublicKey().Bytes()); got != rfcASPublic {
		t.Fatalf("as_public = %s", got)
	}
	body, err := encryptWebPush(mustB64(t, rfcUAPublic), mustB64(t, rfcAuth), []byte(rfcPlaintext), asPriv, mustB64(t, rfcSalt))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(body); got != rfcBody {
		t.Fatalf("body =\n%s\nwant\n%s", got, rfcBody)
	}
	// The receiver side recovers the plaintext.
	uaPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decryptWebPushForTest(uaPriv, mustB64(t, rfcAuth), body)
	if err != nil || string(plain) != rfcPlaintext {
		t.Fatalf("decrypt = %q %v", plain, err)
	}
}

func TestEncryptWebPushRejectsBadInput(t *testing.T) {
	asPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	ua := mustB64(t, rfcUAPublic)
	auth := mustB64(t, rfcAuth)
	salt := mustB64(t, rfcSalt)
	cases := []struct {
		name           string
		ua, auth, salt []byte
		plain          []byte
	}{
		{"short auth", ua, auth[:8], salt, []byte("x")},
		{"short salt", ua, auth, salt[:4], []byte("x")},
		{"not a point", append([]byte{4}, make([]byte, 64)...), auth, salt, []byte("x")},
		{"too large", ua, auth, salt, make([]byte, webPushRecordSize)},
	}
	for _, tc := range cases {
		if _, err := encryptWebPush(tc.ua, tc.auth, tc.plain, asPriv, tc.salt); err == nil {
			t.Errorf("%s: want error", tc.name)
		}
	}
}

// decryptWebPushForTest is the user-agent side of RFC 8291 (single record).
func decryptWebPushForTest(uaPriv *ecdh.PrivateKey, auth, body []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, fmt.Errorf("short body")
	}
	salt := body[:16]
	idlen := int(body[20])
	if len(body) < 21+idlen {
		return nil, fmt.Errorf("short header")
	}
	if rs := binary.BigEndian.Uint32(body[16:20]); rs != webPushRecordSize {
		return nil, fmt.Errorf("record size %d", rs)
	}
	asPublic := body[21 : 21+idlen]
	asKey, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		return nil, err
	}
	secret, err := uaPriv.ECDH(asKey)
	if err != nil {
		return nil, err
	}
	uaPublic := uaPriv.PublicKey().Bytes()
	prkKey := hmacSHA256(auth, secret)
	info := append([]byte("WebPush: info\x00"), uaPublic...)
	info = append(info, asPublic...)
	ikm := hmacSHA256(prkKey, info, []byte{1})
	prk := hmacSHA256(salt, ikm)
	cek := hmacSHA256(prk, []byte("Content-Encoding: aes128gcm\x00"), []byte{1})[:16]
	nonce := hmacSHA256(prk, []byte("Content-Encoding: nonce\x00"), []byte{1})[:12]
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		return nil, err
	}
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		return nil, fmt.Errorf("missing last-record delimiter")
	}
	return plain[:len(plain)-1], nil
}

func TestVAPIDAuthorizationVerifies(t *testing.T) {
	key, err := newVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	header, err := vapidAuthorization(key, "https://push.example.net:8443/push/abc?x=1", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(header, "vapid t=") {
		t.Fatalf("header = %s", header)
	}
	parts := strings.SplitN(strings.TrimPrefix(header, "vapid t="), ", k=", 2)
	if len(parts) != 2 {
		t.Fatalf("header = %s", header)
	}
	pub, _ := key.PublicKey.Bytes()
	if parts[1] != b64.EncodeToString(pub) {
		t.Fatalf("k = %s", parts[1])
	}
	jwt := strings.Split(parts[0], ".")
	if len(jwt) != 3 {
		t.Fatalf("jwt = %s", parts[0])
	}
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(mustB64(t, jwt[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Aud != "https://push.example.net:8443" || claims.Sub == "" {
		t.Fatalf("claims = %+v", claims)
	}
	if exp := time.Unix(claims.Exp, 0); !exp.After(now) || exp.Sub(now) > 24*time.Hour {
		t.Fatalf("exp = %v", exp)
	}
	sig := mustB64(t, jwt[2])
	if len(sig) != 64 {
		t.Fatalf("sig len = %d", len(sig))
	}
	digest := sha256.Sum256([]byte(jwt[0] + "." + jwt[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	verifyKey, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub)
	if err != nil {
		t.Fatal(err)
	}
	if !ecdsa.Verify(verifyKey, digest[:], r, s) {
		t.Fatal("VAPID signature does not verify")
	}
}

// pushEndpoint is a stand-in push service. Tests never call a real one.
type pushEndpoint struct {
	mu     sync.Mutex
	status int
	got    []pushRequest
	srv    *httptest.Server
}

type pushRequest struct {
	header http.Header
	body   []byte
}

func newPushEndpoint(t *testing.T) *pushEndpoint {
	e := &pushEndpoint{status: http.StatusCreated}
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.got = append(e.got, pushRequest{header: r.Header.Clone(), body: body})
		code := e.status
		e.mu.Unlock()
		w.WriteHeader(code)
	}))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *pushEndpoint) requests() []pushRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]pushRequest(nil), e.got...)
}

type pushBrowser struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newPushBrowser(t *testing.T) pushBrowser {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return pushBrowser{priv: priv, auth: auth}
}

func (b pushBrowser) subscribeBody(endpoint string) string {
	out, _ := json.Marshal(map[string]any{
		"endpoint":       endpoint,
		"expirationTime": nil,
		"keys": map[string]string{
			"p256dh": b64.EncodeToString(b.priv.PublicKey().Bytes()),
			"auth":   b64.EncodeToString(b.auth),
		},
	})
	return string(out)
}

func newPushServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	srv := mustServer(t, Options{SessionDir: dir})
	srv.pushes().allowHTTP = true
	if got := notifyCall(t, srv, http.MethodGet, "/v1/notifications", ""); got.code != http.StatusOK {
		t.Fatalf("init = %d", got.code)
	}
	return srv, dir
}

func TestPushSendsGenericPayloadForNeedsYouKinds(t *testing.T) {
	srv, dir := newPushServer(t)
	endpoint := newPushEndpoint(t)
	browser := newPushBrowser(t)

	// History before subscribing never pushes.
	appendSessionLine(t, dir, "root1", `{"type":"permission.asked","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","requestId":"root1:perm_old","permission":"bash"}}`)
	if res := notifyCall(t, srv, http.MethodPost, "/v1/notifications/push-subscriptions", browser.subscribeBody(endpoint.srv.URL+"/push/1")); res.code != http.StatusCreated {
		t.Fatalf("subscribe = %d %s", res.code, res.raw)
	}
	srv.pushTick()
	if n := len(endpoint.requests()); n != 0 {
		t.Fatalf("backlog pushed %d times", n)
	}

	const secretName = "SECRET_CHILD_NAME_7731"
	const secretSummary = "SECRET_SUMMARY_TEXT_4410"
	appendSessionLine(t, dir, "root1", `{"type":"permission.asked","time":"2026-10-26T12:01:00Z","data":{"sessionId":"root1","requestId":"root1:perm_2","permission":"bash","patterns":["`+notifySecret+`"]}}`)
	appendSessionLine(t, dir, "root1", `{"type":"child.completed","time":"2026-10-26T12:02:00Z","data":{"sessionId":"child-9","parentSessionId":"root1","status":"blocked","name":"`+secretName+`","summary":"`+secretSummary+`"}}`)
	// A finished run is not a needs-you kind.
	appendSessionLine(t, dir, "root1", `{"type":"turn.completed","time":"2026-10-26T12:03:00Z","data":{"sessionId":"root1","turnId":"t9","stopReason":"end_turn"}}`)
	srv.pushTick()

	reqs := endpoint.requests()
	if len(reqs) != 2 {
		t.Fatalf("pushes = %d, want 2 (approval + blocked)", len(reqs))
	}
	var kinds []string
	for _, req := range reqs {
		for _, h := range []string{"TTL", "Urgency"} {
			if req.header.Get(h) == "" {
				t.Fatalf("missing %s header", h)
			}
		}
		if req.header.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(req.header.Get("Authorization"), "vapid t=") {
			t.Fatalf("headers = %v", req.header)
		}
		raw := string(req.body)
		for _, banned := range []string{secretName, secretSummary, notifySecret, "bash"} {
			if strings.Contains(raw, banned) {
				t.Fatalf("ciphertext carries %q", banned)
			}
		}
		plain, err := decryptWebPushForTest(browser.priv, browser.auth, req.body)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{secretName, secretSummary, notifySecret, "bash", "title", "body"} {
			if strings.Contains(string(plain), banned) {
				t.Fatalf("payload %s carries %q", plain, banned)
			}
		}
		var p pushPayload
		if err := json.Unmarshal(plain, &p); err != nil {
			t.Fatal(err)
		}
		if p.Type != "notify" || !strings.HasPrefix(p.Href, "/#session=root1") || p.Tag == "" {
			t.Fatalf("payload = %+v", p)
		}
		kinds = append(kinds, p.Kind)
	}
	if strings.Join(kinds, ",") != noteApproval+","+noteChildBlocked {
		t.Fatalf("kinds = %v", kinds)
	}

	// Nothing new, nothing sent; status reports the last result.
	srv.pushTick()
	if len(endpoint.requests()) != 2 {
		t.Fatal("re-sent old rows")
	}
	body := notifyBody(t, srv)
	if body.Push.Subscriptions != 1 || body.Push.LastResult != "ok" || body.Push.LastAt == "" {
		t.Fatalf("push status = %+v", body.Push)
	}
}

func TestPushRespectsPrefsAndPrunesDeadSubscriptions(t *testing.T) {
	srv, dir := newPushServer(t)
	endpoint := newPushEndpoint(t)
	browser := newPushBrowser(t)
	if res := notifyCall(t, srv, http.MethodPost, "/v1/notifications/push-subscriptions", browser.subscribeBody(endpoint.srv.URL+"/push/1")); res.code != http.StatusCreated {
		t.Fatalf("subscribe = %d %s", res.code, res.raw)
	}
	steps := []struct {
		name  string
		prefs string
		want  int
	}{
		{"disabled", `{"enabled":false,"quiet":{"enabled":false,"start":"22:00","end":"07:00"},"mutedSessions":[]}`, 0},
		{"muted session", `{"enabled":true,"quiet":{"enabled":false,"start":"22:00","end":"07:00"},"mutedSessions":["root1"]}`, 0},
		{"quiet all day", `{"enabled":true,"quiet":{"enabled":true,"start":"00:00","end":"00:00"},"mutedSessions":[]}`, 0},
		{"allowed", `{"enabled":true,"quiet":{"enabled":false,"start":"22:00","end":"07:00"},"mutedSessions":[]}`, 1},
	}
	sent := 0
	for i, step := range steps {
		if res := notifyCall(t, srv, http.MethodPut, "/v1/notifications/prefs", step.prefs); res.code != http.StatusOK {
			t.Fatalf("%s: prefs = %d", step.name, res.code)
		}
		appendSessionLine(t, dir, "root1", fmt.Sprintf(`{"type":"permission.asked","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","requestId":"root1:perm_%d","permission":"edit"}}`, i))
		srv.pushTick()
		sent += step.want
		if got := len(endpoint.requests()); got != sent {
			t.Fatalf("%s: pushes = %d, want %d", step.name, got, sent)
		}
	}

	// A push service error is recorded, the subscription kept.
	endpoint.mu.Lock()
	endpoint.status = http.StatusInternalServerError
	endpoint.mu.Unlock()
	appendSessionLine(t, dir, "root1", `{"type":"permission.asked","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","requestId":"root1:perm_err","permission":"edit"}}`)
	srv.pushTick()
	if st := notifyBody(t, srv).Push; st.LastResult != "failed" || st.Subscriptions != 1 {
		t.Fatalf("after 500 = %+v", st)
	}

	// 410 Gone prunes it.
	endpoint.mu.Lock()
	endpoint.status = http.StatusGone
	endpoint.mu.Unlock()
	appendSessionLine(t, dir, "root1", `{"type":"permission.asked","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","requestId":"root1:perm_gone","permission":"edit"}}`)
	srv.pushTick()
	if st := notifyBody(t, srv).Push; st.Subscriptions != 0 {
		t.Fatalf("410 did not prune: %+v", st)
	}
}

func TestPushSubscriptionRoutes(t *testing.T) {
	srv, dir := newPushServer(t)
	browser := newPushBrowser(t)

	key := notifyCall(t, srv, http.MethodGet, "/v1/notifications/push-key", "")
	var kr struct {
		PublicKey string `json:"publicKey"`
	}
	if key.code != http.StatusOK || json.Unmarshal([]byte(key.raw), &kr) != nil {
		t.Fatalf("push-key = %d %s", key.code, key.raw)
	}
	if pub := mustB64(t, kr.PublicKey); len(pub) != 65 || pub[0] != 4 {
		t.Fatalf("public key = %x", pub)
	}
	// The private key lives outside the session dir, 0600, and is stable.
	vapid := filepath.Join(filepath.Dir(dir), "push", "vapid.json")
	info, err := os.Stat(vapid)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("vapid.json = %v %v", info, err)
	}
	if again := notifyCall(t, srv, http.MethodGet, "/v1/notifications/push-key", ""); !strings.Contains(again.raw, kr.PublicKey) {
		t.Fatal("public key changed between calls")
	}

	good := browser.subscribeBody("https://push.example.net/sub/1")
	cases := []struct {
		name string
		body string
		want int
	}{
		{"valid", good, http.StatusCreated},
		{"same endpoint refreshes", good, http.StatusCreated},
		{"plain http refused", browser.subscribeBody("http://push.example.net/sub/2"), http.StatusBadRequest},
		{"not a url", browser.subscribeBody("javascript:alert(1)"), http.StatusBadRequest},
		{"bad p256dh", `{"endpoint":"https://push.example.net/sub/3","keys":{"p256dh":"AAAA","auth":"` + b64.EncodeToString(browser.auth) + `"}}`, http.StatusBadRequest},
		{"bad auth", `{"endpoint":"https://push.example.net/sub/3","keys":{"p256dh":"` + b64.EncodeToString(browser.priv.PublicKey().Bytes()) + `","auth":"AA"}}`, http.StatusBadRequest},
		{"unknown field", `{"endpoint":"https://push.example.net/sub/3","keys":{},"extra":1}`, http.StatusBadRequest},
	}
	srv.pushes().allowHTTP = false
	for _, tc := range cases {
		if res := notifyCall(t, srv, http.MethodPost, "/v1/notifications/push-subscriptions", tc.body); res.code != tc.want {
			t.Errorf("%s = %d %s", tc.name, res.code, res.raw)
		}
	}
	if st := notifyBody(t, srv).Push; st.Subscriptions != 1 {
		t.Fatalf("subscriptions = %d, want 1", st.Subscriptions)
	}
	// The endpoint is never echoed back in the centre response.
	if body := notifyCall(t, srv, http.MethodGet, "/v1/notifications", ""); strings.Contains(body.raw, "push.example.net") {
		t.Fatalf("endpoint leaked: %s", body.raw)
	}

	for i := 2; i <= pushMaxSubs; i++ {
		notifyCall(t, srv, http.MethodPost, "/v1/notifications/push-subscriptions", browser.subscribeBody(fmt.Sprintf("https://push.example.net/sub/x%d", i)))
	}
	if res := notifyCall(t, srv, http.MethodPost, "/v1/notifications/push-subscriptions", browser.subscribeBody("https://push.example.net/sub/over")); res.code != http.StatusTooManyRequests {
		t.Fatalf("over cap = %d", res.code)
	}

	del := notifyCall(t, srv, http.MethodDelete, "/v1/notifications/push-subscriptions", `{"endpoint":"https://push.example.net/sub/1"}`)
	if del.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", del.code, del.raw)
	}
	if again := notifyCall(t, srv, http.MethodDelete, "/v1/notifications/push-subscriptions", `{"endpoint":"https://push.example.net/sub/1"}`); again.code != http.StatusNotFound {
		t.Fatalf("second delete = %d", again.code)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "notifications", "push.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(filepath.Join(dir, "notifications", "push.json")); info.Mode().Perm() != 0o600 {
		t.Fatalf("push.json mode = %o", info.Mode().Perm())
	}
	if strings.Contains(string(stored), "sub/1\"") {
		t.Fatalf("deleted endpoint still stored: %s", stored)
	}
}

func TestPushRoutesRequireAuth(t *testing.T) {
	srv := mustServer(t, Options{Auth: true, Token: "t", SessionDir: t.TempDir()})
	for _, rt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/notifications/push-key"},
		{http.MethodPost, "/v1/notifications/push-subscriptions"},
		{http.MethodDelete, "/v1/notifications/push-subscriptions"},
	} {
		res := notifyCall(t, srv, rt.method, rt.path, `{}`)
		if res.code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d", rt.method, rt.path, res.code)
		}
	}
}

func TestBuildPushPayloadIsGeneric(t *testing.T) {
	n := Notification{
		ID: "abc123", Kind: noteChildBlocked, SessionID: "root1", Title: "SECRET title", Body: "SECRET body",
		Href: "/#session=root1",
	}
	b, ok := buildPushPayload(n, NotifyPrefs{})
	if !ok || strings.Contains(string(b), "SECRET") {
		t.Fatalf("payload = %s ok=%v", b, ok)
	}
	if _, ok := buildPushPayload(Notification{Kind: noteSessionFinished}, NotifyPrefs{}); ok {
		t.Fatal("finished runs must not push by default")
	}
	odd, _ := buildPushPayload(Notification{Kind: noteApproval, Href: "https://evil.example/"}, NotifyPrefs{})
	if !strings.Contains(string(odd), `"href":"/"`) {
		t.Fatalf("off-site href kept: %s", odd)
	}
}

func TestPushClientDoesNotFollowRedirects(t *testing.T) {
	var hitTarget bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitTarget = true
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)

	srv, _ := newPushServer(t)
	browser := newPushBrowser(t)
	p := srv.pushes()
	sub := pushSub{
		ID:       "push_x",
		Endpoint: redirect.URL + "/push",
		P256dh:   b64.EncodeToString(browser.priv.PublicKey().Bytes()),
		Auth:     b64.EncodeToString(browser.auth),
	}
	code, err := p.deliver(sub, []byte(`{"type":"notify"}`))
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusTemporaryRedirect || hitTarget {
		t.Fatalf("code = %d, followed = %v", code, hitTarget)
	}
}

func TestPushableKindsFollowFinishedPref(t *testing.T) {
	off, on := NotifyPrefs{}, NotifyPrefs{PushFinished: true}
	cases := []struct {
		kind    string
		off, on bool
	}{
		{noteApproval, true, true},
		{noteChildBlocked, true, true},
		{noteSessionFinished, false, true},
		{noteChildFinished, false, true},
		{noteScriptFinished, false, false},
		{noteScriptFailed, false, false},
		{"gateway.disconnected", false, false},
	}
	for _, tc := range cases {
		if got := pushable(tc.kind, off); got != tc.off {
			t.Errorf("%s with pushFinished off = %v, want %v", tc.kind, got, tc.off)
		}
		if got := pushable(tc.kind, on); got != tc.on {
			t.Errorf("%s with pushFinished on = %v, want %v", tc.kind, got, tc.on)
		}
	}
	n := Notification{ID: "fin1", Kind: noteSessionFinished, Title: "SECRET title", Body: "SECRET body", Href: "/#session=root1"}
	b, ok := buildPushPayload(n, on)
	if !ok {
		t.Fatal("opted-in finished run did not build a payload")
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got["kind"] != noteSessionFinished || got["href"] != "/#session=root1" || got["tag"] != "fin1" || got["type"] != "notify" {
		t.Fatalf("payload not generic: %s", b)
	}
}

func TestPushFinishedRunsOnlyWhenOptedIn(t *testing.T) {
	srv, dir := newPushServer(t)
	endpoint := newPushEndpoint(t)
	browser := newPushBrowser(t)
	if res := notifyCall(t, srv, http.MethodPost, "/v1/notifications/push-subscriptions", browser.subscribeBody(endpoint.srv.URL+"/push/1")); res.code != http.StatusCreated {
		t.Fatalf("subscribe = %d %s", res.code, res.raw)
	}
	steps := []struct {
		name  string
		prefs string
		want  int
	}{
		{"default off", `{"enabled":true,"quiet":{"enabled":false,"start":"22:00","end":"07:00"},"mutedSessions":[]}`, 0},
		{"opted in", `{"enabled":true,"quiet":{"enabled":false,"start":"22:00","end":"07:00"},"mutedSessions":[],"pushFinished":true}`, 1},
		{"opted out again", `{"enabled":true,"quiet":{"enabled":false,"start":"22:00","end":"07:00"},"mutedSessions":[],"pushFinished":false}`, 0},
	}
	sent := 0
	for i, step := range steps {
		res := notifyCall(t, srv, http.MethodPut, "/v1/notifications/prefs", step.prefs)
		if res.code != http.StatusOK {
			t.Fatalf("%s: prefs = %d", step.name, res.code)
		}
		if want := step.want == 1; !strings.Contains(res.raw, fmt.Sprintf(`"pushFinished":%v`, want)) {
			t.Fatalf("%s: prefs echo = %s", step.name, res.raw)
		}
		appendSessionLine(t, dir, "root1", fmt.Sprintf(`{"type":"turn.completed","time":"2026-10-26T12:00:00Z","data":{"sessionId":"root1","turnId":"turn-%d","stopReason":"end_turn"}}`, i))
		srv.pushTick()
		sent += step.want
		if got := len(endpoint.requests()); got != sent {
			t.Fatalf("%s: pushes = %d, want %d", step.name, got, sent)
		}
	}
	if body := notifyBody(t, srv); body.Prefs.PushFinished {
		t.Fatal("pushFinished should read back off")
	}
}
