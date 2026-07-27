package main

import (
	"context"
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

Options are the same as strike ( --provider, --model, --effort,
--auto / --dangerously-skip-permissions ). Permission and question prompts cannot be
answered interactively; asks are rejected unless --auto or
--dangerously-skip-permissions is set (configured/agent denies still apply).`

const (
	headlessPermissionReject = "headless mode: permission asks are denied; pass --auto or --dangerously-skip-permissions to allow tool calls that would prompt"
	headlessQuestionReject   = "headless mode: interactive questions are not supported"
)

// runExecCLI parses `strike exec` args and runs a one-shot headless turn.
func runExecCLI(args []string, stdout, stderr io.Writer) int {
	opts, prompt, err := parseExecArgs(args, os.Stdin)
	if err != nil {
		if errors.Is(err, errExecHelp) {
			fmt.Fprintln(stdout, execUsage)
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		fmt.Fprintln(stderr, execUsage)
		return 2
	}
	if err := runExec(opts, prompt, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return 0
}

var errExecHelp = errors.New("exec help")

func parseExecArgs(args []string, stdin io.Reader) (cliOptions, string, error) {
	var flagArgs, positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			return cliOptions{}, "", errExecHelp
		}
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if len(arg) > 1 && arg[0] == '-' && arg != "-" {
			flagArgs = append(flagArgs, arg)
			// Consume a separate value for non-bool long flags when present.
			name := strings.TrimLeft(arg, "-")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				continue
			}
			switch name {
			case "provider", "model", "effort":
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					i++
					flagArgs = append(flagArgs, args[i])
				}
			case "auto", "dangerously-skip-permissions":
				// bool; optional =value already handled via '=' branch
			}
			continue
		}
		positionals = append(positionals, args[i:]...)
		break
	}

	opts, err := parseCLIOptions(flagArgs)
	if err != nil {
		return cliOptions{}, "", err
	}

	prompt, err := resolveExecPrompt(positionals, stdin)
	if err != nil {
		return cliOptions{}, "", err
	}
	return opts, prompt, nil
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

// runHeadlessFrontend submits one user prompt, streams TextDelta to stdout,
// auto-rejects interactive asks, and returns when the turn completes.
func runHeadlessFrontend(
	ops chan<- protocol.Op,
	events <-chan protocol.Event,
	prompt string,
	stdout, stderr io.Writer,
) error {
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

	var turnErr error
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				if turnErr != nil {
					return turnErr
				}
				return nil
			}
			switch e := ev.(type) {
			case protocol.TextDelta:
				if _, err := io.WriteString(stdout, e.Text); err != nil {
					return err
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
				turnErr = errors.New(e.Message)
			case protocol.TurnCompleted:
				if turnErr != nil {
					return turnErr
				}
				if e.StopReason == "error" {
					return errors.New("turn ended with error")
				}
				return nil
			}
		case <-interrupted:
			// Drain until turn completes or events close after interrupt.
			for ev := range events {
				switch e := ev.(type) {
				case protocol.TextDelta:
					_, _ = io.WriteString(stdout, e.Text)
				case protocol.TurnCompleted:
					return context.Canceled
				case protocol.EngineError:
					turnErr = errors.New(e.Message)
				}
			}
			if turnErr != nil {
				return turnErr
			}
			return context.Canceled
		}
	}
}
