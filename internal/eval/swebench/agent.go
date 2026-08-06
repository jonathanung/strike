package swebench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ExecResult is the parsed strike exec --json payload plus timing.
type ExecResult struct {
	OK         bool
	Text       string
	StopReason string
	Error      string
	SessionID  string
	Provider   string
	Model      string
	Usage      *Usage
	Duration   time.Duration
	Raw        []byte
	ExitCode   int
}

// execJSON mirrors cmd/strike.execJSONResult (keep fields in sync).
type execJSON struct {
	Type       string `json:"type"`
	OK         bool   `json:"ok"`
	Text       string `json:"text,omitempty"`
	StopReason string `json:"stopReason,omitempty"`
	Error      string `json:"error,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Usage      *Usage `json:"usage,omitempty"`
}

// AgentDriver runs strike exec against a workspace.
type AgentDriver interface {
	Run(ctx context.Context, workDir, prompt string, opts AgentOpts) (ExecResult, error)
}

// AgentOpts configures the strike exec invocation.
type AgentOpts struct {
	// Strike is the strike binary (default: "strike" on PATH, or STRIKE_BIN).
	Strike   string
	Provider string
	Model    string
	Effort   string
	// ExtraArgs are appended before the prompt (e.g. sandbox flags).
	ExtraArgs []string
	// Timeout bounds the whole exec (0 = no extra timeout beyond ctx).
	Timeout time.Duration
	// Env extra environment for the child.
	Env []string
}

// StrikeExec drives `strike exec --json --auto` in workDir.
type StrikeExec struct {
	// LookPath resolves the binary; defaults to exec.LookPath.
	LookPath func(string) (string, error)
	// RunCommand runs strike; tests inject fakes. When nil, uses exec.CommandContext.
	RunCommand func(ctx context.Context, workDir, bin string, args []string, env []string) (stdout, stderr []byte, exitCode int, err error)
}

// Run implements AgentDriver.
func (s *StrikeExec) Run(ctx context.Context, workDir, prompt string, opts AgentOpts) (ExecResult, error) {
	if strings.TrimSpace(prompt) == "" {
		return ExecResult{}, fmt.Errorf("swebench: empty agent prompt")
	}
	if workDir == "" {
		return ExecResult{}, fmt.Errorf("swebench: empty workDir")
	}
	bin := opts.Strike
	if bin == "" {
		bin = os.Getenv("STRIKE_BIN")
	}
	if bin == "" {
		bin = "strike"
	}
	look := s.lookPath
	if resolved, err := look(bin); err == nil {
		bin = resolved
	} else if !strings.Contains(bin, string(os.PathSeparator)) {
		return ExecResult{}, fmt.Errorf("swebench: strike binary %q not found: %w", bin, err)
	}

	args := []string{"exec", "--json", "--auto"}
	if opts.Provider != "" {
		args = append(args, "--provider", opts.Provider)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	args = append(args, opts.ExtraArgs...)
	// Prompt via stdin ("-") avoids shell quoting / argv length issues.
	args = append(args, "-")

	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	start := time.Now()
	var stdout, stderr []byte
	var code int
	var runErr error
	if s != nil && s.RunCommand != nil {
		stdout, stderr, code, runErr = s.RunCommand(runCtx, workDir, bin, args, opts.Env)
	} else {
		stdout, stderr, code, runErr = runStrikeExec(runCtx, workDir, bin, args, opts.Env, prompt)
	}
	dur := time.Since(start)
	if runErr != nil {
		return ExecResult{Duration: dur, ExitCode: code, Raw: stdout}, fmt.Errorf("swebench: strike exec: %w\nstderr: %s", runErr, truncate(string(stderr), 500))
	}

	res, err := ParseExecJSON(stdout)
	res.Duration = dur
	res.ExitCode = code
	res.Raw = append([]byte(nil), stdout...)
	if err != nil {
		// Still return partial result for metrics when possible.
		if res.Error == "" {
			res.Error = err.Error()
		}
		return res, fmt.Errorf("swebench: parse exec json: %w\nstderr: %s", err, truncate(string(stderr), 300))
	}
	if !res.OK && res.Error == "" {
		res.Error = "strike exec reported ok=false"
	}
	return res, nil
}

func (s *StrikeExec) lookPath(file string) (string, error) {
	if s != nil && s.LookPath != nil {
		return s.LookPath(file)
	}
	return exec.LookPath(file)
}

func runStrikeExec(ctx context.Context, workDir, bin string, args []string, env []string, prompt string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir
	cmd.Stdin = strings.NewReader(prompt)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			// Non-zero exit still may have JSON on stdout.
			err = nil
		} else {
			return stdout.Bytes(), stderr.Bytes(), -1, err
		}
	}
	return stdout.Bytes(), stderr.Bytes(), code, nil
}

// ParseExecJSON decodes the last JSON object from strike exec --json stdout.
func ParseExecJSON(stdout []byte) (ExecResult, error) {
	trim := bytes.TrimSpace(stdout)
	if len(trim) == 0 {
		return ExecResult{}, fmt.Errorf("empty stdout")
	}
	// Prefer last non-empty line (stream noise before final object is rare but possible).
	lines := bytes.Split(trim, []byte("\n"))
	var last []byte
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) > 0 {
			last = line
			break
		}
	}
	var raw execJSON
	if err := json.Unmarshal(last, &raw); err != nil {
		// Try whole buffer.
		if err2 := json.Unmarshal(trim, &raw); err2 != nil {
			return ExecResult{}, fmt.Errorf("json: %w", err)
		}
	}
	return ExecResult{
		OK:         raw.OK,
		Text:       raw.Text,
		StopReason: raw.StopReason,
		Error:      raw.Error,
		SessionID:  raw.SessionID,
		Provider:   raw.Provider,
		Model:      raw.Model,
		Usage:      raw.Usage,
	}, nil
}

// BuildAgentPrompt formats the SWE-bench problem for strike exec.
func BuildAgentPrompt(in Instance) string {
	var b strings.Builder
	b.WriteString("You are fixing a real GitHub issue inside a checked-out repository at the current working directory.\n")
	b.WriteString("Implement a minimal correct patch that resolves the issue. Do not modify tests unless required.\n")
	b.WriteString("When done, ensure the working tree contains your changes (git-tracked edits).\n\n")
	fmt.Fprintf(&b, "Instance: %s\n", in.InstanceID)
	if in.Repo != "" {
		fmt.Fprintf(&b, "Repository: %s\n", in.Repo)
	}
	b.WriteString("\n--- ISSUE ---\n")
	b.WriteString(strings.TrimSpace(in.ProblemStatement))
	b.WriteString("\n")
	return b.String()
}
