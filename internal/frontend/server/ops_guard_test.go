package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/trust/audit"
)

type memAuditor struct {
	mu   sync.Mutex
	recs []ServeOpPayload
}

func (m *memAuditor) Record(family string, sessionID, turnID, toolCallID, chainID string, payload any) error {
	if family != audit.FamilyServeOp {
		return nil
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var p ServeOpPayload
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	m.mu.Lock()
	m.recs = append(m.recs, p)
	m.mu.Unlock()
	return nil
}

func (m *memAuditor) payloads() []ServeOpPayload {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ServeOpPayload, len(m.recs))
	copy(out, m.recs)
	return out
}

func TestOpLimiterBurstAndRefill(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	lim := newOpLimiter(10, 2, func() time.Time { return now })
	if !lim.allow("1.2.3.4") || !lim.allow("1.2.3.4") {
		t.Fatal("burst should allow 2")
	}
	if lim.allow("1.2.3.4") {
		t.Fatal("third should be denied")
	}
	// Other IP independent.
	if !lim.allow("9.9.9.9") {
		t.Fatal("other IP should have its own burst")
	}
	now = now.Add(200 * time.Millisecond) // +2 tokens at 10/s
	if !lim.allow("1.2.3.4") {
		t.Fatal("want refill allow")
	}
}

func TestOpsReadOnlyHTTP(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("ro1", "/cwd", nil, ops)
	defer live.Close()
	aud := &memAuditor{}
	srv := mustServer(t, Options{
		Auth: true, Token: "t", SessionDir: t.TempDir(), Live: live,
		ReadOnly: true, Audit: aud,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/ops", strings.NewReader(`{"type":"interrupt"}`))
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.StatusCode)
	}
	select {
	case op := <-ops:
		t.Fatalf("unexpected op: %#v", op)
	default:
	}
	got := aud.payloads()
	if len(got) != 1 || got[0].Outcome != opOutcomeReadOnly || got[0].OpType != "interrupt" {
		t.Fatalf("audit = %+v", got)
	}
	if got[0].Channel != "http" || got[0].SourceIP == "" {
		t.Fatalf("audit fields = %+v", got[0])
	}
}

func TestOpsRateLimitHTTP(t *testing.T) {
	ops := make(chan protocol.Op, 8)
	live := NewLive("rl1", "/cwd", nil, ops)
	defer live.Close()
	aud := &memAuditor{}
	now := time.Unix(1_000_000, 0).UTC()
	srv := mustServer(t, Options{
		Auth: true, Token: "t", SessionDir: t.TempDir(), Live: live,
		Audit: aud, OpsRate: 1, OpsBurst: 1,
		OpsClock: func() time.Time { return now },
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	post := func() int {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/ops", strings.NewReader(`{"type":"interrupt"}`))
		req.Header.Set("Authorization", "Bearer t")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	if code := post(); code != http.StatusOK {
		t.Fatalf("first = %d, want 200", code)
	}
	if code := post(); code != http.StatusTooManyRequests {
		t.Fatalf("second = %d, want 429", code)
	}
	// Drain one op from first success.
	select {
	case <-ops:
	case <-time.After(time.Second):
		t.Fatal("no op")
	}
	var sawLimited bool
	for _, p := range aud.payloads() {
		if p.Outcome == opOutcomeRateLimited {
			sawLimited = true
		}
	}
	if !sawLimited {
		t.Fatalf("want rate_limited audit, got %+v", aud.payloads())
	}
}

func TestOpsAuditOK(t *testing.T) {
	ops := make(chan protocol.Op, 1)
	live := NewLive("ok1", "/cwd", nil, ops)
	defer live.Close()
	aud := &memAuditor{}
	srv := mustServer(t, Options{
		Auth: true, Token: "t", SessionDir: t.TempDir(), Live: live, Audit: aud,
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/v1/ops", strings.NewReader(`{"type":"interrupt"}`))
	req.Header.Set("Authorization", "Bearer t")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	got := aud.payloads()
	if len(got) != 1 || got[0].Outcome != opOutcomeOK || got[0].OpType != "interrupt" {
		t.Fatalf("audit = %+v", got)
	}
}
