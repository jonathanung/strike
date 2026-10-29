package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/daemon/trigger"
	"github.com/jonathanung/strike/internal/protocol"
)

type spawnCtxKey struct{}

// fakeEngine models serve.go's pre-fix wiring: the engine goroutine runs on
// exactly the context the spawn callback is handed, and it only starts
// processing ops once the test releases it — after the HTTP handler has
// returned and the request context has been cancelled. That turns the
// auditor's race (engine processing vs handler return) into a deterministic
// ordering: on the buggy code the engine context is already dead when it is
// released, so the turn can never progress past startup.
type fakeEngine struct {
	ops        chan protocol.Op
	release    chan struct{}
	progressed chan []protocol.Op
	spawnCtx   context.Context
}

// spawnRecorder records one fakeEngine per spawn, so the replay subtest can
// assert on the replay-spawned session alone.
type spawnRecorder struct {
	engines []*fakeEngine
}

func (r *spawnRecorder) spawn(hub *LiveHub) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		e := &fakeEngine{
			ops:        make(chan protocol.Op, 4),
			release:    make(chan struct{}),
			progressed: make(chan []protocol.Op, 1),
			spawnCtx:   ctx,
		}
		r.engines = append(r.engines, e)
		id := "root-ctx-" + strconv.Itoa(len(r.engines))
		live := NewLive(id, "/work", nil, e.ops)
		hub.Add(id, live)
		ops := e.ops
		go func() {
			<-e.release
			if ctx.Err() != nil {
				e.progressed <- nil
				return
			}
			var got []protocol.Op
			for len(got) < 2 {
				select {
				case <-ctx.Done():
					e.progressed <- nil
					return
				case op := <-ops:
					got = append(got, op)
				}
			}
			e.progressed <- got
		}()
		return id, nil
	}
}

func (r *spawnRecorder) last(t *testing.T) *fakeEngine {
	t.Helper()
	if len(r.engines) == 0 {
		t.Fatal("no spawn recorded")
	}
	return r.engines[len(r.engines)-1]
}

// assertProgressesAfterCancel cancels the request context (what net/http
// does when the hook response returns), then releases the engine and
// requires the spawned session to consume both startup ops — the
// permission-mode application and the user prompt — i.e. to progress past
// its startup events and run its turn.
func (e *fakeEngine) assertProgressesAfterCancel(t *testing.T, cancel context.CancelFunc) {
	t.Helper()
	cancel()
	if v := e.spawnCtx.Value(spawnCtxKey{}); v != "kept" {
		t.Fatalf("spawn context lost its values: %v", v)
	}
	close(e.release)
	select {
	case got := <-e.progressed:
		if len(got) != 2 {
			t.Fatalf("session did not progress after request context was cancelled: ops=%v", got)
		}
		if _, ok := got[0].(protocol.SetPermissionMode); !ok {
			t.Fatalf("first op = %#v, want SetPermissionMode", got[0])
		}
		ui, ok := got[1].(protocol.UserInput)
		if !ok || strings.TrimSpace(ui.Text) == "" {
			t.Fatalf("second op = %#v, want UserInput with the prompt", got[1])
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session stalled after request context was cancelled (no turn progress)")
	}
}

func newSpawnCtxHarness(t *testing.T) (*spawnRecorder, *trigger.Service, *Server, time.Time) {
	t.Helper()
	rec := &spawnRecorder{}
	var hub *LiveHub
	hub = NewLiveHub(nil, nil)
	hub.SetUnattendedSpawn(rec.spawn(hub))
	store, err := trigger.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := trigger.New(store, NewRootLauncher(hub))
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return now })
	srv := newTriggerServer(t, svc, true)
	return rec, svc, srv, now
}

func createTriggerViaHTTP(t *testing.T, srv *Server, kind string) (id, secret string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/triggers", bytes.NewReader([]byte(`{"name":"ci","kind":"`+kind+`"}`)))
	req.Header.Set("Authorization", "Bearer t")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Secret == "" {
		t.Fatalf("created = %+v", created)
	}
	return created.ID, created.Secret
}

// TestHookSpawnSurvivesRequestContextCancel is the regression test for the
// #1467 audit defect: a hook-fired root inherited the HTTP request context,
// so its engine was cancelled when the hook response returned.
func TestHookSpawnSurvivesRequestContextCancel(t *testing.T) {
	t.Run("webhook", func(t *testing.T) {
		spawns, _, srv, now := newSpawnCtxHarness(t)
		id, secret := createTriggerViaHTTP(t, srv, "webhook")

		body := []byte(`{"hello":"strike"}`)
		ts := strconv.FormatInt(now.Unix(), 10)
		reqCtx, cancel := context.WithCancel(context.WithValue(context.Background(), spawnCtxKey{}, "kept"))
		hook := httptest.NewRequest(http.MethodPost, "/hooks/"+id, bytes.NewReader(body)).WithContext(reqCtx)
		hook.Header.Set("X-Strike-Timestamp", ts)
		hook.Header.Set("X-Strike-Delivery", "ctx-1")
		hook.Header.Set("X-Strike-Signature", trigger.SignWebhook(secret, ts, body))
		hook.Header.Set("X-Strike-Event", "deploy")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, hook)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("hook = %d %s", rec.Code, rec.Body.String())
		}
		spawns.last(t).assertProgressesAfterCancel(t, cancel)
	})

	t.Run("github", func(t *testing.T) {
		spawns, _, srv, _ := newSpawnCtxHarness(t)
		id, secret := createTriggerViaHTTP(t, srv, "github")

		body := []byte(`{"action":"opened","issue":{"number":7,"title":"bug","body":"broken"},"repository":{"full_name":"jonathanung/strike"},"sender":{"login":"octo"}}`)
		reqCtx, cancel := context.WithCancel(context.WithValue(context.Background(), spawnCtxKey{}, "kept"))
		hook := httptest.NewRequest(http.MethodPost, "/hooks/"+id, bytes.NewReader(body)).WithContext(reqCtx)
		hook.Header.Set("X-GitHub-Delivery", "ctx-gh-1")
		hook.Header.Set("X-Hub-Signature-256", trigger.SignGitHub(secret, body))
		hook.Header.Set("X-GitHub-Event", "issues")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, hook)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("github hook = %d %s", rec.Code, rec.Body.String())
		}
		spawns.last(t).assertProgressesAfterCancel(t, cancel)
	})

	t.Run("test", func(t *testing.T) {
		spawns, _, srv, _ := newSpawnCtxHarness(t)
		id, _ := createTriggerViaHTTP(t, srv, "webhook")

		reqCtx, cancel := context.WithCancel(context.WithValue(context.Background(), spawnCtxKey{}, "kept"))
		req := httptest.NewRequest(http.MethodPost, "/v1/triggers/"+id+"/test",
			bytes.NewReader([]byte(`{"event":"deploy","deliveryId":"ctx-test-1","body":{"hello":"strike"}}`))).WithContext(reqCtx)
		req.Header.Set("Authorization", "Bearer t")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("test delivery = %d %s", rec.Code, rec.Body.String())
		}
		spawns.last(t).assertProgressesAfterCancel(t, cancel)
	})

	t.Run("replay", func(t *testing.T) {
		spawns, _, srv, now := newSpawnCtxHarness(t)
		id, secret := createTriggerViaHTTP(t, srv, "webhook")

		// First delivery lands (its engine is left unreleased; replay is
		// what spawns the session under test).
		body := []byte(`{"hello":"strike"}`)
		ts := strconv.FormatInt(now.Unix(), 10)
		hook := httptest.NewRequest(http.MethodPost, "/hooks/"+id, bytes.NewReader(body))
		hook.Header.Set("X-Strike-Timestamp", ts)
		hook.Header.Set("X-Strike-Delivery", "ctx-replay-src")
		hook.Header.Set("X-Strike-Signature", trigger.SignWebhook(secret, ts, body))
		hook.Header.Set("X-Strike-Event", "deploy")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, hook)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("hook = %d %s", rec.Code, rec.Body.String())
		}

		reqCtx, cancel := context.WithCancel(context.WithValue(context.Background(), spawnCtxKey{}, "kept"))
		req := httptest.NewRequest(http.MethodPost, "/v1/triggers/"+id+"/replay",
			bytes.NewReader([]byte(`{"deliveryId":"ctx-replay-src"}`))).WithContext(reqCtx)
		req.Header.Set("Authorization", "Bearer t")
		rec = httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("replay = %d %s", rec.Code, rec.Body.String())
		}
		spawns.last(t).assertProgressesAfterCancel(t, cancel)
	})
}
