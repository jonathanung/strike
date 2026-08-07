package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// LifecycleVocabularyVersion is the versioned lifecycle hook event set.
// Bump when adding/renaming events or changing payload contracts in a
// breaking way. Config and docs reference this string.
const LifecycleVocabularyVersion = "1.0.0"

// Hook lifecycle events fired by the engine (vocabulary v1.0.0).
//
// Dispatch order (deterministic):
//  1. Declarative rules (permission.HookRuleset) for the event
//  2. Shell hooks (HookDef) in config order for the same event
//
// Tool path specifically:
//
//	declarative pre_tool_use → shell pre_tool_use → Execute →
//	shell post_tool_use → declarative post_tool_use
//
// Failure policy:
//   - Shell exit 0: allow; stdout may inject (pre_tool_use only affects gate)
//   - Shell non-zero: blocks only on pre_tool_use; other events fail-open
//     (observe-only) so hooks cannot mutate completed side effects
//   - Timeout / start failure: always fail-open
//   - Cancellation: return ctx.Err() with partial inject
//   - Hard permission denials are evaluated before hooks; hooks cannot widen them
//
// Block action (declarative) is only valid on pre_tool_use.
const (
	HookEventPreToolUse           = "pre_tool_use"
	HookEventPostToolUse          = "post_tool_use"
	HookEventTurnStart            = "turn_start"
	HookEventTurnEnd              = "turn_end"
	HookEventSessionStart         = "session_start"
	HookEventSessionResume        = "session_resume"
	HookEventSessionEnd           = "session_end"
	HookEventProviderAttempt      = "provider_attempt"
	HookEventProviderRetry        = "provider_retry"
	HookEventPermissionResolution = "permission_resolution"
	HookEventCompaction           = "compaction"
	HookEventPhaseTransition      = "phase_transition"
	HookEventChildLifecycle       = "child_lifecycle"
	HookEventVerificationGate     = "verification_gate"
)

// HookPayloadMaxField is the max runes kept per free-text payload field after
// redaction (bounded so hooks cannot be DoS'd by huge tool output).
const HookPayloadMaxField = 4096

// HookPayloadMaxInputBytes bounds tool_input JSON after redaction.
const HookPayloadMaxInputBytes = 8192

const (
	hookDefaultTimeout = 30 * time.Second
	hookMaxTimeout     = 2 * time.Minute
	hookMaxOutput      = 30000
)

// KnownLifecycleEvents is the full v1 vocabulary (stable order for docs/tests).
var KnownLifecycleEvents = []string{
	HookEventSessionStart,
	HookEventSessionResume,
	HookEventSessionEnd,
	HookEventTurnStart,
	HookEventTurnEnd,
	HookEventProviderAttempt,
	HookEventProviderRetry,
	HookEventPermissionResolution,
	HookEventCompaction,
	HookEventPhaseTransition,
	HookEventChildLifecycle,
	HookEventVerificationGate,
	HookEventPreToolUse,
	HookEventPostToolUse,
}

// ValidLifecycleEvent reports whether event is in the versioned vocabulary.
func ValidLifecycleEvent(event string) bool {
	switch event {
	case HookEventPreToolUse, HookEventPostToolUse,
		HookEventTurnStart, HookEventTurnEnd,
		HookEventSessionStart, HookEventSessionResume, HookEventSessionEnd,
		HookEventProviderAttempt, HookEventProviderRetry,
		HookEventPermissionResolution, HookEventCompaction,
		HookEventPhaseTransition, HookEventChildLifecycle,
		HookEventVerificationGate:
		return true
	default:
		return false
	}
}

// HookCanBlock reports whether a non-zero shell exit may set Allow=false.
// pre_tool_use blocks Execute; post_tool_use marks feedback blocked (compat)
// but cannot undo completed side effects. All other lifecycle events are
// observe-only (fail-open on non-zero).
func HookCanBlock(event string) bool {
	return event == HookEventPreToolUse || event == HookEventPostToolUse
}

// DeclarativeBlockAllowed reports whether action=block is valid for event.
// Only pre_tool_use may declaratively block (before side effects).
func DeclarativeBlockAllowed(event string) bool {
	return event == HookEventPreToolUse
}

// Hook failure policy tokens (config failClosed / failOpen).
const (
	// HookFailClosed blocks the protected operation when the hook cannot run
	// to a clean exit 0 (timeout, launch error, process error). Default for
	// blocking events (pre_tool_use / post_tool_use) — #1031.
	HookFailClosed = "closed"
	// HookFailOpen allows the operation when the hook cannot run cleanly.
	// Explicit opt-in for availability-oriented side-effect hooks.
	HookFailOpen = "open"
)

// HookDef is one shell-command hook from config.
type HookDef struct {
	// Event is a lifecycle vocabulary name (see HookEvent*).
	Event string
	// Command is run via bash -c with event JSON on stdin.
	Command string
	// TimeoutMs bounds the run (default 30000, max 120000).
	TimeoutMs int
	// Matcher is a doublestar glob over the subject (tool name, phase name,
	// child status, permission name, …); empty or "*" matches all.
	Matcher string
	// FailClosed, when non-nil, overrides the default failure policy for
	// timeout/launch/process errors. nil → fail-closed on blocking events
	// (pre/post_tool_use), fail-open on observe-only events (#1031).
	FailClosed *bool
	// Sandbox, when non-nil, is the OS policy for the hook process.
	// nil uses DefaultHookSandbox (read-only + no network; degrade allowed
	// only when the hook is fail-open).
	Sandbox *sandbox.Policy
}

// HookPayload is the JSON object written to a hook's stdin.
// Fields are redacted and bounded before marshal (see BoundHookPayload).
type HookPayload struct {
	// SchemaVersion is LifecycleVocabularyVersion for consumers.
	SchemaVersion string `json:"schema_version"`
	Event         string `json:"event"`
	SessionID     string `json:"session_id"`
	// Correlation (stable ids for replay/audit).
	TurnID            string `json:"turn_id,omitempty"`
	ProviderRequestID string `json:"provider_request_id,omitempty"`
	ParentSessionID   string `json:"parent_session_id,omitempty"`
	Depth             int    `json:"depth,omitempty"`
	Attempt           int    `json:"attempt,omitempty"`
	CWD               string `json:"cwd"`
	// Subject is the matcher target (tool name, phase, permission, …).
	Subject string `json:"subject,omitempty"`
	// Tool fields (pre/post_tool_use).
	ToolName   string          `json:"tool_name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`
	ToolOutput string          `json:"tool_output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
	// Lifecycle detail (redacted, bounded).
	Detail string `json:"detail,omitempty"`
	// Status is a short machine label (e.g. child status, gate result, decision).
	Status string `json:"status,omitempty"`
}

// BoundHookPayload returns a copy with secrets scrubbed and free-text fields
// truncated. Safe to marshal for shell stdin.
func BoundHookPayload(p HookPayload) HookPayload {
	out := p
	if out.SchemaVersion == "" {
		out.SchemaVersion = LifecycleVocabularyVersion
	}
	out.SessionID = boundField(redact.String(out.SessionID), 256)
	out.TurnID = boundField(out.TurnID, 128)
	out.ProviderRequestID = boundField(out.ProviderRequestID, 128)
	out.ParentSessionID = boundField(out.ParentSessionID, 256)
	out.CWD = boundField(out.CWD, 1024)
	out.Subject = boundField(redact.String(out.Subject), 256)
	out.ToolName = boundField(out.ToolName, 128)
	out.ToolCallID = boundField(out.ToolCallID, 128)
	out.Status = boundField(out.Status, 128)
	out.Detail = boundField(redact.String(out.Detail), HookPayloadMaxField)
	out.ToolOutput = boundField(redact.String(out.ToolOutput), HookPayloadMaxField)
	if len(out.ToolInput) > 0 {
		s := redact.String(string(out.ToolInput))
		if len(s) > HookPayloadMaxInputBytes {
			s = s[:HookPayloadMaxInputBytes]
			// Avoid cutting mid-rune.
			for len(s) > 0 && !utf8.ValidString(s) {
				s = s[:len(s)-1]
			}
			s += "…[truncated]"
		}
		out.ToolInput = json.RawMessage(s)
	}
	return out
}

func boundField(s string, maxRunes int) string {
	if maxRunes <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxRunes]) + "…[truncated]"
}

// HookDecision is one shell hook's allow/block/fail outcome (for events/audit).
// Command is truncated; never includes secret values from the payload.
type HookDecision struct {
	Command    string // truncated command text
	Decision   string // allow|block|fail_open|fail_closed|canceled
	Reason     string // human-readable, redacted
	FailClosed bool
	ExitCode   int
}

// HookOutcome is the aggregated result of matching hooks for one event.
// Allow is false when a blocking event had a non-zero exit, trust deny, or
// fail-closed infrastructure failure. Inject is stdout from hooks.
type HookOutcome struct {
	Allow     bool
	Inject    string
	Decisions []HookDecision
}

// DefaultHookSandbox returns the OS policy for shell hooks: read-only FS,
// no host network. AllowDegrade is always true so hosts without bwrap/seatbelt
// still run hooks; fail-closed policy applies to timeout/launch/exit of the
// hook command itself, not OS backend availability (#1031).
func DefaultHookSandbox(failOpen bool) sandbox.Policy {
	_ = failOpen // reserved: callers may pass tighter policies via HookDef.Sandbox
	return sandbox.Policy{
		Mode:         sandbox.ModeReadOnly,
		NoNetwork:    true,
		AllowDegrade: true,
	}
}

// hookFailOpen reports whether infrastructure failures should allow the op.
// Explicit FailClosed wins; otherwise blocking events default closed, others open.
func hookFailOpen(def HookDef, event string) bool {
	if def.FailClosed != nil {
		return !*def.FailClosed
	}
	// Safe default: fail-closed on events that can gate tool execution.
	if HookCanBlock(event) {
		return false
	}
	return true
}

// RunHooks executes matching shell hooks in config order.
//
// Exit 0 allows; non-zero blocks only when HookCanBlock(event). Stdout
// (trimmed) is collected as Inject.
//
// Failure policy (#1031):
//   - Timeout / launch failure / process error: fail-closed by default on
//     pre/post_tool_use (blocks); fail-open on observe-only events. Override
//     per hook with FailClosed.
//   - Cancellation: return ctx.Err() with partial inject
//   - Payload is always BoundHookPayload (redacted) before crossing the boundary
//   - Process runs under DefaultHookSandbox unless def.Sandbox is set
//
// ask is invoked once per distinct command before the first run (trust gate).
func RunHooks(ctx context.Context, defs []HookDef, event string, payload HookPayload, workDir string, ask func(ctx context.Context, command string) error) (HookOutcome, error) {
	out := HookOutcome{Allow: true}
	if len(defs) == 0 || event == "" {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}

	payload.Event = event
	// Redact before subprocess boundary — never send raw secrets to hooks.
	body, err := json.Marshal(BoundHookPayload(payload))
	if err != nil {
		return out, fmt.Errorf("marshal hook payload: %w", err)
	}

	var injects []string
	var decisions []HookDecision
	trusted := map[string]struct{}{}
	subject := payload.Subject
	if subject == "" {
		subject = payload.ToolName
	}
	canBlock := HookCanBlock(event)

	for _, def := range defs {
		if !hookMatches(def, event, subject) {
			continue
		}
		cmd := strings.TrimSpace(def.Command)
		if cmd == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return HookOutcome{Allow: out.Allow, Inject: joinInjects(injects), Decisions: decisions}, err
		}

		failOpen := hookFailOpen(def, event)
		cmdLabel := boundField(cmd, 200)

		if _, ok := trusted[cmd]; !ok {
			if ask != nil {
				if err := ask(ctx, cmd); err != nil {
					reason := strings.TrimSpace(err.Error())
					if reason == "" {
						reason = "hook trust denied"
					}
					injects = append(injects, reason)
					decisions = append(decisions, HookDecision{
						Command:    cmdLabel,
						Decision:   "block",
						Reason:     reason,
						FailClosed: !failOpen,
					})
					if canBlock {
						return HookOutcome{Allow: false, Inject: joinInjects(injects), Decisions: decisions}, nil
					}
					continue
				}
			}
			trusted[cmd] = struct{}{}
		}

		pol := DefaultHookSandbox(failOpen)
		if def.Sandbox != nil {
			pol = *def.Sandbox
		} else if workDir != "" {
			pol.WorkDir = workDir
		}

		res, runErr := RunProcess(ctx, ProcessSpec{
			Argv:      []string{"bash", "-c", cmd},
			Dir:       workDir,
			Stdin:     body,
			Timeout:   hookTimeout(def.TimeoutMs),
			MaxOutput: hookMaxOutput,
			Combine:   false,
			Sandbox:   pol,
			// Bounded resources for hook processes (Linux prlimit; no-op elsewhere).
			Limits: ProcessLimits{MemoryBytes: 512 << 20, CPUSeconds: 60},
		}, ProcessObserver{})
		if runErr != nil {
			reason := "hook launch failed: " + runErr.Error()
			// sandbox_denied from fail-closed degrade is a launch failure.
			if !failOpen && canBlock {
				injects = append(injects, reason)
				decisions = append(decisions, HookDecision{
					Command: cmdLabel, Decision: "fail_closed", Reason: reason, FailClosed: true,
				})
				return HookOutcome{Allow: false, Inject: joinInjects(injects), Decisions: decisions}, nil
			}
			decisions = append(decisions, HookDecision{
				Command: cmdLabel, Decision: "fail_open", Reason: reason, FailClosed: false,
			})
			continue
		}
		switch res.Status {
		case ProcessStatusTimeout, ProcessStatusCanceled, ProcessStatusError:
			if res.Status == ProcessStatusCanceled || ctx.Err() != nil {
				decisions = append(decisions, HookDecision{
					Command: cmdLabel, Decision: "canceled", Reason: "hook canceled", FailClosed: !failOpen,
				})
				return HookOutcome{Allow: out.Allow, Inject: joinInjects(injects), Decisions: decisions}, ctx.Err()
			}
			reason := "hook " + string(res.Status)
			if res.Status == ProcessStatusTimeout {
				reason = "hook timed out"
			}
			if !failOpen && canBlock {
				injects = append(injects, reason)
				decisions = append(decisions, HookDecision{
					Command: cmdLabel, Decision: "fail_closed", Reason: reason, FailClosed: true,
				})
				return HookOutcome{Allow: false, Inject: joinInjects(injects), Decisions: decisions}, nil
			}
			decisions = append(decisions, HookDecision{
				Command: cmdLabel, Decision: "fail_open", Reason: reason, FailClosed: false,
			})
			continue
		}

		msg := strings.TrimSpace(res.Stdout)
		if msg == "" {
			msg = strings.TrimSpace(res.Stderr)
		}
		// Scrub hook stdout before inject (may echo secrets from env).
		msg = redact.String(msg)
		if res.ExitCode != 0 {
			if msg == "" {
				msg = fmt.Sprintf("hook exited %d", res.ExitCode)
			}
			injects = append(injects, msg)
			if canBlock {
				decisions = append(decisions, HookDecision{
					Command: cmdLabel, Decision: "block", Reason: msg, FailClosed: !failOpen, ExitCode: res.ExitCode,
				})
				return HookOutcome{Allow: false, Inject: joinInjects(injects), Decisions: decisions}, nil
			}
			decisions = append(decisions, HookDecision{
				Command: cmdLabel, Decision: "fail_open", Reason: msg, FailClosed: false, ExitCode: res.ExitCode,
			})
			continue
		}
		if msg != "" {
			injects = append(injects, msg)
		}
		decisions = append(decisions, HookDecision{
			Command: cmdLabel, Decision: "allow", Reason: "exit 0", FailClosed: !failOpen, ExitCode: 0,
		})
	}
	return HookOutcome{Allow: true, Inject: joinInjects(injects), Decisions: decisions}, nil
}

func hookMatches(def HookDef, event, subject string) bool {
	if strings.TrimSpace(def.Event) != event {
		return false
	}
	m := strings.TrimSpace(def.Matcher)
	if m == "" || m == "*" {
		return true
	}
	// Empty subject: only empty/"*" match (handled above).
	if subject == "" {
		return false
	}
	ok, err := doublestar.Match(m, subject)
	return err == nil && ok
}

func hookTimeout(ms int) time.Duration {
	if ms <= 0 {
		return hookDefaultTimeout
	}
	d := time.Duration(ms) * time.Millisecond
	if d > hookMaxTimeout {
		return hookMaxTimeout
	}
	return d
}

func joinInjects(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}
