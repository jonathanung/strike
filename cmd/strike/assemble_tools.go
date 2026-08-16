package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/admission"
	"github.com/jonathanung/strike-cli/internal/artifact"
	"github.com/jonathanung/strike-cli/internal/attachment"
	"github.com/jonathanung/strike-cli/internal/audit"
	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/fn"
	"github.com/jonathanung/strike-cli/internal/fn/external"
	"github.com/jonathanung/strike-cli/internal/goal"
	"github.com/jonathanung/strike-cli/internal/history"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/host/local"
	"github.com/jonathanung/strike-cli/internal/issue"
	"github.com/jonathanung/strike-cli/internal/ledger"
	"github.com/jonathanung/strike-cli/internal/lsp"
	"github.com/jonathanung/strike-cli/internal/mcp"
	"github.com/jonathanung/strike-cli/internal/memory"
	"github.com/jonathanung/strike-cli/internal/models"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/plan"
	"github.com/jonathanung/strike-cli/internal/plugin"
	"github.com/jonathanung/strike-cli/internal/project"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/scheduler"
	"github.com/jonathanung/strike-cli/internal/session"
	"github.com/jonathanung/strike-cli/internal/tool"
	"github.com/jonathanung/strike-cli/providers"
	"github.com/jonathanung/strike-cli/providers/factory"
)

// assembled is the composition-root product shared by the TUI and headless
// exec frontends: engine, session manager binding, and host services.
type assembled struct {
	eng       *engine.Engine
	sessions  *session.Manager
	store     session.Bound
	sessionID string
	// replay is prior transcript events for --continue (UI only; not re-appended).
	replay  []protocol.Event
	workDir string // tool CWD (session worktree when bound, else launch cwd)
	cfg     config.Config
	// sandboxMode is the resolved OS sandbox dial (canonical token).
	sandboxMode string
	// sandboxExplain is the multi-line /sandbox explain text (config layers).
	sandboxExplain string
	services       host.Services
	historyClose   func() error
	memoryClose    func() error
	issuesClose    func() error
	goalsClose     func() error
	plansClose     func() error
	artifactsClose func() error
	ledgerClose    func() error
	// worktreeClose removes a strike-managed worktree when cleanup=delete.
	worktreeClose func() error
	// worktreeNotice is a user-visible soft-fail message (e.g. non-git cwd).
	worktreeNotice string
	// mcpClose stops MCP server sessions (stdio/HTTP; process-scoped).
	mcpClose func() error
	// lspClose stops language server sessions (stdio; process-scoped).
	lspClose func() error
	// harnessClose stops persistent external harness workers.
	harnessClose func() error
	// schedulerClose shuts down the shared in-process admission controller.
	schedulerClose func()
	// spawnRoot creates additional concurrent root engines (interactive multi-root).
	// resumeID empty = new session; non-empty opens that durable root.
	spawnRoot rootSpawner
	// firstSlot is the initial live root for multiRootHub (same as eng/store).
	firstSlot *rootSlot
	// frameStore holds the last painted TUI frame for tui_snapshot (#1183).
	frameStore *tool.TUIFrameStore
}

// assemble resolves project/config/auth, builds the engine and session store,
// and wraps host services. requireProvider forces credential validation even
// when --provider was not set (headless exec cannot pick a provider later).
// The caller must invoke historyClose and, if it never hands store to
// runSession, store.Close.
func assemble(opts cliOptions, requireProvider bool) (*assembled, error) {
	launchDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	// workDir is the launch cwd until a session worktree is bound (tools +
	// host.Files). Config/agents/skills always load from the launch tree.
	workDir := launchDir
	projectIdentity, err := project.Resolve(context.Background(), launchDir)
	if err != nil {
		return nil, fmt.Errorf("resolving project identity: %w", err)
	}
	globalRoot := config.GlobalRoot()
	if globalRoot == "" {
		return nil, fmt.Errorf("opening prompt history: global config root is unavailable")
	}
	historyStore, err := history.Open(globalRoot, projectIdentity.Key)
	if err != nil {
		return nil, fmt.Errorf("opening prompt history: %w", err)
	}
	memoryStore, err := memory.Open(globalRoot, projectIdentity.Key)
	if err != nil {
		_ = historyStore.Close()
		return nil, fmt.Errorf("opening project memory: %w", err)
	}
	issueStore, err := issue.Open(globalRoot, projectIdentity.Key)
	if err != nil {
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("opening project issues: %w", err)
	}
	goalStore, err := goal.Open(globalRoot, projectIdentity.Key)
	if err != nil {
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("opening project goals: %w", err)
	}
	planStore, err := plan.Open(globalRoot, projectIdentity.Key)
	if err != nil {
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("opening project plans: %w", err)
	}
	artifactStore, err := artifact.Open(globalRoot, projectIdentity.Key)
	if err != nil {
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("opening project artifacts: %w", err)
	}
	ledgerStore, err := ledger.Open(globalRoot, projectIdentity.Key)
	if err != nil {
		_ = artifactStore.Close()
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("opening project ledger: %w", err)
	}
	cfg, err := config.Load(workDir)
	if err != nil {
		_ = ledgerStore.Close()
		_ = artifactStore.Close()
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("loading config: %w", err)
	}
	// Trusted plugin MCP / harness / hook activation (#728). Untrusted
	// executable contributions never start; user config names win collisions.
	{
		userMCP := make(map[string]struct{}, len(cfg.MCP.Servers))
		for name := range cfg.MCP.Servers {
			userMCP[name] = struct{}{}
		}
		userHarness := make(map[string]struct{}, len(cfg.Harnesses))
		for name := range cfg.Harnesses {
			userHarness[name] = struct{}{}
		}
		execSet := plugin.CompileExecutables(plugin.Options{WorkDir: workDir}, userMCP, userHarness)
		var pdiags []plugin.Diagnostic
		cfg, pdiags = config.ApplyPluginExecutables(workDir, cfg, execSet, true)
		for _, d := range pdiags {
			if d.Code == "shadowed" {
				continue
			}
			fmt.Fprintf(os.Stderr, "plugin: %s\n", d.String())
		}
	}
	sandboxMode, err := resolveSandboxMode(cfg.Sandbox, opts.sandbox, cfg.Managed.Sandbox)
	if err != nil {
		_ = ledgerStore.Close()
		_ = artifactStore.Close()
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, err
	}
	if opts.providerSet && opts.provider != "" {
		cfg.Provider = config.CanonicalProviderID(opts.provider)
	}
	cfg.Provider = config.CanonicalProviderID(cfg.Provider)
	if opts.model != "" {
		cfg.Model = opts.model
	}
	// An explicit --effort must be a level we can actually send; a bad one is
	// a startup error rather than a silent fall-through to the default.
	if opts.effort != "" {
		level, ok := protocol.ParseEffort(opts.effort)
		if !ok || level == protocol.EffortDefault {
			_ = ledgerStore.Close()
			_ = artifactStore.Close()
			_ = planStore.Close()
			_ = goalStore.Close()
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("unknown effort %q (want off, low, medium, high, xhigh, or max)", opts.effort)
		}
		cfg.Effort = level
	}

	authStore, err := auth.OpenStore(auth.DefaultPath())
	if err != nil {
		_ = ledgerStore.Close()
		_ = artifactStore.Close()
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("opening auth store: %w", err)
	}

	customStore := config.NewCustomStoreWithOverlays(cfg.Providers, cfg.ModelOverlays, cfg.EndpointOverlays, workDir)
	customStore.SetDisableDefault(cfg.DisableDefaultProviders, cfg.DisableDefaultPer)

	// selectProvider is a thin call into the providers factory. Custom
	// names and builtin endpoint overlays are resolved from already-parsed
	// config (live; includes mid-session /settings adds).
	selectProvider := func(name string) (provider.Provider, string, error) {
		return providers.Select(name, factoryOptions(authStore, customStore))
	}

	// An explicit --provider must work or fail loudly; the config default is
	// only a silent best-effort initial selection (pick one in-app with
	// /provider otherwise). Headless exec always requires a usable provider.
	if requireProvider || (opts.providerSet && opts.provider != "") {
		if cfg.Provider == "" {
			_ = ledgerStore.Close()
			_ = artifactStore.Close()
			_ = planStore.Close()
			_ = goalStore.Close()
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("no provider configured (pass --provider or set provider in config)")
		}
		if _, _, err := selectProvider(cfg.Provider); err != nil {
			_ = ledgerStore.Close()
			_ = artifactStore.Close()
			_ = planStore.Close()
			_ = goalStore.Close()
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, err
		}
	}

	// Admission policy (MCP/skills/plugins). Distinct from permissionPreset /
	// OS sandbox. Fail closed on unknown preset at config load; resolve here
	// for runtime scanners.
	admitPol, err := config.ResolveAdmission(cfg)
	if err != nil {
		_ = ledgerStore.Close()
		_ = artifactStore.Close()
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("admission policy: %w", err)
	}
	// Collect admission verdicts to append onto the first session after open.
	var admissionVerdicts []admission.Verdict

	// Skills load before the tool registry so the skill tool can advertise
	// available names in its description at construction time.
	skills, err := config.LoadSkillsWithError(workDir)
	if err != nil {
		_ = ledgerStore.Close()
		_ = artifactStore.Close()
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("loading skills: %w", err)
	}
	var skillVerdicts []admission.Verdict
	skills, skillVerdicts = config.FilterSkills(admitPol, skills, workDir)
	admissionVerdicts = append(admissionVerdicts, skillVerdicts...)
	// Plugin path/capability admission (trust remains a separate gate).
	pluginVerdicts := config.AdmitPlugins(admitPol, config.DiscoverPlugins(workDir), workDir)
	admissionVerdicts = append(admissionVerdicts, pluginVerdicts...)
	skillInfos := make([]tool.SkillInfo, len(skills))
	for i, s := range skills {
		skillInfos[i] = tool.SkillInfo{Name: s.Name, Description: s.Description, Template: s.Template, Path: s.Path}
	}

	todoStore := tool.NewTodoStore()
	registry := tool.NewRegistry(
		tool.NewRead(),
		tool.NewGlob(),
		tool.NewGrep(),
		tool.NewEdit(),
		tool.NewWrite(),
		tool.NewApplyPatch(),
		tool.NewMove(),
		tool.NewDelete(),
		tool.NewStatus(),
		tool.NewBash(),
		tool.NewGit(),
		tool.NewVerify(),
		tool.NewTask(),
		tool.NewTaskStatus(),
		tool.NewTaskRead(),
		tool.NewTaskMessage(),
		tool.NewTaskInterrupt(),
		tool.NewDelegate(),
		tool.NewWait(),
		tool.NewAgentRoster(),
		tool.NewAgentOwnership(),
		tool.NewAgentMessage(),
		tool.NewAgentBroadcast(),
		tool.NewAgentThread(),
		tool.NewTeamTask(),
		tool.NewPatchCollab(),
		tool.NewWebFetch(),
		tool.NewWebSearch(),
		tool.NewBrowser(),
		tool.NewTodoWrite(todoStore),
		tool.NewTodoRead(todoStore),
		tool.NewMemoryWrite(memoryStore),
		tool.NewMemoryRead(memoryStore),
		tool.NewIssueWrite(issueStore),
		tool.NewIssueRead(issueStore),
		tool.NewPlanWrite(planStore),
		tool.NewPlanRead(planStore),
		tool.NewPlanDelegate(planStore),
		tool.NewArtifactWrite(artifactStore),
		tool.NewArtifactRead(artifactStore),
		tool.NewLedgerWrite(ledgerStore),
		tool.NewLedgerRead(ledgerStore),
		tool.NewContextBundle(),
		tool.NewTUISnapshot(),
		tool.NewNotebookEdit(),
		tool.NewSleep(),
		tool.NewSkill(skillInfos),
		tool.NewQuestion(),
		tool.NewEnterPlanMode(),
		tool.NewExitPlanMode(),
		tool.NewPhaseDone(),
	)
	registry.Register(tool.NewToolSearch(registry))
	frameStore := &tool.TUIFrameStore{}
	if config.DeferToolsEnabled(cfg.DeferTools) {
		registry.SetDeferLoading(true)
	}

	// Built-ins (build, plan, explore, general, commit, …) plus user agents
	// from ~/.strike/agents and ./.strike/agents. Empty Prompt on build/plan
	// means "compose shared baseline + provider/plan overlay at request time";
	// other personas supply a body that becomes the persona layer.
	loadedAgents, err := config.LoadAgentsWithError(workDir)
	if err != nil {
		_ = ledgerStore.Close()
		_ = artifactStore.Close()
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("loading agents: %w", err)
	}
	agents := make([]engine.Agent, 0, len(loadedAgents))
	for _, a := range loadedAgents {
		agents = append(agents, engine.Agent(a))
	}
	if len(agents) == 0 {
		agents = []engine.Agent{
			{Name: "build", Description: "Default coding agent. Full tools subject to permission rules."},
		}
	}
	harnessRegistry := fn.NewRegistry()
	// Config only creates external subprocess harnesses. A custom Strike binary
	// may register embedded Go functions here before validating agent references.
	var harnessClosers []func() error
	for name, hc := range cfg.Harnesses {
		adapter, err := external.Command(external.Config{Command: hc.Command, Args: hc.Args, Env: hc.Env})
		if err != nil {
			return nil, fmt.Errorf("configuring harness %q: %w", name, err)
		}
		var h fn.Func
		if config.IsPersistentHarness(hc) {
			opts := external.WorkerOptions{
				MaxConcurrent: hc.MaxConcurrent,
				MaxRestarts:   hc.MaxRestarts,
			}
			if hc.IdleTimeoutMs != 0 {
				opts.IdleTimeout = time.Duration(hc.IdleTimeoutMs) * time.Millisecond
			}
			var closeFn func() error
			h, closeFn, err = external.NewPersistent(name, adapter, opts)
			if err != nil {
				return nil, fmt.Errorf("configuring harness %q: %w", name, err)
			}
			if closeFn != nil {
				harnessClosers = append(harnessClosers, closeFn)
			}
		} else {
			h, err = external.New(name, adapter)
			if err != nil {
				return nil, fmt.Errorf("configuring harness %q: %w", name, err)
			}
		}
		harnessRegistry.Register(name, h)
	}
	for _, agent := range agents {
		if agent.Harness != "" && agent.Harness != "default" && !harnessRegistry.Known(agent.Harness) {
			return nil, fmt.Errorf("agent %q references unknown harness %q", agent.Name, agent.Harness)
		}
	}
	instructions := config.LoadInstructions(workDir, projectIdentity.Root)
	workflows, err := config.LoadWorkflows(workDir)
	if err != nil {
		_ = ledgerStore.Close()
		_ = artifactStore.Close()
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("loading workflows: %w", err)
	}

	// Concurrent session manager owns durable JSONL logs. --continue /
	// --session reopen a root session and restore model history; otherwise Create.
	sessions := session.NewManager(session.DefaultDir())
	sessionDir := sessions.Dir()
	shellHooks := cfg.ShellHooks()
	hookDefs := make([]tool.HookDef, 0, len(shellHooks))
	for _, h := range shellHooks {
		hookDefs = append(hookDefs, tool.HookDef{
			Event:      h.Event,
			Command:    h.Command,
			TimeoutMs:  h.TimeoutMs,
			Matcher:    h.Matcher,
			FailClosed: h.FailClosed,
		})
	}
	// Shared catalog (models.dev + providers.jsonc) for task model pins and
	// context-window lookups — same source as host.Catalog / TUI /model.
	modelCatalog := local.NewCatalog(customStore)
	listModels := func(ctx context.Context, providerName string) ([]string, error) {
		return modelCatalog.ModelIDs(ctx, providerName)
	}
	lookupContextWindow := func(providerName, model string) int {
		// Config limit overlays win over models.dev when set.
		if defs := customStore.ModelOverlay(providerName); len(defs) > 0 {
			if n, ok := config.ModelDefsContext(defs, model); ok {
				return n
			}
		}
		// Best-effort catalog lookup for threshold compaction. Failures
		// leave the window unknown; overflow recovery still works.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cat, err := models.Load(ctx)
		if err != nil {
			return 0
		}
		n, ok := cat.ContextWindow(providerName, model)
		if !ok {
			return 0
		}
		return n
	}
	// Session cost envelope pricing (#577): models.dev rates (USD / million tokens).
	// Returns 0 when pricing is unknown — engine never invents a dollar figure.
	// Rates are cached per provider/model for the process lifetime.
	type costRate struct {
		in, out float64
		ok      bool
	}
	var costRateMu sync.Mutex
	costRateCache := map[string]costRate{}
	estimateUsageCost := func(providerName, model string, u provider.Usage) float64 {
		if providerName == "" || model == "" {
			return 0
		}
		key := providerName + "/" + model
		costRateMu.Lock()
		if cached, hit := costRateCache[key]; hit {
			costRateMu.Unlock()
			if !cached.ok {
				return 0
			}
			return usageCostUSD(cached.in, cached.out, u)
		}
		costRateMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Prefer host catalog (includes providers.jsonc overlays).
		var rate costRate
		infos, err := modelCatalog.Models(ctx, providerName)
		if err == nil {
			for _, info := range infos {
				if info.ID == model && info.HasCost {
					rate = costRate{in: info.InputCost, out: info.OutputCost, ok: true}
					break
				}
			}
		}
		if !rate.ok {
			if cat, err := models.Load(ctx); err == nil {
				for _, info := range cat.Infos(providerName) {
					if info.ID == model && info.HasCost {
						rate = costRate{in: info.InputCost, out: info.OutputCost, ok: true}
						break
					}
				}
			}
		}
		costRateMu.Lock()
		costRateCache[key] = rate // may be negative cache
		costRateMu.Unlock()
		if !rate.ok {
			return 0
		}
		return usageCostUSD(rate.in, rate.out, u)
	}
	maxSessionCost := cfg.Session.MaxSessionCostUSD
	if opts.maxCostSet {
		maxSessionCost = opts.maxCost
	}

	// Process-local scheduler shared by every root and child engine so model
	// and bash (process/build/test) pools cap aggregate concurrency inside
	// this OS process. Omitted limits stay unlimited (no wait).
	schedEff, err := cfg.SchedulerEffective()
	if err != nil {
		_ = ledgerStore.Close()
		_ = artifactStore.Close()
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("scheduler policy: %w", err)
	}
	sched, err := scheduler.New(schedEff.SchedulerConfig())
	if err != nil {
		_ = ledgerStore.Close()
		_ = artifactStore.Close()
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("scheduler: %w", err)
	}

	// Language servers (stdio): process-scoped, shared by all roots via FileSync.
	// StartAll runs after the first root opens; NotifyFile no-ops until then and
	// when a server is dead (crash isolation).
	lspMgr := lsp.NewManager(launchDir)
	// Optional LSP tools (definition/references/symbols/diagnostics). Not core —
	// omitted from provider Tools when deferTools is on until toolsearch/direct call.
	registry.Register(tool.NewDefinition(lspMgr))
	registry.Register(tool.NewReferences(lspMgr))
	registry.Register(tool.NewSymbols(lspMgr))
	registry.Register(tool.NewDiagnostics(lspMgr))
	registry.Register(tool.NewCallHierarchy(lspMgr))
	registry.Register(tool.NewRenamePreview(lspMgr))
	registry.Register(tool.NewImpact(lspMgr))

	// openRoot builds one live root engine. resumeID empty creates a fresh
	// session; non-empty opens that durable root (subagents rejected).
	// applyCLI pins: only the process's first root applies --provider/--model/--effort.
	openRoot := func(resumeID string, applyCLI bool) (*rootSlot, []protocol.Event, error) {
		resumeID = strings.TrimSpace(resumeID)
		var (
			sessionID          string
			bound              session.Bound
			replay             []protocol.Event
			initialProvider    = cfg.Provider
			initialModel       = cfg.Model
			initialEffort      = cfg.Effort
			initialAgent       = cfg.DefaultAgent
			initialMessages    []provider.Message
			initialPriority    bool
			initialTitled      bool
			initialAutonomy    protocol.Autonomy
			initialPermMode    = cfg.PermissionMode
			initialPhaseWF     string
			initialPhaseIndex  int
			initialPhaseName   string
			initialPhaseFP     string
			initialPhaseGrant  engine.PhaseGrantApproval
			initialAlways      permission.Ruleset
			initialPlanHandoff engine.PlanHandoffState
			quietStartup       bool
			resuming           bool
			openRootsBefore    int
		)
		if resumeID != "" {
			resuming = true
			opened, err := openResumeSession(sessions, resumeID)
			if err != nil {
				return nil, nil, err
			}
			bound = opened.bound
			sessionID = opened.id
			replay = opened.replay
			restored := engine.Restore(replay)
			initialMessages = restored.Messages
			initialPriority = restored.Priority
			initialTitled = restored.Titled
			initialAutonomy = restored.Autonomy
			// Managed permissionMode wins over session resume (enterprise lock).
			if !cfg.Managed.PermissionMode {
				initialPermMode = restored.PermissionMode
			}
			initialPhaseWF = restored.PhaseWorkflow
			initialPhaseIndex = restored.PhaseIndex
			initialPhaseName = restored.PhaseName
			initialPhaseFP = restored.PhaseFingerprint
			initialPhaseGrant = restored.PhaseGrant
			initialAlways = restored.AlwaysGrants
			initialPlanHandoff = restored.PlanHandoff
			if !(applyCLI && opts.providerSet) && restored.Provider != "" {
				initialProvider = restored.Provider
			}
			if !(applyCLI && opts.model != "") && restored.Model != "" {
				initialModel = restored.Model
			}
			if !(applyCLI && opts.effort != "") && restored.Effort != protocol.EffortDefault {
				initialEffort = restored.Effort
			}
			if restored.Agent != "" {
				initialAgent = restored.Agent
			}
			// CLI provider/model/effort overrides on resume must still be logged
			// so the next Restore sees the switch; keep startup noisy then.
			quietStartup = !(applyCLI && (opts.providerSet || opts.model != "" || opts.effort != ""))
		} else {
			openRootsBefore = sessions.CountOpenRoots()
			info, err := sessions.Create(session.CreateOptions{ProjectKey: projectIdentity.Key})
			if err != nil {
				return nil, nil, fmt.Errorf("creating session: %w", err)
			}
			bound, err = sessions.Bind(info.ID)
			if err != nil {
				_ = sessions.Close(info.ID)
				return nil, nil, fmt.Errorf("binding session: %w", err)
			}
			sessionID = info.ID
		}

		// Bind tool CWD to a per-session git worktree when configured / forced.
		// Project-scoped state stays on projectIdentity.Key (launch repo).
		// Non-git cwd soft-fails (launchDir + notice) so the app still starts.
		toolDir, wtClose, wtNotice, err := bindSessionWorktree(sessions, sessionID, launchDir, cfg, opts.worktree, resuming, openRootsBefore)
		if err != nil {
			if !resuming {
				_ = sessions.Destroy(sessionID)
			} else {
				_ = bound.Close()
			}
			return nil, nil, err
		}

		// Refuse yolo + sandbox off without --i-know (startup gate).
		if err := sandbox.CheckYoloSandbox(string(initialPermMode.Normalize()), sandboxMode, opts.iKnow); err != nil {
			if !resuming {
				_ = sessions.Destroy(sessionID)
			} else {
				_ = bound.Close()
			}
			if wtClose != nil {
				_ = wtClose()
			}
			return nil, nil, err
		}

		sid := sessionID
		// Shared audit sink for protocol Observe + secret_ref_use inject (#1032).
		var auditSink *audit.Sink
		if s, err := openAuditSink(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "strike: audit sink unavailable: %v\n", err)
		} else {
			auditSink = s
		}
		attStore, attErr := attachment.Open(globalRoot)
		if attErr != nil {
			if !resuming {
				_ = sessions.Destroy(sessionID)
			} else {
				_ = bound.Close()
			}
			if wtClose != nil {
				_ = wtClose()
			}
			return nil, nil, fmt.Errorf("opening attachments: %w", attErr)
		}
		eng := engine.New(engine.Options{
			SessionID:        sid,
			Select:           selectProvider,
			Registry:         registry,
			WorkDir:          toolDir,
			Audit:            auditSink,
			CheckpointDir:    tool.DefaultCheckpointDir(sid),
			ProjectRoot:      projectIdentity.Root,
			Instructions:     instructions,
			Memory:           memoryStore,
			Ledger:           ledgerStore,
			Attachments:      attStore,
			SystemPrompt:     cfg.SystemPrompt,
			SystemPromptMode: cfg.SystemPromptMode,
			LeanCode:         cfg.LeanCode,
			HarnessRegistry:  harnessRegistry,
			Scheduler:        sched,
			SchedulerPolicy:  schedEff,
			FileSync: func(absPath, content string, deleted bool) {
				// Background context: document sync must not be canceled with the tool call.
				lspMgr.NotifyFile(context.Background(), absPath, content, deleted)
			},
			CollectDiagnostics:   makeLSPCollectDiagnostics(lspMgr, toolDir, cfg.LSP),
			TUISnapshot:          frameStore.Capture,
			MaxChildDepth:        cfg.MaxChildDepth,
			TurnTimeout:          resolveRootTurnTimeout(cfg, opts),
			ChildIsolation:       cfg.Session.ChildIsolation,
			MaxToolRetryAttempts: cfg.ToolRetry.MaxAttempts,
			ToolLoopThreshold:    cfg.ToolRetry.LoopThreshold,
			ToolRetryBackoff:     toolRetryBackoffFromConfig(cfg.ToolRetry),
			OverlapPolicy:        cfg.Session.OverlapPolicy,
			DefaultChildBudget: tool.AgentBudgetLimits{
				MaxWallClockS:     cfg.Session.AgentBudget.MaxWallClockS,
				MaxTokens:         cfg.Session.AgentBudget.MaxTokens,
				MaxCostUSD:        cfg.Session.AgentBudget.MaxCostUSD,
				MaxToolCalls:      cfg.Session.AgentBudget.MaxToolCalls,
				MaxDangerousTools: cfg.Session.AgentBudget.MaxDangerousTools,
				StallAfterS:       cfg.Session.AgentBudget.StallAfterS,
				LoopDetectN:       cfg.Session.AgentBudget.LoopDetectN,
			},
			MaxSessionCostUSD: maxSessionCost,
			MaxTurnTokens:     cfg.Session.MaxTurnTokens,
			EstimateUsageCost: estimateUsageCost,
			DelegationPolicy: func() engine.DelegationPolicyConfig {
				// Product default is enforce when config omits the block.
				p := engine.DelegationPolicyConfig{
					Mode:            cfg.Session.DelegationPolicy.Mode,
					TinyPromptRunes: cfg.Session.DelegationPolicy.TinyPromptRunes,
					MaxPathsLocal:   cfg.Session.DelegationPolicy.MaxPathsLocal,
					MaxLiveChildren: cfg.Session.DelegationPolicy.MaxLiveChildren,
				}
				if strings.TrimSpace(p.Mode) == "" {
					p.Mode = engine.PolicyEnforce
				}
				return p
			}(),
			InitialProvider:       initialProvider,
			InitialModel:          initialModel,
			InitialEffort:         initialEffort,
			InitialAutonomy:       initialAutonomy,
			InitialPermissionMode: initialPermMode,
			SandboxMode:           sandboxMode,
			SandboxAllowDegrade:   cfg.SandboxAllowDegrade,
			NetworkAllow:          sessionNetworkAllow(cfg.Network.Allow, opts.skipNetworkAllow()),
			BashSecrets:           cloneBashSecrets(cfg.BashSecrets),
			ContentGuard: tool.ContentGuardSettings{
				Mode:       cfg.ContentGuard.Mode,
				PathAllow:  append([]string(nil), cfg.ContentGuard.PathAllow...),
				ForcedDeny: cfg.Managed.ContentGuardForcedDeny,
			},
			WebSearch: tool.WebSearchSettings{
				Provider:  cfg.WebSearch.Provider,
				APIKeyEnv: cfg.WebSearch.APIKeyEnv,
				BaseURL:   cfg.WebSearch.BaseURL,
			},
			AllowYoloWithoutSandbox:    opts.iKnow,
			Agents:                     agents,
			InitialAgent:               initialAgent,
			InitialMessages:            initialMessages,
			InitialPriority:            initialPriority,
			InitialTitled:              initialTitled,
			InitialPhaseWorkflow:       initialPhaseWF,
			InitialPhaseIndex:          initialPhaseIndex,
			InitialPhaseName:           initialPhaseName,
			InitialPhaseFingerprint:    initialPhaseFP,
			InitialPhaseGrantApproval:  initialPhaseGrant,
			InitialAlwaysGrants:        initialAlways,
			InitialPlanHandoff:         initialPlanHandoff,
			PlanStore:                  planStore,
			QuietStartup:               quietStartup,
			DangerouslySkipPermissions: opts.skipPermissionAsks(),
			Workflows:                  workflows,
			Rules:                      permissionLayersWithPreset(cfg.Permissions, cfg.PermissionPreset, opts.skipPermissionAsks()),
			RuleLayerNames:             permissionLayerNames(cfg.PermissionPreset, opts.skipPermissionAsks()),
			ManagedRules:               append(permission.Ruleset(nil), cfg.Managed.DenyRules...),
			LockPermissionMode:         cfg.Managed.PermissionMode,
			Hooks:                      hookDefs,
			HookRules:                  cfg.HookRules(),
			CompactionStrategy:         cfg.CompactionStrategy,
			CompactionModel:            cfg.CompactionModel,
			CompactionThreshold:        cfg.CompactionThreshold,
			CompactionBuffer:           cfg.CompactionBuffer,
			KeepUserTurns:              cfg.KeepUserTurns,
			PruneProtectTokens:         cfg.PruneProtectTokens,
			PruneMinimumTokens:         cfg.PruneMinimumTokens,
			PruneKeepUserTurns:         cfg.PruneKeepUserTurns,
			PruneProtectTools:          cfg.PruneProtectTools,
			LookupContextWindow:        lookupContextWindow,
			ListModels:                 listModels,
			PersistProjectRule: func(rule permission.Rule) error {
				return config.AppendProjectPermission(launchDir, rule)
			},
			PersistSessionMeta: func(m protocol.SessionMeta) error {
				_, err := session.UpdateMeta(sessionDir, sid, func(meta *session.Meta) {
					if m.PRURL != "" {
						meta.PRURL = m.PRURL
					}
					if m.PRNumber != 0 {
						meta.PRNumber = m.PRNumber
					}
					if st := session.NormalizePRState(m.PRState); st != "" {
						meta.PRState = st
					} else if meta.PRState == "" && meta.PRURL != "" {
						meta.PRState = session.PRStateOpen
					}
					meta.PRUpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
				})
				return err
			},
			OpenChildSession: func(parentID, childID, title string) (string, error) {
				parentMeta, _ := session.ReadMeta(sessionDir, parentID)
				info, err := sessions.Create(session.CreateOptions{
					ID:              childID,
					ParentSessionID: parentID,
					LeadSessionID:   session.ResolveChildLeadID(parentID, parentMeta),
					Title:           title,
					ProjectKey:      projectIdentity.Key,
				})
				if err != nil {
					return "", err
				}
				return info.ID, nil
			},
			AppendChildEvent: func(childID string, ev protocol.Event) error {
				return sessions.Append(childID, ev)
			},
			CloseChildSession: func(childID string) error {
				return sessions.Close(childID)
			},
			LoadChildSession: func(childID string) (engine.ChildSessionSnapshot, error) {
				childID = strings.TrimSpace(childID)
				if childID == "" {
					return engine.ChildSessionSnapshot{}, fmt.Errorf("child session id is empty")
				}
				meta, err := session.ReadMeta(sessionDir, childID)
				if err != nil {
					return engine.ChildSessionSnapshot{}, err
				}
				// Missing meta with no log → not found.
				evs, err := sessions.Replay(childID)
				if err != nil {
					return engine.ChildSessionSnapshot{}, err
				}
				if len(evs) == 0 && strings.TrimSpace(meta.ParentSessionID) == "" {
					// Distinguish empty/unknown: require parent lineage for resume.
					if _, statErr := os.Stat(sessions.Path(childID)); statErr != nil {
						return engine.ChildSessionSnapshot{}, fmt.Errorf("child session %q not found", childID)
					}
				}
				return engine.ChildSessionSnapshot{
					SessionID:       childID,
					ParentSessionID: meta.ParentSessionID,
					LeadSessionID:   meta.LeadSessionID,
					Title:           meta.Title,
					Events:          evs,
				}, nil
			},
			ReopenChildSession: func(childID string) error {
				_, err := sessions.Open(childID)
				return err
			},
		})
		return &rootSlot{
			id:       sid,
			eng:      eng,
			bound:    bound,
			workDir:  toolDir,
			wtClose:  wtClose,
			wtNotice: wtNotice,
		}, replay, nil
	}

	resumeID := strings.TrimSpace(opts.sessionID)
	if opts.continueSession {
		info, err := sessions.LatestRoot(projectIdentity.Key)
		if err != nil {
			sched.Close()
			_ = ledgerStore.Close()
			_ = artifactStore.Close()
			_ = planStore.Close()
			_ = goalStore.Close()
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("continue: %w", err)
		}
		resumeID = info.ID
	}
	first, replay, err := openRoot(resumeID, true)
	if err != nil {
		sched.Close()
		_ = ledgerStore.Close()
		_ = artifactStore.Close()
		_ = planStore.Close()
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, err
	}
	workDir = first.workDir

	// External MCP servers (stdio/HTTP): process-scoped, shared by all root
	// engines via the common registry. Stdio CWD is the launch tree (not a
	// session worktree). Per-server failures are recorded and do not abort assemble.
	// Admission runs after tools/list and before registry bind.
	mcpMgr := mcp.NewManager()
	mcpMgr.SetAdmissionPolicy(admitPol)
	mcpMgr.SetAdmissionHook(func(v admission.Verdict) {
		admissionVerdicts = append(admissionVerdicts, v)
		if v.Action != admission.ActionAllow || len(v.Findings) > 0 {
			fmt.Fprintf(os.Stderr, "%s\n", admission.FormatVerdict(v))
		}
	})
	if len(cfg.MCP.Servers) > 0 {
		fields := make(map[string]mcp.ServerConfigFields, len(cfg.MCP.Servers))
		for name, s := range cfg.MCP.Servers {
			fields[name] = mcp.ServerConfigFields{
				Type:    s.Type,
				Command: s.Command,
				Args:    s.Args,
				Env:     s.Env,
				Cwd:     s.Cwd,
				URL:     s.URL,
				Headers: s.Headers,
				OAuth:   configMCPOAuth(s.OAuth),
			}
		}
		mcpCtx, mcpCancel := context.WithTimeout(context.Background(), 45*time.Second)
		mcpMgr.StartAll(mcpCtx, mcp.ConfigsFromMap(fields, launchDir), registry)
		mcpCancel()
	}
	// Persist admission audit events on the first root session (timeline).
	if first != nil && first.id != "" && len(admissionVerdicts) > 0 {
		sid := first.id
		for _, v := range admissionVerdicts {
			if v.Action == admission.ActionAllow && len(v.Findings) == 0 && v.ScanError == "" {
				continue
			}
			var findings []string
			for _, f := range v.Findings {
				if f.Rule != "" {
					findings = append(findings, f.Rule)
				}
			}
			_ = first.bound.Append(protocol.AdmissionDecided{
				Correlation: protocol.Correlation{SessionID: sid},
				Surface:     v.Surface,
				Target:      v.Target,
				Action:      string(v.Action),
				Reason:      v.Reason,
				Preset:      admitPol.Preset,
				Findings:    findings,
			})
		}
	}

	// External language servers (stdio JSON-RPC). RootDir/CWD is the launch
	// tree. Per-server failures are recorded and do not abort assemble.
	if len(cfg.LSP.Servers) > 0 {
		fields := make(map[string]lsp.ServerConfigFields, len(cfg.LSP.Servers))
		for name, s := range cfg.LSP.Servers {
			fields[name] = lsp.ServerConfigFields{
				Command:    s.Command,
				Args:       s.Args,
				Env:        s.Env,
				Extensions: s.Extensions,
			}
		}
		lspCtx, lspCancel := context.WithTimeout(context.Background(), 45*time.Second)
		lspMgr.StartAll(lspCtx, lsp.ConfigsFromMap(fields, launchDir))
		lspCancel()
	}

	agentNames := make([]string, len(agents))
	for i, a := range agents {
		agentNames[i] = a.Name
	}
	// local.New wraps the real backend stores in the host.Services contract;
	// the TUI never sees auth/config/models/history/memory/issues directly.
	services := local.New(authStore, historyStore, memoryStore, issueStore, agentNames, skills, customStore, workDir)
	services.Files = local.NewFiles(workDir)
	// Compile base sandbox profile from defaults + config (+ optional dangerous
	// allow-all). Engine recompiles per bash call with live agent/phase layers.
	basePermLayers := permissionLayersWithPreset(cfg.Permissions, cfg.PermissionPreset, opts.skipPermissionAsks())
	sandboxPolicy := permission.CompileSandbox(
		sandbox.ResolveMode(sandboxMode),
		workDir,
		basePermLayers...,
	)
	sandboxPolicy.NetworkAllow = sessionNetworkAllow(cfg.Network.Allow, opts.skipNetworkAllow())
	sandboxPolicy.AllowDegrade = cfg.SandboxAllowDegrade
	sandboxExplain := sandbox.Explain(sandboxPolicy)
	services.Shell = local.NewShell(workDir, sandboxPolicy)
	// Permission explain/presets for /permission. Live explain binds to the
	// first root engine; base layers cover startup before Run.
	permHost := local.NewPermissions(
		basePermLayers,
		permissionLayerNames(cfg.PermissionPreset, opts.skipPermissionAsks()),
	)
	if first != nil && first.eng != nil {
		eng := first.eng
		permHost.SetLive(func(perm, pat string) permission.DetailedExplanation {
			return eng.ExplainPermission(perm, pat)
		})
	}
	services.Permissions = permHost
	services.Goals = local.NewGoals(goalStore, workDir)
	services.Plans = local.NewPlans(planStore)
	services.Artifacts = local.NewArtifacts(artifactStore)
	services.Ledger = local.NewLedger(ledgerStore)
	services.Sessions = local.NewSessions(sessions, projectIdentity.Key)
	services.Init = local.NewProjectInit(workDir)
	services.MCP = local.NewMCP(mcpMgr)
	services.LSP = local.NewLSP(lspMgr)
	services.Plugins = local.NewPlugins(workDir)
	services.Panes = local.NewPanes(workDir)
	services.Telemetry = local.NewTelemetry()
	services.Workflows = local.NewWorkflowsWithOpts(workflows, nil, local.WorkflowsOpts{
		WorkDir: workDir,
		Agents:  agentNames,
	})
	services.WorkflowDrafts = local.NewWorkflowDrafts(workDir)
	services.Themes = themesAdapter{}

	spawn := rootSpawner(func(id string) (*rootSlot, error) {
		slot, _, err := openRoot(id, false)
		return slot, err
	})

	return &assembled{
		eng:            first.eng,
		sessions:       sessions,
		store:          first.bound,
		sessionID:      first.id,
		replay:         replay,
		workDir:        workDir,
		cfg:            cfg,
		sandboxMode:    sandboxMode,
		sandboxExplain: sandboxExplain,
		services:       services,
		historyClose: func() error {
			return historyStore.Close()
		},
		memoryClose: func() error {
			return memoryStore.Close()
		},
		issuesClose: func() error {
			return issueStore.Close()
		},
		goalsClose: func() error {
			return goalStore.Close()
		},
		plansClose: func() error {
			return planStore.Close()
		},
		artifactsClose: func() error {
			return artifactStore.Close()
		},
		ledgerClose: func() error {
			return ledgerStore.Close()
		},
		worktreeClose:  first.wtClose,
		worktreeNotice: first.wtNotice,
		mcpClose: func() error {
			return mcpMgr.Close()
		},
		lspClose: func() error {
			return lspMgr.Close()
		},
		harnessClose: func() error {
			var first error
			for i := len(harnessClosers) - 1; i >= 0; i-- {
				if err := harnessClosers[i](); err != nil && first == nil {
					first = err
				}
			}
			return first
		},
		schedulerClose: sched.Close,
		spawnRoot:      spawn,
		firstSlot:      first,
		frameStore:     frameStore,
	}, nil
}

// makeLSPCollectDiagnostics wires Manager.CollectForPaths into tool.Context
// using config severity / max-chars / wait knobs. Nil manager → nil callback.
func makeLSPCollectDiagnostics(mgr *lsp.Manager, workDir string, cfg config.LSPConfig) func(context.Context, []string) string {
	if mgr == nil {
		return nil
	}
	opts := lsp.InjectOptions{WorkDir: workDir}
	if sev, err := lsp.ParseSeverityName(cfg.DiagnosticsSeverity); err == nil {
		opts.MinSeverity = sev
	}
	if cfg.DiagnosticsMaxChars > 0 {
		opts.MaxChars = cfg.DiagnosticsMaxChars
	}
	if cfg.DiagnosticsWaitMs > 0 {
		opts.Wait = time.Duration(cfg.DiagnosticsWaitMs) * time.Millisecond
	} else if cfg.DiagnosticsWaitMs < 0 {
		opts.Wait = -1 // snapshot immediately
	}
	return func(ctx context.Context, absPaths []string) string {
		return mgr.CollectForPaths(ctx, absPaths, opts)
	}
}

func factoryOptions(store *auth.Store, customStore *config.CustomStore) factory.Options {
	return factory.Options{
		Store:        store,
		Disabled:     customStore.IsBuiltinDisabled,
		DefaultModel: config.DefaultModel,
		LookupEndpoint: func(name string) (factory.Endpoint, bool) {
			ep, ok := customStore.Endpoint(name)
			if !ok {
				return factory.Endpoint{}, false
			}
			ep = config.ResolveEndpoint(ep)
			return factory.Endpoint{BaseURL: ep.BaseURL, APIKeyEnv: ep.APIKeyEnv, Headers: ep.Headers}, true
		},
		LookupCustom: func(name string) (factory.Custom, bool) {
			cp, ok := customStore.Get(name)
			if !ok {
				return factory.Custom{}, false
			}
			return toFactoryCustom(cp), true
		},
	}
}

func toFactoryCustom(cp config.CustomProvider) factory.Custom {
	cp = config.ResolveCustom(cp)
	return factory.Custom{
		Name:         cp.Name,
		BaseURL:      cp.BaseURL,
		API:          string(cp.API),
		Headers:      cp.Headers,
		APIKeyEnv:    cp.APIKeyEnv,
		DefaultModel: config.DefaultModelCustom(cp),
	}
}

// buildCustomProvider adapts a config custom provider for existing cmd tests.
func buildCustomProvider(cp config.CustomProvider, store *auth.Store) (provider.Provider, string, error) {
	return factory.BuildCustom(toFactoryCustom(cp), store)
}

// buildBuiltinWithEndpoint adapts a config endpoint overlay for existing cmd tests.
func buildBuiltinWithEndpoint(name string, ep config.ProviderEndpoint, store *auth.Store) (provider.Provider, string, error, bool) {
	ep = config.ResolveEndpoint(ep)
	return factory.BuiltinEndpoint(name, factory.Endpoint{
		BaseURL: ep.BaseURL, APIKeyEnv: ep.APIKeyEnv, Headers: ep.Headers,
	}, factory.Options{Store: store, DefaultModel: config.DefaultModel})
}

func optionalBearer(name string, store *auth.Store, envName string) func(context.Context) (string, error) {
	return factory.OptionalBearer(name, store, envName)
}

// toolRetryBackoffFromConfig builds the engine backoff func from config delays.
// nil when both delays are zero (engine default jittered backoff).
func toolRetryBackoffFromConfig(tr config.ToolRetryConfig) func(int) time.Duration {
	if tr.BaseDelayMs == 0 && tr.MaxDelayMs == 0 {
		return nil
	}
	base := time.Duration(tr.BaseDelayMs) * time.Millisecond
	max := time.Duration(tr.MaxDelayMs) * time.Millisecond
	return func(nextAttempt int) time.Duration {
		return tool.ToolRetryDelay(nextAttempt, base, max)
	}
}

func configMCPOAuth(o *config.MCPOAuth) *mcp.OAuthConfig {
	if o == nil {
		return nil
	}
	return &mcp.OAuthConfig{
		ClientID:     o.ClientID,
		ClientSecret: o.ClientSecret,
		Scopes:       o.Scopes,
		AuthorizeURL: o.AuthorizeURL,
		TokenURL:     o.TokenURL,
		RevokeURL:    o.RevokeURL,
		DiscoveryURL: o.DiscoveryURL,
		TokenFile:    o.TokenFile,
		RedirectURL:  o.RedirectURL,
	}
}

func cloneBashSecrets(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// resolveRootTurnTimeout picks CLI --turn-timeout over session.turnTimeoutS,
// then applies the product default (30m) when unset. Negative config/CLI
// disables the deadline. Fresh per turn — resume does not carry an expired
// wall clock from a prior process.
func resolveRootTurnTimeout(cfg config.Config, opts cliOptions) time.Duration {
	secs := cfg.Session.TurnTimeoutS
	if opts.turnTimeoutSet {
		parsed, err := parseTurnTimeoutFlag(opts.turnTimeout)
		if err == nil {
			secs = parsed
		}
	}
	return config.ResolveTurnTimeout(secs)
}

// usageCostUSD estimates USD from catalog rates (per million tokens) and usage.
func usageCostUSD(inputPerM, outputPerM float64, u provider.Usage) float64 {
	in := float64(u.InputTokens) / 1e6 * inputPerM
	out := float64(u.OutputTokens) / 1e6 * outputPerM
	// Cache tokens: treat cache read/write as input-priced when no separate rate.
	cache := float64(u.CacheReadTokens+u.CacheCreationTokens) / 1e6 * inputPerM
	return in + out + cache
}
