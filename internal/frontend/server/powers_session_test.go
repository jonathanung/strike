package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/pkg/protocol"
)

// Activations key on the durable session id the root engine runs under, so
// the engine's per-session tool and skill scope sees them whether the
// cockpit sent the root id or the session id (#1943).
func TestPowersKeyOnEngineSessionID(t *testing.T) {
	hub := NewLiveHub(nil, nil)
	hub.Add("root-b", NewLive("sess-b", "/cwd", nil, make(chan protocol.Op, 1)))
	defer hub.Close()
	fake := newFakePowers()
	srv, err := New(Options{SessionDir: t.TempDir(), LiveHub: hub, Services: &host.Services{Powers: fake}})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		id   string
	}{
		{name: "root id", id: "root-b"},
		{name: "session id", id: "sess-b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := powersDo(t, srv, http.MethodPost, "/v1/powers/activate", `{"session":"`+tt.id+`","name":"aws"}`)
			if res.Code != http.StatusOK {
				t.Fatalf("activate = %d %s", res.Code, res.Body.String())
			}
			if got := fake.Active("sess-b"); len(got) != 1 {
				t.Fatalf("active under engine session id = %+v", got)
			}
			if got := fake.Active("root-b"); len(got) != 0 {
				t.Fatalf("activation keyed on the root id: %+v", got)
			}
			list := powersDo(t, srv, http.MethodGet, "/v1/powers?session="+tt.id, "")
			if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"aws-docs"`) {
				t.Fatalf("list for %s = %d %s", tt.id, list.Code, list.Body.String())
			}
			res = powersDo(t, srv, http.MethodPost, "/v1/powers/deactivate", `{"session":"`+tt.id+`","name":"aws"}`)
			if res.Code != http.StatusOK {
				t.Fatalf("deactivate = %d %s", res.Code, res.Body.String())
			}
			if got := fake.Active("sess-b"); len(got) != 0 {
				t.Fatalf("still active after deactivate: %+v", got)
			}
		})
	}
}
