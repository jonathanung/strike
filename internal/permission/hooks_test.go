package permission

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestValidateHookRule(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		rule    HookRule
		wantErr string
	}{
		{
			name: "valid block",
			rule: HookRule{Event: HookEventPreToolUse, Matcher: "write", Action: HookActionBlock, Message: "no"},
		},
		{
			name: "valid log turn",
			rule: HookRule{Event: HookEventTurnEnd, Action: HookActionNotify},
		},
		{
			name:    "empty event",
			rule:    HookRule{Action: HookActionLog},
			wantErr: "empty event",
		},
		{
			name:    "unknown event",
			rule:    HookRule{Event: "tool.pre", Action: HookActionLog},
			wantErr: "unknown event",
		},
		{
			name:    "empty action",
			rule:    HookRule{Event: HookEventPreToolUse},
			wantErr: "empty action",
		},
		{
			name:    "unknown action",
			rule:    HookRule{Event: HookEventPreToolUse, Action: "deny"},
			wantErr: "unknown action",
		},
		{
			name:    "block only pre_tool_use",
			rule:    HookRule{Event: HookEventPostToolUse, Action: HookActionBlock},
			wantErr: `action "block" only allowed`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateHookRule(tc.rule)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateHookRule = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateHookRule = %v, want containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestEvaluateHooksBlockWrite(t *testing.T) {
	t.Parallel()
	rules := HookRuleset{
		{Event: HookEventPreToolUse, Matcher: "read", Action: HookActionLog},
		{Event: HookEventPreToolUse, Matcher: "write", Action: HookActionBlock, Message: "writes not allowed"},
		{Event: HookEventPreToolUse, Matcher: "write", Action: HookActionLog},
	}
	d := EvaluateHooks(rules, HookEventPreToolUse, "write")
	if !d.Block || d.BlockMessage() != "writes not allowed" {
		t.Fatalf("decision = %+v", d)
	}
	if len(d.Log) != 1 {
		t.Fatalf("Log = %#v", d.Log)
	}
	if EvaluateHooks(rules, HookEventPreToolUse, "read").Block {
		t.Fatal("read should not block")
	}
}

func TestEvaluateHooksLastBlockWins(t *testing.T) {
	t.Parallel()
	rules := HookRuleset{
		{Event: HookEventPreToolUse, Matcher: "write", Action: HookActionBlock, Message: "first"},
		{Event: HookEventPreToolUse, Matcher: "w*", Action: HookActionBlock, Message: "second"},
	}
	d := EvaluateHooks(rules, HookEventPreToolUse, "write")
	if !d.Block || d.BlockMessage() != "second" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestEvaluateHooksTurnEvents(t *testing.T) {
	t.Parallel()
	rules := HookRuleset{
		{Event: HookEventTurnEnd, Matcher: "write", Action: HookActionNotify, Message: "nope"},
		{Event: HookEventTurnEnd, Action: HookActionNotify, Message: "turn done"},
		{Event: HookEventTurnEnd, Matcher: "*", Action: HookActionLog},
	}
	d := EvaluateHooks(rules, HookEventTurnEnd, "")
	if len(d.Notify) != 1 || d.Notify[0].Message != "turn done" {
		t.Fatalf("Notify = %#v", d.Notify)
	}
	if len(d.Log) != 1 {
		t.Fatalf("Log = %#v", d.Log)
	}
}

func TestBlockedErrorUsesToolFeedback(t *testing.T) {
	t.Parallel()
	err := &BlockedError{Message: "planning phase"}
	if got := err.Error(); got != protocol.ToolFeedbackBlocked("planning phase") {
		t.Errorf("Error() = %q", got)
	}
}
