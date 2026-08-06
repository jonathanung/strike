package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestRootsUnavailableWithoutLiveHub(t *testing.T) {
	dir := t.TempDir()
	live := NewLive("live", "/", nil, make(chan protocol.Op))
	defer live.Close()
	srv, err := New(Options{Auth: true, Token: "t", SessionDir: dir, Live: live})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/roots"},
		{http.MethodPost, "/v1/roots/r1/activate"},
		{http.MethodPost, "/v1/roots/r1/resume"},
		{http.MethodDelete, "/v1/roots/r1"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer t")
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, req)
		if res.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", tc.method, tc.path, res.Code)
		}
	}
}

func TestRootsListAndActivate(t *testing.T) {
	dir := t.TempDir()
	live1 := NewLive("r1", "/a", nil, make(chan protocol.Op))
	live2 := NewLive("r2", "/b", nil, make(chan protocol.Op))
	defer live1.Close()
	defer live2.Close()
	live1.Publish(protocol.AgentSelected{Name: "build"})
	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live1)
	hub.Add("r2", live2)

	srv, err := New(Options{Auth: true, Token: "t", SessionDir: dir, LiveHub: hub})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/roots", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var rr rootsResponse
	if err := json.NewDecoder(res.Body).Decode(&rr); err != nil {
		t.Fatal(err)
	}
	if len(rr.Roots) != 2 {
		t.Fatalf("roots = %d, want 2", len(rr.Roots))
	}
	if rr.ActiveID != "r1" {
		t.Fatalf("activeId = %q, want r1", rr.ActiveID)
	}
	if rr.Roots[0].Agent != "build" {
		t.Fatalf("agent = %q, want build", rr.Roots[0].Agent)
	}

	// Activate r2
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/roots/r2/activate", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("activate = %d", res.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/v1/roots", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if err := json.NewDecoder(res.Body).Decode(&rr); err != nil {
		t.Fatal(err)
	}
	if rr.ActiveID != "r2" {
		t.Fatalf("activeId after activate = %q, want r2", rr.ActiveID)
	}
}

func TestRootCreate(t *testing.T) {
	dir := t.TempDir()
	live := NewLive("r1", "/a", nil, make(chan protocol.Op))
	defer live.Close()
	var hub *LiveHub
	var spawnedLives []*Live
	hub = NewLiveHub(
		func(ctx context.Context) (string, error) {
			newLive := NewLive("new-root", "/new", nil, make(chan protocol.Op, 1))
			spawnedLives = append(spawnedLives, newLive)
			hub.Add("new-root", newLive)
			return "new-root", nil
		},
		nil,
	)
	defer func() {
		for _, l := range spawnedLives {
			l.Close()
		}
	}()
	hub.Add("r1", live)
	srv, err := New(Options{Auth: true, Token: "t", SessionDir: dir, LiveHub: hub})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/roots", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d", res.StatusCode)
	}
	var cr RootCreateResult
	if err := json.NewDecoder(res.Body).Decode(&cr); err != nil {
		t.Fatal(err)
	}
	if cr.ID != "new-root" {
		t.Fatalf("id = %q", cr.ID)
	}
}

func TestRootResume(t *testing.T) {
	dir := t.TempDir()
	live := NewLive("r1", "/a", nil, make(chan protocol.Op))
	defer live.Close()
	var hub *LiveHub
	var spawnedLives []*Live
	hub = NewLiveHub(
		nil,
		func(ctx context.Context, sessionID string) (string, bool, error) {
			if sessionID == "bad-child" {
				return "", false, fmt.Errorf("cannot resume child session %q", sessionID)
			}
			newLive := NewLive(sessionID, "/resumed", nil, make(chan protocol.Op, 1))
			spawnedLives = append(spawnedLives, newLive)
			hub.Add(sessionID, newLive)
			return sessionID, false, nil
		},
	)
	defer func() {
		for _, l := range spawnedLives {
			l.Close()
		}
	}()
	hub.Add("r1", live)
	srv, err := New(Options{Auth: true, Token: "t", SessionDir: dir, LiveHub: hub})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	cases := []struct {
		name       string
		sessionID  string
		wantStatus int
		wantActive bool
	}{
		{"resume new session", "durable-1", http.StatusOK, false},
		{"reject child session", "bad-child", http.StatusBadRequest, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/roots/"+tc.sessionID+"/resume", nil)
			req.Header.Set("Authorization", "Bearer t")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusOK {
				var rr RootResumeResult
				if err := json.NewDecoder(res.Body).Decode(&rr); err != nil {
					t.Fatal(err)
				}
				if rr.WasActive != tc.wantActive {
					t.Fatalf("WasActive = %v, want %v", rr.WasActive, tc.wantActive)
				}
			}
		})
	}
}

func TestRootClose(t *testing.T) {
	dir := t.TempDir()
	live1 := NewLive("r1", "/a", nil, make(chan protocol.Op))
	live2 := NewLive("r2", "/b", nil, make(chan protocol.Op))
	defer live1.Close()
	defer live2.Close()
	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live1)
	hub.Add("r2", live2)
	if err := hub.Activate("r2"); err != nil {
		t.Fatal(err)
	}

	srv, err := New(Options{Auth: true, Token: "t", SessionDir: dir, LiveHub: hub})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Unknown root → 404
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v1/roots/missing", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing close = %d, want 404", res.StatusCode)
	}

	// Close active r2 → r1 becomes active
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/v1/roots/r2", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("close = %d", res.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/v1/roots", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var rr rootsResponse
	if err := json.NewDecoder(res.Body).Decode(&rr); err != nil {
		t.Fatal(err)
	}
	if len(rr.Roots) != 1 || rr.Roots[0].ID != "r1" {
		t.Fatalf("roots after close = %+v, want [r1]", rr.Roots)
	}
	if rr.ActiveID != "r1" {
		t.Fatalf("activeId after close = %q, want r1", rr.ActiveID)
	}
	if hub.LiveFor("r2") != nil {
		t.Fatal("r2 still in hub after close")
	}
}

func TestRootScopedOpsAndStatus(t *testing.T) {
	dir := t.TempDir()
	ops1 := make(chan protocol.Op, 4)
	ops2 := make(chan protocol.Op, 4)
	live1 := NewLive("r1", "/a", nil, ops1)
	live2 := NewLive("r2", "/b", nil, ops2)
	defer live1.Close()
	defer live2.Close()
	live2.Publish(protocol.AgentSelected{Name: "plan"})

	hub := NewLiveHub(nil, nil)
	hub.Add("r1", live1)
	hub.Add("r2", live2)

	srv, err := New(Options{Auth: true, Token: "t", SessionDir: dir, LiveHub: hub})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	// Status scoped to r2 via ?root=
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/status?root=r2", nil)
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var st StatusSnapshot
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	if st.Agent != "plan" {
		t.Fatalf("agent = %q, want plan", st.Agent)
	}

	// Ops scoped to r1
	body := `{"type":"user.input","data":{"text":"to-r1"}}`
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/v1/ops?root=r1", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set("Content-Type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	select {
	case op := <-ops1:
		ui, ok := op.(protocol.UserInput)
		if !ok || ui.Text != "to-r1" {
			t.Fatalf("op = %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for op on r1")
	}
	// r2 should be empty
	select {
	case op := <-ops2:
		t.Fatalf("unexpected op on r2: %#v", op)
	default:
	}
}
