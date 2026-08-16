package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestBootstrapAdvertisesTeamControl(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("root-tc", t.TempDir(), nil, ops)
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var boot bootstrapResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if !boot.Capabilities.TeamControl {
		t.Fatalf("teamControl not set: %+v", boot.Capabilities)
	}
	want := map[string]bool{}
	for _, name := range protocol.TeamControlOpNames() {
		want[name] = false
	}
	for _, op := range boot.ProtocolOps {
		if _, ok := want[op]; ok {
			want[op] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("protocolOps missing %s: %v", name, boot.ProtocolOps)
		}
	}
}

func TestAttachOnlyBootstrapOmitsTeamControl(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/bootstrap", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	var boot bootstrapResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if boot.Capabilities.TeamControl || boot.Capabilities.Team {
		t.Fatalf("attach-only should not advertise team control: %+v", boot.Capabilities)
	}
	for _, op := range boot.ProtocolOps {
		if strings.HasPrefix(op, "team.") {
			t.Fatalf("attach-only protocolOps includes %s", op)
		}
	}
}

func TestTeamControlHTTPCrossRootDenied(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("root-a", t.TempDir(), nil, ops)
	// Drain ops so Submit does not block if engine is absent — we deny before submit.
	go func() {
		for range ops {
		}
	}()
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"type": protocol.OpTeamBoardCreate,
		"data": map[string]any{
			"rootSessionId":  "other-root",
			"idempotencyKey": "k1",
			"title":          "x",
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/ops", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var errBody opErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.Code != protocol.ErrTeamCrossRoot {
		t.Fatalf("err=%+v", errBody)
	}
}

func TestTeamControlHTTPReadOnly(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("root-ro", t.TempDir(), nil, ops)
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"type": protocol.OpTeamBoardCreate,
		"data": map[string]any{
			"idempotencyKey": "k-ro",
			"title":          "x",
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/ops", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestTeamControlHTTPAttachOnly(t *testing.T) {
	srv, err := New(Options{SessionDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"type": protocol.OpTeamBoardCreate,
		"data": map[string]any{
			"idempotencyKey": "k-ao",
			"title":          "x",
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/ops", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "attach_only") {
		t.Fatalf("body=%s", rec.Body.String())
	}
}

func TestTeamControlHTTPSuccessPath(t *testing.T) {
	// Bridge: Live.Submit → fake engine loop that replies on team ops.
	ops := make(chan protocol.Op, 8)
	live := NewLive("root-ok", t.TempDir(), nil, ops)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case op, ok := <-ops:
				if !ok {
					return
				}
				if !protocol.IsTeamControlOp(op) {
					continue
				}
				out := protocol.TeamOpOutcome{OK: true, TaskID: "t1", Version: 1}
				if ch := protocol.TeamControlReply(op); ch != nil {
					ch <- out
				}
			}
		}
	}()
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"type": protocol.OpTeamBoardCreate,
		"data": map[string]any{
			"rootSessionId":  "root-ok",
			"idempotencyKey": "k-ok",
			"title":          "hello",
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/ops", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var okBody opOKResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &okBody); err != nil {
		t.Fatal(err)
	}
	if !okBody.OK || okBody.TaskID != "t1" {
		t.Fatalf("ok=%+v", okBody)
	}
}

func TestTeamControlHTTPConflictMaps409(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	live := NewLive("root-cf", t.TempDir(), nil, ops)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case op, ok := <-ops:
				if !ok {
					return
				}
				if ch := protocol.TeamControlReply(op); ch != nil {
					ch <- protocol.TeamOpOutcome{
						OK: false, Code: protocol.ErrTeamConflict, Error: protocol.ErrTeamConflict, CurrentVersion: 7,
					}
				}
			}
		}
	}()
	srv, err := New(Options{SessionDir: t.TempDir(), Live: live})
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"type": protocol.OpTeamBoardClaim,
		"data": map[string]any{
			"idempotencyKey":  "k-cf",
			"taskId":          "t1",
			"expectedVersion": 1,
		},
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/ops", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	// Give the loop a moment.
	time.Sleep(10 * time.Millisecond)
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var errBody opErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatal(err)
	}
	if errBody.CurrentVersion != 7 {
		t.Fatalf("err=%+v", errBody)
	}
}
