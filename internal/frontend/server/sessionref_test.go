package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jonathanung/strike/internal/persist/session"
	"github.com/jonathanung/strike/internal/protocol"
)

func TestSessionReferenceSizes(t *testing.T) {
	dir := t.TempDir()
	log := `{"type":"session.titled","time":"2026-11-17T09:30:00Z","data":{"title":"Auth investigation"}}
{"type":"user.message","time":"2026-11-17T09:30:01Z","data":{"text":"why do sessions drop?"}}
{"type":"text.delta","time":"2026-11-17T09:30:02Z","data":{"text":"refresh races logout"}}
{"type":"compaction.completed","time":"2026-11-17T09:31:00Z","data":{"reason":"auto","summary":"Refresh races logout."}}
`
	for _, id := range []string{"auth-tue", "sess-self"} {
		if err := os.WriteFile(session.LogPath(dir, id), []byte(log), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	live := NewLive("sess-self", t.TempDir(), nil, make(chan protocol.Op, 1))
	defer live.Close()
	srv, err := New(Options{SessionDir: dir, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	get := func(id string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/v1/sessions/"+id+"/reference", nil)
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, req)
		return res
	}

	cases := []struct {
		name     string
		id       string
		code     int
		self     bool
		wantSize bool
	}{
		{name: "other session", id: "auth-tue", code: http.StatusOK, wantSize: true},
		{name: "self reference", id: "sess-self", code: http.StatusOK, self: true},
		{name: "missing or pruned", id: "gone", code: http.StatusNotFound},
		{name: "dot-dot id", id: "a..b", code: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := get(tc.id)
			if res.Code != tc.code {
				t.Fatalf("status = %d, want %d: %s", res.Code, tc.code, res.Body.String())
			}
			if tc.code != http.StatusOK {
				return
			}
			var got sessionReferenceResponse
			if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Self != tc.self {
				t.Fatalf("self = %v, want %v", got.Self, tc.self)
			}
			if tc.wantSize {
				if got.Chars <= 0 || got.Tokens != (got.Chars+3)/4 || !got.Estimated {
					t.Fatalf("size = %#v", got)
				}
				if got.Title != "Auth investigation" || got.Turns != 2 || !got.HasSummary {
					t.Fatalf("ref = %#v", got)
				}
			} else if got.Chars != 0 {
				t.Fatalf("self reference reports size %d", got.Chars)
			}
		})
	}
}
