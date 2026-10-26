package server

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/persist/workspace"
)

func newTerminalServer(t *testing.T, mutate func(*Options)) (*Server, *workspace.Store, workspace.Workspace) {
	t.Helper()
	store, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	ws, err := store.Add(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Auth:       true,
		Token:      "secret",
		Addr:       "127.0.0.1:8787",
		SessionDir: t.TempDir(),
		Workspaces: store,
	}
	if mutate != nil {
		mutate(&opts)
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if srv.terms != nil {
			srv.terms.Close()
		}
	})
	return srv, store, ws
}

func TestTerminalAuthFailure(t *testing.T) {
	srv, _, _ := newTerminalServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cases := []struct {
		name   string
		method string
		path   string
		header string
		body   string
		want   int
	}{
		{name: "list missing", method: http.MethodGet, path: "/v1/terminals", want: http.StatusUnauthorized},
		{name: "list wrong", method: http.MethodGet, path: "/v1/terminals", header: "Bearer nope", want: http.StatusUnauthorized},
		{name: "list query token", method: http.MethodGet, path: "/v1/terminals?token=secret", want: http.StatusUnauthorized},
		{name: "create missing", method: http.MethodPost, path: "/v1/terminals", body: `{}`, want: http.StatusUnauthorized},
		{name: "stream query token", method: http.MethodGet, path: "/v1/terminals/abc/stream?token=secret", want: http.StatusUnauthorized},
		{name: "stream query ticket", method: http.MethodGet, path: "/v1/terminals/abc/stream?ticket=abc", want: http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, ts.URL+tc.path, strings.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.want {
				b, _ := io.ReadAll(res.Body)
				t.Fatalf("status = %d, want %d body %s", res.StatusCode, tc.want, b)
			}
		})
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/terminals", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("authed list = %d", res.StatusCode)
	}
}

func TestTerminalCWDConfinementOverHTTP(t *testing.T) {
	srv, _, ws := newTerminalServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	outside := t.TempDir()
	res := authedJSON(t, ts.URL, http.MethodPost, "/v1/terminals", `{"cwd":"`+outside+`"}`)
	if res.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("outside cwd status = %d body %s", res.StatusCode, b)
	}
	res.Body.Close()

	res = authedJSON(t, ts.URL, http.MethodPost, "/v1/terminals", `{"workspaceId":"missing"}`)
	if res.StatusCode != http.StatusConflict {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("missing workspace status = %d body %s", res.StatusCode, b)
	}
	res.Body.Close()

	res = authedJSON(t, ts.URL, http.MethodGet, "/v1/terminals", "")
	var listed struct {
		Terminals []map[string]any `json:"terminals"`
	}
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if len(listed.Terminals) != 0 {
		t.Fatalf("rejected creates left terminals: %+v", listed.Terminals)
	}

	res = authedJSON(t, ts.URL, http.MethodPost, "/v1/terminals", `{"workspaceId":"`+ws.ID+`","cols":40,"rows":12}`)
	if res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("create status = %d body %s", res.StatusCode, b)
	}
	var info struct {
		ID  string `json:"id"`
		CWD string `json:"cwd"`
	}
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if info.CWD != ws.Path {
		t.Fatalf("cwd = %q, want %q", info.CWD, ws.Path)
	}

	conn := dialTerminal(t, ts.URL, "/v1/terminals/"+info.ID+"/stream", "Bearer secret", "")
	defer conn.Close()
	writeWSText(t, conn, `{"type":"input","data":"pwd\n"}`)
	out := readWSUntil(t, conn, ws.Path, 8*time.Second)
	if !strings.Contains(out, ws.Path) {
		t.Fatalf("stream output = %q, want workspace %s", out, ws.Path)
	}
}

func TestTerminalPlanModeAndRemoteBind(t *testing.T) {
	srv, store, ws := newTerminalServer(t, nil)
	mode := "plan"
	if _, err := store.SetPolicy(ws.ID, nil, &mode); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/terminals", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer secret")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "plan mode") {
		t.Fatalf("plan status = %d body %s", rec.Code, rec.Body.String())
	}

	remote, _, _ := newTerminalServer(t, func(o *Options) {
		o.Addr = "10.1.2.3:8787"
	})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/terminals", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer secret")
	remote.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "non-loopback") {
		t.Fatalf("remote status = %d body %s", rec.Code, rec.Body.String())
	}

	allowed, _, allowedWS := newTerminalServer(t, func(o *Options) {
		o.Addr = "10.1.2.3:8787"
		o.TerminalRemote = true
	})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/terminals", strings.NewReader(`{"workspaceId":"`+allowedWS.ID+`"}`))
	req.Header.Set("Authorization", "Bearer secret")
	allowed.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("explicit remote status = %d body %s", rec.Code, rec.Body.String())
	}

	if _, err := New(Options{Auth: false, TerminalRemote: true, Addr: "10.1.2.3:8787"}); err == nil {
		t.Fatal("terminal remote without auth should fail")
	}
}

func TestTerminalTicketAuthenticatesStream(t *testing.T) {
	srv, _, ws := newTerminalServer(t, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	res := authedJSON(t, ts.URL, http.MethodPost, "/v1/terminals", `{"workspaceId":"`+ws.ID+`"}`)
	if res.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("create = %d %s", res.StatusCode, b)
	}
	var info struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	res = authedJSON(t, ts.URL, http.MethodPost, "/v1/terminals/"+info.ID+"/ticket", `{}`)
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("ticket = %d %s", res.StatusCode, b)
	}
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(res.Body).Decode(&ticket); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	conn := dialTerminal(t, ts.URL, "/v1/terminals/"+info.ID+"/stream", "", "strike.terminal, ticket."+ticket.Ticket)
	hello := readWSText(t, conn, 3*time.Second)
	conn.Close()
	if !strings.Contains(hello, `"type":"hello"`) {
		t.Fatalf("hello = %q", hello)
	}

	bad := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/terminals/"+info.ID+"/stream", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "strike.terminal, ticket."+ticket.Ticket)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	srv.Handler().ServeHTTP(bad, req)
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("reused ticket status = %d body %s", bad.Code, bad.Body.String())
	}
}

func TestTerminalOriginRejected(t *testing.T) {
	srv, _, ws := newTerminalServer(t, nil)
	res := authedJSONRequest(t, srv, http.MethodPost, "/v1/terminals", `{"workspaceId":"`+ws.ID+`"}`)
	if res.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", res.Code, res.Body.String())
	}
	var info struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/terminals/"+info.ID+"/stream", nil)
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("origin status = %d body %s", rec.Code, rec.Body.String())
	}
}

func authedJSON(t *testing.T, base, method, path, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func authedJSONRequest(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

type wsConnClient struct {
	net.Conn
	br *bufio.Reader
}

func dialTerminal(t *testing.T, httpURL, path, auth, protocols string) *wsConnClient {
	t.Helper()
	u := strings.TrimPrefix(httpURL, "http://")
	conn, err := net.Dial("tcp", u)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("GET " + path + " HTTP/1.1\r\n")
	b.WriteString("Host: " + u + "\r\n")
	b.WriteString("Upgrade: websocket\r\n")
	b.WriteString("Connection: Upgrade\r\n")
	b.WriteString("Sec-WebSocket-Version: 13\r\n")
	b.WriteString("Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n")
	if auth != "" {
		b.WriteString("Authorization: " + auth + "\r\n")
	}
	if protocols != "" {
		b.WriteString("Sec-WebSocket-Protocol: " + protocols + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if !strings.Contains(status, "101") {
		rest, _ := io.ReadAll(br)
		conn.Close()
		t.Fatalf("upgrade status %q body %s", status, rest)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			conn.Close()
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	return &wsConnClient{Conn: conn, br: br}
}

func writeWSText(t *testing.T, c *wsConnClient, text string) {
	t.Helper()
	payload := []byte(text)
	mask := [4]byte{9, 8, 7, 6}
	frame := []byte{0x81}
	n := len(payload)
	switch {
	case n < 126:
		frame = append(frame, 0x80|byte(n))
	default:
		frame = append(frame, 0x80|126, byte(n>>8), byte(n))
	}
	frame = append(frame, mask[:]...)
	for i, b := range payload {
		frame = append(frame, b^mask[i%4])
	}
	if _, err := c.Write(frame); err != nil {
		t.Fatal(err)
	}
}

func readWSText(t *testing.T, c *wsConnClient, d time.Duration) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(d))
	for {
		h := make([]byte, 2)
		if _, err := io.ReadFull(c.br, h); err != nil {
			t.Fatal(err)
		}
		opcode := h[0] & 0x0f
		n := int(h[1] & 0x7f)
		switch n {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(c.br, ext); err != nil {
				t.Fatal(err)
			}
			n = int(binary.BigEndian.Uint16(ext))
		case 127:
			t.Fatal("unexpected 64-bit frame")
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(c.br, payload); err != nil {
			t.Fatal(err)
		}
		if opcode == 0x9 {
			continue
		}
		if opcode == 0x1 {
			return string(payload)
		}
		if opcode == 0x8 {
			t.Fatal("websocket closed")
		}
	}
}

func readWSUntil(t *testing.T, c *wsConnClient, needle string, d time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(d)
	var all strings.Builder
	for time.Now().Before(deadline) {
		text := readWSText(t, c, time.Until(deadline))
		var msg struct {
			Type     string `json:"type"`
			Data     string `json:"data"`
			Encoding string `json:"encoding"`
			Scroll   string `json:"scrollback"`
			Reason   string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(text), &msg); err != nil {
			all.WriteString(text)
			continue
		}
		if msg.Encoding == "base64" {
			if msg.Data != "" {
				raw, err := base64.StdEncoding.DecodeString(msg.Data)
				if err == nil {
					all.Write(raw)
				}
			}
			if msg.Scroll != "" {
				raw, err := base64.StdEncoding.DecodeString(msg.Scroll)
				if err == nil {
					all.Write(raw)
				}
			}
		}
		if msg.Type == "exit" {
			t.Fatalf("terminal exited: %s output %s", msg.Reason, all.String())
		}
		if strings.Contains(all.String(), needle) {
			return all.String()
		}
	}
	t.Fatalf("timeout waiting for %q, have %q", needle, all.String())
	return ""
}
