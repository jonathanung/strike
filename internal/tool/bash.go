package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	bashDefaultTimeout = 2 * time.Minute
	bashMaxTimeout     = 10 * time.Minute
	bashMaxOutput      = 30000
)

type bashTool struct{}

func NewBash() Tool { return bashTool{} }

func (bashTool) Name() string { return "bash" }

func (bashTool) Description() string {
	return `Executes a shell command with bash in the working directory. Returns combined stdout/stderr.

Usage notes:
- Prefer dedicated tools over shell: use read, glob, grep, edit, and write instead of cat, find, grep, sed, awk, or echo when those tools can do the job.
- Always quote file paths that contain spaces.
- Explain non-trivial commands that change the user's system before running them.
- Independent commands may be issued as parallel tool calls; chain dependent commands with && in one call.
- Optional timeoutMs (default 120000, max 600000). Long output is truncated.
- Do not use bash to communicate with the user; send a normal assistant message instead.`
}

func (bashTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"command": {"type": "string", "description": "The command to execute"},
			"timeoutMs": {"type": "integer", "description": "Timeout in milliseconds (default 120000, max 600000)"}
		},
		"required": ["command"]
	}`)
}

type bashArgs struct {
	Command   string `json:"command"`
	TimeoutMs int    `json:"timeoutMs"`
}

func (bashTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a bashArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return Result{}, fmt.Errorf("command is empty")
	}
	// An "always" grant covers the command's first word as a prefix class
	// (e.g. approving "git status" always also covers "git pull").
	always := []string{a.Command}
	if fields := strings.Fields(a.Command); len(fields) > 1 {
		always = []string{fields[0] + " *"}
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "bash", Patterns: []string{a.Command}, Always: always}); err != nil {
		return Result{}, err
	}

	timeout := bashDefaultTimeout
	if a.TimeoutMs > 0 {
		timeout = min(time.Duration(a.TimeoutMs)*time.Millisecond, bashMaxTimeout)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "bash", "-c", a.Command)
	cmd.Dir = tc.WorkDir
	out, err := cmd.CombinedOutput()

	output := string(out)
	if len(output) > bashMaxOutput {
		output = output[:bashMaxOutput] + fmt.Sprintf("\n… (output truncated, %d bytes total)", len(out))
	}
	exitCode := 0
	if err != nil {
		exitCode = cmd.ProcessState.ExitCode()
		if runCtx.Err() == context.DeadlineExceeded {
			output += fmt.Sprintf("\n(command timed out after %s)", timeout)
		} else if exitCode < 0 {
			return Result{}, err
		}
		output += fmt.Sprintf("\n(exit code %d)", exitCode)
	}
	if strings.TrimSpace(output) == "" {
		output = "(no output)"
	}
	meta, _ := json.Marshal(map[string]any{"exitCode": exitCode})
	return Result{Title: a.Command, Output: output, Metadata: meta}, nil
}
