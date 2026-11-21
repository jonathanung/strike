package deploywatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/pkg/redact"
)

const (
	shellTimeout = 60 * time.Second
	shellOutMax  = 16 << 10
	maxCommand   = 2000
)

// Shell runs workspace-configured shell checks (#1783) under the same OS
// sandbox dial as agent bash. Exit 0 is healthy, any other exit is failing,
// and a check that could not run (no shell, sandbox required but missing,
// timeout) is unreachable, never healthy.
type Shell struct {
	// Sandbox is the user's sandbox preference. Zero value is ModeOff (host
	// execution), which remains a supported mode.
	Sandbox sandbox.Policy
	// WorkDir is the check's working directory and the sandbox workspace.
	WorkDir string
	// Timeout bounds one run. Zero means 60s.
	Timeout time.Duration
}

// Run executes command with bash -c.
func (sh *Shell) Run(ctx context.Context, command string) (state, detail string) {
	timeout := sh.Timeout
	if timeout <= 0 {
		timeout = shellTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	bash, err := exec.LookPath("bash")
	if err != nil {
		return StateUnreachable, "bash not found"
	}
	policy := sh.Sandbox
	policy.WorkDir = sh.WorkDir
	wrap := sandbox.WrapResult([]string{bash, "-c", command}, policy)
	if wrap.Degraded && policy.Mode != sandbox.ModeOff && !policy.AllowDegrade {
		return StateUnreachable, "OS sandbox is required for shell checks and is unavailable"
	}
	cmd := exec.CommandContext(ctx, wrap.Argv[0], wrap.Argv[1:]...)
	cmd.Dir = sh.WorkDir
	var out capBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	tail := redact.String(lastLine(out.String()))
	if ctx.Err() == context.DeadlineExceeded {
		return StateUnreachable, fmt.Sprintf("timed out after %s", timeout)
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
		return StateHealthy, withTail("exit 0", tail)
	case errors.As(err, &exit) && exit.ExitCode() > 0:
		return StateFailing, withTail(fmt.Sprintf("exit %d", exit.ExitCode()), tail)
	default:
		return StateUnreachable, clip(err.Error(), maxDetail)
	}
}

func withTail(head, tail string) string {
	if tail == "" {
		return head
	}
	return clip(head+": "+tail, maxDetail)
}

// lastLine is the final non-empty output line, the usual place a check
// prints its reason.
func lastLine(s string) string {
	lines := bytes.Split(bytes.TrimSpace([]byte(s)), []byte("\n"))
	if len(lines) == 0 {
		return ""
	}
	return string(bytes.TrimSpace(lines[len(lines)-1]))
}

// capBuffer keeps the last shellOutMax bytes of output.
type capBuffer struct{ b []byte }

func (c *capBuffer) Write(p []byte) (int, error) {
	c.b = append(c.b, p...)
	if len(c.b) > shellOutMax {
		c.b = c.b[len(c.b)-shellOutMax:]
	}
	return len(p), nil
}

func (c *capBuffer) String() string { return string(c.b) }
