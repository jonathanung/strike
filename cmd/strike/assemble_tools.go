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
	"github.com/jonathanung/strike-cli/internal/history"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/host/local"
	"github.com/jonathanung/strike-cli/internal/issue"
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
	"github.com/jonathanung/strike-cli/internal/provider/openaicompat"
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
	replay       []protocol.Event
	workDir      string // tool CWD (session worktree when bound, else launch cwd)
	cfg          config.Config
	services     host.Services
	firstRun     bool
	historyClose func() error
	memoryClose  func() error
	issuesClose  func() error
	// worktreeClose removes a strike-managed worktree when cleanup=delete.
	worktreeClose func() error
	// mcpClose stops MCP server sessions (stdio/HTTP; process-scoped).
	mcpClose func() error
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
	cfg, err := config.Load(workDir)
	if err != nil {
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if opts.providerSet && opts.provider != "" {
		cfg.Provider = opts.provider
	}
	if opts.model != "" {
		cfg.Model = opts.model
	}
	// An explicit --effort must be a level we can actually send; a bad one is
	// a startup error rather than a silent fall-through to the default.
	if opts.effort != "" {
		level, ok := protocol.ParseEffort(opts.effort)
		if !ok || level == protocol.EffortDefault {
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("unknown effort %q (want off, low, medium, high, xhigh, or max)", opts.effort)
		}
		cfg.Effort = level
	}

	authStore, err := auth.OpenStore(auth.DefaultPath())
	if err != nil {
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("opening auth store: %w", err)
	}

	customStore := config.NewCustomStore(cfg.Providers)

	// selectProvider constructs a provider by name, probing credentials so
	// a bad /provider selection fails at select time with a clear message
	// instead of on the first prompt. Custom names resolve through
	// customStore (live; includes mid-session /settings adds).
	selectProvider := func(name string) (provider.Provider, string, error) {
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
		default:
			cp, ok := customStore.Get(name)
			if !ok {
				return nil, "", fmt.Errorf("unknown provider %q (want anthropic, openai, xai, echo, or a custom name from /settings)", name)
			}
			return buildCustomProvider(cp, authStore)
		}
	}

	// An explicit --provider must work or fail loudly; the config default is
	// only a silent best-effort initial selection (pick one in-app with
	// /provider otherwise). Headless exec always requires a usable provider.
	if requireProvider || (opts.providerSet && opts.provider != "") {
		if cfg.Provider == "" {
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("no provider configured (pass --provider or set provider in config)")
		}
		if _, _, err := selectProvider(cfg.Provider); err != nil {
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

	// Built-ins (build, plan, explore, general, commit, …) plus user agents
	// from ~/.strike/agents and ./.strike/agents. Empty Prompt on build/plan
	// means "compose shared baseline + provider/plan overlay at request time";
	// other personas supply a body that becomes the persona layer.
	loadedAgents, err := config.LoadAgentsWithError(workDir)
	if err != nil {
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
	instructions := config.LoadInstructions(workDir, projectIdentity.Root)
	workflows, err := config.LoadWorkflows(workDir)
	if err != nil {
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
	lookupContextWindow := func(providerName, model string) int {
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
		toolDir, wtClose, err := bindSessionWorktree(sessions, sessionID, launchDir, cfg, opts.worktree, resuming, openRootsBefore)
		if err != nil {
			if !resuming {
				_ = sessions.Destroy(sessionID)
			} else {
				_ = bound.Close()
			}
			return nil, nil, err
		}

		sid := sessionID
		eng := engine.New(engine.Options{
			SessionID:             sid,
			Select:                selectProvider,
			Registry:              registry,
			WorkDir:               toolDir,
			ProjectRoot:           projectIdentity.Root,
			Instructions:          instructions,
			Memory:                memoryStore,
			SystemPrompt:          cfg.SystemPrompt,
			LeanCode:              cfg.LeanCode,
			MaxChildDepth:         cfg.MaxChildDepth,
			InitialProvider:       initialProvider,
			InitialModel:          initialModel,
			InitialEffort:         initialEffort,
			InitialAutonomy:       initialAutonomy,
			InitialPermissionMode: initialPermMode,
			Agents:                agents,
			InitialAgent:          initialAgent,
			InitialMessages:       initialMessages,
			InitialPriority:       initialPriority,
			InitialTitled:         initialTitled,
			InitialPhaseWorkflow:  initialPhaseWF,
			InitialPhaseIndex:     initialPhaseIndex,
			InitialAlwaysGrants:   initialAlways,
			QuietStartup:          quietStartup,
			Workflows:             workflows,
			Rules:                 permissionLayers(cfg.Permissions, opts.dangerouslySkipPermissions),
			Hooks:                 hookDefs,
			HookRules:             cfg.HookRules(),
			CompactionStrategy:    cfg.CompactionStrategy,
			CompactionModel:       cfg.CompactionModel,
			LookupContextWindow:   lookupContextWindow,
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
				info, err := sessions.Create(session.CreateOptions{
					ID:              childID,
					ParentSessionID: parentID,
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
			id:      sid,
			eng:     eng,
			bound:   bound,
			workDir: toolDir,
			wtClose: wtClose,
		}, replay, nil
	}

	resumeID := strings.TrimSpace(opts.sessionID)
	if opts.continueSession {
		info, err := sessions.LatestRoot(projectIdentity.Key)
		if err != nil {
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("continue: %w", err)
		}
		resumeID = info.ID
	}
	first, replay, err := openRoot(resumeID, true)
	if err != nil {
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

	agentNames := make([]string, len(agents))
	for i, a := range agents {
		agentNames[i] = a.Name
	}
	// local.New wraps the real backend stores in the host.Services contract;
	// the TUI never sees auth/config/models/history/memory/issues directly.
	services := local.New(authStore, historyStore, memoryStore, issueStore, agentNames, skills, customStore, workDir)
	services.Files = local.NewFiles(workDir)
	services.Sessions = local.NewSessions(sessions, projectIdentity.Key)
	services.Init = local.NewProjectInit(workDir)
	services.MCP = local.NewMCP(mcpMgr)

	spawn := rootSpawner(func(id string) (*rootSlot, error) {
		slot, _, err := openRoot(id, false)
		return slot, err
	})

	return &assembled{
		eng:       first.eng,
		sessions:  sessions,
		store:     first.bound,
		sessionID: first.id,
		replay:    replay,
		workDir:   workDir,
		cfg:       cfg,
		services:  services,
		firstRun:  isFreshStrikeHome(authStore),
		historyClose: func() error {
			return historyStore.Close()
		},
		memoryClose: func() error {
			return memoryStore.Close()
		},
		issuesClose: func() error {
			return issueStore.Close()
		},
		worktreeClose: first.wtClose,
		mcpClose: func() error {
			return mcpMgr.Close()
		},
		spawnRoot: spawn,
		firstSlot: first,
	}, nil
}

// buildCustomProvider maps a config custom provider onto the openaicompat or
// anthropic adapter with the declared base URL and auth-store credentials.
func buildCustomProvider(cp config.CustomProvider, store *auth.Store) (provider.Provider, string, error) {
	defaultModel := config.DefaultModelCustom(cp)
	switch cp.API {
	case config.WireOpenAI:
		// Empty key is allowed (local gateways like ollama); the adapter sends
		// Authorization only when a token is present.
		source := optionalBearer(cp.Name, store, cp.APIKeyEnv)
		return openaicompat.NewWithHeaders(cp.Name, cp.BaseURL, source, cp.Headers), defaultModel, nil
	case config.WireAnthropic:
		key, _ := auth.APIKeyEnv(cp.Name, store, cp.APIKeyEnv)
		p, err := anthropic.NewCustom(cp.Name, cp.BaseURL, key, cp.Headers)
		if err != nil {
			return nil, "", err
		}
		return p, defaultModel, nil
	default:
		return nil, "", fmt.Errorf("custom provider %s: unknown api %q", cp.Name, cp.API)
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
