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

	"github.com/jonathanung/strike/internal/daemon/rootstart"
	"github.com/jonathanung/strike/internal/daemon/trigger"
)

func TestTriggersHTTPAuthHMACAndList(t *testing.T) {
	dir := t.TempDir()
	store, err := trigger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	launch := &recordingLaunch{}
	svc := trigger.New(store, launch)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return now })
	srv := newTriggerServer(t, svc, true)
	h := srv.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/triggers", nil))
	if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "{") {
		t.Fatalf("unauth list = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/triggers", bytes.NewReader([]byte(`{"name":"ci","kind":"webhook"}`)))
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		trigger.View
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" || created.ID == "" {
		t.Fatalf("created = %+v", created)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/triggers", nil)
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), created.Secret) {
		t.Fatalf("list leaked or failed: %d %s", rec.Code, rec.Body.String())
	}

	body := []byte(`{"hello":"strike"}`)
	ts := strconv.FormatInt(now.Unix(), 10)
	hook := httptest.NewRequest(http.MethodPost, "/hooks/"+created.ID, bytes.NewReader(body))
	hook.Header.Set("X-Strike-Timestamp", ts)
	hook.Header.Set("X-Strike-Delivery", "http-1")
	hook.Header.Set("X-Strike-Signature", trigger.SignWebhook(created.Secret, ts, body))
	hook.Header.Set("X-Strike-Event", "deploy")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, hook)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"sessionId":"sess-http"`) {
		t.Fatalf("hook = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), created.Secret) {
		t.Fatal("hook response contains secret")
	}

	bad := httptest.NewRequest(http.MethodPost, "/hooks/"+created.ID, bytes.NewReader(body))
	bad.Header.Set("X-Strike-Timestamp", ts)
	bad.Header.Set("X-Strike-Delivery", "http-2")
	bad.Header.Set("X-Strike-Signature", "sha256=0000000000000000000000000000000000000000000000000000000000000000")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, bad)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "signature rejected") {
		t.Fatalf("bad hook = %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/triggers/"+created.ID+"/deliveries", nil)
	req.Header.Set("Authorization", "Bearer t")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "rejected") {
		t.Fatalf("deliveries = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), created.Secret) {
		t.Fatal("deliveries leaked secret")
	}
}

func TestTriggersReadOnlyAndMissing(t *testing.T) {
	dir := t.TempDir()
	srv, err := New(Options{SessionDir: dir, ReadOnly: true, Triggers: trigger.New(mustStore(t, dir), &recordingLaunch{})})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/triggers", bytes.NewReader([]byte(`{"name":"a","kind":"webhook"}`))))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("readonly create = %d", rec.Code)
	}
	bare, err := New(Options{SessionDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	bare.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/triggers", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing = %d", rec.Code)
	}
}

type recordingLaunch struct{}

func (recordingLaunch) StartRoot(ctx context.Context, req rootstart.Request) (rootstart.Result, error) {
	return rootstart.Result{SessionID: "sess-http", Created: true, PermissionMode: "plan"}, nil
}

func newTriggerServer(t *testing.T, svc *trigger.Service, auth bool) *Server {
	t.Helper()
	opts := Options{SessionDir: t.TempDir(), Triggers: svc}
	if auth {
		opts.Auth = true
		opts.Token = "t"
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func mustStore(t *testing.T, dir string) *trigger.Store {
	t.Helper()
	st, err := trigger.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
