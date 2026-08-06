// Package replay provides deterministic session replay against the offline
// echo provider for eval and regression harnesses (epic E3), plus run
// recording, branch-from-event, structured compare (#791), and multi-agent
// run snapshots (#782).
//
// A golden JSONL session is a recorded protocol event log. Replay extracts
// user inputs, re-runs them through the engine with echo, and diffs the
// normalized tool-call sequence (name + canonical args). Call IDs,
// timestamps, and tool outputs are ignored so runs stay comparable.
//
// Recording (recording.go) derives a versioned, secret-redacted artifact with
// settings digests, tool results, provider attempts, handoff/gate fields, and
// nondeterministic markers. CompareRecordings produces structured deltas;
// BranchFromEvent forks a session JSONL prefix without replaying live side effects.
//
// RunSnapshot (snapshot.go) is the multi-agent counterpart: a compact spawn +
// optional completion capture (prompt, context bundle, model/tools/permissions,
// repo identity, config digests, handoff/gates) that complements session JSONL
// rather than duplicating the full transcript. Persist under ~/.strike/runs/,
// load for offline echo replay (ReplayRunSnapshot), and diff with
// CompareRunSnapshots. Field concepts are shared with Recording.
//
// Prompt regression (E3.2) builds on the same harness: CollectMetrics reports
// tool-call count, turn count, and token/system-prompt deltas per scenario.
// See metrics.go and TestPromptRegressionReport.
//
// Harness evaluation (#807) is the theme-oriented regression pack
// (correctness/safety/recovery/latency-cost + recording consumption):
// harness_eval.go and TestHarnessEvalSuite / make harness-eval. Soft CI
// report first; composes with #459, does not own SWE-bench (#561).
package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/echo"
	"github.com/jonathanung/strike-cli/internal/session"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// ToolCall is one normalized tool invocation for sequence comparison.
// Call IDs and outputs are omitted so echo re-runs stay deterministic.
type ToolCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// Result is the outcome of one echo replay run.
type Result struct {
	Events    []protocol.Event
	ToolCalls []ToolCall
	// Turns is the number of TurnCompleted events observed.
	Turns int
	// UserInputs is the ordered user text fed into the engine.
	UserInputs []string
	// Effective is the last EffectivePrompt snapshot when InspectPrompt was set.
	Effective *protocol.EffectivePrompt
}

// LoadJSONL reads a session event log (one protocol envelope per line).
func LoadJSONL(path string) ([]protocol.Event, error) {
	return session.Replay(path)
}

// ExtractUserInputs returns root-session user.message texts in log order.
// Child-lineage events (ParentSessionID or Depth > 0) are skipped.
func ExtractUserInputs(events []protocol.Event) []string {
	var out []string
	for _, ev := range events {
		um, ok := ev.(protocol.UserMessage)
		if !ok || !isRootEvent(ev) {
			continue
		}
		out = append(out, um.Text)
	}
	return out
}

// ExtractToolCalls returns root-session tool.begin invocations in log order,
// with args compacted to canonical JSON.
func ExtractToolCalls(events []protocol.Event) []ToolCall {
	var out []ToolCall
	for _, ev := range events {
		tb, ok := ev.(protocol.ToolCallBegin)
		if !ok || !isRootEvent(ev) {
			continue
		}
		out = append(out, ToolCall{
			Name: tb.Name,
			Args: compactJSON(tb.Args),
		})
	}
	return out
}

// DiffToolCalls reports the first divergence between want and got sequences.
// Returns nil when the sequences match.
func DiffToolCalls(want, got []ToolCall) error {
	n := len(want)
	if len(got) < n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		if want[i].Name != got[i].Name {
			return fmt.Errorf("tool[%d]: name %q != %q", i, got[i].Name, want[i].Name)
		}
		if !bytes.Equal(compactJSON(want[i].Args), compactJSON(got[i].Args)) {
			return fmt.Errorf("tool[%d] %s: args %s != %s", i, want[i].Name, got[i].Args, want[i].Args)
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("tool-call count %d != %d", len(got), len(want))
	}
	return nil
}

// Options configures an echo replay run.
type Options struct {
	// WorkDir is the engine workspace. Required; Run errors when empty.
	WorkDir string
	// Registry supplies tools. nil registers bash only (echo "run " path).
	Registry *tool.Registry
	// Timeout bounds the entire multi-turn run. Zero defaults to 30s.
	Timeout time.Duration
	// SessionID stamps emitted events. Empty lets the engine mint one.
	SessionID string
	// Agents overrides engine personas. nil uses the engine default (build).
	Agents []engine.Agent
	// InitialAgent selects a persona from Agents (or the engine default list).
	InitialAgent string
	// LeanCode is off|lite|full for prompt composition. Empty → engine default (lite).
	LeanCode string
	// InspectPrompt, when true, requests EffectivePrompt after inputs complete
	// so Result.Effective carries system/layer attribution for metrics.
	InspectPrompt bool
}

// Run feeds user inputs through the engine with the echo provider and
// collects the resulting event stream. Permissions allow all tools so the
// harness never blocks on asks. Sandbox is off for portable offline runs.
//
// When inputs is empty and InspectPrompt is true, the engine still starts so
// the composed system prompt can be inspected (prompt-only scenarios).
func Run(ctx context.Context, inputs []string, opts Options) (Result, error) {
	if opts.WorkDir == "" {
		return Result{}, fmt.Errorf("replay: WorkDir is required")
	}
	if len(inputs) == 0 && !opts.InspectPrompt {
		return Result{UserInputs: inputs}, nil
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	reg := opts.Registry
	if reg == nil {
		reg = tool.NewRegistry(tool.NewBash())
	}
	allowAll := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.Allow},
	}
	engOpts := engine.Options{
		SessionID:       opts.SessionID,
		Select:          selectEcho,
		InitialProvider: "echo",
		Registry:        reg,
		WorkDir:         opts.WorkDir,
		Rules:           []permission.Ruleset{permission.Defaults(), allowAll},
		SandboxMode:     "off",
		LeanCode:        opts.LeanCode,
		InitialAgent:    opts.InitialAgent,
	}
	if len(opts.Agents) > 0 {
		engOpts.Agents = append([]engine.Agent(nil), opts.Agents...)
	}
	eng := engine.New(engOpts)
	go eng.Run(runCtx)

	var events []protocol.Event
	var turns int
	for i, text := range inputs {
		eng.Ops() <- protocol.UserInput{Text: text}
		completed := false
		for !completed {
			select {
			case <-runCtx.Done():
				return Result{Events: events, UserInputs: inputs}, fmt.Errorf("replay: turn %d (%q): %w", i, text, runCtx.Err())
			case ev, ok := <-eng.Events():
				if !ok {
					return Result{Events: events, UserInputs: inputs}, fmt.Errorf("replay: turn %d (%q): engine events closed", i, text)
				}
				events = append(events, ev)
				switch e := ev.(type) {
				case protocol.TurnCompleted:
					// Child/subagent completions share the event stream; only
					// the root turn ends the user-input wait.
					if e.ParentSessionID != "" || e.Depth > 0 {
						continue
					}
					turns++
					completed = true
				case protocol.EngineError:
					if e.ParentSessionID != "" || e.Depth > 0 {
						continue
					}
					return Result{Events: events, UserInputs: inputs, Turns: turns}, fmt.Errorf("replay: turn %d (%q): engine error: %s", i, text, e.Message)
				}
			}
		}
	}

	res := Result{
		Events:     events,
		ToolCalls:  ExtractToolCalls(events),
		Turns:      turns,
		UserInputs: append([]string(nil), inputs...),
	}

	if opts.InspectPrompt {
		eff, err := inspectEffective(runCtx, eng, &res.Events)
		if err != nil {
			return res, err
		}
		res.Effective = eff
	}

	return res, nil
}

// inspectEffective sends InspectEffectivePrompt and waits for the snapshot.
// Startup events may still be draining; non-matching events are appended.
func inspectEffective(ctx context.Context, eng *engine.Engine, events *[]protocol.Event) (*protocol.EffectivePrompt, error) {
	eng.Ops() <- protocol.InspectEffectivePrompt{}
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("replay: inspect effective prompt: %w", ctx.Err())
		case ev, ok := <-eng.Events():
			if !ok {
				return nil, fmt.Errorf("replay: inspect effective prompt: engine events closed")
			}
			*events = append(*events, ev)
			if ep, ok := ev.(protocol.EffectivePrompt); ok {
				cp := ep
				return &cp, nil
			}
			if e, ok := ev.(protocol.EngineError); ok && e.ParentSessionID == "" && e.Depth == 0 {
				return nil, fmt.Errorf("replay: inspect effective prompt: engine error: %s", e.Message)
			}
		}
	}
}

// WriteJSONL persists events as a session JSONL log (protocol envelopes).
func WriteJSONL(path string, events []protocol.Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ev := range events {
		env, err := protocol.Wrap(ev)
		if err != nil {
			return err
		}
		if err := enc.Encode(env); err != nil {
			return err
		}
	}
	return nil
}

func selectEcho(string) (provider.Provider, string, error) {
	return echo.New(), "echo", nil
}

func compactJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("null")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		// Preserve non-JSON args rather than failing the whole diff.
		return append(json.RawMessage(nil), raw...)
	}
	return buf.Bytes()
}

func isRootEvent(ev protocol.Event) bool {
	switch e := ev.(type) {
	case protocol.UserMessage:
		return e.ParentSessionID == "" && e.Depth == 0
	case protocol.ToolCallBegin:
		return e.ParentSessionID == "" && e.Depth == 0
	default:
		// Extractors only care about the cases above; other events are ignored.
		return true
	}
}
