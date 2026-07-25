package tool

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestRunHooksTimeoutFailOpen(t *testing.T) {
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
	if !out.Allow {
		t.Fatal("timeout should fail-open")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %v", elapsed)
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
