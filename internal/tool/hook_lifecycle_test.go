package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestLifecycleVocabularyComplete(t *testing.T) {
	t.Parallel()
	if LifecycleVocabularyVersion != "1.0.0" {
		t.Fatalf("version = %q", LifecycleVocabularyVersion)
	}
	want := map[string]bool{
		HookEventSessionStart: true, HookEventSessionResume: true, HookEventSessionEnd: true,
		HookEventTurnStart: true, HookEventTurnEnd: true,
		HookEventProviderAttempt: true, HookEventProviderRetry: true,
		HookEventPermissionResolution: true, HookEventCompaction: true,
		HookEventPhaseTransition: true, HookEventChildLifecycle: true,
		HookEventVerificationGate: true,
		HookEventPreToolUse:       true, HookEventPostToolUse: true,
	}
	if len(KnownLifecycleEvents) != len(want) {
		t.Fatalf("KnownLifecycleEvents len=%d want %d", len(KnownLifecycleEvents), len(want))
	}
	for _, ev := range KnownLifecycleEvents {
		if !want[ev] || !ValidLifecycleEvent(ev) {
			t.Fatalf("missing/invalid %q", ev)
		}
	}
	if ValidLifecycleEvent("tool.pre") {
		t.Fatal("unknown event accepted")
	}
}

func TestHookCanBlockPolicy(t *testing.T) {
	t.Parallel()
	if !HookCanBlock(HookEventPreToolUse) || !HookCanBlock(HookEventPostToolUse) {
		t.Fatal("tool events should shell-block")
	}
	if !DeclarativeBlockAllowed(HookEventPreToolUse) {
		t.Fatal("declarative block on pre")
	}
	if DeclarativeBlockAllowed(HookEventPostToolUse) {
		t.Fatal("declarative block must not allow post")
	}
	for _, ev := range []string{
		HookEventSessionStart, HookEventTurnStart, HookEventProviderAttempt,
		HookEventPermissionResolution, HookEventCompaction, HookEventPhaseTransition,
		HookEventChildLifecycle, HookEventVerificationGate,
	} {
		if HookCanBlock(ev) {
			t.Fatalf("%s must be observe-only for shell block", ev)
		}
		if DeclarativeBlockAllowed(ev) {
			t.Fatalf("%s must not allow declarative block", ev)
		}
	}
}

func TestBoundHookPayloadRedactsAndTruncates(t *testing.T) {
	t.Parallel()
	secret := "sk-ant-api03-" + strings.Repeat("a", 40)
	huge := strings.Repeat("x", HookPayloadMaxField+500)
	p := BoundHookPayload(HookPayload{
		Event:      HookEventPostToolUse,
		SessionID:  "s1",
		ToolOutput: "token=" + secret + " " + huge,
		ToolInput:  json.RawMessage(`{"command":"export KEY=` + secret + `"}`),
		Detail:     "Bearer " + secret,
	})
	if p.SchemaVersion != LifecycleVocabularyVersion {
		t.Fatalf("schema = %q", p.SchemaVersion)
	}
	if strings.Contains(p.ToolOutput, "sk-ant-") || strings.Contains(p.Detail, secret) {
		t.Fatalf("secret leaked: out=%q detail=%q", p.ToolOutput, p.Detail)
	}
	if !strings.Contains(p.ToolOutput, "[REDACTED") && !strings.Contains(p.ToolOutput, "REDACTED") {
		// redact may use various placeholders
		if strings.Contains(p.ToolOutput, secret) {
			t.Fatal("secret still present")
		}
	}
	if len([]rune(p.ToolOutput)) > HookPayloadMaxField+20 {
		t.Fatalf("tool_output not bounded: %d runes", len([]rune(p.ToolOutput)))
	}
	if strings.Contains(string(p.ToolInput), secret) {
		t.Fatalf("tool_input leaked secret: %s", p.ToolInput)
	}
}

func TestRunHooksObserveOnlyNonZeroFailOpen(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:   HookEventSessionStart,
		Command: `printf 'observed'; exit 2`,
	}}, HookEventSessionStart, HookPayload{
		Event:     HookEventSessionStart,
		SessionID: "s",
	}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allow {
		t.Fatal("session_start must fail-open on non-zero")
	}
	if !strings.Contains(out.Inject, "observed") {
		t.Fatalf("inject=%q", out.Inject)
	}
}

func TestRunHooksOrderingConfigOrder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, err := RunHooks(context.Background(), []HookDef{
		{Event: HookEventTurnStart, Command: `printf 'a'`},
		{Event: HookEventTurnStart, Command: `printf 'b'`},
		{Event: HookEventTurnStart, Command: `printf 'c'`},
	}, HookEventTurnStart, HookPayload{Event: HookEventTurnStart, SessionID: "s"}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Inject != "a\nb\nc" {
		t.Fatalf("order inject=%q", out.Inject)
	}
}

func TestRunHooksCancellation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RunHooks(ctx, []HookDef{{
		Event:   HookEventTurnStart,
		Command: `printf hi`,
	}}, HookEventTurnStart, HookPayload{Event: HookEventTurnStart}, dir, nil)
	if err == nil {
		t.Fatal("want ctx error")
	}
}

func TestRunHooksPayloadIncludesCorrelation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	payload := HookPayload{
		Event:             HookEventProviderAttempt,
		SessionID:         "sess",
		TurnID:            "turn1",
		ProviderRequestID: "req9",
		Attempt:           2,
		CWD:               dir,
		Subject:           "anthropic",
		Status:            "attempt",
		Detail:            "provider=anthropic",
	}
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:   HookEventProviderAttempt,
		Command: `cat`,
	}}, HookEventProviderAttempt, payload, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got HookPayload
	if err := json.Unmarshal([]byte(out.Inject), &got); err != nil {
		t.Fatalf("inject=%q err=%v", out.Inject, err)
	}
	if got.SchemaVersion != LifecycleVocabularyVersion {
		t.Fatalf("schema=%q", got.SchemaVersion)
	}
	if got.TurnID != "turn1" || got.ProviderRequestID != "req9" || got.Attempt != 2 {
		t.Fatalf("corr=%#v", got)
	}
	if got.Subject != "anthropic" || got.Status != "attempt" {
		t.Fatalf("subject/status=%#v", got)
	}
}

func TestRunHooksReplayStableMarshal(t *testing.T) {
	t.Parallel()
	// Same payload → same stdin JSON (ordering of struct fields is stable).
	p := BoundHookPayload(HookPayload{
		Event:     HookEventCompaction,
		SessionID: "s",
		Status:    "trim",
		Detail:    "removed=3 kept=2",
	})
	a, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(BoundHookPayload(HookPayload{
		Event:     HookEventCompaction,
		SessionID: "s",
		Status:    "trim",
		Detail:    "removed=3 kept=2",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("unstable marshal\n%s\n%s", a, b)
	}
}

func TestRunHooksTimeoutStillFailOpenLifecycle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	start := time.Now()
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:     HookEventVerificationGate,
		Command:   `sleep 5`,
		TimeoutMs: 40,
	}}, HookEventVerificationGate, HookPayload{Event: HookEventVerificationGate}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allow {
		t.Fatal("timeout fail-open")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("too slow")
	}
}
