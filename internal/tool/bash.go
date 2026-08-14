package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/scheduler"
)

const (
	bashDefaultTimeout = 2 * time.Minute
	bashMaxTimeout     = 10 * time.Minute
	// bashMaxOutput caps retained stdout+stderr bytes per call (token efficiency).
	// Agents can re-run with head/tail/grep when they need a different slice.
	bashMaxOutput = 16_000
)

type bashTool struct{}

func NewBash() Tool { return bashTool{} }

func (bashTool) Name() string { return "bash" }

func (bashTool) Contract() Contract {
	return staticContract(SideEffectProcess, IdempotencyUnsafe)
}

func (bashTool) Description() string {
	return `Executes a shell command with bash in the session working directory. Returns combined stdout/stderr.

Usage notes:
- Prefer dedicated tools over shell: use read, glob, grep, edit, and write instead of cat, find, grep, sed, awk, or echo when those tools can do the job.
- Always quote file paths that contain spaces.
- Explain non-trivial commands that change the user's system before running them.
- Independent commands may be issued as parallel tool calls; chain dependent commands with && in one call.
- Each invocation starts at the session working directory (workspace/worktree root). A cd inside one call does not affect later bash or other tools — use (cd subdir && …) or && in the same command when you need a subdirectory.
- Optional timeoutMs (default 120000, max 600000). Output is capped at ~16KB and truncated with a byte-total note.
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
		return Result{}, ErrInvalidArgs(fmt.Sprintf("invalid arguments: %v", err))
	}
	if strings.TrimSpace(a.Command) == "" {
		return Result{}, ErrInvalidArgs("command is empty")
	}
	// An "always" grant covers the command's first word as a prefix class
	// (e.g. approving "git status" always also covers "git pull").
	always := []string{a.Command}
	if fields := strings.Fields(a.Command); len(fields) > 1 {
		always = []string{fields[0] + " *"}
	}
	// Terminal-Bench live image: both eval env vars route the command into
	// the bind-mounted task container so /app paths, compilers, and image
	// Python work. SWE-bench sets only STRIKE_EVAL_CONTAINER and stays on
	// host bash + eval-test. Routed commands skip host workspace/egress
	// guards — the process is docker exec, not host shell.
	routeArgv, routed := evalContainerArgv(a.Command)

	policy := bashSandboxPolicy(tc)
	if !routed {
		// Best-effort workspace path guard for known destructive ops — runs before
		// Ask. Incomplete static parse only; not a security boundary.
		if err := checkBashWorkspaceBoundary(a.Command, tc.WorkDir); err != nil {
			return Result{}, err
		}
		// Application-layer egress preflight: when network.allow is set, fail-closed
		// on known clients outside the list, interpreters, shell networking, and
		// unknown binaries while OS host networking remains on (#1030).
		allow := networkAllowFrom(tc)
		if len(allow) == 0 {
			// Composer ! shell and other callers may only set Sandbox.NetworkAllow.
			allow = sandbox.CloneNetworkAllow(policy.NetworkAllow)
		}
		if err := checkBashNetworkAllowOpts(a.Command, allow, policy.NetworkEnabled()); err != nil {
			return Result{}, err
		}
	}
	if err := tc.Ask(ctx, AskRequest{Permission: "bash", Patterns: []string{a.Command}, Always: always}); err != nil {
		return Result{}, err
	}

	// Bash may mutate the workspace outside harness SnapshotPath calls.
	// MarkUncovered captures a shadow-git baseline; CommitTurn reconciles
	// changed paths into the checkpoint frame and clears "bash" when covered (#572).
	tc.MarkUncovered("bash")

	// Admit after permission approval and before process start so a canceled
	// waiter never emits process-started or starts an OS process. Command
	// timeout begins only after admission (RunProcess applies Timeout).
	lease, err := acquireBashLease(ctx, tc, a.Command)
	if err != nil {
		return Result{}, err
	}
	defer lease.Release() // start failure, exit, timeout, cancel, panic-safe

	timeout := bashDefaultTimeout
	if a.TimeoutMs > 0 {
		timeout = min(time.Duration(a.TimeoutMs)*time.Millisecond, bashMaxTimeout)
	}

	obs := tc.Process
	if tc.ReportOutput != nil {
		prev := obs.Output
		obs.Output = func(id, stream, data string) {
			if prev != nil {
				prev(id, stream, data)
			}
			tc.ReportOutput(data)
		}
	}

	// Minimal documented environment + explicit secret refs (never full host
	// env — avoids leaking credentials into bash children) (#1030).
	env, err := bashEnv(tc)
	if err != nil {
		return Result{}, ErrInvalidArgs(err.Error())
	}

	// Fresh process each call: Dir is always the session workdir so shell cd
	// cannot stick across tool invocations. OS sandbox (bwrap / seatbelt)
	// confines the shell when the platform backend is available and mode is
	// not off (config/CLI dial + permission-compiled denials/network).
	// Sandbox wrap stays inside RunProcess; the pool lease wraps the whole run.
	// Degrade is fail-closed unless policy.AllowDegrade (sandboxAllowDegrade).
	argv := []string{"bash", "-c", a.Command}
	if routed {
		argv = routeArgv
	}
	proc, err := RunProcess(ctx, ProcessSpec{
		Argv:      argv,
		Dir:       tc.WorkDir,
		Env:       env,
		Timeout:   timeout,
		MaxOutput: bashMaxOutput,
		Combine:   true,
		Sandbox:   policy,
	}, obs)
	if err != nil {
		return Result{}, err
	}

	output := proc.Output
	if proc.Truncated {
		output += fmt.Sprintf("\n… (output truncated, %d bytes total)", proc.BytesSeen)
	}
	exitCode := proc.ExitCode
	var errCode string
	switch proc.Status {
	case ProcessStatusTimeout:
		output += fmt.Sprintf("\n(command timed out after %s)", timeout)
		output += fmt.Sprintf("\n(exit code %d)", exitCode)
		errCode = ErrorCodeTimeout
	case ProcessStatusCanceled:
		// Preserve partial stdout/stderr; engine stamps canceled code + incomplete marker.
		if exitCode != 0 {
			output += fmt.Sprintf("\n(exit code %d)", exitCode)
		}
		errCode = ErrorCodeCanceled
	default:
		// OS sandbox capability blocks → structured sandbox_denied (not a bare
		// non-zero exit the model might ignore). Wall-time/cancel already handled.
		if reason, ok := detectSandboxDenial(proc); ok {
			if exitCode != 0 {
				output += fmt.Sprintf("\n(exit code %d)", exitCode)
			}
			output = formatSandboxDenial(reason, output)
			errCode = ErrorCodeSandboxDenied
		} else if exitCode != 0 {
			output += fmt.Sprintf("\n(exit code %d)", exitCode)
		}
	}
	if strings.TrimSpace(output) == "" {
		output = "(no output)"
	}
	metaFields := map[string]any{"exitCode": exitCode}
	if proc.SandboxApplied {
		metaFields["sandboxApplied"] = true
		if proc.SandboxBackend != "" {
			metaFields["sandboxBackend"] = proc.SandboxBackend
		}
	}
	if proc.SandboxDegraded {
		metaFields["sandboxDegraded"] = true
	}
	if errCode != "" {
		metaFields["errorCode"] = errCode
		if errCode == ErrorCodeTimeout || errCode == ErrorCodeCanceled {
			metaFields["incomplete"] = true
		}
	}
	if exitCode == 0 && errCode == "" {
		if pr, ok := extractSessionPR(a.Command, output); ok {
			metaFields["prUrl"] = pr.URL
			if pr.Number > 0 {
				metaFields["prNumber"] = pr.Number
			}
			if pr.State != "" {
				metaFields["prState"] = pr.State
			}
			if tc.RecordSessionPR != nil {
				_ = tc.RecordSessionPR(pr)
			}
		}
	}
	meta, _ := json.Marshal(metaFields)
	return Result{Title: a.Command, Output: output, Metadata: meta, ErrorCode: errCode}, nil
}

// acquireBashLease acquires process (+ build/test when classified) pools.
// When Scheduler is nil, returns a no-op lease (unlimited / current behavior).
// Multi-pool grants use Scheduler.Acquire's deadlock-free atomic path (FIFO).
// Prefer SchedulerAcquire when set so the engine can emit queue lifecycle events.
func acquireBashLease(ctx context.Context, tc *Context, command string) (*scheduler.Lease, error) {
	if tc == nil || (tc.Scheduler == nil && tc.SchedulerAcquire == nil) {
		return nil, nil // Lease.Release is nil-safe
	}
	pools := bashPoolsForCommand(tc, command)
	label := bashQueueLabel(pools)
	if tc.SchedulerAcquire != nil {
		lease, err := tc.SchedulerAcquire(ctx, label, pools...)
		if err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
		return lease, nil
	}
	lease, err := tc.Scheduler.Acquire(ctx, pools...)
	if err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	return lease, nil
}

// bashQueueLabel is a short UI/protocol tag for bash admission (no argv leak).
func bashQueueLabel(pools []string) string {
	extra := make([]string, 0, len(pools))
	for _, p := range pools {
		if p == scheduler.PoolProcess {
			continue
		}
		extra = append(extra, p)
	}
	if len(extra) == 0 {
		return "bash"
	}
	return "bash:" + strings.Join(extra, ",")
}

// bashPoolsForCommand returns admission pool names for a bash command.
func bashPoolsForCommand(tc *Context, command string) []string {
	if tc != nil && tc.SchedulerPolicy != nil {
		return tc.SchedulerPolicy.PoolsForCommand(command)
	}
	return scheduler.PoolsForClass(scheduler.ClassGeneral)
}

// bashSandboxMode resolves the OS sandbox dial from tool context.
// Empty/unset defaults to workspace-write (product default).
func bashSandboxMode(tc *Context) sandbox.Mode {
	if tc == nil {
		return sandbox.DefaultMode
	}
	return sandbox.ResolveMode(tc.SandboxMode)
}

// bashSandboxPolicy returns the OS policy for a bash invocation.
// When tc.Sandbox carries a compiled policy (WorkDir and/or denial/network
// fields, or a non-off Mode), that policy is used (WorkDir filled from tc
// when empty). Otherwise Mode/WorkDir are derived from SandboxMode + WorkDir.
// Host networking stays on unless the compiled policy sets NoNetwork
// (zero-value Policy keeps network — product default).
func bashSandboxPolicy(tc *Context) sandbox.Policy {
	if tc == nil {
		return sandbox.Policy{Mode: sandbox.DefaultMode}
	}
	p := tc.Sandbox
	if compiledSandboxPolicy(p) {
		if strings.TrimSpace(p.WorkDir) == "" {
			p.WorkDir = tc.WorkDir
		}
	} else {
		p = sandbox.Policy{
			Mode:    bashSandboxMode(tc),
			WorkDir: tc.WorkDir,
		}
	}
	// Always surface config network.allow on the OS policy snapshot (explain +
	// shared shape). Enforcement for bash is application preflight, not OS.
	if len(p.NetworkAllow) == 0 {
		p.NetworkAllow = sandbox.CloneNetworkAllow(tc.NetworkAllow)
	}
	return p
}

func compiledSandboxPolicy(p sandbox.Policy) bool {
	return p.Mode != sandbox.ModeOff ||
		strings.TrimSpace(p.WorkDir) != "" ||
		p.NoWorkspaceWrite ||
		p.NoNetwork ||
		len(p.DenyWritePaths) > 0 ||
		len(p.DenyWriteGlobs) > 0
}

// githubPRURLRe matches common GitHub pull request URLs in gh CLI output.
var githubPRURLRe = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/pull/(\d+)`)

// githubPRStateRe matches "state":"OPEN" style JSON from gh pr view --json.
var githubPRStateRe = regexp.MustCompile(`(?i)"state"\s*:\s*"(open|merged|closed)"`)

// extractSessionPR pulls a PR URL/number/state from successful gh pr command output.
func extractSessionPR(command, output string) (SessionPR, bool) {
	if !looksLikeGHPRCommand(command) {
		return SessionPR{}, false
	}
	m := githubPRURLRe.FindStringSubmatch(output)
	if m == nil {
		return SessionPR{}, false
	}
	n, _ := strconv.Atoi(m[1])
	state := "open"
	if sm := githubPRStateRe.FindStringSubmatch(output); sm != nil {
		state = strings.ToLower(sm[1])
	}
	return SessionPR{URL: m[0], Number: n, State: state}, true
}

func looksLikeGHPRCommand(command string) bool {
	fields := strings.Fields(command)
	if len(fields) < 2 {
		return false
	}
	// Allow env prefixes: FOO=bar gh pr create …
	idx := 0
	for idx < len(fields) && strings.Contains(fields[idx], "=") && !strings.HasPrefix(fields[idx], "-") {
		idx++
	}
	if idx >= len(fields) {
		return false
	}
	cmd := fields[idx]
	if base := fields[idx]; strings.Contains(base, "/") {
		// path/to/gh
		parts := strings.Split(base, "/")
		cmd = parts[len(parts)-1]
	}
	if cmd != "gh" {
		return false
	}
	for _, f := range fields[idx+1:] {
		if f == "pr" {
			return true
		}
	}
	return false
}
