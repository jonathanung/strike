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

	"github.com/jonathanung/strike-cli/internal/protocol"
)

const execUsage = `Run one prompt headlessly and stream the assistant reply to stdout.

Usage:
  strike exec [options] <prompt...>
  strike exec [options] -              read prompt from stdin

Options are the same as strike ( --provider, --model, --effort, --sandbox,
--i-know, --auto / --dangerously-skip-permissions ), plus:

  --output-format <fmt>   text (default) | json | stream-json
  --json                  shorthand for --output-format=json

Formats:
  text         plain assistant text (current default)
  json         single result object on stdout when the turn ends
  stream-json  one protocol Event envelope (JSONL) per line

Exit codes: 0 turn ok, 1 turn/runtime error, 2 usage error.
Permission and question prompts cannot be answered interactively; asks are
rejected unless --auto or --dangerously-skip-permissions is set
(configured/agent denies still apply).`

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
	opts, prompt, format, err := parseExecArgs(args, os.Stdin)
	if err != nil {
		if errors.Is(err, errExecHelp) {
			fmt.Fprintln(stdout, execUsage)
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		fmt.Fprintln(stderr, execUsage)
		return 2
	}
	if err := runExec(opts, prompt, format, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return 0
}

var errExecHelp = errors.New("exec help")

func parseExecArgs(args []string, stdin io.Reader) (cliOptions, string, execOutputFormat, error) {
	var flagArgs, positionals []string
	format := execFormatText
	formatSet := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			return cliOptions{}, "", "", errExecHelp
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
					return cliOptions{}, "", "", fmt.Errorf("--json does not take a value")
				}
				if formatSet && format != execFormatJSON {
					return cliOptions{}, "", "", fmt.Errorf("conflicting --json and --output-format=%s", format)
				}
				format = execFormatJSON
				formatSet = true
				continue
			case "output-format":
				if !hasEq {
					if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
						return cliOptions{}, "", "", fmt.Errorf("--output-format requires a value (text|json|stream-json)")
					}
					i++
					value = args[i]
				}
				f, err := parseExecOutputFormat(value)
				if err != nil {
					return cliOptions{}, "", "", err
				}
				if formatSet && format != f {
					return cliOptions{}, "", "", fmt.Errorf("conflicting output format flags")
				}
				format = f
				formatSet = true
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
		return cliOptions{}, "", "", err
	}

	prompt, err := resolveExecPrompt(positionals, stdin)
	if err != nil {
		return cliOptions{}, "", "", err
	}
	return opts, prompt, format, nil
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
	Format    execOutputFormat
	SessionID string
}

// runHeadlessFrontend submits one user prompt, writes output per Format,
// auto-rejects interactive asks, and returns when the turn completes.
func runHeadlessFrontend(
	ops chan<- protocol.Op,
	events <-chan protocol.Event,
	prompt string,
	stdout, stderr io.Writer,
	opts headlessOpts,
) error {
	if opts.Format == "" {
		opts.Format = execFormatText
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
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

	state := &headlessState{format: opts.Format, sessionID: opts.SessionID}
	var turnErr error
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if err := state.flushJSON(stdout, turnErr, ""); err != nil {
					return err
				}
				if turnErr != nil {
					return turnErr
				}
				return nil
			}
			if err := state.handleEvent(ev, ops, stdout, stderr, interrupted, &turnErr); err != nil {
				return err
			}
			if state.done {
				if err := state.flushJSON(stdout, turnErr, state.stopReason); err != nil {
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
					_ = state.flushJSON(stdout, turnErr, e.StopReason)
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
			_ = state.flushJSON(stdout, turnErr, state.stopReason)
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
		select {
		case ops <- protocol.PermissionReply{
			RequestID: e.RequestID,
			Decision:  protocol.DecisionReject,
			Message:   headlessPermissionReject,
		}:
		case <-interrupted:
		}
	case protocol.QuestionAsked:
		select {
		case ops <- protocol.QuestionReply{RequestID: e.RequestID}:
		case <-interrupted:
		}
		fmt.Fprintln(stderr, "strike:", headlessQuestionReject)
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

func (s *headlessState) flushJSON(stdout io.Writer, turnErr error, stopReason string) error {
	if s.format != execFormatJSON || s.jsonFlushed {
		return nil
	}
	s.jsonFlushed = true
	if stopReason != "" {
		s.stopReason = stopReason
	}
	res := execJSONResult{
		Type:       "result",
		OK:         turnErr == nil && s.stopReason != "error" && s.errMsg == "",
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
	}
	if s.hasUsage {
		u := s.usage
		res.Usage = &u
	}
	enc := json.NewEncoder(stdout)
	return enc.Encode(res)
}
