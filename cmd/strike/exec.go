package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

const execUsage = `Run one prompt headlessly and stream the assistant reply to stdout.

Usage:
  strike exec [options] <prompt...>
  strike exec [options] -              read prompt from stdin

Options are the same as strike ( --provider, --model, --effort, --sandbox,
--i-know, --auto, --dangerously-skip-permissions, --max-cost ), plus:

  --output-format <fmt>   text (default) | json | stream-json
  --json                  shorthand for --output-format=json
  --max-cost <usd>        session cost envelope (hard stop when exceeded)
  --approval-control <path>
                          unix socket (preferred) or bidirectional FIFO for
                          machine-readable permission/question callbacks
  --approval-timeout <dur>
                          max wait per approval reply (default 60s)

Formats:
  text         plain assistant text (current default)
  json         single result object on stdout when the turn ends
  stream-json  one protocol Event envelope (JSONL) per line

Exit codes: 0 turn ok, 1 turn/runtime error, 2 usage error.

Legacy noninteractive behavior (no --approval-control): permission and
question prompts cannot be answered interactively; asks are rejected unless
--auto or --dangerously-skip-permissions is set (configured/agent denies
still apply).

Approval control channel (NDJSON, one object per line on the socket/FIFO):
  ← {"type":"permission.request","requestId":"…","permission":"bash","patterns":["…"]}
  → {"type":"permission.reply","requestId":"…","decision":"once|reject","message":"…"}
  ← {"type":"question.request","requestId":"…","questions":[…]}
  → {"type":"question.reply","requestId":"…","answers":["…"]}
Decisions always/project require "durable":true. Disconnected, timed-out, or
malformed controllers fail closed (reject). Request payloads are secret-redacted.`

const (
	headlessPermissionReject = "headless mode: permission asks are denied; pass --auto or --dangerously-skip-permissions to allow tool calls that would prompt"
	headlessQuestionReject   = "headless mode: interactive questions are not supported"
)

// execOutputFormat controls how runHeadlessFrontend writes to stdout.
type execOutputFormat string

const (
	execFormatText       execOutputFormat = "text"
	execFormatJSON       execOutputFormat = "json"
	execFormatStreamJSON execOutputFormat = "stream-json"
)

// execJSONResult is the single object written by --output-format=json.
type execJSONResult struct {
	Type       string         `json:"type"` // always "result"
	OK         bool           `json:"ok"`
	Text       string         `json:"text,omitempty"`
	StopReason string         `json:"stopReason,omitempty"`
	Error      string         `json:"error,omitempty"`
	SessionID  string         `json:"sessionId,omitempty"`
	Provider   string         `json:"provider,omitempty"`
	Model      string         `json:"model,omitempty"`
	Usage      *execJSONUsage `json:"usage,omitempty"`
}

// execJSONUsage aggregates known token counts from UsageReported events.
type execJSONUsage struct {
	Input         int `json:"input,omitempty"`
	Output        int `json:"output,omitempty"`
	CacheRead     int `json:"cacheRead,omitempty"`
	CacheCreation int `json:"cacheCreation,omitempty"`
}

// runExecCLI parses `strike exec` args and runs a one-shot headless turn.
func runExecCLI(args []string, stdout, stderr io.Writer) int {
	parsed, err := parseExecArgs(args, os.Stdin)
	if err != nil {
		if errors.Is(err, errExecHelp) {
			fmt.Fprintln(stdout, execUsage)
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		fmt.Fprintln(stderr, execUsage)
		return 2
	}
	if err := runExec(parsed.opts, parsed.prompt, parsed.format, stdout, stderr, headlessExtra{
		ApprovalControl: parsed.approvalControl,
		ApprovalTimeout: parsed.approvalTimeout,
	}); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return 0
}

// headlessExtra carries exec-only options into runExec without breaking
// existing call sites that only pass format.
type headlessExtra struct {
	ApprovalControl string
	ApprovalTimeout time.Duration
}

var errExecHelp = errors.New("exec help")

// execParseResult is the product of parseExecArgs including approval control.
type execParseResult struct {
	opts            cliOptions
	prompt          string
	format          execOutputFormat
	approvalControl string
	approvalTimeout time.Duration
}

func parseExecArgs(args []string, stdin io.Reader) (execParseResult, error) {
	var flagArgs, positionals []string
	format := execFormatText
	formatSet := false
	approvalPath := ""
	approvalTimeout := defaultApprovalTimeout
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			return execParseResult{}, errExecHelp
		}
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(arg) > 1 && arg[0] == '-' && arg != "-" {
			name := strings.TrimLeft(arg, "-")
			var value string
			hasEq := false
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				value = name[eq+1:]
				name = name[:eq]
				hasEq = true
			}
			switch name {
			case "json":
				if hasEq {
					return execParseResult{}, fmt.Errorf("--json does not take a value")
				}
				if formatSet && format != execFormatJSON {
					return execParseResult{}, fmt.Errorf("conflicting --json and --output-format=%s", format)
				}
				format = execFormatJSON
				formatSet = true
				continue
			case "output-format":
				if !hasEq {
					if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
						return execParseResult{}, fmt.Errorf("--output-format requires a value (text|json|stream-json)")
					}
					i++
					value = args[i]
				}
				f, err := parseExecOutputFormat(value)
				if err != nil {
					return execParseResult{}, err
				}
				if formatSet && format != f {
					return execParseResult{}, fmt.Errorf("conflicting output format flags")
				}
				format = f
				formatSet = true
				continue
			case "approval-control":
				if !hasEq {
					if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
						return execParseResult{}, fmt.Errorf("--approval-control requires a path")
					}
					i++
					value = args[i]
				}
				approvalPath = strings.TrimSpace(value)
				if approvalPath == "" {
					return execParseResult{}, fmt.Errorf("--approval-control requires a path")
				}
				continue
			case "approval-timeout":
				if !hasEq {
					if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
						return execParseResult{}, fmt.Errorf("--approval-timeout requires a duration")
					}
					i++
					value = args[i]
				}
				d, err := time.ParseDuration(strings.TrimSpace(value))
				if err != nil || d <= 0 {
					return execParseResult{}, fmt.Errorf("invalid --approval-timeout %q", value)
				}
				approvalTimeout = d
				continue
			case "provider", "model", "effort", "sandbox":
				flagArgs = append(flagArgs, arg)
				if !hasEq && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					flagArgs = append(flagArgs, args[i])
				}
				continue
			case "auto", "dangerously-skip-permissions", "i-know":
				flagArgs = append(flagArgs, arg)
				continue
			default:
				// Pass through other flags to the shared CLI parser.
				flagArgs = append(flagArgs, arg)
				continue
			}
		}
		positionals = append(positionals, args[i:]...)
		break
	}

	opts, err := parseCLIOptions(flagArgs)
	if err != nil {
		return execParseResult{}, err
	}

	prompt, err := resolveExecPrompt(positionals, stdin)
	if err != nil {
		return execParseResult{}, err
	}
	return execParseResult{
		opts:            opts,
		prompt:          prompt,
		format:          format,
		approvalControl: approvalPath,
		approvalTimeout: approvalTimeout,
	}, nil
}

func parseExecOutputFormat(value string) (execOutputFormat, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(execFormatText):
		return execFormatText, nil
	case string(execFormatJSON):
		return execFormatJSON, nil
	case string(execFormatStreamJSON):
		return execFormatStreamJSON, nil
	default:
		return "", fmt.Errorf("invalid --output-format %q (want text|json|stream-json)", value)
	}
}

func resolveExecPrompt(positionals []string, stdin io.Reader) (string, error) {
	if len(positionals) == 0 {
		return "", fmt.Errorf("missing prompt (pass arguments or '-' to read stdin)")
	}
	if len(positionals) == 1 && positionals[0] == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("reading stdin: %w", err)
		}
		prompt := strings.TrimRight(string(data), "\n")
		if strings.TrimSpace(prompt) == "" {
			return "", fmt.Errorf("empty prompt on stdin")
		}
		return prompt, nil
	}
	for _, p := range positionals {
		if p == "-" {
			return "", fmt.Errorf("'-' to read stdin must be the only prompt argument")
		}
	}
	prompt := strings.Join(positionals, " ")
	if strings.TrimSpace(prompt) == "" {
		return "", fmt.Errorf("missing prompt (pass arguments or '-' to read stdin)")
	}
	return prompt, nil
}

// headlessOpts configures runHeadlessFrontend output and metadata.
type headlessOpts struct {
	Format          execOutputFormat
	SessionID       string
	ApprovalControl string
	ApprovalTimeout time.Duration
	// approvals, when set, is used instead of opening ApprovalControl (tests).
	approvals *approvalController
}

// runHeadlessFrontend submits one user prompt, writes output per Format,
// auto-rejects interactive asks, and returns when the turn completes.
// parent is combined with os.Interrupt; cancel either to interrupt the turn.
func runHeadlessFrontend(
	parent context.Context,
	ops chan<- protocol.Op,
	events <-chan protocol.Event,
	prompt string,
	stdout, stderr io.Writer,
	opts headlessOpts,
) error {
	if parent == nil {
		parent = context.Background()
	}
	if opts.Format == "" {
		opts.Format = execFormatText
	}

	ctx, stop := signal.NotifyContext(parent, os.Interrupt)
	defer stop()

	interrupted := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(interrupted)
		select {
		case ops <- protocol.Interrupt{}:
		default:
		}
	}()

	select {
	case ops <- protocol.UserInput{Text: prompt}:
	case <-interrupted:
		return context.Canceled
	}

	approvals := opts.approvals
	if approvals == nil && strings.TrimSpace(opts.ApprovalControl) != "" {
		var err error
		approvals, err = openApprovalController(opts.ApprovalControl, opts.ApprovalTimeout)
		if err != nil {
			return err
		}
		defer func() { _ = approvals.Close() }()
	}

	state := &headlessState{format: opts.Format, sessionID: opts.SessionID, approvals: approvals}
	var turnErr error
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				// Channel closed without TurnCompleted — not a successful turn.
				if err := state.flushJSON(stdout, turnErr, state.done); err != nil {
					return err
				}
				if turnErr != nil {
					return turnErr
				}
				return nil
			}
			if err := state.handleEvent(ctx, ev, ops, stdout, stderr, interrupted, &turnErr); err != nil {
				return err
			}
			if state.done {
				if err := state.flushJSON(stdout, turnErr, true); err != nil {
					return err
				}
				if turnErr != nil {
					return turnErr
				}
				if state.stopReason == "error" {
					return errors.New("turn ended with error")
				}
				return nil
			}
		case <-interrupted:
			// Drain until turn completes or events close after interrupt.
			for ev := range events {
				_ = state.emitStream(ev, stdout)
				switch e := ev.(type) {
				case protocol.TextDelta:
					if state.format == execFormatText {
						_, _ = io.WriteString(stdout, e.Text)
					}
					if state.format == execFormatJSON {
						state.text.WriteString(e.Text)
					}
				case protocol.TurnCompleted:
					state.stopReason = e.StopReason
					state.done = true
					_ = state.flushJSON(stdout, turnErr, true)
					return context.Canceled
				case protocol.EngineError:
					turnErr = errors.New(e.Message)
					state.errMsg = e.Message
				case protocol.ModelSelected:
					state.provider, state.model = e.Provider, e.Model
				case protocol.UsageReported:
					state.addUsage(e)
				}
			}
			// Interrupted before TurnCompleted: result must not claim ok.
			_ = state.flushJSON(stdout, turnErr, false)
			if turnErr != nil {
				return turnErr
			}
			return context.Canceled
		}
	}
}

// headlessState accumulates turn output for json/stream-json formats.
type headlessState struct {
	format      execOutputFormat
	sessionID   string
	approvals   *approvalController
	text        strings.Builder
	provider    string
	model       string
	usage       execJSONUsage
	hasUsage    bool
	errMsg      string
	stopReason  string
	done        bool
	jsonFlushed bool
}

func (s *headlessState) handleEvent(
	ctx context.Context,
	ev protocol.Event,
	ops chan<- protocol.Op,
	stdout, stderr io.Writer,
	interrupted <-chan struct{},
	turnErr *error,
) error {
	if err := s.emitStream(ev, stdout); err != nil {
		return err
	}

	switch e := ev.(type) {
	case protocol.TextDelta:
		switch s.format {
		case execFormatText:
			if _, err := io.WriteString(stdout, e.Text); err != nil {
				return err
			}
		case execFormatJSON:
			s.text.WriteString(e.Text)
		}
	case protocol.PermissionAsked:
		var reply protocol.PermissionReply
		if s.approvals != nil {
			reply = s.approvals.resolvePermission(ctx, e, s.sessionID)
		} else {
			reply = protocol.PermissionReply{
				RequestID: e.RequestID,
				Decision:  protocol.DecisionReject,
				Message:   headlessPermissionReject,
			}
		}
		select {
		case ops <- reply:
		case <-interrupted:
		}
	case protocol.QuestionAsked:
		var reply protocol.QuestionReply
		if s.approvals != nil {
			reply = s.approvals.resolveQuestion(ctx, e, s.sessionID)
		} else {
			reply = protocol.QuestionReply{RequestID: e.RequestID}
			fmt.Fprintln(stderr, "strike:", headlessQuestionReject)
		}
		select {
		case ops <- reply:
		case <-interrupted:
		}
	case protocol.EngineError:
		*turnErr = errors.New(e.Message)
		s.errMsg = e.Message
	case protocol.TurnCompleted:
		s.stopReason = e.StopReason
		s.done = true
	case protocol.ModelSelected:
		s.provider, s.model = e.Provider, e.Model
	case protocol.UsageReported:
		s.addUsage(e)
	}
	return nil
}

func (s *headlessState) emitStream(ev protocol.Event, stdout io.Writer) error {
	if s.format != execFormatStreamJSON {
		return nil
	}
	env, err := protocol.Wrap(ev)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	return enc.Encode(env)
}

func (s *headlessState) addUsage(e protocol.UsageReported) {
	s.hasUsage = true
	if e.Input.Known {
		s.usage.Input += e.Input.N
	}
	if e.Output.Known {
		s.usage.Output += e.Output.N
	}
	if e.CacheRead.Known {
		s.usage.CacheRead += e.CacheRead.N
	}
	if e.CacheCreation.Known {
		s.usage.CacheCreation += e.CacheCreation.N
	}
}

// flushJSON writes the single --output-format=json result object once.
// completed is true only when TurnCompleted was observed; incomplete turns
// (interrupt, early channel close) must not report ok=true.
func (s *headlessState) flushJSON(stdout io.Writer, turnErr error, completed bool) error {
	if s.format != execFormatJSON || s.jsonFlushed {
		return nil
	}
	s.jsonFlushed = true
	ok := completed && turnErr == nil && s.stopReason != "error" && s.errMsg == ""
	res := execJSONResult{
		Type:       "result",
		OK:         ok,
		Text:       s.text.String(),
		StopReason: s.stopReason,
		SessionID:  s.sessionID,
		Provider:   s.provider,
		Model:      s.model,
	}
	if s.errMsg != "" {
		res.Error = s.errMsg
	} else if turnErr != nil {
		res.Error = turnErr.Error()
	} else if s.stopReason == "error" {
		res.Error = "turn ended with error"
	} else if !completed {
		res.Error = "turn incomplete"
	}
	if s.hasUsage {
		u := s.usage
		res.Usage = &u
	}
	enc := json.NewEncoder(stdout)
	return enc.Encode(res)
}
