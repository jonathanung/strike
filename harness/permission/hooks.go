package permission

import (
	"fmt"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// Declarative hook lifecycle events. Names match tool.HookEvent* so config
// can mix shell commands and rules under one hooks list. Vocabulary version:
// tool.LifecycleVocabularyVersion.
const (
	HookEventPreToolUse           = tool.HookEventPreToolUse
	HookEventPostToolUse          = tool.HookEventPostToolUse
	HookEventTurnStart            = tool.HookEventTurnStart
	HookEventTurnEnd              = tool.HookEventTurnEnd
	HookEventSessionStart         = tool.HookEventSessionStart
	HookEventSessionResume        = tool.HookEventSessionResume
	HookEventSessionEnd           = tool.HookEventSessionEnd
	HookEventProviderAttempt      = tool.HookEventProviderAttempt
	HookEventProviderRetry        = tool.HookEventProviderRetry
	HookEventPermissionResolution = tool.HookEventPermissionResolution
	HookEventCompaction           = tool.HookEventCompaction
	HookEventPhaseTransition      = tool.HookEventPhaseTransition
	HookEventChildLifecycle       = tool.HookEventChildLifecycle
	HookEventVerificationGate     = tool.HookEventVerificationGate
)

// Declarative hook actions.
const (
	HookActionLog    = "log"
	HookActionBlock  = "block"
	HookActionNotify = "notify"
)

// HookRule is one declarative matcher → action entry.
// Event is a lifecycle name; Matcher is a doublestar glob over the subject
// (tool name for tool events; phase/permission/status for others; empty/"*" = any).
// Message is optional block/notify text.
type HookRule struct {
	Event   string `json:"event"`
	Matcher string `json:"matcher,omitempty"`
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
}

// HookRuleset is an ordered list of declarative hook rules.
type HookRuleset []HookRule

// HookHit is one matched rule outcome.
type HookHit struct {
	Event   string
	Matcher string
	Action  string
	Message string
	Tool    string // subject (tool name or other matcher target)
}

// HookDecision aggregates matching declarative rules for one event.
type HookDecision struct {
	Block    bool
	BlockHit HookHit
	Log      []HookHit
	Notify   []HookHit
}

// BlockMessage returns the last block rule's message, or empty.
func (d HookDecision) BlockMessage() string {
	return strings.TrimSpace(d.BlockHit.Message)
}

var knownHookEvents = map[string]struct{}{}

func init() {
	for _, ev := range tool.KnownLifecycleEvents {
		knownHookEvents[ev] = struct{}{}
	}
}

var knownHookActions = map[string]struct{}{
	HookActionLog:    {},
	HookActionBlock:  {},
	HookActionNotify: {},
}

// ValidHookEvent reports whether event is a known lifecycle name.
func ValidHookEvent(event string) bool {
	_, ok := knownHookEvents[event]
	return ok
}

// ValidHookAction reports whether action is log, block, or notify.
func ValidHookAction(action string) bool {
	_, ok := knownHookActions[action]
	return ok
}

// ValidateHookRule rejects unknown events/actions and block outside pre_tool_use.
func ValidateHookRule(r HookRule) error {
	if strings.TrimSpace(r.Event) == "" {
		return fmt.Errorf("empty event")
	}
	if !ValidHookEvent(r.Event) {
		return fmt.Errorf("unknown event %q", r.Event)
	}
	if strings.TrimSpace(r.Action) == "" {
		return fmt.Errorf("empty action")
	}
	if !ValidHookAction(r.Action) {
		return fmt.Errorf("unknown action %q", r.Action)
	}
	if r.Action == HookActionBlock && !tool.DeclarativeBlockAllowed(r.Event) {
		return fmt.Errorf("action %q only allowed on event %q", HookActionBlock, HookEventPreToolUse)
	}
	return nil
}

// ValidateHookRuleset validates every rule.
func ValidateHookRuleset(rs HookRuleset) error {
	for i, r := range rs {
		if err := ValidateHookRule(r); err != nil {
			return fmt.Errorf("hook rule %d: %w", i, err)
		}
	}
	return nil
}

// EvaluateHooks walks rules in order. Any matching block sets Block; later
// block messages override. subject is the matcher target (tool name, phase, …).
func EvaluateHooks(rules HookRuleset, event, subject string) HookDecision {
	var d HookDecision
	for _, rule := range rules {
		if rule.Event != event {
			continue
		}
		if !hookMatch(rule.Matcher, subject) {
			continue
		}
		hit := HookHit{
			Event:   rule.Event,
			Matcher: rule.Matcher,
			Action:  rule.Action,
			Message: rule.Message,
			Tool:    subject,
		}
		switch rule.Action {
		case HookActionLog:
			d.Log = append(d.Log, hit)
		case HookActionNotify:
			d.Notify = append(d.Notify, hit)
		case HookActionBlock:
			if tool.DeclarativeBlockAllowed(event) {
				d.Block = true
				d.BlockHit = hit
			}
		}
	}
	return d
}

func hookMatch(pattern, subject string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	// Events with no subject; only empty/"*" match them.
	if subject == "" {
		return false
	}
	ok, err := doublestar.Match(pattern, subject)
	return err == nil && ok
}

// BlockedError is returned when a declarative hook blocks a tool call.
type BlockedError struct {
	Message string
}

func (e *BlockedError) Error() string {
	return protocol.ToolFeedbackBlocked(e.Message)
}

// DefaultBlockMessage builds a reason when a block rule has no message.
func DefaultBlockMessage(event, matcher, toolName string) string {
	if matcher == "" {
		matcher = "*"
	}
	if toolName != "" {
		return fmt.Sprintf("hook %s matcher=%s tool=%s", event, matcher, toolName)
	}
	return fmt.Sprintf("hook %s matcher=%s", event, matcher)
}
