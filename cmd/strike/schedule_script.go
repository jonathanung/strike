package main

import (
	"context"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/schedule/scriptrun"
	"github.com/jonathanung/strike/internal/trust/audit"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

// newScriptRunner builds the script-schedule fire path for strike serve.
// The permission layers are rebuilt per run from the config an unattended
// root under the run's dir would load; admission is the process-wide
// scheduler every root shares; the sandbox dial is the daemon's.
func newScriptRunner(a *assembled, sink *audit.Sink, tokenFile, cwd string) *scriptrun.Runner {
	if a == nil {
		return nil
	}
	r := &scriptrun.Runner{
		Policy:          scriptPolicyLoader(a.sandboxMode, tokenFile, cwd),
		Scheduler:       a.procScheduler,
		SchedulerPolicy: a.procSchedPolicy,
	}
	if sink != nil {
		r.OnHook = func(hit permission.HookHit) {
			_ = sink.Observe(protocol.HookMatched{
				Event:   hit.Event,
				Action:  hit.Action,
				Matcher: hit.Matcher,
				Tool:    hit.Tool,
				Message: hit.Message,
			})
		}
		r.OnSecretRefUse = func(refClass, refHash, action string) {
			_ = sink.RecordSecretRefUse("", "", "", refClass, refHash, action, "script_schedule")
		}
	}
	return r
}

// scriptPolicyLoader resolves the policy for one run's dir: managed and
// global config always, the project config under dir only when that
// workspace is trusted (#1352), exactly as assemble scopes a root.
func scriptPolicyLoader(sandboxMode, tokenFile, cwd string) func(dir string) (scriptrun.Policy, error) {
	return func(dir string) (scriptrun.Policy, error) {
		root := dir
		if id, err := project.Resolve(context.Background(), dir); err == nil && id.Root != "" {
			root = id.Root
		}
		var trust *workspacetrust.Store
		if g := config.GlobalRoot(); g != "" {
			if st, err := workspacetrust.Open(workspacetrust.DefaultPath(g)); err == nil {
				trust = st
			}
		}
		cfg, err := config.Load(resolveWorkspaceTrust(trust, dir, root).scope(dir))
		if err != nil {
			return scriptrun.Policy{}, err
		}
		return scriptPolicyFromConfig(cfg, sandboxMode, tokenFile, cwd), nil
	}
}

// scriptPolicyFromConfig is the unattended-root rule set for cfg. The
// process --auto / --dangerously-skip-permissions allow-all layer is never
// included (processBypassEnabled(skip, unattended=true) is false), and the
// serve token file is denied the same way it is for every session.
func scriptPolicyFromConfig(cfg config.Config, sandboxMode, tokenFile, cwd string) scriptrun.Policy {
	bypass := processBypassEnabled(true, true)
	pol := scriptrun.Policy{
		Rules:        permissionLayersWithPreset(cfg.Permissions, cfg.PermissionPreset, bypass),
		LayerNames:   permissionLayerNames(cfg.PermissionPreset, bypass),
		Managed:      sessionManagedRules(cfg.Managed.DenyRules, tokenFileDenyRules(tokenFile, cwd)),
		HookRules:    cfg.HookRules(),
		ShellHooks:   scriptShellHooks(cfg),
		SandboxMode:  sandbox.ResolveMode(sandboxMode),
		AllowDegrade: cfg.SandboxAllowDegrade,
		NetworkAllow: sandbox.CloneNetworkAllow(cfg.Network.Allow),
	}
	if cfg.Managed.PermissionMode && cfg.PermissionMode != "" {
		pol.LockedMode = string(cfg.PermissionMode.Normalize())
	}
	return pol
}

// scriptShellHooks converts the config's shell-command hooks the way
// assemble does for an engine.
func scriptShellHooks(cfg config.Config) []tool.HookDef {
	hooks := cfg.ShellHooks()
	if len(hooks) == 0 {
		return nil
	}
	out := make([]tool.HookDef, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, tool.HookDef{
			Event:      h.Event,
			Command:    h.Command,
			TimeoutMs:  h.TimeoutMs,
			Matcher:    h.Matcher,
			FailClosed: h.FailClosed,
		})
	}
	return out
}
