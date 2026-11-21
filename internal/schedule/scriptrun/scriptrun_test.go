package scriptrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/harness/scheduler"
	"github.com/jonathanung/strike/harness/tool"
)

// hostPolicy is the product defaults (bash asks) with the sandbox dial off,
// plus extra config rules.
func hostPolicy(extra ...permission.Rule) Policy {
	return Policy{
		Rules:       []permission.Ruleset{permission.Defaults(), extra},
		SandboxMode: sandbox.ModeOff,
	}
}

func runner(pol Policy) *Runner {
	return &Runner{Policy: func(string) (Policy, error) { return pol, nil }}
}

type startLog struct {
	mu     sync.Mutex
	starts []Start
}

func (l *startLog) add(st Start) {
	l.mu.Lock()
	l.starts = append(l.starts, st)
	l.mu.Unlock()
}

func (l *startLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.starts)
}

func TestRunPermissionDecisions(t *testing.T) {
	cases := []struct {
		name         string
		pol          Policy
		mode         string
		wantStatus   string
		wantDecision string
		wantProcess  bool
	}{
		{
			name:       "config deny refuses before exec",
			pol:        hostPolicy(permission.Rule{Permission: "bash", Pattern: "touch marker", Action: permission.Deny}),
			mode:       "default",
			wantStatus: StatusDenied, wantDecision: DecisionDeny,
		},
		{
			name: "managed deny wins over a config allow",
			pol: func() Policy {
				p := hostPolicy(permission.Rule{Permission: "bash", Pattern: "*", Action: permission.Allow})
				p.Managed = permission.Ruleset{{Permission: "bash", Pattern: "touch marker", Action: permission.Deny}}
				return p
			}(),
			mode:       "default",
			wantStatus: StatusDenied, wantDecision: DecisionDeny,
		},
		{
			name:       "config allow runs as allow",
			pol:        hostPolicy(permission.Rule{Permission: "bash", Pattern: "touch marker", Action: permission.Allow}),
			mode:       "default",
			wantStatus: StatusFinished, wantDecision: DecisionAllow, wantProcess: true,
		},
		{
			name:       "ask is answered by the arming confirmation",
			pol:        hostPolicy(),
			mode:       "default",
			wantStatus: StatusFinished, wantDecision: DecisionArmed, wantProcess: true,
		},
		{
			name:       "yolo mode upgrades the ask to allow",
			pol:        hostPolicy(),
			mode:       "yolo",
			wantStatus: StatusFinished, wantDecision: DecisionAllow, wantProcess: true,
		},
		{
			name:       "yolo mode does not lift a deny",
			pol:        hostPolicy(permission.Rule{Permission: "bash", Pattern: "touch marker", Action: permission.Deny}),
			mode:       "yolo",
			wantStatus: StatusDenied, wantDecision: DecisionDeny,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			starts := &startLog{}
			res := runner(tc.pol).Run(context.Background(), Request{
				Command: "touch marker", Dir: dir, Timeout: 30 * time.Second, PermissionMode: tc.mode,
			}, starts.add)
			if res.Status != tc.wantStatus || res.Decision != tc.wantDecision {
				t.Fatalf("status=%q decision=%q err=%q", res.Status, res.Decision, res.Error)
			}
			_, statErr := os.Stat(filepath.Join(dir, "marker"))
			ran := statErr == nil
			if ran != tc.wantProcess {
				t.Fatalf("process ran=%v, want %v", ran, tc.wantProcess)
			}
			if tc.wantProcess {
				if starts.count() != 1 || res.ExitCode == nil || *res.ExitCode != 0 || res.StartedAt.IsZero() || res.EndedAt.IsZero() {
					t.Fatalf("started=%d result %+v", starts.count(), res)
				}
				return
			}
			if starts.count() != 0 || res.ExitCode != nil {
				t.Fatalf("refused run reported a start: started=%d exit=%v", starts.count(), res.ExitCode)
			}
		})
	}
}

func TestRunHookBlock(t *testing.T) {
	dir := t.TempDir()
	pol := hostPolicy(permission.Rule{Permission: "bash", Pattern: "*", Action: permission.Allow})
	pol.HookRules = permission.HookRuleset{
		{Event: permission.HookEventPreToolUse, Matcher: "bash", Action: permission.HookActionLog},
		{Event: permission.HookEventPreToolUse, Matcher: "bash", Action: permission.HookActionBlock, Message: "no shell tonight"},
	}
	var hits []permission.HookHit
	r := runner(pol)
	r.OnHook = func(hit permission.HookHit) { hits = append(hits, hit) }
	starts := &startLog{}
	res := r.Run(context.Background(), Request{Command: "touch marker", Dir: dir, Timeout: 30 * time.Second, PermissionMode: "default"}, starts.add)
	if res.Status != StatusDenied || res.Decision != DecisionHookBlock || !strings.Contains(res.Error, "no shell tonight") {
		t.Fatalf("result %+v", res)
	}
	if starts.count() != 0 || res.ExitCode != nil {
		t.Fatal("hook-blocked run started")
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
		t.Fatal("hook-blocked command ran")
	}
	if len(hits) != 2 || hits[0].Action != permission.HookActionLog || hits[1].Action != permission.HookActionBlock || hits[1].Tool != "bash" {
		t.Fatalf("hook hits %+v", hits)
	}
}

func TestRunShellHooks(t *testing.T) {
	cases := []struct {
		name      string
		hook      string
		allowHook bool
		wantRun   bool
		wantErr   string
	}{
		{name: "allowed hook exiting 0 lets the run proceed", hook: "exit 0", allowHook: true, wantRun: true},
		{name: "allowed hook exiting non-zero blocks", hook: "echo nope-from-hook; exit 2", allowHook: true, wantErr: "nope-from-hook"},
		{name: "hook command not allowed blocks (nobody to ask)", hook: "exit 0", wantErr: "not allowed for unattended runs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			extra := []permission.Rule{{Permission: "bash", Pattern: "*", Action: permission.Allow}}
			if tc.allowHook {
				extra = append(extra, permission.Rule{Permission: "hook", Pattern: "*", Action: permission.Allow})
			}
			pol := hostPolicy(extra...)
			off := sandbox.Policy{Mode: sandbox.ModeOff}
			pol.ShellHooks = []tool.HookDef{{Event: permission.HookEventPreToolUse, Matcher: "bash", Command: tc.hook, Sandbox: &off}}
			var hits []permission.HookHit
			r := runner(pol)
			r.OnHook = func(hit permission.HookHit) { hits = append(hits, hit) }
			starts := &startLog{}
			res := r.Run(context.Background(), Request{Command: "touch marker", Dir: dir, Timeout: 30 * time.Second, PermissionMode: "default"}, starts.add)
			_, statErr := os.Stat(filepath.Join(dir, "marker"))
			if tc.wantRun {
				if res.Status != StatusFinished || statErr != nil || starts.count() != 1 {
					t.Fatalf("result %+v marker=%v", res, statErr)
				}
			} else {
				if res.Status != StatusDenied || res.Decision != DecisionHookBlock || !strings.Contains(res.Error, tc.wantErr) {
					t.Fatalf("result %+v", res)
				}
				if statErr == nil || starts.count() != 0 {
					t.Fatal("blocked command ran")
				}
			}
			if len(hits) != 1 || !strings.HasPrefix(hits[0].Action, "shell_") || hits[0].Tool != "bash" {
				t.Fatalf("hook hits %+v", hits)
			}
		})
	}
}

func TestPrepareSandboxPosture(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name          string
		dial          sandbox.Mode
		mode          string
		locked        string
		wantMode      sandbox.Mode
		wantReadOnlyW bool
	}{
		{name: "plan with the dial on denies workspace writes", dial: sandbox.ModeWorkspaceWrite, mode: "plan", wantMode: sandbox.ModeWorkspaceWrite, wantReadOnlyW: true},
		{name: "default with the dial on keeps the workspace writable", dial: sandbox.ModeWorkspaceWrite, mode: "default", wantMode: sandbox.ModeWorkspaceWrite},
		{name: "managed locked plan wins over the schedule's yolo", dial: sandbox.ModeWorkspaceWrite, mode: "yolo", locked: "plan", wantMode: sandbox.ModeWorkspaceWrite, wantReadOnlyW: true},
		{name: "dial off runs on the host even in plan", dial: sandbox.ModeOff, mode: "plan", wantMode: sandbox.ModeOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pol := hostPolicy()
			pol.SandboxMode = tc.dial
			pol.LockedMode = tc.locked
			pol.AllowDegrade = true
			pol.NetworkAllow = []string{"example.com"}
			prep, refused := runner(pol).prepare(context.Background(), Request{Command: "ls", Dir: dir, PermissionMode: tc.mode})
			if refused != nil {
				t.Fatalf("refused: %+v", refused)
			}
			if prep.policy.Mode != tc.wantMode || prep.policy.NoWorkspaceWrite != tc.wantReadOnlyW || prep.policy.WorkDir != dir {
				t.Fatalf("policy %+v", prep.policy)
			}
			if !prep.policy.AllowDegrade || len(prep.policy.NetworkAllow) != 1 {
				t.Fatalf("degrade/network not carried: %+v", prep.policy)
			}
		})
	}
}

func TestRunHostModeWrites(t *testing.T) {
	dir := t.TempDir()
	res := runner(hostPolicy()).Run(context.Background(), Request{
		Command: "echo hello > out.txt && cat out.txt", Dir: dir, Timeout: 30 * time.Second, PermissionMode: "plan",
	}, nil)
	if res.Status != StatusFinished || res.Sandbox != "off" {
		t.Fatalf("result %+v", res)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "out.txt")); err != nil || string(got) != "hello\n" {
		t.Fatalf("host write: %q %v", got, err)
	}
	if string(res.Output) != "hello\n" {
		t.Fatalf("output %q", res.Output)
	}
}

func TestRunTimeoutKillsAndFails(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	res := runner(hostPolicy()).Run(context.Background(), Request{
		Command: "echo begin; sleep 60", Dir: dir, Timeout: 300 * time.Millisecond, PermissionMode: "default",
	}, nil)
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("timeout did not kill the process (took %s)", elapsed)
	}
	if res.Status != StatusFailed || !strings.Contains(res.Error, "timed out after 300ms") {
		t.Fatalf("result %+v", res)
	}
	if res.ExitCode == nil || !strings.Contains(string(res.Output), "begin") {
		t.Fatalf("exit=%v output=%q", res.ExitCode, res.Output)
	}
}

func TestRunNonZeroExitKeepsTheTail(t *testing.T) {
	dir := t.TempDir()
	res := runner(hostPolicy()).Run(context.Background(), Request{
		Command: "for i in $(seq 1 20000); do echo line$i; done; echo 'assertion failed' >&2; exit 3",
		Dir:     dir, Timeout: 30 * time.Second, PermissionMode: "default",
	}, nil)
	if res.Status != StatusFailed || res.Error != "exit code 3" || res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("result status=%q err=%q exit=%v", res.Status, res.Error, res.ExitCode)
	}
	if len(res.Output) > maxTail || !strings.HasSuffix(string(res.Output), "assertion failed\n") {
		t.Fatalf("output len %d tail %q", len(res.Output), res.Output[max(0, len(res.Output)-40):])
	}
}

func TestRunEnvIsMinimalPlusSecretRefs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STRIKE_TEST_SCRIPT_REF", "ref-value-123")
	t.Setenv("STRIKE_TEST_DAEMON_ONLY", "daemon-secret-456")
	var uses []string
	r := runner(hostPolicy())
	r.OnSecretRefUse = func(class, hash, action string) { uses = append(uses, class+":"+hash+":"+action) }
	res := r.Run(context.Background(), Request{
		Command: "env", Dir: dir, Timeout: 30 * time.Second, PermissionMode: "default",
		SecretRefs: []string{"secret://env/STRIKE_TEST_SCRIPT_REF"},
	}, nil)
	if res.Status != StatusFinished {
		t.Fatalf("result %+v", res)
	}
	out := string(res.Output)
	if !strings.Contains(out, "STRIKE_TEST_SCRIPT_REF=ref-value-123") {
		t.Fatal("declared secret ref not injected")
	}
	if strings.Contains(out, "daemon-secret-456") || strings.Contains(out, "STRIKE_TEST_DAEMON_ONLY") {
		t.Fatal("daemon environment leaked into the script")
	}
	if len(uses) != 1 || strings.Contains(uses[0], "ref-value-123") {
		t.Fatalf("secret ref uses %v", uses)
	}

	bad := r.Run(context.Background(), Request{Command: "true", Dir: dir, Timeout: 30 * time.Second, SecretRefs: []string{"not a ref"}}, nil)
	if bad.Status != StatusError || !strings.Contains(bad.Error, "secret refs") {
		t.Fatalf("bad ref result %+v", bad)
	}
}

func TestRunAdmissionCancelledNeverStarts(t *testing.T) {
	sched, err := scheduler.New(scheduler.Config{Pools: map[string]int{scheduler.PoolProcess: 1}})
	if err != nil {
		t.Fatal(err)
	}
	defer sched.Close()
	held, err := sched.Acquire(context.Background(), scheduler.PoolProcess)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	dir := t.TempDir()
	r := runner(hostPolicy())
	r.Scheduler = sched
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	starts := &startLog{}
	res := r.Run(ctx, Request{Command: "touch marker", Dir: dir, Timeout: 30 * time.Second, PermissionMode: "default"}, starts.add)
	if res.Status != StatusError || !strings.Contains(res.Error, "admission") || res.Decision != DecisionArmed {
		t.Fatalf("result %+v", res)
	}
	if starts.count() != 0 {
		t.Fatal("cancelled waiter reported a start")
	}
	if _, err := os.Stat(filepath.Join(dir, "marker")); err == nil {
		t.Fatal("cancelled waiter started a process")
	}
}

func TestRunPolicyErrorAndUnconfigured(t *testing.T) {
	r := &Runner{Policy: func(string) (Policy, error) { return Policy{}, errors.New("bad config") }}
	if res := r.Run(context.Background(), Request{Command: "true", Dir: t.TempDir()}, nil); res.Status != StatusError || !strings.Contains(res.Error, "bad config") {
		t.Fatalf("policy error result %+v", res)
	}
	var nilRunner *Runner
	if res := nilRunner.Run(context.Background(), Request{Command: "true"}, nil); res.Status != StatusError {
		t.Fatalf("nil runner result %+v", res)
	}
}
