package tool

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/sandbox"
)

func TestRunHooksAllowInject(t *testing.T) {
	dir := t.TempDir()
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:   HookEventPreToolUse,
		Command: `printf 'note from hook'`,
	}}, HookEventPreToolUse, HookPayload{
		Event:    HookEventPreToolUse,
		ToolName: "bash",
	}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allow {
		t.Fatalf("allow=false inject=%q", out.Inject)
	}
	if out.Inject != "note from hook" {
		t.Fatalf("inject=%q", out.Inject)
	}
}

func TestRunHooksBlockExitCode(t *testing.T) {
	dir := t.TempDir()
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:   HookEventPreToolUse,
		Command: `printf 'no force push' >&1; exit 2`,
	}}, HookEventPreToolUse, HookPayload{
		Event:    HookEventPreToolUse,
		ToolName: "bash",
	}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Allow {
		t.Fatal("want block")
	}
	if out.Inject != "no force push" {
		t.Fatalf("inject=%q", out.Inject)
	}
}

func TestRunHooksStdinJSON(t *testing.T) {
	dir := t.TempDir()
	payload := HookPayload{
		Event:      HookEventPreToolUse,
		SessionID:  "sess1",
		CWD:        dir,
		ToolName:   "bash",
		ToolCallID: "c1",
		ToolInput:  json.RawMessage(`{"command":"ls"}`),
	}
	// Echo stdin and exit 0; assert payload round-trips via inject.
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:   HookEventPreToolUse,
		Command: `cat`,
	}}, HookEventPreToolUse, payload, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allow {
		t.Fatal("blocked")
	}
	var got HookPayload
	if err := json.Unmarshal([]byte(out.Inject), &got); err != nil {
		t.Fatalf("inject not JSON: %q err=%v", out.Inject, err)
	}
	if got.SessionID != "sess1" || got.ToolName != "bash" || got.ToolCallID != "c1" {
		t.Fatalf("payload = %#v", got)
	}
	if string(got.ToolInput) != `{"command":"ls"}` {
		t.Fatalf("tool_input = %s", got.ToolInput)
	}
}

func TestRunHooksMatcher(t *testing.T) {
	dir := t.TempDir()
	defs := []HookDef{{
		Event:   HookEventPreToolUse,
		Command: `printf blocked; exit 1`,
		Matcher: "write",
	}}
	out, err := RunHooks(context.Background(), defs, HookEventPreToolUse, HookPayload{
		Event: HookEventPreToolUse, ToolName: "bash",
	}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allow || out.Inject != "" {
		t.Fatalf("bash should not match write matcher: %#v", out)
	}
	out, err = RunHooks(context.Background(), defs, HookEventPreToolUse, HookPayload{
		Event: HookEventPreToolUse, ToolName: "write",
	}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Allow {
		t.Fatal("write should be blocked")
	}
}

func TestRunHooksTimeoutFailClosedDefault(t *testing.T) {
	// pre_tool_use defaults to fail-closed on timeout (#1031).
	dir := t.TempDir()
	start := time.Now()
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:     HookEventPreToolUse,
		Command:   `sleep 5`,
		TimeoutMs: 50,
	}}, HookEventPreToolUse, HookPayload{Event: HookEventPreToolUse, ToolName: "bash"}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Allow {
		t.Fatal("timeout should fail-closed on pre_tool_use")
	}
	if len(out.Decisions) == 0 || out.Decisions[0].Decision != "fail_closed" {
		t.Fatalf("decisions = %#v", out.Decisions)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %v", elapsed)
	}
}

func TestRunHooksTimeoutFailOpenExplicit(t *testing.T) {
	dir := t.TempDir()
	failOpen := false // FailClosed=false → fail-open
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:      HookEventPreToolUse,
		Command:    `sleep 5`,
		TimeoutMs:  50,
		FailClosed: &failOpen,
	}}, HookEventPreToolUse, HookPayload{Event: HookEventPreToolUse, ToolName: "bash"}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allow {
		t.Fatal("explicit fail-open should allow on timeout")
	}
	if len(out.Decisions) == 0 || out.Decisions[0].Decision != "fail_open" {
		t.Fatalf("decisions = %#v", out.Decisions)
	}
}

func TestRunHooksLaunchErrorFailClosed(t *testing.T) {
	// Explicit sandbox policy that refuses degrade forces launch failure when
	// the OS backend is missing.
	dir := t.TempDir()
	if sandbox.Available() {
		t.Skip("need unavailable sandbox to force launch sandbox_denied")
	}
	pol := sandbox.Policy{Mode: sandbox.ModeReadOnly, NoNetwork: true, AllowDegrade: false}
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:   HookEventPreToolUse,
		Command: `printf should-not-run`,
		Sandbox: &pol,
	}}, HookEventPreToolUse, HookPayload{Event: HookEventPreToolUse, ToolName: "bash"}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Allow {
		t.Fatal("fail-closed sandbox policy should block when backend unavailable")
	}
}

func TestRunHooksPayloadRedacted(t *testing.T) {
	dir := t.TempDir()
	secret := "sk-ant-api03-supersecrettokenvalue000000000000000000000000"
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:   HookEventPreToolUse,
		Command: `cat`,
	}}, HookEventPreToolUse, HookPayload{
		Event:     HookEventPreToolUse,
		ToolName:  "bash",
		ToolInput: json.RawMessage(`{"command":"echo ` + secret + `"}`),
	}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Inject, secret) {
		t.Fatalf("raw secret leaked into hook stdin/inject: %q", out.Inject)
	}
	if !strings.Contains(out.Inject, "REDACTED") && !strings.Contains(out.Inject, "sk-ant") {
		// redact may replace with placeholder; ensure original token gone is enough
	}
}

func TestRunHooksFailedBlockCannotAllow(t *testing.T) {
	// A failed blocking hook must not silently allow a protected operation.
	dir := t.TempDir()
	fc := true
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:      HookEventPreToolUse,
		Command:    `sleep 5`,
		TimeoutMs:  30,
		FailClosed: &fc,
	}}, HookEventPreToolUse, HookPayload{Event: HookEventPreToolUse, ToolName: "write"}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Allow {
		t.Fatal("failed blocking hook silently allowed protected op")
	}
}

func TestRunHooksTrustAsk(t *testing.T) {
	dir := t.TempDir()
	var asked []string
	ask := func(ctx context.Context, command string) error {
		asked = append(asked, command)
		return nil
	}
	cmd := `printf ok`
	_, err := RunHooks(context.Background(), []HookDef{
		{Event: HookEventPreToolUse, Command: cmd},
		{Event: HookEventPreToolUse, Command: cmd},
	}, HookEventPreToolUse, HookPayload{Event: HookEventPreToolUse, ToolName: "bash"}, dir, ask)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != cmd {
		t.Fatalf("asked = %#v, want once", asked)
	}
}

func TestRunHooksTrustDenied(t *testing.T) {
	dir := t.TempDir()
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:   HookEventPreToolUse,
		Command: `printf should-not-run`,
	}}, HookEventPreToolUse, HookPayload{Event: HookEventPreToolUse, ToolName: "bash"}, dir,
		func(context.Context, string) error { return errors.New("user rejected hook") })
	if err != nil {
		t.Fatal(err)
	}
	if out.Allow {
		t.Fatal("want block on trust deny")
	}
	if !strings.Contains(out.Inject, "user rejected hook") {
		t.Fatalf("inject=%q", out.Inject)
	}
}

func TestRunHooksEventFilter(t *testing.T) {
	dir := t.TempDir()
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:   HookEventPostToolUse,
		Command: `printf post; exit 1`,
	}}, HookEventPreToolUse, HookPayload{Event: HookEventPreToolUse, ToolName: "bash"}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Allow {
		t.Fatal("post hook should not fire on pre")
	}
}

func TestRunHooksWorkDir(t *testing.T) {
	dir := t.TempDir()
	out, err := RunHooks(context.Background(), []HookDef{{
		Event:   HookEventPreToolUse,
		Command: `pwd`,
	}}, HookEventPreToolUse, HookPayload{Event: HookEventPreToolUse, ToolName: "bash"}, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := filepath.Clean(strings.TrimSpace(out.Inject))
	want := filepath.Clean(dir)
	if got != want {
		t.Fatalf("pwd=%q want %q", got, want)
	}
}
