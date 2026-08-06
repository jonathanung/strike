package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/goal"
	"github.com/jonathanung/strike-cli/internal/harness"
	"github.com/jonathanung/strike-cli/internal/harness/external"
	"github.com/jonathanung/strike-cli/internal/history"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/host/local"
	"github.com/jonathanung/strike-cli/internal/issue"
	"github.com/jonathanung/strike-cli/internal/lsp"
	"github.com/jonathanung/strike-cli/internal/mcp"
	"github.com/jonathanung/strike-cli/internal/memory"
	"github.com/jonathanung/strike-cli/internal/models"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/project"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/anthropic"
	"github.com/jonathanung/strike-cli/internal/provider/chatgpt"
	"github.com/jonathanung/strike-cli/internal/provider/echo"
	"github.com/jonathanung/strike-cli/internal/provider/google"
	"github.com/jonathanung/strike-cli/internal/provider/openaicompat"
	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/scheduler"
	"github.com/jonathanung/strike-cli/internal/session"
	"github.com/jonathanung/strike-cli/internal/tool"
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
	// worktreeClose removes a strike-managed worktree when cleanup=delete.
	worktreeClose func() error
	// worktreeNotice is a user-visible soft-fail message (e.g. non-git cwd).
	worktreeNotice string
	// mcpClose stops MCP server sessions (stdio/HTTP; process-scoped).
	mcpClose func() error
	// lspClose stops language server sessions (stdio; process-scoped).
	lspClose func() error
	// schedulerClose shuts down the shared in-process admission controller.
	schedulerClose func()
	// spawnRoot creates additional concurrent root engines (interactive multi-root).
	// resumeID empty = new session; non-empty opens that durable root.
	spawnRoot rootSpawner
	// firstSlot is the initial live root for multiRootHub (same as eng/store).
	firstSlot *rootSlot
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
	cfg, err := config.Load(workDir)
	if err != nil {
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("loading config: %w", err)
	}
	sandboxMode, err := resolveSandboxMode(cfg.Sandbox, opts.sandbox)
	if err != nil {
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, err
	}
	// Loud once when OS sandbox backend is missing/blocked (bash degrades).
	// Skip when the dial is off — operator chose no isolation.
	if sandbox.ResolveMode(sandboxMode) != sandbox.ModeOff {
		sandbox.WarnUnavailable()
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
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("opening auth store: %w", err)
	}

	customStore := config.NewCustomStoreWithOverlays(cfg.Providers, cfg.ModelOverlays, cfg.EndpointOverlays, workDir)
	customStore.SetDisableDefault(cfg.DisableDefaultProviders, cfg.DisableDefaultPer)

	// selectProvider constructs a provider by name, probing credentials so
	// a bad /provider selection fails at select time with a clear message
	// instead of on the first prompt. Custom names resolve through
	// customStore (live; includes mid-session /settings adds). Builtin
	// endpoint overlays from providers.jsonc (baseURL/apiKey) apply here.
	selectProvider := func(name string) (provider.Provider, string, error) {
		name = config.CanonicalProviderID(name)
		if customStore.IsBuiltinDisabled(name) {
			return nil, "", fmt.Errorf("provider %q is disabled (set disable-default-%s to false, or disable-default-providers to false)", name, name)
		}
		if name != "echo" {
			if ep, ok := customStore.Endpoint(name); ok {
				if p, model, err, handled := buildBuiltinWithEndpoint(name, ep, authStore); handled {
					return p, model, err
				}
			}
		}
		switch name {
		case "echo":
			return echo.New(), "echo", nil
		case "anthropic":
			key, _ := auth.APIKey("anthropic", authStore)
			p, err := anthropic.New(key)
			if err != nil {
				return nil, "", err
			}
			return p, config.DefaultModel(name), nil
		case "openai":
			// Routing decides who gets billed: an explicit API key (env or
			// pasted) targets the platform API; a ChatGPT OAuth login
			// targets the subscription-billed ChatGPT backend.
			if os.Getenv("OPENAI_API_KEY") != "" {
				return openaicompat.NewOpenAI(auth.BearerSource(name, authStore)), config.DefaultModel(name), nil
			}
			cred, ok := authStore.Get("openai")
			switch {
			case ok && cred.Type == auth.TypeOAuth:
				source := auth.ChatGPTSource(authStore)
				if _, _, err := source(context.Background()); err != nil {
					return nil, "", err
				}
				return chatgpt.New(source), config.DefaultModel(name), nil
			case ok && cred.APIKey != "":
				return openaicompat.NewOpenAI(auth.BearerSource(name, authStore)), config.DefaultModel(name), nil
			default:
				return nil, "", fmt.Errorf("no OpenAI credentials: set OPENAI_API_KEY or run `strike auth login openai` (or /auth openai)")
			}
		case "xai":
			source := auth.BearerSource(name, authStore)
			if _, err := source(context.Background()); err != nil {
				return nil, "", err
			}
			return openaicompat.NewXAI(source), config.DefaultModel(name), nil
		case "google":
			source := auth.BearerSource(name, authStore)
			if _, err := source(context.Background()); err != nil {
				return nil, "", err
			}
			return google.New(source), config.DefaultModel(name), nil
		case "kimi":
			source := auth.BearerSource(name, authStore)
			if _, err := source(context.Background()); err != nil {
				return nil, "", err
			}
			return openaicompat.New("kimi", "https://api.moonshot.cn/v1", source), config.DefaultModel(name), nil
		case "deepseek":
			source := auth.BearerSource(name, authStore)
			if _, err := source(context.Background()); err != nil {
				return nil, "", err
			}
			return openaicompat.NewTextOnly("deepseek", "https://api.deepseek.com/v1", source), config.DefaultModel(name), nil
		default:
			cp, ok := customStore.Get(name)
			if !ok {
				return nil, "", fmt.Errorf("unknown provider %q (want anthropic, openai, xai, google, kimi, deepseek, echo, or a custom name from /settings; gemini is accepted as an alias of google)", name)
			}
			return buildCustomProvider(cp, authStore)
		}
	}

	// An explicit --provider must work or fail loudly; the config default is
	// only a silent best-effort initial selection (pick one in-app with
	// /provider otherwise). Headless exec always requires a usable provider.
	if requireProvider || (opts.providerSet && opts.provider != "") {
		if cfg.Provider == "" {
			_ = goalStore.Close()
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("no provider configured (pass --provider or set provider in config)")
		}
		if _, _, err := selectProvider(cfg.Provider); err != nil {
			_ = goalStore.Close()
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, err
		}
	}

	// Skills load before the tool registry so the skill tool can advertise
	// available names in its description at construction time.
	skills, err := config.LoadSkillsWithError(workDir)
	if err != nil {
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("loading skills: %w", err)
	}
	skillInfos := make([]tool.SkillInfo, len(skills))
	for i, s := range skills {
		skillInfos[i] = tool.SkillInfo{Name: s.Name, Description: s.Description, Template: s.Template}
	}

	todoStore := tool.NewTodoStore()
	registry := tool.NewRegistry(
		tool.NewRead(),
		tool.NewGlob(),
		tool.NewGrep(),
		tool.NewEdit(),
		tool.NewWrite(),
		tool.NewApplyPatch(),
		tool.NewBash(),
		tool.NewTask(),
		tool.NewTaskStatus(),
		tool.NewTaskRead(),
		tool.NewTaskMessage(),
		tool.NewTaskInterrupt(),
		tool.NewAgentRoster(),
		tool.NewAgentMessage(),
		tool.NewAgentBroadcast(),
		tool.NewTeamTask(),
		tool.NewWebFetch(),
		tool.NewTodoWrite(todoStore),
		tool.NewTodoRead(todoStore),
		tool.NewMemoryWrite(memoryStore),
		tool.NewMemoryRead(memoryStore),
		tool.NewIssueWrite(issueStore),
		tool.NewIssueRead(issueStore),
		tool.NewNotebookEdit(),
		tool.NewSleep(),
		tool.NewSkill(skillInfos),
		tool.NewQuestion(),
		tool.NewEnterPlanMode(),
		tool.NewExitPlanMode(),
		tool.NewPhaseDone(),
	)
	registry.Register(tool.NewToolSearch(registry))
	if config.DeferToolsEnabled(cfg.DeferTools) {
		registry.SetDeferLoading(true)
	}

	// Built-ins (build, plan, explore, general, commit, …) plus user agents
	// from ~/.strike/agents and ./.strike/agents. Empty Prompt on build/plan
	// means "compose shared baseline + provider/plan overlay at request time";
	// other personas supply a body that becomes the persona layer.
	loadedAgents, err := config.LoadAgentsWithError(workDir)
	if err != nil {
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
	harnessRegistry := harness.NewRegistry()
	// Config only creates external subprocess harnesses. A custom Strike binary
	// may register embedded Go functions here before validating agent references.
	for name, hc := range cfg.Harnesses {
		adapter, err := external.Command(external.Config{Command: hc.Command, Args: hc.Args, Env: hc.Env})
		if err != nil {
			return nil, fmt.Errorf("configuring harness %q: %w", name, err)
		}
		h, err := external.New(name, adapter)
		if err != nil {
			return nil, fmt.Errorf("configuring harness %q: %w", name, err)
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
			Event:     h.Event,
			Command:   h.Command,
			TimeoutMs: h.TimeoutMs,
			Matcher:   h.Matcher,
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

	// Process-local scheduler shared by every root and child engine so model
	// and bash (process/build/test) pools cap aggregate concurrency inside
	// this OS process. Omitted limits stay unlimited (no wait).
	schedEff, err := cfg.SchedulerEffective()
	if err != nil {
		_ = goalStore.Close()
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("scheduler policy: %w", err)
	}
	sched, err := scheduler.New(schedEff.SchedulerConfig())
	if err != nil {
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

	// openRoot builds one live root engine. resumeID empty creates a fresh
	// session; non-empty opens that durable root (subagents rejected).
	// applyCLI pins: only the process's first root applies --provider/--model/--effort.
	openRoot := func(resumeID string, applyCLI bool) (*rootSlot, []protocol.Event, error) {
		resumeID = strings.TrimSpace(resumeID)
		var (
			sessionID         string
			bound             session.Bound
			replay            []protocol.Event
			initialProvider   = cfg.Provider
			initialModel      = cfg.Model
			initialEffort     = cfg.Effort
			initialAgent      = cfg.DefaultAgent
			initialMessages   []provider.Message
			initialPriority   bool
			initialTitled     bool
			initialAutonomy   protocol.Autonomy
			initialPermMode   = cfg.PermissionMode
			initialPhaseWF    string
			initialPhaseIndex int
			initialAlways     permission.Ruleset
			quietStartup      bool
			resuming          bool
			openRootsBefore   int
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
			initialPermMode = restored.PermissionMode
			initialPhaseWF = restored.PhaseWorkflow
			initialPhaseIndex = restored.PhaseIndex
			initialAlways = restored.AlwaysGrants
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
		eng := engine.New(engine.Options{
			SessionID:       sid,
			Select:          selectProvider,
			Registry:        registry,
			WorkDir:         toolDir,
			ProjectRoot:     projectIdentity.Root,
			Instructions:    instructions,
			Memory:          memoryStore,
			SystemPrompt:    cfg.SystemPrompt,
			LeanCode:        cfg.LeanCode,
			HarnessRegistry: harnessRegistry,
			Scheduler:       sched,
			SchedulerPolicy: schedEff,
			FileSync: func(absPath, content string, deleted bool) {
				// Background context: document sync must not be canceled with the tool call.
				lspMgr.NotifyFile(context.Background(), absPath, content, deleted)
			},
			MaxChildDepth:           cfg.MaxChildDepth,
			InitialProvider:         initialProvider,
			InitialModel:            initialModel,
			InitialEffort:           initialEffort,
			InitialAutonomy:         initialAutonomy,
			InitialPermissionMode:   initialPermMode,
			SandboxMode:             sandboxMode,
			NetworkAllow:            sandbox.CloneNetworkAllow(cfg.Network.Allow),
			AllowYoloWithoutSandbox: opts.iKnow,
			Agents:                  agents,
			InitialAgent:            initialAgent,
			InitialMessages:         initialMessages,
			InitialPriority:         initialPriority,
			InitialTitled:           initialTitled,
			InitialPhaseWorkflow:    initialPhaseWF,
			InitialPhaseIndex:       initialPhaseIndex,
			InitialAlwaysGrants:     initialAlways,
			QuietStartup:            quietStartup,
			Workflows:               workflows,
			Rules:                   permissionLayers(cfg.Permissions, opts.dangerouslySkipPermissions),
			Hooks:                   hookDefs,
			HookRules:               cfg.HookRules(),
			CompactionStrategy:      cfg.CompactionStrategy,
			CompactionModel:         cfg.CompactionModel,
			CompactionThreshold:     cfg.CompactionThreshold,
			CompactionBuffer:        cfg.CompactionBuffer,
			KeepUserTurns:           cfg.KeepUserTurns,
			PruneProtectTokens:      cfg.PruneProtectTokens,
			PruneMinimumTokens:      cfg.PruneMinimumTokens,
			PruneKeepUserTurns:      cfg.PruneKeepUserTurns,
			PruneProtectTools:       cfg.PruneProtectTools,
			LookupContextWindow:     lookupContextWindow,
			ListModels:              listModels,
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
	mcpMgr := mcp.NewManager()
	if len(cfg.MCP.Servers) > 0 {
		fields := make(map[string]mcp.ServerConfigFields, len(cfg.MCP.Servers))
		for name, s := range cfg.MCP.Servers {
			fields[name] = mcp.ServerConfigFields{
				Type:    s.Type,
				Command: s.Command,
				Args:    s.Args,
				Env:     s.Env,
				URL:     s.URL,
				Headers: s.Headers,
			}
		}
		mcpCtx, mcpCancel := context.WithTimeout(context.Background(), 45*time.Second)
		mcpMgr.StartAll(mcpCtx, mcp.ConfigsFromMap(fields, launchDir), registry)
		mcpCancel()
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
	sandboxPolicy := permission.CompileSandbox(
		sandbox.ResolveMode(sandboxMode),
		workDir,
		permissionLayers(cfg.Permissions, opts.dangerouslySkipPermissions)...,
	)
	sandboxPolicy.NetworkAllow = sandbox.CloneNetworkAllow(cfg.Network.Allow)
	sandboxExplain := sandbox.Explain(sandboxPolicy)
	services.Shell = local.NewShell(workDir, sandboxPolicy)
	services.Goals = local.NewGoals(goalStore, workDir)
	services.Sessions = local.NewSessions(sessions, projectIdentity.Key)
	services.Init = local.NewProjectInit(workDir)
	services.MCP = local.NewMCP(mcpMgr)
	services.Telemetry = local.NewTelemetry()

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
		worktreeClose:  first.wtClose,
		worktreeNotice: first.wtNotice,
		mcpClose: func() error {
			return mcpMgr.Close()
		},
		lspClose: func() error {
			return lspMgr.Close()
		},
		schedulerClose: sched.Close,
		spawnRoot:      spawn,
		firstSlot:      first,
	}, nil
}

// buildCustomProvider maps a config custom provider onto the openaicompat or
// anthropic adapter with the declared base URL and auth-store credentials.
// Env placeholders in baseURL/headers/apiKeyEnv are expanded from the process env.
// When apiKeyEnv is set, a missing key fails clearly at select time.
func buildCustomProvider(cp config.CustomProvider, store *auth.Store) (provider.Provider, string, error) {
	cp = config.ResolveCustom(cp)
	if err := config.ValidateBaseURL(cp.BaseURL); err != nil {
		return nil, "", fmt.Errorf("custom provider %s: %w", cp.Name, err)
	}
	defaultModel := config.DefaultModelCustom(cp)
	switch cp.API {
	case config.WireOpenAI:
		if cp.APIKeyEnv != "" {
			if _, ok := auth.APIKeyEnv(cp.Name, store, cp.APIKeyEnv); !ok {
				return nil, "", fmt.Errorf("custom provider %s: set %s (or paste a key via /auth %s)", cp.Name, cp.APIKeyEnv, cp.Name)
			}
		}
		// Empty key is allowed when no apiKeyEnv (local gateways like ollama).
		source := optionalBearer(cp.Name, store, cp.APIKeyEnv)
		return openaicompat.NewWithHeaders(cp.Name, cp.BaseURL, source, cp.Headers), defaultModel, nil
	case config.WireResponses:
		// OpenCode @ai-sdk/openai default: platform Responses API (/v1/responses).
		if cp.APIKeyEnv != "" {
			if _, ok := auth.APIKeyEnv(cp.Name, store, cp.APIKeyEnv); !ok {
				return nil, "", fmt.Errorf("custom provider %s: set %s (or paste a key via /auth %s)", cp.Name, cp.APIKeyEnv, cp.Name)
			}
		}
		source := optionalBearer(cp.Name, store, cp.APIKeyEnv)
		return openaicompat.NewResponses(cp.Name, cp.BaseURL, source, cp.Headers), defaultModel, nil
	case config.WireAnthropic:
		key, ok := auth.APIKeyEnv(cp.Name, store, cp.APIKeyEnv)
		if cp.APIKeyEnv != "" && !ok {
			return nil, "", fmt.Errorf("custom provider %s: set %s (or paste a key via /auth %s)", cp.Name, cp.APIKeyEnv, cp.Name)
		}
		p, err := anthropic.NewCustom(cp.Name, cp.BaseURL, key, cp.Headers)
		if err != nil {
			return nil, "", err
		}
		return p, defaultModel, nil
	default:
		return nil, "", fmt.Errorf("custom provider %s: unknown api %q", cp.Name, cp.API)
	}
}

// builtinDefaultBaseURL is the stock origin for chat-completions builtins.
func builtinDefaultBaseURL(name string) string {
	switch name {
	case "anthropic":
		return "https://api.anthropic.com"
	case "openai":
		return "https://api.openai.com/v1"
	case "xai":
		return "https://api.x.ai/v1"
	case "kimi":
		return "https://api.moonshot.cn/v1"
	case "deepseek":
		return "https://api.deepseek.com/v1"
	default:
		return ""
	}
}

func builtinKeyHint(name, envName string) string {
	if envName != "" {
		return envName
	}
	switch name {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "xai":
		return "XAI_API_KEY"
	case "google":
		return "GEMINI_API_KEY"
	case "kimi":
		return "KIMI_API_KEY"
	case "deepseek":
		return "DEEPSEEK_API_KEY"
	default:
		return "API key"
	}
}

// buildBuiltinWithEndpoint applies a providers.jsonc endpoint overlay onto a
// built-in provider. handled is false when the overlay has nothing actionable
// for this builtin (caller falls through to default wiring).
func buildBuiltinWithEndpoint(name string, ep config.ProviderEndpoint, store *auth.Store) (provider.Provider, string, error, bool) {
	ep = config.ResolveEndpoint(ep)
	if !ep.Active() {
		return nil, "", nil, false
	}
	defaultModel := config.DefaultModel(name)
	envName := ep.APIKeyEnv
	hint := builtinKeyHint(name, envName)

	switch name {
	case "anthropic":
		baseURL := ep.BaseURL
		if baseURL == "" {
			baseURL = builtinDefaultBaseURL(name)
		} else if err := config.ValidateBaseURL(baseURL); err != nil {
			return nil, "", fmt.Errorf("anthropic endpoint: %w", err), true
		}
		var (
			key string
			ok  bool
		)
		if envName != "" {
			key, ok = auth.APIKeyEnv(name, store, envName)
		} else {
			key, ok = auth.APIKey(name, store)
		}
		if !ok || key == "" {
			return nil, "", fmt.Errorf("no Anthropic credentials: set %s or run `strike auth login anthropic`", hint), true
		}
		p, err := anthropic.NewCustom("anthropic", baseURL, key, ep.Headers)
		if err != nil {
			return nil, "", err, true
		}
		return p, defaultModel, nil, true

	case "openai", "xai", "kimi", "deepseek":
		baseURL := ep.BaseURL
		if baseURL == "" {
			baseURL = builtinDefaultBaseURL(name)
		} else if err := config.ValidateBaseURL(baseURL); err != nil {
			return nil, "", fmt.Errorf("%s endpoint: %w", name, err), true
		}
		// Overlay forces chat-completions (not ChatGPT OAuth backend).
		var source openaicompat.BearerSource
		if envName != "" {
			if _, ok := auth.APIKeyEnv(name, store, envName); !ok {
				return nil, "", fmt.Errorf("no %s credentials: set %s or paste a key via /auth %s", name, envName, name), true
			}
			source = optionalBearer(name, store, envName)
		} else {
			source = auth.BearerSource(name, store)
			if _, err := source(context.Background()); err != nil {
				return nil, "", fmt.Errorf("no %s credentials: set %s or run `strike auth login %s`", name, hint, name), true
			}
		}
		return openaicompat.NewWithHeaders(name, baseURL, source, ep.Headers), defaultModel, nil, true

	case "google":
		if ep.BaseURL != "" {
			return nil, "", fmt.Errorf("google endpoint baseURL overlay is not supported yet"), true
		}
		if envName == "" {
			return nil, "", nil, false
		}
		if _, ok := auth.APIKeyEnv(name, store, envName); !ok {
			return nil, "", fmt.Errorf("no google credentials: set %s", envName), true
		}
		return google.New(optionalBearer(name, store, envName)), defaultModel, nil, true

	default:
		return nil, "", nil, false
	}
}

func optionalBearer(name string, store *auth.Store, envName string) openaicompat.BearerSource {
	return func(ctx context.Context) (string, error) {
		if key, ok := auth.APIKeyEnv(name, store, envName); ok {
			return key, nil
		}
		return "", nil
	}
}
