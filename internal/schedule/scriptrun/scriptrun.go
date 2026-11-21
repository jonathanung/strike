// Package scriptrun is the script-schedule fire path: one `bash -c` run
// with the same permission, hook, sandbox, and admission posture an
// agent's bash call gets, and no session (docs/design/script-schedules.md,
// "Fire path").
//
// It does not import internal/schedule; cmd/strike/schedule_wire.go maps
// schedule.ScriptRequest onto Request and Result back onto
// schedule.ScriptResult.
package scriptrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/harness/scheduler"
	"github.com/jonathanung/strike/harness/secretref"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// Outcome statuses (match schedule.Status*).
const (
	StatusFinished = "finished"
	StatusFailed   = "failed"
	StatusDenied   = "denied"
	StatusError    = "error"
)

// Permission decisions recorded on every run (match schedule.Decision*).
const (
	DecisionAllow = "allow"
	// DecisionArmed is an Ask answered by the arming confirmation: the
	// human confirmed this exact command string when the schedule was armed.
	DecisionArmed     = "armed"
	DecisionDeny      = "deny"
	DecisionHookBlock = "hook_block"
)

// Output budgets: the head is the first line of a short preview; the tail
// is what the run keeps (sidecar cap, design "Output retention").
const (
	maxHead = 4 << 10
	maxTail = 64 << 10
)

// Request is one script fire. Dir is resolved and exists; PermissionMode is
// resolved (workspace, then schedule, then plan).
type Request struct {
	Command        string
	Dir            string
	Timeout        time.Duration
	SecretRefs     []string
	PermissionMode string
}

// Policy is the rule set an unattended root under Dir would get: managed,
// global, and (trusted) project config. It never carries the process
// --auto / --dangerously-skip-permissions allow-all layer.
type Policy struct {
	Rules      []permission.Ruleset
	LayerNames []string
	Managed    permission.Ruleset
	HookRules  permission.HookRuleset
	// ShellHooks are the config's shell-command hooks; pre_tool_use ones
	// matching "bash" run before the script as they would before an
	// agent's bash call.
	ShellHooks []tool.HookDef
	// LockedMode, when set, is a managed permission mode that wins over
	// the request's mode, as it does for an unattended root.
	LockedMode   string
	SandboxMode  sandbox.Mode
	AllowDegrade bool
	NetworkAllow []string
}

// Start is reported immediately before exec.
type Start struct {
	Decision  string
	Sandbox   string
	StartedAt time.Time
}

// Result is the outcome. ExitCode is nil when no process started; Output is
// the raw combined tail (the caller scrubs it).
type Result struct {
	Status    string
	Decision  string
	Sandbox   string
	StartedAt time.Time
	EndedAt   time.Time
	ExitCode  *int
	Output    []byte
	Error     string
}

// Runner holds the daemon-wide pieces. Policy is required; a nil Scheduler
// admits immediately (same as the bash tool with no scheduler).
type Runner struct {
	Policy          func(dir string) (Policy, error)
	Scheduler       *scheduler.Scheduler
	SchedulerPolicy *scheduler.Effective
	// OnHook receives each matched declarative hook rule (the hook audit
	// family). OnSecretRefUse receives each resolved ref's class and name
	// hash, never its value.
	OnHook         func(permission.HookHit)
	OnSecretRefUse func(refClass, refHash, action string)
	Now            func() time.Time
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// prepared is everything decided before admission and exec.
type prepared struct {
	decision string
	policy   sandbox.Policy
	env      []string
}

// Run executes req. started is called once, right before exec, and not at
// all when the run is refused.
func (r *Runner) Run(ctx context.Context, req Request, started func(Start)) Result {
	if r == nil || r.Policy == nil {
		return Result{Status: StatusError, Error: "script runner is not configured"}
	}
	prep, refused := r.prepare(ctx, req)
	if refused != nil {
		return *refused
	}
	sb := describeSandbox(prep.policy, false, false)

	// Admission after permission and before exec, so a cancelled waiter
	// never starts a process. The timeout starts after admission.
	if r.Scheduler != nil {
		lease, err := r.Scheduler.Acquire(ctx, tool.PoolsForCommand(r.SchedulerPolicy, req.Command)...)
		if err != nil {
			return Result{Status: StatusError, Decision: prep.decision, Sandbox: sb, Error: fmt.Sprintf("admission: %v", err)}
		}
		defer lease.Release()
	}

	start := r.now()
	if started != nil {
		started(Start{Decision: prep.decision, Sandbox: sb, StartedAt: start})
	}
	proc, err := tool.RunProcess(ctx, tool.ProcessSpec{
		Argv:      []string{"bash", "-c", req.Command},
		Dir:       req.Dir,
		Env:       prep.env,
		Timeout:   req.Timeout,
		MaxOutput: maxHead,
		TailMax:   maxTail,
		Combine:   true,
		Sandbox:   prep.policy,
	}, tool.ProcessObserver{})
	return finish(req, prep, start, r.now(), proc, err)
}

// prepare is steps 2–4 of the fire path plus the env: mode, permission,
// hooks, sandbox policy. A non-nil Result means the run is refused before
// any process starts.
func (r *Runner) prepare(ctx context.Context, req Request) (prepared, *Result) {
	pol, err := r.Policy(req.Dir)
	if err != nil {
		return prepared{}, &Result{Status: StatusError, Error: fmt.Sprintf("permission policy: %v", err)}
	}
	mode := protocol.PermissionMode(req.PermissionMode)
	if strings.TrimSpace(pol.LockedMode) != "" {
		mode = protocol.PermissionMode(pol.LockedMode)
	}
	perms := permission.New(nil, pol.Rules...)
	if len(pol.LayerNames) > 0 {
		perms.SetBaseLayerNames(pol.LayerNames...)
	}
	if len(pol.Managed) > 0 {
		perms.SetManagedRules(pol.Managed)
	}
	// Plan's write denies live in the mode-late layer and reach the
	// compiled sandbox policy only through it.
	perms.SetPermissionMode(mode)

	var decision string
	switch perms.Peek("bash", req.Command) {
	case permission.Deny:
		return prepared{}, &Result{Status: StatusDenied, Decision: DecisionDeny, Error: "denied by permission rules"}
	case permission.Allow:
		decision = DecisionAllow
	default:
		// Nobody is there to ask: the arming confirmation is the answer for
		// this exact command string (the hash was checked before the fire).
		decision = DecisionArmed
	}

	hooks := permission.EvaluateHooks(pol.HookRules, permission.HookEventPreToolUse, "bash")
	if r.OnHook != nil {
		for _, hit := range hooks.Log {
			r.OnHook(hit)
		}
		for _, hit := range hooks.Notify {
			r.OnHook(hit)
		}
	}
	if hooks.Block {
		hit := hooks.BlockHit
		if strings.TrimSpace(hit.Message) == "" {
			hit.Message = permission.DefaultBlockMessage(hit.Event, hit.Matcher, "bash")
		}
		if r.OnHook != nil {
			r.OnHook(hit)
		}
		return prepared{}, &Result{Status: StatusDenied, Decision: DecisionHookBlock, Error: "blocked by hook: " + hit.Message}
	}

	if blocked := r.runShellHooks(ctx, perms, pol.ShellHooks, req); blocked != "" {
		return prepared{}, &Result{Status: StatusDenied, Decision: DecisionHookBlock, Error: "blocked by hook: " + blocked}
	}

	policy := perms.CompileSandbox(pol.SandboxMode, req.Dir)
	policy.NetworkAllow = sandbox.CloneNetworkAllow(pol.NetworkAllow)
	policy.AllowDegrade = pol.AllowDegrade

	env, err := tool.BashEnv(secretEnv(req.SecretRefs), r.OnSecretRefUse)
	if err != nil {
		return prepared{}, &Result{Status: StatusError, Decision: decision, Error: fmt.Sprintf("secret refs: %v", err)}
	}
	return prepared{decision: decision, policy: policy, env: env}, nil
}

// runShellHooks runs the pre_tool_use shell hooks for "bash" and returns
// the block reason, or "" when the run may proceed. Running a hook command
// is itself a "hook" permission: with nobody to ask, only an allowed hook
// command runs; any other is refused, which blocks (fail closed).
func (r *Runner) runShellHooks(ctx context.Context, perms *permission.Service, defs []tool.HookDef, req Request) string {
	if len(defs) == 0 {
		return ""
	}
	input, _ := json.Marshal(map[string]string{"command": req.Command})
	out, err := tool.RunHooks(ctx, defs, permission.HookEventPreToolUse, tool.HookPayload{
		SchemaVersion: tool.LifecycleVocabularyVersion,
		CWD:           req.Dir,
		Subject:       "bash",
		ToolName:      "bash",
		ToolInput:     input,
	}, req.Dir, func(_ context.Context, command string) error {
		if perms.Peek("hook", command) == permission.Allow {
			return nil
		}
		return fmt.Errorf("hook command is not allowed for unattended runs")
	})
	if r.OnHook != nil {
		for _, d := range out.Decisions {
			action := d.Decision
			if action == "" {
				action = "allow"
			}
			msg := strings.TrimSpace(d.Reason)
			if d.Command != "" {
				msg = strings.TrimSpace(d.Command + ": " + msg)
			}
			r.OnHook(permission.HookHit{Event: permission.HookEventPreToolUse, Action: "shell_" + action, Message: msg, Tool: "bash"})
		}
	}
	if err != nil {
		return fmt.Sprintf("hook did not complete: %v", err)
	}
	if !out.Allow {
		if reason := strings.TrimSpace(out.Inject); reason != "" {
			return reason
		}
		return "shell hook refused the command"
	}
	return ""
}

// secretEnv maps each secret://env/NAME ref onto destination NAME, the way
// a script reads it ($NAME). Invalid refs pass through so BashEnv rejects
// them with its own error.
func secretEnv(refs []string) map[string]string {
	if len(refs) == 0 {
		return nil
	}
	out := make(map[string]string, len(refs))
	for _, raw := range refs {
		ref, ok := secretref.ParseRef(raw)
		if !ok {
			out[raw] = raw
			continue
		}
		out[ref.Name] = ref.String()
	}
	return out
}

// finish maps the process outcome onto a run status.
func finish(req Request, prep prepared, start, end time.Time, proc tool.ProcessResult, err error) Result {
	res := Result{
		Decision:  prep.decision,
		Sandbox:   describeSandbox(prep.policy, proc.SandboxApplied, proc.SandboxDegraded),
		StartedAt: start,
		EndedAt:   end,
		Output:    []byte(proc.Tail),
	}
	if proc.SandboxBackend != "" {
		res.Sandbox += " (" + proc.SandboxBackend + ")"
	}
	if len(res.Output) == 0 && proc.Output != "" {
		res.Output = []byte(proc.Output)
	}
	if err != nil {
		var coded *tool.CodedError
		if errors.As(err, &coded) && coded.Code == tool.CodeSandboxDenied {
			res.Status = StatusFailed
			res.Error = "sandbox: " + coded.Message
			return res
		}
		res.Status = StatusError
		res.Error = fmt.Sprintf("start: %v", err)
		return res
	}
	code := proc.ExitCode
	res.ExitCode = &code
	switch proc.Status {
	case tool.ProcessStatusTimeout:
		res.Status = StatusFailed
		res.Error = fmt.Sprintf("timed out after %s", req.Timeout)
	case tool.ProcessStatusCanceled:
		res.Status = StatusFailed
		res.Error = "canceled: daemon shutting down"
	default:
		if code == 0 {
			res.Status = StatusFinished
		} else {
			res.Status = StatusFailed
			res.Error = fmt.Sprintf("exit code %d", code)
		}
	}
	return res
}

// describeSandbox is the audit/run sandbox note: the dial before exec, the
// applied state after.
func describeSandbox(p sandbox.Policy, applied, degraded bool) string {
	switch {
	case applied:
		return "applied"
	case degraded:
		return "degraded"
	case p.Mode == sandbox.ModeOff:
		return "off"
	default:
		return p.Mode.String()
	}
}
