package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/integrate/powers"
)

// fakePowers is a host.Powers for route tests.
type fakePowers struct {
	mu     sync.Mutex
	active map[string][]host.PowerActivation
	muted  map[string]bool
	ended  []string
}

func newFakePowers() *fakePowers {
	return &fakePowers{active: map[string][]host.PowerActivation{}, muted: map[string]bool{}}
}

func (f *fakePowers) Packs() ([]host.PowerPack, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []host.PowerPack{
		{Name: "aws", Scope: "workspace", Keywords: []string{"aws"}, Servers: []string{"aws-docs", "broken"}, Muted: f.muted["aws"]},
		{Name: "exfil", Scope: "global", Servers: []string{"exfil"}},
	}, []string{"powers: bad/plugin.json: missing"}
}

func (f *fakePowers) Suggest(session, prompt string) []string {
	if strings.Contains(strings.ToLower(prompt), "aws") {
		return []string{"aws"}
	}
	return nil
}

func (f *fakePowers) Activate(session, name string) (host.PowerActivation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch name {
	case "exfil":
		return host.PowerActivation{}, fmt.Errorf("%w: exfil: denied", powers.ErrDenied)
	case "aws":
		a := host.PowerActivation{Name: "aws", Admitted: []string{"aws-docs"}, Failed: map[string]string{"broken": "connection refused"}}
		f.active[session] = append(f.active[session], a)
		return a, nil
	}
	return host.PowerActivation{}, errors.New("powers: unknown pack")
}

func (f *fakePowers) Deactivate(session, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, a := range f.active[session] {
		if a.Name == name {
			f.active[session] = append(f.active[session][:i], f.active[session][i+1:]...)
			return nil
		}
	}
	return errors.New("powers: not active")
}

func (f *fakePowers) Active(session string) []host.PowerActivation {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]host.PowerActivation(nil), f.active[session]...)
}

func (f *fakePowers) SetMuted(name string, muted bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.muted[name] = muted
	return nil
}

func (f *fakePowers) EndSession(session string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = append(f.ended, session)
	delete(f.active, session)
}

func powersDo(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	res := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	srv.Handler().ServeHTTP(res, req)
	return res
}

func TestPowersCapabilityUnavailable(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ method, path string }{
		{http.MethodGet, "/v1/powers"},
		{http.MethodPost, "/v1/powers/suggest"},
		{http.MethodPost, "/v1/powers/activate"},
		{http.MethodPost, "/v1/powers/deactivate"},
		{http.MethodPost, "/v1/powers/mute"},
	} {
		res := powersDo(t, srv, p.method, p.path, `{}`)
		if res.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d %s", p.method, p.path, res.Code, res.Body.String())
		}
	}
}

func TestPowersRoutes(t *testing.T) {
	fake := newFakePowers()
	srv, err := New(Options{SessionDir: t.TempDir(), Services: &host.Services{Powers: fake}})
	if err != nil {
		t.Fatal(err)
	}
	if boot := powersDo(t, srv, http.MethodGet, "/v1/bootstrap", ""); !strings.Contains(boot.Body.String(), `"powers":true`) {
		t.Fatalf("bootstrap missing powers: %s", boot.Body.String())
	}

	sug := powersDo(t, srv, http.MethodPost, "/v1/powers/suggest", `{"session":"s1","prompt":"deploy to AWS"}`)
	if sug.Code != http.StatusOK || !strings.Contains(sug.Body.String(), `"suggestions":["aws"]`) {
		t.Fatalf("suggest = %d %s", sug.Code, sug.Body.String())
	}
	if sug := powersDo(t, srv, http.MethodPost, "/v1/powers/suggest", `{"session":"s1","prompt":"hello"}`); !strings.Contains(sug.Body.String(), `"suggestions":[]`) {
		t.Fatalf("empty suggest should be an empty list: %s", sug.Body.String())
	}
	if len(fake.Active("s1")) != 0 {
		t.Fatal("suggest must never activate")
	}

	act := powersDo(t, srv, http.MethodPost, "/v1/powers/activate", `{"session":"s1","name":"aws"}`)
	if act.Code != http.StatusOK {
		t.Fatalf("activate = %d %s", act.Code, act.Body.String())
	}
	var res host.PowerActivation
	if err := json.Unmarshal(act.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Failed["broken"] == "" || len(res.Admitted) != 1 {
		t.Fatalf("activation = %+v", res)
	}

	list := powersDo(t, srv, http.MethodGet, "/v1/powers?session=s1", "")
	var payload powersListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Packs) != 2 || len(payload.LoadErrors) != 1 || len(payload.Active) != 1 || payload.Active[0].Failed["broken"] == "" {
		t.Fatalf("list = %s", list.Body.String())
	}
	if other := powersDo(t, srv, http.MethodGet, "/v1/powers?session=s2", ""); !strings.Contains(other.Body.String(), `"active":[]`) {
		t.Fatalf("other session should have no active packs: %s", other.Body.String())
	}

	if den := powersDo(t, srv, http.MethodPost, "/v1/powers/activate", `{"session":"s1","name":"exfil"}`); den.Code != http.StatusForbidden {
		t.Fatalf("denied activate = %d %s", den.Code, den.Body.String())
	}
	if bad := powersDo(t, srv, http.MethodPost, "/v1/powers/activate", `{"session":"s1","name":"nope"}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown activate = %d", bad.Code)
	}
	if bad := powersDo(t, srv, http.MethodPost, "/v1/powers/activate", `{"name":"aws"}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("missing session = %d", bad.Code)
	}

	if de := powersDo(t, srv, http.MethodPost, "/v1/powers/deactivate", `{"session":"s1","name":"aws"}`); de.Code != http.StatusOK {
		t.Fatalf("deactivate = %d %s", de.Code, de.Body.String())
	}
	if de := powersDo(t, srv, http.MethodPost, "/v1/powers/deactivate", `{"session":"s1","name":"aws"}`); de.Code != http.StatusBadRequest {
		t.Fatalf("second deactivate = %d", de.Code)
	}

	if mu := powersDo(t, srv, http.MethodPost, "/v1/powers/mute", `{"name":"aws","muted":true}`); mu.Code != http.StatusOK {
		t.Fatalf("mute = %d %s", mu.Code, mu.Body.String())
	}
	if !fake.muted["aws"] {
		t.Fatal("mute not applied")
	}
	if mu := powersDo(t, srv, http.MethodPost, "/v1/powers/mute", `{"muted":true}`); mu.Code != http.StatusBadRequest {
		t.Fatalf("mute without name = %d", mu.Code)
	}

	srv.endPowers("s1", "s1", "", "s2")
	if got := strings.Join(fake.ended, ","); got != "s1,s2" {
		t.Fatalf("ended = %q", got)
	}
}

func TestPowersActivateRequiresLiveSession(t *testing.T) {
	fake := newFakePowers()
	srv, err := New(Options{SessionDir: t.TempDir(), LiveHub: NewLiveHub(nil, nil), Services: &host.Services{Powers: fake}})
	if err != nil {
		t.Fatal(err)
	}
	res := powersDo(t, srv, http.MethodPost, "/v1/powers/activate", `{"session":"ghost","name":"aws"}`)
	if res.Code != http.StatusNotFound {
		t.Fatalf("activate for a non-live session = %d %s", res.Code, res.Body.String())
	}
	if len(fake.Active("ghost")) != 0 {
		t.Fatal("nothing may activate outside a live session")
	}
}

func TestPowersReadOnly(t *testing.T) {
	fake := newFakePowers()
	srv, err := New(Options{SessionDir: t.TempDir(), ReadOnly: true, Services: &host.Services{Powers: fake}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/powers/activate", "/v1/powers/deactivate", "/v1/powers/mute"} {
		res := powersDo(t, srv, http.MethodPost, path, `{"session":"s1","name":"aws","muted":true}`)
		if res.Code != http.StatusForbidden {
			t.Errorf("%s in read-only = %d", path, res.Code)
		}
	}
	if len(fake.Active("s1")) != 0 || fake.muted["aws"] {
		t.Fatal("read-only server mutated Powers state")
	}
}
