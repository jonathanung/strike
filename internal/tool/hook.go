package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// Hook lifecycle events fired by the engine.
const (
	HookEventPreToolUse  = "pre_tool_use"
	HookEventPostToolUse = "post_tool_use"
)

const (
	hookDefaultTimeout = 30 * time.Second
	hookMaxTimeout     = 2 * time.Minute
	hookMaxOutput      = 30000
)

// HookDef is one shell-command hook from config.
type HookDef struct {
	// Event is pre_tool_use or post_tool_use.
	Event string
	// Command is run via bash -c with event JSON on stdin.
	Command string
	// TimeoutMs bounds the run (default 30000, max 120000).
	TimeoutMs int
	// Matcher is a doublestar glob over the tool name; empty or "*" matches all.
	Matcher string
}

// HookPayload is the JSON object written to a hook's stdin.
type HookPayload struct {
	Event      string          `json:"event"`
	SessionID  string          `json:"session_id"`
	CWD        string          `json:"cwd"`
	ToolName   string          `json:"tool_name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolInput  json.RawMessage `json:"tool_input,omitempty"`
	ToolOutput string          `json:"tool_output,omitempty"`
	IsError    bool            `json:"is_error,omitempty"`
}

// HookOutcome is the aggregated result of matching hooks for one event.
// Allow is false when any hook exits non-zero. Inject is stdout from hooks
// (block reasons and allow-side messages), joined with newlines.
type HookOutcome struct {
	Allow  bool
	Inject string
}

// RunHooks executes matching shell hooks in config order. Exit 0 allows;
// non-zero blocks. Stdout (trimmed) is collected as Inject. Timeouts and
// start failures fail-open (allow) so a broken hook cannot brick the agent.
// ask is invoked once per distinct command before the first run (trust gate);
// a non-nil error from ask blocks without running the command.
func RunHooks(ctx context.Context, defs []HookDef, event string, payload HookPayload, workDir string, ask func(ctx context.Context, command string) error) (HookOutcome, error) {
	out := HookOutcome{Allow: true}
	if len(defs) == 0 || event == "" {
		return out, nil
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return out, fmt.Errorf("marshal hook payload: %w", err)
	}

	var injects []string
	trusted := map[string]struct{}{}

	for _, def := range defs {
		if !hookMatches(def, event, payload.ToolName) {
			continue
		}
		cmd := strings.TrimSpace(def.Command)
		if cmd == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return HookOutcome{Allow: out.Allow, Inject: joinInjects(injects)}, err
		}

		if _, ok := trusted[cmd]; !ok {
			if ask != nil {
				if err := ask(ctx, cmd); err != nil {
					reason := strings.TrimSpace(err.Error())
					if reason == "" {
						reason = "hook trust denied"
					}
					injects = append(injects, reason)
					return HookOutcome{Allow: false, Inject: joinInjects(injects)}, nil
				}
			}
			trusted[cmd] = struct{}{}
		}

		res, runErr := RunProcess(ctx, ProcessSpec{
			Argv:      []string{"bash", "-c", cmd},
			Dir:       workDir,
			Stdin:     body,
			Timeout:   hookTimeout(def.TimeoutMs),
			MaxOutput: hookMaxOutput,
			Combine:   false,
		}, ProcessObserver{})
		if runErr != nil {
			// Start/setup failure: fail-open.
			continue
		}
		switch res.Status {
		case ProcessStatusTimeout, ProcessStatusCanceled, ProcessStatusError:
			if res.Status == ProcessStatusCanceled || ctx.Err() != nil {
				return HookOutcome{Allow: out.Allow, Inject: joinInjects(injects)}, ctx.Err()
			}
			// Timeout / process error: fail-open.
			continue
		}

		msg := strings.TrimSpace(res.Stdout)
		if msg == "" {
			msg = strings.TrimSpace(res.Stderr)
		}
		if res.ExitCode != 0 {
			if msg == "" {
				msg = fmt.Sprintf("hook exited %d", res.ExitCode)
			}
			injects = append(injects, msg)
			return HookOutcome{Allow: false, Inject: joinInjects(injects)}, nil
		}
		if msg != "" {
			injects = append(injects, msg)
		}
	}
	return HookOutcome{Allow: true, Inject: joinInjects(injects)}, nil
}

func hookMatches(def HookDef, event, toolName string) bool {
	if strings.TrimSpace(def.Event) != event {
		return false
	}
	m := strings.TrimSpace(def.Matcher)
	if m == "" || m == "*" {
		return true
	}
	ok, err := doublestar.Match(m, toolName)
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
