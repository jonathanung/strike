package permission

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/pkg/protocol"
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

func TestValidateHookRuleset(t *testing.T) {
	t.Parallel()
	t.Run("empty ok", func(t *testing.T) {
		t.Parallel()
		if err := ValidateHookRuleset(nil); err != nil {
			t.Fatalf("nil ruleset: %v", err)
		}
		if err := ValidateHookRuleset(HookRuleset{}); err != nil {
			t.Fatalf("empty ruleset: %v", err)
		}
	})
	t.Run("all valid", func(t *testing.T) {
		t.Parallel()
		rs := HookRuleset{
			{Event: HookEventPreToolUse, Matcher: "write", Action: HookActionBlock, Message: "no"},
			{Event: HookEventTurnStart, Action: HookActionLog},
			{Event: HookEventPostToolUse, Matcher: "bash", Action: HookActionNotify},
		}
		if err := ValidateHookRuleset(rs); err != nil {
			t.Fatalf("ValidateHookRuleset = %v", err)
		}
	})
	t.Run("indexes first bad rule", func(t *testing.T) {
		t.Parallel()
		rs := HookRuleset{
			{Event: HookEventPreToolUse, Action: HookActionLog},
			{Event: HookEventTurnEnd, Action: HookActionBlock},
			{Event: "nope", Action: HookActionLog},
		}
		err := ValidateHookRuleset(rs)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "hook rule 1:") {
			t.Fatalf("error = %v, want index 1", err)
		}
		if !strings.Contains(err.Error(), `action "block" only allowed`) {
			t.Fatalf("error = %v, want block restriction", err)
		}
	})
	t.Run("propagates unknown event", func(t *testing.T) {
		t.Parallel()
		err := ValidateHookRuleset(HookRuleset{
			{Event: "tool.pre", Action: HookActionLog},
		})
		if err == nil || !strings.Contains(err.Error(), "hook rule 0:") || !strings.Contains(err.Error(), "unknown event") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestDefaultBlockMessage(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		event   string
		matcher string
		tool    string
		want    string
	}{
		{
			name:    "with tool and matcher",
			event:   HookEventPreToolUse,
			matcher: "write",
			tool:    "write",
			want:    "hook pre_tool_use matcher=write tool=write",
		},
		{
			name:    "empty matcher becomes star",
			event:   HookEventPreToolUse,
			matcher: "",
			tool:    "bash",
			want:    "hook pre_tool_use matcher=* tool=bash",
		},
		{
			name:    "no tool omits tool field",
			event:   HookEventPreToolUse,
			matcher: "w*",
			tool:    "",
			want:    "hook pre_tool_use matcher=w*",
		},
		{
			name:    "empty matcher and no tool",
			event:   HookEventPreToolUse,
			matcher: "",
			tool:    "",
			want:    "hook pre_tool_use matcher=*",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := DefaultBlockMessage(tc.event, tc.matcher, tc.tool); got != tc.want {
				t.Fatalf("DefaultBlockMessage = %q, want %q", got, tc.want)
			}
		})
	}
}
