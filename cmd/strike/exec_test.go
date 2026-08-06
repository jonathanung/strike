package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestParseExecArgsPromptForms(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		stdin   string
		want    string
		wantOpt cliOptions
	}{
		{
			name: "joined positionals",
			args: []string{"hello", "world"},
			want: "hello world",
		},
		{
			name:    "flags then prompt",
			args:    []string{"--provider", "echo", "--model", "m", "hi"},
			want:    "hi",
			wantOpt: cliOptions{provider: "echo", model: "m", providerSet: true},
		},
		{
			name:    "equals flags",
			args:    []string{"--provider=echo", "--effort=high", "ping"},
			want:    "ping",
			wantOpt: cliOptions{provider: "echo", effort: "high", providerSet: true},
		},
		{
			name:    "stdin dash",
			args:    []string{"--provider=echo", "-"},
			stdin:   "from stdin\n",
			want:    "from stdin",
			wantOpt: cliOptions{provider: "echo", providerSet: true},
		},
		{
			name:    "double dash separator",
			args:    []string{"--provider=echo", "--", "--looks-like-flag"},
			want:    "--looks-like-flag",
			wantOpt: cliOptions{provider: "echo", providerSet: true},
		},
		{
			name:    "auto flag",
			args:    []string{"--auto", "hi"},
			want:    "hi",
			wantOpt: cliOptions{dangerouslySkipPermissions: true},
		},
		{
			name:    "dangerously-skip-permissions flag",
			args:    []string{"--dangerously-skip-permissions", "hi"},
			want:    "hi",
			wantOpt: cliOptions{dangerouslySkipPermissions: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, prompt, err := parseExecArgs(tt.args, strings.NewReader(tt.stdin))
			if err != nil {
				t.Fatalf("parseExecArgs: %v", err)
			}
			if prompt != tt.want {
				t.Errorf("prompt = %q, want %q", prompt, tt.want)
			}
			if opts != tt.wantOpt {
				t.Errorf("opts = %+v, want %+v", opts, tt.wantOpt)
			}
		})
	}
}

func TestParseExecArgsErrors(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		stdin string
		help  bool
		err   string
	}{
		{name: "missing prompt", args: nil, err: "missing prompt"},
		{name: "empty stdin", args: []string{"-"}, stdin: "\n", err: "empty prompt on stdin"},
		{name: "dash among words", args: []string{"a", "-", "b"}, err: "'-' to read stdin must be the only prompt argument"},
		{name: "help long", args: []string{"--help"}, help: true},
		{name: "help short", args: []string{"-h"}, help: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseExecArgs(tt.args, strings.NewReader(tt.stdin))
			if tt.help {
				if !errors.Is(err, errExecHelp) {
					t.Fatalf("err = %v, want errExecHelp", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Fatalf("err = %v, want containing %q", err, tt.err)
			}
		})
	}
}

func TestRunExecCLIHelpAndUsageErrors(t *testing.T) {
	t.Run("help", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runExecCLI([]string{"--help"}, &stdout, &stderr); code != 0 {
			t.Fatalf("exit = %d, want 0", code)
		}
		if !strings.Contains(stdout.String(), "strike exec") {
			t.Errorf("stdout = %q, want exec usage", stdout.String())
		}
		if stderr.Len() != 0 {
			t.Errorf("stderr = %q, want empty", stderr.String())
		}
	})
	t.Run("missing prompt", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runExecCLI(nil, &stdout, &stderr); code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "missing prompt") {
			t.Errorf("stderr = %q", stderr.String())
		}
		if stdout.Len() != 0 {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
	})
}

func TestRunCLIDispatchesExec(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"exec", "--help"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), execUsage) {
		t.Errorf("stdout missing exec usage: %q", stdout.String())
	}
}

func TestRunHeadlessFrontendStreamsTextAndCompletes(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	events := make(chan protocol.Event, 8)
	var stdout bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- runHeadlessFrontend(context.Background(), ops, events, "hello", &stdout, io.Discard)
	}()

	select {
	case op := <-ops:
		ui, ok := op.(protocol.UserInput)
		if !ok || ui.Text != "hello" {
			t.Fatalf("op = %#v, want UserInput hello", op)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for UserInput")
	}

	events <- protocol.ModelSelected{Provider: "echo", Model: "echo"}
	events <- protocol.TextDelta{Text: "You said: "}
	events <- protocol.TextDelta{Text: "hello"}
	events <- protocol.TurnCompleted{StopReason: "end_turn"}
	close(events)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runHeadlessFrontend: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for frontend")
	}
	if got := stdout.String(); got != "You said: hello" {
		t.Errorf("stdout = %q, want %q", got, "You said: hello")
	}
}

func TestRunHeadlessFrontendRejectsPermissionAsks(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	events := make(chan protocol.Event, 8)
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessFrontend(context.Background(), ops, events, "run true", io.Discard, io.Discard)
	}()

	select {
	case <-ops: // UserInput
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for UserInput")
	}

	events <- protocol.PermissionAsked{RequestID: "p1", Permission: "bash", Patterns: []string{"true"}}
	select {
	case op := <-ops:
		reply, ok := op.(protocol.PermissionReply)
		if !ok {
			t.Fatalf("op = %T, want PermissionReply", op)
		}
		if reply.RequestID != "p1" || reply.Decision != protocol.DecisionReject {
			t.Fatalf("reply = %#v", reply)
		}
		if !strings.Contains(reply.Message, "headless mode") {
			t.Errorf("reject message = %q", reply.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for PermissionReply")
	}

	events <- protocol.TurnCompleted{StopReason: "end_turn"}
	close(events)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestRunHeadlessFrontendRejectsQuestions(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	events := make(chan protocol.Event, 8)
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessFrontend(context.Background(), ops, events, "ask me", io.Discard, &stderr)
	}()

	select {
	case <-ops:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for UserInput")
	}

	events <- protocol.QuestionAsked{
		RequestID: "q1",
		Questions: []protocol.QuestionPrompt{{ID: "1", Question: "ok?"}},
	}
	select {
	case op := <-ops:
		reply, ok := op.(protocol.QuestionReply)
		if !ok || reply.RequestID != "q1" || len(reply.Answers) != 0 {
			t.Fatalf("reply = %#v", op)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for QuestionReply")
	}

	events <- protocol.TurnCompleted{StopReason: "end_turn"}
	close(events)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
	if !strings.Contains(stderr.String(), headlessQuestionReject) {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestRunHeadlessFrontendEngineError(t *testing.T) {
	ops := make(chan protocol.Op, 2)
	events := make(chan protocol.Event, 4)
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessFrontend(context.Background(), ops, events, "hi", io.Discard, io.Discard)
	}()
	select {
	case <-ops:
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
	events <- protocol.EngineError{Message: "No model selected"}
	events <- protocol.TurnCompleted{StopReason: "error"}
	close(events)
	select {
	case err := <-done:
		if err == nil || err.Error() != "No model selected" {
			t.Fatalf("err = %v, want No model selected", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}
}

func TestRunExecEchoOneShot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Session store under HOME via DefaultDir.
	work := t.TempDir()
	// runExec uses Getwd; change into temp workdir.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	var stdout, stderr bytes.Buffer
	err = runExec(cliOptions{provider: "echo", providerSet: true}, "hello exec", &stdout, &stderr)
	if err != nil {
		t.Fatalf("runExec: %v\nstderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "You said: hello exec") {
		t.Errorf("stdout = %q, want echo of prompt", stdout.String())
	}
	// stdout must be pure assistant text — no session log line
	if strings.Contains(stdout.String(), "session log:") {
		t.Errorf("stdout polluted with session log: %q", stdout.String())
	}

	// A session JSONL should exist under ~/.strike/sessions
	sessionsDir := filepath.Join(home, ".strike", "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		t.Fatalf("sessions dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected a session log file")
	}
}

func TestRunExecRequiresUsableProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "")
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	// Default config provider is anthropic; without credentials exec must fail
	// at startup (unlike the TUI, which can pick a provider later).
	err = runExec(cliOptions{}, "hi", io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("err = %v, want credentials failure", err)
	}
}

func TestRunExecCLIEndToEndEcho(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	var stdout, stderr bytes.Buffer
	code := runCLI([]string{"exec", "--provider", "echo", "ping"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "You said: ping") {
		t.Errorf("stdout = %q", stdout.String())
	}
}
