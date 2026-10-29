package trigger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/daemon/rootstart"
)

type fakeLaunch struct {
	mu    sync.Mutex
	calls []rootstart.Request
	err   error
}

func (f *fakeLaunch) StartRoot(_ context.Context, req rootstart.Request) (rootstart.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	if f.err != nil {
		return rootstart.Result{}, f.err
	}
	created := req.ContinueID == ""
	return rootstart.Result{SessionID: "sess-1", Created: created, PermissionMode: "plan"}, nil
}

func (f *fakeLaunch) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeLaunch) last() rootstart.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func newSvc(t *testing.T) (*Service, *fakeLaunch) {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	launch := &fakeLaunch{}
	svc := New(store, launch)
	svc.SetClock(func() time.Time { return time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC) })
	return svc, launch
}

func TestCreateHidesSecretAndDefaultsToPlan(t *testing.T) {
	svc, _ := newSvc(t)
	view, secret, err := svc.Create(Input{Name: "ci", Kind: KindWebhook})
	if err != nil {
		t.Fatal(err)
	}
	if secret == "" || view.SecretSet != true {
		t.Fatalf("secret once = %q set=%v", secret, view.SecretSet)
	}
	if strings.Contains(mustJSON(t, view), secret) {
		t.Fatal("view JSON contains the secret")
	}
	if view.ResolvedPermissionMode != "plan" || view.PermissionMode != "" {
		t.Fatalf("mode stored=%q resolved=%q", view.PermissionMode, view.ResolvedPermissionMode)
	}
	again := svc.List()
	if len(again) != 1 || strings.Contains(mustJSON(t, again), secret) {
		t.Fatalf("list leaked secret: %s", mustJSON(t, again))
	}
	info, err := os.Stat(filepath.Join(svc.store.dir, "triggers.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode = %o, want 600", info.Mode().Perm())
	}
}

func TestWebhookHMACReplayRateAndRedaction(t *testing.T) {
	svc, launch := newSvc(t)
	rate := 2
	view, secret, err := svc.Create(Input{Name: "hook", Kind: KindWebhook, RatePerMinute: &rate})
	if err != nil {
		t.Fatal(err)
	}
	now := svc.now()
	body := []byte(`{"note":"` + secret + ` ghp_1234567890123456789012345678901234"}`)
	sign := func(ts, id string, payload []byte) Hook {
		return Hook{
			ID:         view.ID,
			Body:       payload,
			Timestamp:  ts,
			DeliveryID: id,
			Signature:  SignWebhook(secret, ts, payload),
			Event:      "deploy",
		}
	}
	ok := svc.Receive(context.Background(), sign(unix(now), "d1", body))
	if ok.Status != 202 || ok.SessionID != "sess-1" || launch.count() != 1 {
		t.Fatalf("accept = %+v launches=%d", ok, launch.count())
	}
	if launch.last().PermissionMode != "" || launch.last().Source != rootstart.SourceTrigger {
		t.Fatalf("request = %+v", launch.last())
	}
	if strings.Contains(launch.last().Prompt, secret) || strings.Contains(launch.last().Prompt, "ghp_") {
		t.Fatalf("prompt leaked secret material: %q", launch.last().Prompt)
	}
	dup := svc.Receive(context.Background(), sign(unix(now), "d1", body))
	if dup.Status != 200 || !dup.Duplicate || launch.count() != 1 {
		t.Fatalf("replay = %+v launches=%d", dup, launch.count())
	}
	same := svc.Receive(context.Background(), sign(unix(now), "d2", body))
	if same.Status != 200 || !same.Duplicate || launch.count() != 1 {
		t.Fatalf("dedupe = %+v launches=%d", same, launch.count())
	}
	other := []byte(`{"note":"different"}`)
	third := svc.Receive(context.Background(), sign(unix(now), "d3", other))
	if third.Status != 202 || launch.count() != 2 {
		t.Fatalf("second event = %+v launches=%d", third, launch.count())
	}
	limited := svc.Receive(context.Background(), sign(unix(now), "d4", []byte(`{"note":"three"}`)))
	if limited.Status != 429 || launch.count() != 2 {
		t.Fatalf("rate = %+v launches=%d", limited, launch.count())
	}
	bad := sign(unix(now), "d5", other)
	bad.Signature = SignWebhook("not-the-secret-value", unix(now), other)
	rejected := svc.Receive(context.Background(), bad)
	if rejected.Status != 401 || launch.count() != 2 {
		t.Fatalf("bad sig = %+v launches=%d", rejected, launch.count())
	}
	stale := sign(unix(now.Add(-10*time.Minute)), "d6", []byte(`{"note":"old"}`))
	if got := svc.Receive(context.Background(), stale); got.Status != 401 || launch.count() != 2 {
		t.Fatalf("stale = %+v", got)
	}
	log := string(mustRead(t, filepath.Join(svc.store.dir, "triggers", view.ID+".jsonl")))
	if strings.Contains(log, secret) || strings.Contains(log, "ghp_") {
		t.Fatalf("delivery log leaked secret material")
	}
	rows, err := svc.Deliveries(view.ID)
	if err != nil || len(rows) == 0 {
		t.Fatalf("deliveries: %v %d", err, len(rows))
	}
	if strings.Contains(mustJSON(t, rows), secret) {
		t.Fatal("delivery view leaked secret")
	}
}

func TestGitHubPingAndIssue(t *testing.T) {
	svc, launch := newSvc(t)
	view, secret, err := svc.Create(Input{Name: "gh", Kind: KindGitHub})
	if err != nil {
		t.Fatal(err)
	}
	ping := []byte(`{"zen":"keep it logically awesome"}`)
	pingRes := svc.Receive(context.Background(), Hook{
		ID:         view.ID,
		Body:       ping,
		DeliveryID: "ping-1",
		Signature:  SignGitHub(secret, ping),
		Event:      "ping",
	})
	if pingRes.Outcome != OutcomeIgnored || launch.count() != 0 {
		t.Fatalf("ping = %+v launches=%d", pingRes, launch.count())
	}
	issue := []byte(`{"action":"opened","repository":{"full_name":"acme/widgets"},"issue":{"number":7,"title":"Broken","body":"look here","html_url":"https://example.test/7"},"sender":{"login":"ada"}}`)
	got := svc.Receive(context.Background(), Hook{
		ID:         view.ID,
		Body:       issue,
		DeliveryID: "issue-1",
		Signature:  SignGitHub(secret, issue),
		Event:      "issues",
	})
	if got.Status != 202 || launch.count() != 1 {
		t.Fatalf("issue = %+v launches=%d", got, launch.count())
	}
	prompt := launch.last().Prompt
	if !strings.Contains(prompt, "acme/widgets") || !strings.Contains(prompt, "#7") || !strings.Contains(prompt, "Broken") {
		t.Fatalf("prompt = %q", prompt)
	}
	filtered, secret2, err := svc.Create(Input{Name: "prs", Kind: KindGitHub, Events: []string{"pull_request"}})
	if err != nil {
		t.Fatal(err)
	}
	skip := svc.Receive(context.Background(), Hook{
		ID:         filtered.ID,
		Body:       issue,
		DeliveryID: "issue-2",
		Signature:  SignGitHub(secret2, issue),
		Event:      "issues",
	})
	if skip.Outcome != OutcomeIgnored || skip.Status != 200 || launch.count() != 1 {
		t.Fatalf("filtered = %+v launches=%d", skip, launch.count())
	}
}

func TestContinueAndDisabledAndChannel(t *testing.T) {
	svc, launch := newSvc(t)
	view, secret, err := svc.Create(Input{
		Name:              "stay",
		Kind:              KindWebhook,
		ContinueSessionID: "root-9",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("hello")
	res := svc.Receive(context.Background(), Hook{
		ID:         view.ID,
		Body:       body,
		Timestamp:  unix(svc.now()),
		DeliveryID: "c1",
		Signature:  SignWebhook(secret, unix(svc.now()), body),
	})
	if res.Status != 202 || launch.last().ContinueID != "root-9" {
		t.Fatalf("continue = %+v req=%+v", res, launch.last())
	}
	off := false
	if _, _, err := svc.Update(view.ID, Input{Name: "stay", Kind: KindWebhook, Enabled: &off, ContinueSessionID: "root-9"}); err != nil {
		t.Fatal(err)
	}
	disabled := svc.Receive(context.Background(), Hook{
		ID:         view.ID,
		Body:       []byte("next"),
		Timestamp:  unix(svc.now()),
		DeliveryID: "c2",
		Signature:  SignWebhook(secret, unix(svc.now()), []byte("next")),
	})
	if disabled.Status != 403 || launch.count() != 1 {
		t.Fatalf("disabled = %+v launches=%d", disabled, launch.count())
	}

	ch, _, err := svc.Create(Input{Name: "chat", Kind: KindChannel, Adapter: "slack", Channel: "ops"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Register(staticAdapter("slack")); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Accept(context.Background(), Message{
		Adapter: "slack", Channel: "ops", MessageID: "m1", Sender: "ada", Text: "ship it",
	})
	if err != nil || len(got) != 1 || got[0].Status != 202 {
		t.Fatalf("accept = %+v err=%v", got, err)
	}
	if !strings.Contains(launch.last().Prompt, "ship it") || launch.last().PermissionMode != "" {
		t.Fatalf("channel prompt = %+v", launch.last())
	}
	again, err := svc.Accept(context.Background(), Message{
		Adapter: "slack", Channel: "ops", MessageID: "m1", Sender: "ada", Text: "ship it",
	})
	if err != nil || !again[0].Duplicate {
		t.Fatalf("channel replay = %+v", again)
	}
	miss, err := svc.Accept(context.Background(), Message{
		Adapter: "slack", Channel: "other", MessageID: "m2", Sender: "ada", Text: "nope",
	})
	if err != nil || len(miss) != 0 {
		t.Fatalf("channel filter = %+v", miss)
	}
	if ch.HookPath == "" || ch.SecretSet {
		t.Fatalf("channel view = %+v", ch)
	}
}

func TestReplayUsesStoredPrompt(t *testing.T) {
	svc, launch := newSvc(t)
	view, secret, err := svc.Create(Input{Name: "hook", Kind: KindWebhook})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("first body")
	res := svc.Receive(context.Background(), Hook{
		ID:         view.ID,
		Body:       body,
		Timestamp:  unix(svc.now()),
		DeliveryID: "r1",
		Signature:  SignWebhook(secret, unix(svc.now()), body),
	})
	if res.Status != 202 {
		t.Fatalf("seed = %+v", res)
	}
	replay := svc.Replay(context.Background(), view.ID, "r1")
	if replay.Outcome != OutcomeReplayed || replay.SessionID != "sess-1" || launch.count() != 2 {
		t.Fatalf("replay = %+v launches=%d", replay, launch.count())
	}
	if strings.Contains(launch.last().Prompt, secret) {
		t.Fatal("replay prompt contains secret")
	}
	missing := svc.Replay(context.Background(), view.ID, "nope")
	if missing.Status != 404 {
		t.Fatalf("missing replay = %+v", missing)
	}
}

func TestBadSignatureDoesNotClaimDeliveryID(t *testing.T) {
	svc, launch := newSvc(t)
	view, secret, err := svc.Create(Input{Name: "hook", Kind: KindWebhook})
	if err != nil {
		t.Fatal(err)
	}
	now := svc.now()
	body := []byte(`{"note":"real"}`)
	bad := svc.Receive(context.Background(), Hook{
		ID:         view.ID,
		Body:       body,
		Timestamp:  unix(now),
		DeliveryID: "shared",
		Signature:  SignWebhook("other-secret-value-0123", unix(now), body),
		Event:      "deploy",
	})
	if bad.Status != 401 || launch.count() != 0 {
		t.Fatalf("bad sig = %+v launches=%d", bad, launch.count())
	}
	good := svc.Receive(context.Background(), Hook{
		ID:         view.ID,
		Body:       body,
		Timestamp:  unix(now),
		DeliveryID: "shared",
		Signature:  SignWebhook(secret, unix(now), body),
		Event:      "deploy",
	})
	if good.Status != 202 || launch.count() != 1 {
		t.Fatalf("real delivery = %+v launches=%d", good, launch.count())
	}
	again := svc.Receive(context.Background(), Hook{
		ID:         view.ID,
		Body:       []byte(`{"note":"other"}`),
		Timestamp:  unix(now),
		DeliveryID: "shared",
		Signature:  SignWebhook(secret, unix(now), []byte(`{"note":"other"}`)),
		Event:      "deploy",
	})
	if again.Status != 200 || !again.Duplicate || launch.count() != 1 {
		t.Fatalf("replay after accept = %+v launches=%d", again, launch.count())
	}
}

func TestRejectFloodDoesNotEvictAcceptedDelivery(t *testing.T) {
	svc, launch := newSvc(t)
	rate := 120
	view, secret, err := svc.Create(Input{Name: "hook", Kind: KindWebhook, RatePerMinute: &rate, DedupeSeconds: intPtr(0)})
	if err != nil {
		t.Fatal(err)
	}
	now := svc.now()
	body := []byte(`{"note":"keep"}`)
	ok := svc.Receive(context.Background(), Hook{
		ID:         view.ID,
		Body:       body,
		Timestamp:  unix(now),
		DeliveryID: "keep",
		Signature:  SignWebhook(secret, unix(now), body),
	})
	if ok.Status != 202 {
		t.Fatalf("seed = %+v", ok)
	}
	for i := 0; i < maxLog+10; i++ {
		payload := []byte(`{"n":` + strconv.Itoa(i) + `}`)
		got := svc.Receive(context.Background(), Hook{
			ID:         view.ID,
			Body:       payload,
			Timestamp:  unix(now),
			DeliveryID: "bad-" + strconv.Itoa(i),
			Signature:  "sha256=0000000000000000000000000000000000000000000000000000000000000000",
		})
		if got.Status != 401 && got.Status != 429 {
			t.Fatalf("flood %d = %+v", i, got)
		}
	}
	restarted := New(svc.store, launch)
	restarted.SetClock(func() time.Time { return now })
	replay := restarted.Receive(context.Background(), Hook{
		ID:         view.ID,
		Body:       []byte(`{"note":"again"}`),
		Timestamp:  unix(now),
		DeliveryID: "keep",
		Signature:  SignWebhook(secret, unix(now), []byte(`{"note":"again"}`)),
	})
	if replay.Status != 200 || !replay.Duplicate || launch.count() != 1 {
		t.Fatalf("restarted replay = %+v launches=%d", replay, launch.count())
	}
	rows := restarted.store.Deliveries(view.ID)
	if len(rows) > maxLog {
		t.Fatalf("log len = %d", len(rows))
	}
	found := false
	for _, row := range rows {
		if row.ID == "keep" && row.Outcome == OutcomeAccepted {
			found = true
		}
	}
	if !found {
		t.Fatal("accepted delivery was evicted by signature failures")
	}
}

func intPtr(n int) *int { return &n }

func TestSignatureTable(t *testing.T) {
	secret := "test-secret-value-012345"
	body := []byte("payload")
	now := time.Unix(1_700_000_000, 0)
	ts := unix(now)
	good := SignWebhook(secret, ts, body)
	cases := []struct {
		name string
		sig  string
		ts   string
		want error
	}{
		{name: "good", sig: good, ts: ts},
		{name: "wrong secret", sig: SignWebhook("other-secret-value-0123", ts, body), ts: ts, want: errBadSignature},
		{name: "short", sig: "sha256=abcd", ts: ts, want: errBadSignature},
		{name: "empty", sig: "", ts: ts, want: errBadSignature},
		{name: "prefix", sig: strings.TrimPrefix(good, "sha256="), ts: ts, want: errBadSignature},
		{name: "stale", sig: SignWebhook(secret, unix(now.Add(-10*time.Minute)), body), ts: unix(now.Add(-10 * time.Minute)), want: errStale},
		{name: "future", sig: SignWebhook(secret, unix(now.Add(2*time.Minute)), body), ts: unix(now.Add(2 * time.Minute)), want: errStale},
		{name: "bad time", sig: good, ts: "soon", want: errMalformed},
	}
	for _, tc := range cases {
		err := verifyWebhook(secret, tc.ts, body, tc.sig, now)
		if tc.want == nil && err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if tc.want != nil && err != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, err, tc.want)
		}
	}
	gh := SignGitHub(secret, body)
	if err := verifyGitHub(secret, body, gh); err != nil {
		t.Fatal(err)
	}
	if err := verifyGitHub(secret, body, gh+"00"); err != errBadSignature {
		t.Fatalf("tampered github sig: %v", err)
	}
	// Different length still returns false and does not panic.
	if macEqual(secret, body, "sha256="+hex.EncodeToString(make([]byte, sha256.Size-1))) {
		t.Fatal("short mac matched")
	}
}

type staticAdapter string

func (a staticAdapter) Name() string { return string(a) }

func unix(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
