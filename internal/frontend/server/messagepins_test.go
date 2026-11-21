package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pinsBody(t *testing.T, rec *httptest.ResponseRecorder) []messagePin {
	t.Helper()
	var out messagePinsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	if out.Pins == nil {
		t.Fatalf("pins must be a JSON array, got %s", rec.Body.String())
	}
	return out.Pins
}

func mpAnchors(pins []messagePin) string {
	parts := make([]string, len(pins))
	for i, p := range pins {
		parts[i] = strings.Join([]string{mpItoa(p.Keep), p.Role, mpItoa(p.Seq)}, ":")
	}
	return strings.Join(parts, ",")
}

func mpItoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestMessagePinsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srv := newListServer(t, dir)
	const base = "/v1/sessions/s1/message-pins"

	unauth := httptest.NewRecorder()
	srv.Handler().ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, base, nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("auth = %d", unauth.Code)
	}

	empty := listDo(t, srv, http.MethodGet, base, "")
	if empty.Code != http.StatusOK || len(pinsBody(t, empty)) != 0 {
		t.Fatalf("empty = %d %s", empty.Code, empty.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, messagePinsFile)); !os.IsNotExist(err) {
		t.Fatalf("a read created the pin file: %v", err)
	}

	for _, body := range []string{
		`{"keep":12,"role":"strike","seq":1,"excerpt":"  The fix   is in parse()  ","atMs":1700}`,
		`{"keep":4,"role":"you","seq":0,"excerpt":"why does it fail"}`,
		`{"keep":12,"role":"you","seq":0,"excerpt":"try again"}`,
	} {
		rec := listDo(t, srv, http.MethodPost, base, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("add %s = %d %s", body, rec.Code, rec.Body.String())
		}
	}
	got := pinsBody(t, listDo(t, srv, http.MethodGet, base, ""))
	if a := mpAnchors(got); a != "4:you:0,12:you:0,12:strike:1" {
		t.Fatalf("order = %s", a)
	}
	if got[2].Excerpt != "The fix is in parse()" || got[2].AtMs != 1700 || got[2].PinnedAt == "" {
		t.Fatalf("pin = %+v", got[2])
	}

	// Re-pinning the same anchor refreshes it instead of duplicating.
	again := listDo(t, srv, http.MethodPost, base, `{"keep":12,"role":"strike","seq":1,"excerpt":"The fix is in parse() now"}`)
	if pins := pinsBody(t, again); len(pins) != 3 || pins[2].Excerpt != "The fix is in parse() now" {
		t.Fatalf("re-pin = %s", again.Body.String())
	}

	// Pins follow the session across clients: a fresh server reads them.
	other := newListServer(t, dir)
	if pins := pinsBody(t, listDo(t, other, http.MethodGet, base, "")); len(pins) != 3 {
		t.Fatalf("second client saw %d pins", len(pins))
	}
	// Other sessions are untouched.
	if pins := pinsBody(t, listDo(t, srv, http.MethodGet, "/v1/sessions/s2/message-pins", "")); len(pins) != 0 {
		t.Fatalf("s2 has %d pins", len(pins))
	}

	del := listDo(t, srv, http.MethodDelete, base+"?keep=12&role=you&seq=0", "")
	if del.Code != http.StatusOK || mpAnchors(pinsBody(t, del)) != "4:you:0,12:strike:1" {
		t.Fatalf("delete = %d %s", del.Code, del.Body.String())
	}
	gone := listDo(t, srv, http.MethodDelete, base+"?keep=12&role=you&seq=0", "")
	if gone.Code != http.StatusOK || len(pinsBody(t, gone)) != 2 {
		t.Fatalf("idempotent delete = %d %s", gone.Code, gone.Body.String())
	}
}

func TestMessagePinsValidation(t *testing.T) {
	srv := newListServer(t, t.TempDir())
	const base = "/v1/sessions/s1/message-pins"
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"tool rows are not pinnable", http.MethodPost, base, `{"keep":1,"role":"tool","seq":0,"excerpt":"x"}`, http.StatusBadRequest},
		{"negative keep", http.MethodPost, base, `{"keep":-1,"role":"you","seq":0,"excerpt":"x"}`, http.StatusBadRequest},
		{"negative seq", http.MethodPost, base, `{"keep":1,"role":"you","seq":-2,"excerpt":"x"}`, http.StatusBadRequest},
		{"blank excerpt", http.MethodPost, base, `{"keep":1,"role":"you","seq":0,"excerpt":"   "}`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, base, `{"keep":1,"role":"you","seq":0,"excerpt":"x","id":7}`, http.StatusBadRequest},
		{"bad session id", http.MethodGet, "/v1/sessions/a..b/message-pins", "", http.StatusBadRequest},
		{"delete needs an anchor", http.MethodDelete, base + "?keep=1", "", http.StatusBadRequest},
		{"delete rejects a bad role", http.MethodDelete, base + "?keep=1&role=tool&seq=0", "", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := listDo(t, srv, tc.method, tc.path, tc.body)
			if rec.Code != tc.want {
				t.Fatalf("%s %s = %d %s", tc.method, tc.path, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMessagePinsLimitAndExcerptClip(t *testing.T) {
	srv := newListServer(t, t.TempDir())
	const base = "/v1/sessions/s1/message-pins"
	long := strings.Repeat("é", messagePinExcerptMax+40)
	first := listDo(t, srv, http.MethodPost, base, `{"keep":0,"role":"you","seq":0,"excerpt":"`+long+`"}`)
	pins := pinsBody(t, first)
	if n := len([]rune(pins[0].Excerpt)); n != messagePinExcerptMax {
		t.Fatalf("excerpt runes = %d", n)
	}
	for i := 1; i < messagePinsPerSession; i++ {
		rec := listDo(t, srv, http.MethodPost, base, `{"keep":`+mpItoa(i)+`,"role":"you","seq":0,"excerpt":"m"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("pin %d = %d", i, rec.Code)
		}
	}
	over := listDo(t, srv, http.MethodPost, base, `{"keep":999,"role":"you","seq":0,"excerpt":"m"}`)
	if over.Code != http.StatusConflict {
		t.Fatalf("over limit = %d %s", over.Code, over.Body.String())
	}
	// Refreshing an existing pin at the limit still works.
	refresh := listDo(t, srv, http.MethodPost, base, `{"keep":5,"role":"you","seq":0,"excerpt":"again"}`)
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh at limit = %d", refresh.Code)
	}
}

func TestRemoveMessagePins(t *testing.T) {
	dir := t.TempDir()
	srv := newListServer(t, dir)
	listDo(t, srv, http.MethodPost, "/v1/sessions/s1/message-pins", `{"keep":1,"role":"you","seq":0,"excerpt":"a"}`)
	listDo(t, srv, http.MethodPost, "/v1/sessions/s2/message-pins", `{"keep":1,"role":"you","seq":0,"excerpt":"b"}`)
	if err := removeMessagePins(dir, "s1"); err != nil {
		t.Fatal(err)
	}
	if pins := pinsBody(t, listDo(t, srv, http.MethodGet, "/v1/sessions/s1/message-pins", "")); len(pins) != 0 {
		t.Fatalf("s1 kept %d pins", len(pins))
	}
	if pins := pinsBody(t, listDo(t, srv, http.MethodGet, "/v1/sessions/s2/message-pins", "")); len(pins) != 1 {
		t.Fatalf("s2 lost its pin")
	}
	// Removing pins for a session that never had any writes nothing new.
	st, err := loadMessagePins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := st.Sessions["s1"]; ok {
		t.Fatal("s1 entry left in the file")
	}
}
