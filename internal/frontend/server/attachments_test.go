package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/attachment"
	"github.com/jonathanung/strike/pkg/protocol"
)

const attachmentSecretName = "notes-sk-abcdefghijklmnopqrstuvwxyz.png"

func openAttachmentStore(t *testing.T) *attachment.Store {
	t.Helper()
	s, err := attachment.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func solidPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func postAttachment(t *testing.T, srv *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/attachments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if srv.opts.Auth {
		req.Header.Set("Authorization", "Bearer secret")
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	return res
}

func TestAttachmentIngestStoresRedactsAndOmitsBytes(t *testing.T) {
	store := openAttachmentStore(t)
	ops := make(chan protocol.Op, 1)
	live := NewLive("sess-att", t.TempDir(), nil, ops)
	defer live.Close()
	srv := mustServer(t, Options{Auth: true, Token: "secret", SessionDir: t.TempDir(), Live: live, Attachments: store})

	raw := solidPNG(t, color.RGBA{R: 255, A: 255})
	payload, _ := json.Marshal(map[string]any{
		"name":      attachmentSecretName,
		"mime":      "image/png",
		"data":      base64.StdEncoding.EncodeToString(raw),
		"sessionId": "sess-att",
	})
	res := postAttachment(t, srv, string(payload))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", res.Code, res.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["data"]; ok {
		t.Fatalf("response included bytes: %s", res.Body.String())
	}
	name, _ := got["name"].(string)
	if strings.Contains(name, "sk-abcdefghijklmnopqrstuvwxyz") || !strings.Contains(name, "[REDACTED_API_KEY]") {
		t.Fatalf("name = %q", name)
	}
	ref, _ := got["ref"].(string)
	if !strings.HasPrefix(ref, "att:sha256:") {
		t.Fatalf("ref = %q", ref)
	}
	if strings.Contains(res.Body.String(), base64.StdEncoding.EncodeToString(raw)[:24]) {
		t.Fatal("response echoed payload")
	}
	stored, meta, err := store.Get(ref)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, raw) {
		t.Fatal("stored bytes differ")
	}
	if strings.Contains(meta.Name, "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("stored name leaked: %q", meta.Name)
	}
	if meta.SessionID != "sess-att" || meta.Kind != attachment.KindImage {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestAttachmentIngestLimits(t *testing.T) {
	store := openAttachmentStore(t)
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Attachments: store})

	cases := []struct {
		name string
		body string
		want int
	}{
		{name: "empty", body: `{"data":""}`, want: http.StatusBadRequest},
		{name: "bad base64", body: `{"data":"!!!!"}`, want: http.StatusBadRequest},
		{name: "unsupported", body: `{"mime":"application/octet-stream","data":"` + base64.StdEncoding.EncodeToString([]byte{1, 2, 3, 4}) + `"}`, want: http.StatusBadRequest},
		{name: "unknown field", body: `{"data":"YQ==","extra":true}`, want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := postAttachment(t, srv, tc.body)
			if res.Code != tc.want {
				t.Fatalf("status = %d body = %s", res.Code, res.Body.String())
			}
		})
	}

	big := bytes.Repeat([]byte{0}, attachment.DefaultMaxBytes+1)
	payload, _ := json.Marshal(map[string]string{"data": base64.StdEncoding.EncodeToString(big)})
	res := postAttachment(t, srv, string(payload))
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "exceeds") {
		t.Fatalf("oversize = %d %s", res.Code, res.Body.String())
	}
}

func TestAttachmentIngestRequiresStoreAuthAndWritable(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("sess", t.TempDir(), nil, ops)
	defer live.Close()

	noStore := mustServer(t, Options{SessionDir: t.TempDir(), Live: live})
	res := postAttachment(t, noStore, `{"data":"YQ=="}`)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store = %d %s", res.Code, res.Body.String())
	}

	ro := mustServer(t, Options{SessionDir: t.TempDir(), Live: live, Attachments: openAttachmentStore(t), ReadOnly: true})
	res = postAttachment(t, ro, `{"data":"YQ=="}`)
	if res.Code != http.StatusForbidden {
		t.Fatalf("read-only = %d %s", res.Code, res.Body.String())
	}

	authed := mustServer(t, Options{Auth: true, Token: "secret", SessionDir: t.TempDir(), Live: live, Attachments: openAttachmentStore(t)})
	req := httptest.NewRequest(http.MethodPost, "/v1/attachments", strings.NewReader(`{"data":"YQ=="}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	authed.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth = %d", rec.Code)
	}
}

func TestAttachmentIngestRateLimit(t *testing.T) {
	store := openAttachmentStore(t)
	now := time.Unix(1_700_000_000, 0).UTC()
	srv := mustServer(t, Options{
		SessionDir:  t.TempDir(),
		Attachments: store,
		OpsRate:     1,
		OpsBurst:    1,
		OpsClock:    func() time.Time { return now },
	})
	body := `{"name":"pixel.png","mime":"image/png","data":"` + base64.StdEncoding.EncodeToString(solidPNG(t, color.RGBA{G: 255, A: 255})) + `"}`
	first := postAttachment(t, srv, body)
	if first.Code != http.StatusOK {
		t.Fatalf("first = %d %s", first.Code, first.Body.String())
	}
	second := postAttachment(t, srv, body)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second = %d %s", second.Code, second.Body.String())
	}
}

func TestAttachmentIngestRedactsRegions(t *testing.T) {
	store := openAttachmentStore(t)
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Attachments: store})
	raw := solidPNG(t, color.RGBA{R: 255, A: 255})
	payload, _ := json.Marshal(map[string]any{
		"name":    "shot.png",
		"mime":    "image/png",
		"data":    base64.StdEncoding.EncodeToString(raw),
		"regions": []map[string]int{{"x": 0, "y": 0, "w": 4, "h": 4}},
	})
	res := postAttachment(t, srv, string(payload))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d %s", res.Code, res.Body.String())
	}
	var body attachmentIngestResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	stored, meta, err := store.Get(body.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if meta.MIME != "image/png" {
		t.Fatalf("mime = %s", meta.MIME)
	}
	img, err := png.Decode(bytes.NewReader(stored))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(1, 1).RGBA()
	if r != 0 || g != 0 || b != 0 {
		t.Fatalf("pixel = %d,%d,%d", r, g, b)
	}
}

func TestUserInputAttachmentOpStripsBytesAndRedacts(t *testing.T) {
	store := openAttachmentStore(t)
	ops := make(chan protocol.Op, 1)
	live := NewLive("sess-live", t.TempDir(), nil, ops)
	defer live.Close()
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live, Attachments: store})

	raw := solidPNG(t, color.RGBA{G: 200, A: 255})
	env := map[string]any{
		"type": "user.input",
		"data": map[string]any{
			"text": "look",
			"images": []map[string]string{{
				"mime": "image/png",
				"name": attachmentSecretName,
				"data": base64.StdEncoding.EncodeToString(raw),
			}},
		},
	}
	body, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/v1/ops", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d %s", res.Code, res.Body.String())
	}
	select {
	case op := <-ops:
		ui, ok := op.(protocol.UserInput)
		if !ok || ui.Text != "look" || len(ui.Images) != 1 {
			t.Fatalf("op = %#v", op)
		}
		img := ui.Images[0]
		if img.Data != "" || !strings.HasPrefix(img.Ref, "att:sha256:") {
			t.Fatalf("image = %+v", img)
		}
		if strings.Contains(img.Name, "sk-abcdefghijklmnopqrstuvwxyz") || !strings.Contains(img.Name, "[REDACTED_API_KEY]") {
			t.Fatalf("name = %q", img.Name)
		}
	default:
		t.Fatal("op was not submitted")
	}
}

func TestUserInputAttachmentOpRejectsBeforeSubmit(t *testing.T) {
	store := openAttachmentStore(t)
	ops := make(chan protocol.Op, 1)
	live := NewLive("sess-live", t.TempDir(), nil, ops)
	defer live.Close()
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live, Attachments: store})

	pngRaw := solidPNG(t, color.RGBA{B: 255, A: 255})
	one := map[string]string{"mime": "image/png", "data": base64.StdEncoding.EncodeToString(pngRaw)}
	tooMany := make([]map[string]string, maxAttachmentsPerOp+1)
	for i := range tooMany {
		tooMany[i] = one
	}
	cases := []struct {
		name string
		data any
	}{
		{name: "too many", data: map[string]any{"text": "x", "images": tooMany}},
		{name: "missing", data: map[string]any{"text": "x", "images": []map[string]string{{"ref": "att:sha256:" + strings.Repeat("ab", 32)}}}},
		{name: "bad ref", data: map[string]any{"text": "x", "images": []map[string]string{{"ref": "nope"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"type": "user.input", "data": tc.data})
			req := httptest.NewRequest(http.MethodPost, "/v1/ops", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			res := httptest.NewRecorder()
			srv.Handler().ServeHTTP(res, req)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("status = %d %s", res.Code, res.Body.String())
			}
			select {
			case op := <-ops:
				t.Fatalf("submitted %#v", op)
			default:
			}
		})
	}
}

func TestSteerAttachmentRefAndTextOnlyUnaffected(t *testing.T) {
	store := openAttachmentStore(t)
	ops := make(chan protocol.Op, 2)
	live := NewLive("sess-live", t.TempDir(), nil, ops)
	defer live.Close()
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live, Attachments: store})

	raw := solidPNG(t, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	meta, err := store.Put(raw, attachment.PutInput{Name: "plain.png", MIME: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	ref := attachment.RefFor(meta.SHA256)
	body, _ := json.Marshal(map[string]any{
		"type": "steer",
		"data": map[string]any{
			"text":   "nudge",
			"images": []map[string]string{{"ref": ref, "name": attachmentSecretName}},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/ops", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("steer = %d %s", res.Code, res.Body.String())
	}
	op := <-ops
	st, ok := op.(protocol.Steer)
	if !ok || st.Text != "nudge" || len(st.Images) != 1 || st.Images[0].Data != "" || st.Images[0].Ref != ref {
		t.Fatalf("op = %#v", op)
	}
	if strings.Contains(st.Images[0].Name, "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("name = %q", st.Images[0].Name)
	}

	req = httptest.NewRequest(http.MethodPost, "/v1/ops", strings.NewReader(`{"type":"user.input","data":{"text":"hello"}}`))
	req.Header.Set("Content-Type", "application/json")
	res = httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("text = %d %s", res.Code, res.Body.String())
	}
	op = <-ops
	ui, ok := op.(protocol.UserInput)
	if !ok || ui.Text != "hello" || len(ui.Images) != 0 {
		t.Fatalf("op = %#v", op)
	}
}

func TestUserInputImagesWithoutStoreRejected(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("sess-live", t.TempDir(), nil, ops)
	defer live.Close()
	srv := mustServer(t, Options{SessionDir: t.TempDir(), Live: live})
	raw := solidPNG(t, color.RGBA{A: 255})
	body, _ := json.Marshal(map[string]any{
		"type": "user.input",
		"data": map[string]any{
			"text":   "look",
			"images": []map[string]string{{"mime": "image/png", "data": base64.StdEncoding.EncodeToString(raw)}},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/ops", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d %s", res.Code, res.Body.String())
	}
	select {
	case op := <-ops:
		t.Fatalf("submitted %#v", op)
	default:
	}
}

func getAttachment(t *testing.T, srv *Server, ref string, authed bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/attachments/"+ref, nil)
	if authed {
		req.Header.Set("Authorization", "Bearer secret")
	}
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	return res
}

func TestAttachmentGetServesStoredBlob(t *testing.T) {
	store := openAttachmentStore(t)
	raw := solidPNG(t, color.RGBA{B: 255, A: 255})
	pngMeta, err := store.Put(raw, attachment.PutInput{MIME: "image/png", Name: "shot one.png", MaxBytes: attachment.DefaultMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	pdf := []byte("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n")
	pdfMeta, err := store.Put(pdf, attachment.PutInput{MIME: "application/pdf", Name: "spec.pdf", MaxBytes: attachment.DefaultMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	srv := mustServer(t, Options{Auth: true, Token: "secret", SessionDir: t.TempDir(), Attachments: store})

	cases := []struct {
		name     string
		ref      string
		authed   bool
		want     int
		wantMIME string
		wantBody []byte
		wantDisp string
	}{
		{name: "png inline", ref: attachment.RefFor(pngMeta.SHA256), authed: true, want: http.StatusOK, wantMIME: "image/png", wantBody: raw, wantDisp: "inline; filename*=UTF-8''shot%20one.png"},
		{name: "png escaped ref", ref: strings.ReplaceAll(attachment.RefFor(pngMeta.SHA256), ":", "%3A"), authed: true, want: http.StatusOK, wantMIME: "image/png", wantBody: raw},
		{name: "pdf download", ref: attachment.RefFor(pdfMeta.SHA256), authed: true, want: http.StatusOK, wantMIME: "application/pdf", wantBody: pdf, wantDisp: "attachment; filename*=UTF-8''spec.pdf"},
		{name: "unauthenticated", ref: attachment.RefFor(pngMeta.SHA256), authed: false, want: http.StatusUnauthorized},
		{name: "unknown ref", ref: attachment.RefPrefix + strings.Repeat("0", 64), authed: true, want: http.StatusNotFound},
		{name: "malformed ref", ref: "att:sha256:nothex", authed: true, want: http.StatusBadRequest},
		{name: "not a ref", ref: "etc-passwd", authed: true, want: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := getAttachment(t, srv, tc.ref, tc.authed)
			if res.Code != tc.want {
				t.Fatalf("status = %d, want %d; body = %s", res.Code, tc.want, res.Body.String())
			}
			if tc.want != http.StatusOK {
				return
			}
			if got := res.Header().Get("Content-Type"); got != tc.wantMIME {
				t.Fatalf("Content-Type = %q, want %q", got, tc.wantMIME)
			}
			if !bytes.Equal(res.Body.Bytes(), tc.wantBody) {
				t.Fatalf("body mismatch: %d bytes", res.Body.Len())
			}
			if got := res.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("X-Content-Type-Options = %q", got)
			}
			if csp := res.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
				t.Fatalf("CSP = %q, want sandbox", csp)
			}
			if tc.wantDisp != "" {
				if got := res.Header().Get("Content-Disposition"); got != tc.wantDisp {
					t.Fatalf("Content-Disposition = %q, want %q", got, tc.wantDisp)
				}
			}
		})
	}
}

func TestAttachmentGetStoreAndReadOnly(t *testing.T) {
	ref := attachment.RefPrefix + strings.Repeat("a", 64)
	noStore := mustServer(t, Options{SessionDir: t.TempDir()})
	if res := getAttachment(t, noStore, ref, false); res.Code != http.StatusServiceUnavailable {
		t.Fatalf("no store = %d %s", res.Code, res.Body.String())
	}

	store := openAttachmentStore(t)
	raw := solidPNG(t, color.RGBA{R: 9, A: 255})
	meta, err := store.Put(raw, attachment.PutInput{MIME: "image/png", MaxBytes: attachment.DefaultMaxBytes})
	if err != nil {
		t.Fatal(err)
	}
	// Reads stay available on a read-only gateway and are not charged to
	// the ops rate limit (burst 1 here would refuse the second read).
	ro := mustServer(t, Options{SessionDir: t.TempDir(), Attachments: store, ReadOnly: true, OpsRate: 1, OpsBurst: 1})
	for i := 0; i < 3; i++ {
		res := getAttachment(t, ro, attachment.RefFor(meta.SHA256), false)
		if res.Code != http.StatusOK {
			t.Fatalf("read %d = %d %s", i, res.Code, res.Body.String())
		}
		if got := res.Header().Get("Content-Disposition"); got != "inline" {
			t.Fatalf("unnamed disposition = %q", got)
		}
	}
}

func TestAttachmentDisposition(t *testing.T) {
	cases := []struct{ mime, name, want string }{
		{"image/png", "", "inline"},
		{"IMAGE/JPEG", "a.jpg", "inline; filename*=UTF-8''a.jpg"},
		{"image/svg+xml", "logo.svg", "attachment; filename*=UTF-8''logo.svg"},
		{"text/html", "x\".html", "attachment; filename*=UTF-8''x%22.html"},
		{"application/octet-stream", "  ", "attachment"},
	}
	for _, tc := range cases {
		if got := attachmentDisposition(tc.mime, tc.name); got != tc.want {
			t.Errorf("attachmentDisposition(%q, %q) = %q, want %q", tc.mime, tc.name, got, tc.want)
		}
	}
}
