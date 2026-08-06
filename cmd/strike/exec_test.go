package main

import (
	"bytes"
	"encoding/json"
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
		name       string
		args       []string
		stdin      string
		want       string
		wantOpt    cliOptions
		wantFormat execOutputFormat
	}{
		{
			name: "joined positionals",
			args: []string{"hello", "world"},
			want: "hello world",
		},
		{
			name:       "flags then prompt",
			args:       []string{"--provider", "echo", "--model", "m", "hi"},
			want:       "hi",
			wantOpt:    cliOptions{provider: "echo", model: "m", providerSet: true},
			wantFormat: execFormatText,
		},
		{
			name:       "equals flags",
			args:       []string{"--provider=echo", "--effort=high", "ping"},
			want:       "ping",
			wantOpt:    cliOptions{provider: "echo", effort: "high", providerSet: true},
			wantFormat: execFormatText,
		},
		{
			name:       "stdin dash",
			args:       []string{"--provider=echo", "-"},
			stdin:      "from stdin\n",
			want:       "from stdin",
			wantOpt:    cliOptions{provider: "echo", providerSet: true},
			wantFormat: execFormatText,
		},
		{
			name:       "double dash separator",
			args:       []string{"--provider=echo", "--", "--looks-like-flag"},
			want:       "--looks-like-flag",
			wantOpt:    cliOptions{provider: "echo", providerSet: true},
			wantFormat: execFormatText,
		},
		{
			name:       "auto flag",
			args:       []string{"--auto", "hi"},
			want:       "hi",
			wantOpt:    cliOptions{dangerouslySkipPermissions: true},
			wantFormat: execFormatText,
		},
		{
			name:       "dangerously-skip-permissions flag",
			args:       []string{"--dangerously-skip-permissions", "hi"},
			want:       "hi",
			wantOpt:    cliOptions{dangerouslySkipPermissions: true},
			wantFormat: execFormatText,
		},
		{
			name:       "json shorthand",
			args:       []string{"--json", "--provider=echo", "hi"},
			want:       "hi",
			wantOpt:    cliOptions{provider: "echo", providerSet: true},
			wantFormat: execFormatJSON,
		},
		{
			name:       "output-format json equals",
			args:       []string{"--output-format=json", "hi"},
			want:       "hi",
			wantFormat: execFormatJSON,
		},
		{
			name:       "output-format stream-json separate",
			args:       []string{"--output-format", "stream-json", "hi"},
			want:       "hi",
			wantFormat: execFormatStreamJSON,
		},
		{
			name:       "output-format text explicit",
			args:       []string{"--output-format=text", "hi"},
			want:       "hi",
			wantFormat: execFormatText,
		},
		{
			name:       "json and matching output-format",
			args:       []string{"--json", "--output-format=json", "hi"},
			want:       "hi",
			wantFormat: execFormatJSON,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.wantFormat == "" {
				tt.wantFormat = execFormatText
			}
			opts, prompt, format, err := parseExecArgs(tt.args, strings.NewReader(tt.stdin))
			if err != nil {
				t.Fatalf("parseExecArgs: %v", err)
			}
			if prompt != tt.want {
				t.Errorf("prompt = %q, want %q", prompt, tt.want)
			}
			if opts != tt.wantOpt {
				t.Errorf("opts = %+v, want %+v", opts, tt.wantOpt)
			}
			if format != tt.wantFormat {
				t.Errorf("format = %q, want %q", format, tt.wantFormat)
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
		{name: "bad format", args: []string{"--output-format=yaml", "hi"}, err: "invalid --output-format"},
		{name: "missing format value", args: []string{"--output-format"}, err: "--output-format requires a value"},
		{name: "json with value", args: []string{"--json=true", "hi"}, err: "--json does not take a value"},
		{name: "conflicting formats", args: []string{"--json", "--output-format=text", "hi"}, err: "conflicting"},
		{name: "conflicting formats reverse", args: []string{"--output-format=stream-json", "--json", "hi"}, err: "conflicting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := parseExecArgs(tt.args, strings.NewReader(tt.stdin))
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

func TestParseExecOutputFormat(t *testing.T) {
	tests := []struct {
		in   string
		want execOutputFormat
		ok   bool
	}{
		{"text", execFormatText, true},
		{"JSON", execFormatJSON, true},
		{" stream-json ", execFormatStreamJSON, true},
		{"", execFormatText, true},
		{"yaml", "", false},
	}
	for _, tt := range tests {
		got, err := parseExecOutputFormat(tt.in)
		if tt.ok {
			if err != nil {
				t.Errorf("parseExecOutputFormat(%q): %v", tt.in, err)
				continue
			}
			if got != tt.want {
				t.Errorf("parseExecOutputFormat(%q) = %q, want %q", tt.in, got, tt.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("parseExecOutputFormat(%q) = %q, want error", tt.in, got)
		}
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
		if !strings.Contains(stdout.String(), "--output-format") {
			t.Errorf("stdout missing --output-format docs: %q", stdout.String())
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
	t.Run("bad format exit 2", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := runExecCLI([]string{"--output-format=xml", "hi"}, &stdout, &stderr); code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if !strings.Contains(stderr.String(), "invalid --output-format") {
			t.Errorf("stderr = %q", stderr.String())
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
		done <- runHeadlessFrontend(ops, events, "hello", &stdout, io.Discard, headlessOpts{})
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

func TestRunHeadlessFrontendJSONResult(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	events := make(chan protocol.Event, 16)
	var stdout bytes.Buffer

	done := make(chan error, 1)
	go func() {
		done <- runHeadlessFrontend(ops, events, "hello", &stdout, io.Discard, headlessOpts{
			Format:    execFormatJSON,
			SessionID: "sess-1",
		})
	}()

	select {
	case <-ops:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for UserInput")
	}

	events <- protocol.ModelSelected{Provider: "echo", Model: "echo-1"}
	events <- protocol.TextDelta{Text: "You said: "}
	events <- protocol.TextDelta{Text: "hello"}
	events <- protocol.UsageReported{
		Input:  protocol.KnownTokens(10),
		Output: protocol.KnownTokens(4),
		Source: protocol.UsageSourceActual,
	}
	events <- protocol.TurnCompleted{StopReason: "end_turn"}
	close(events)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runHeadlessFrontend: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	var res execJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode result: %v\nstdout=%s", err, stdout.String())
	}
	if res.Type != "result" || !res.OK {
		t.Fatalf("result = %+v", res)
	}
	if res.Text != "You said: hello" {
		t.Errorf("text = %q", res.Text)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("stopReason = %q", res.StopReason)
	}
	if res.SessionID != "sess-1" || res.Provider != "echo" || res.Model != "echo-1" {
		t.Errorf("meta = session=%q provider=%q model=%q", res.SessionID, res.Provider, res.Model)
	}
	if res.Usage == nil || res.Usage.Input != 10 || res.Usage.Output != 4 {
		t.Errorf("usage = %+v", res.Usage)
	}
	// Single JSON object only — no streamed plain text.
	if strings.Count(stdout.String(), "\n") != 1 {
		t.Errorf("want single JSON line, got %q", stdout.String())
	}
}

func TestRunHeadlessFrontendJSONError(t *testing.T) {
	ops := make(chan protocol.Op, 2)
	events := make(chan protocol.Event, 4)
	var stdout bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessFrontend(ops, events, "hi", &stdout, io.Discard, headlessOpts{Format: execFormatJSON})
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
	var res execJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if res.OK || res.Error != "No model selected" || res.StopReason != "error" {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunHeadlessFrontendStreamJSON(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	events := make(chan protocol.Event, 8)
	var stdout bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessFrontend(ops, events, "hello", &stdout, io.Discard, headlessOpts{Format: execFormatStreamJSON})
	}()
	select {
	case <-ops:
	case <-time.After(time.Second):
		t.Fatal("timed out")
	}

	events <- protocol.ModelSelected{Provider: "echo", Model: "echo"}
	events <- protocol.TextDelta{Text: "hi"}
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

	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d (%q), want 3", len(lines), stdout.String())
	}
	wantTypes := []string{"model.selected", "text.delta", "turn.completed"}
	for i, line := range lines {
		var env protocol.Envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("line %d: %v (%s)", i, err, line)
		}
		if env.Type != wantTypes[i] {
			t.Errorf("line %d type = %q, want %q", i, env.Type, wantTypes[i])
		}
		if env.Version == "" {
			t.Errorf("line %d missing envelope version", i)
		}
		// Round-trip through Decode to ensure wire shape matches pkg/protocol.
		if _, err := env.Decode(); err != nil {
			t.Errorf("line %d Decode: %v", i, err)
		}
	}
	// Must not also dump plain text.
	if strings.Contains(stdout.String(), "hi") && !strings.Contains(stdout.String(), `"text":"hi"`) {
		t.Errorf("unexpected plain text in stream-json output: %q", stdout.String())
	}
}

func TestRunHeadlessFrontendRejectsPermissionAsks(t *testing.T) {
	ops := make(chan protocol.Op, 4)
	events := make(chan protocol.Event, 8)
	done := make(chan error, 1)
	go func() {
		done <- runHeadlessFrontend(ops, events, "run true", io.Discard, io.Discard, headlessOpts{})
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
		done <- runHeadlessFrontend(ops, events, "ask me", io.Discard, &stderr, headlessOpts{})
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
		done <- runHeadlessFrontend(ops, events, "hi", io.Discard, io.Discard, headlessOpts{})
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
	err = runExec(cliOptions{provider: "echo", providerSet: true}, "hello exec", execFormatText, &stdout, &stderr)
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

func TestRunExecEchoJSON(t *testing.T) {
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
	err = runExec(cliOptions{provider: "echo", providerSet: true}, "json please", execFormatJSON, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runExec: %v\nstderr=%s", err, stderr.String())
	}
	var res execJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v\nstdout=%s", err, stdout.String())
	}
	if !res.OK || res.Type != "result" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Text, "You said: json please") {
		t.Errorf("text = %q", res.Text)
	}
	if res.SessionID == "" {
		t.Error("expected sessionId")
	}
	if res.Provider != "echo" {
		t.Errorf("provider = %q", res.Provider)
	}
}

func TestRunExecEchoStreamJSON(t *testing.T) {
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
	err = runExec(cliOptions{provider: "echo", providerSet: true}, "stream me", execFormatStreamJSON, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runExec: %v\nstderr=%s", err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("want multiple JSONL envelopes, got %q", stdout.String())
	}
	var sawText, sawDone bool
	for i, line := range lines {
		var env protocol.Envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("line %d: %v (%s)", i, err, line)
		}
		switch env.Type {
		case "text.delta":
			sawText = true
		case "turn.completed":
			sawDone = true
		}
		if _, err := env.Decode(); err != nil {
			t.Errorf("line %d Decode: %v", i, err)
		}
	}
	if !sawText || !sawDone {
		t.Errorf("sawText=%v sawDone=%v; stdout=%s", sawText, sawDone, stdout.String())
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
	err = runExec(cliOptions{}, "hi", execFormatText, io.Discard, io.Discard)
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

func TestRunExecCLIEndToEndJSON(t *testing.T) {
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
	code := runCLI([]string{"exec", "--json", "--provider", "echo", "ping"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var res execJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v\n%s", err, stdout.String())
	}
	if !res.OK || !strings.Contains(res.Text, "You said: ping") {
		t.Fatalf("result = %+v", res)
	}
}

func TestRunExecCLIEndToEndStreamJSON(t *testing.T) {
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
	code := runCLI([]string{"exec", "--output-format", "stream-json", "--provider", "echo", "ping"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d; stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var sawDone bool
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var env protocol.Envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("bad envelope %q: %v", line, err)
		}
		if env.Type == "turn.completed" {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatalf("no turn.completed in %q", stdout.String())
	}
}
