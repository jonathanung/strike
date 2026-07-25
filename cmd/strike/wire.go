package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/history"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/host/local"
	"github.com/jonathanung/strike-cli/internal/issue"
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
	"github.com/jonathanung/strike-cli/internal/tui"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

// sessionStore is the narrow persistence surface runSession needs from a
// session event log.
type sessionStore interface {
	Append(protocol.Event) error
	Close() error
}

// runSession owns the engine/frontend/session lifecycle: it starts the engine
// and an event tee, runs the frontend, then on frontend return signals
// frontend-done and cancels the engine. The tee appends every engine event
// before optional frontend delivery; after frontend-done it stops forwarding
// but keeps draining and persisting until engine events close. Store.Close
// runs only after the engine and tee have both finished.
func runSession(
	parent context.Context,
	engineRun func(context.Context),
	engineEvents <-chan protocol.Event,
	store sessionStore,
	frontend func(<-chan protocol.Event) error,
) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	frontendEvents := make(chan protocol.Event, 256)
	frontendDone := make(chan struct{})

	var appendErr error
	teeDone := make(chan struct{})
	go func() {
		defer close(teeDone)
		var closeFrontendOnce sync.Once
		closeFrontend := func() {
			closeFrontendOnce.Do(func() { close(frontendEvents) })
		}
		defer closeFrontend()

		forwarding := true
		for ev := range engineEvents {
			if err := store.Append(ev); err != nil && appendErr == nil {
				appendErr = err
			}
			if !forwarding {
				continue
			}
			select {
			case <-frontendDone:
				forwarding = false
				closeFrontend()
				continue
			default:
			}
			select {
			case frontendEvents <- ev:
			case <-frontendDone:
				forwarding = false
				closeFrontend()
			}
		}
	}()

	engineDone := make(chan struct{})
	go func() {
		defer close(engineDone)
		engineRun(ctx)
	}()

	frontendErr := frontend(frontendEvents)
	close(frontendDone)
	cancel()

	<-engineDone
	<-teeDone

	var out error
	if frontendErr != nil {
		out = errors.Join(out, fmt.Errorf("frontend: %w", frontendErr))
	}
	if appendErr != nil {
		out = errors.Join(out, fmt.Errorf("append: %w", appendErr))
	}
	if closeErr := store.Close(); closeErr != nil {
		out = errors.Join(out, fmt.Errorf("close: %w", closeErr))
	}
	return out
}

// assembled is the composition-root product shared by the TUI and headless
// exec frontends: engine, session manager binding, and host services.
type assembled struct {
	eng       *engine.Engine
	sessions  *session.Manager
	store     session.Bound
	sessionID string
	// replay is prior transcript events for --continue (UI only; not re-appended).
	replay       []protocol.Event
	workDir      string
	cfg          config.Config
	services     host.Services
	firstRun     bool
	historyClose func() error
	memoryClose  func() error
	issuesClose  func() error
}

// assemble resolves project/config/auth, builds the engine and session store,
// and wraps host services. requireProvider forces credential validation even
// when --provider was not set (headless exec cannot pick a provider later).
// The caller must invoke historyClose and, if it never hands store to
// runSession, store.Close.
func assemble(opts cliOptions, requireProvider bool) (*assembled, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	projectIdentity, err := project.Resolve(context.Background(), workDir)
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

	// Built-in agents first (build default, then plan). Empty Prompt means
	// "compose shared baseline + provider overlay at request time". User
	// agents from ~/.strike/agents and ./.strike/agents follow and may
	// override same-named built-ins (their body becomes the persona layer).
	agents := []engine.Agent{
		{Name: "build", Description: "The default agent. Executes tools based on configured permissions."},
		{Name: "plan", Description: "Plan mode. Read-only analysis and implementation plans."},
	}
	loadedAgents, err := config.LoadAgentsWithError(workDir)
	if err != nil {
		_ = issueStore.Close()
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("loading agents: %w", err)
	}
	for _, a := range loadedAgents {
		if i := indexAgent(agents, a.Name); i >= 0 {
			agents[i] = engine.Agent(a)
			continue
		}
		agents = append(agents, engine.Agent(a))
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
		initialPhaseWF    string
		initialPhaseIndex int
		initialAlways     permission.Ruleset
		// quietStartup: resume re-applies selections without re-teeing them
		// into JSONL (TUI seeds from replay). Fresh sessions still announce.
		quietStartup bool
	)
	resumeID := strings.TrimSpace(opts.sessionID)
	if opts.continueSession {
		info, err := sessions.LatestRoot()
		if err != nil {
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("continue: %w", err)
		}
		resumeID = info.ID
	}
	if resumeID != "" {
		opened, err := openResumeSession(sessions, resumeID)
		if err != nil {
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, err
		}
		bound = opened.bound
		sessionID = opened.id
		replay = opened.replay
		restored := engine.Restore(replay)
		initialMessages = restored.Messages
		initialPriority = restored.Priority
		initialTitled = restored.Titled
		initialAutonomy = restored.Autonomy
		initialPhaseWF = restored.PhaseWorkflow
		initialPhaseIndex = restored.PhaseIndex
		initialAlways = restored.AlwaysGrants
		if !opts.providerSet && restored.Provider != "" {
			initialProvider = restored.Provider
		}
		if opts.model == "" && restored.Model != "" {
			initialModel = restored.Model
		}
		if opts.effort == "" && restored.Effort != protocol.EffortDefault {
			initialEffort = restored.Effort
		}
		if restored.Agent != "" {
			initialAgent = restored.Agent
		}
		// CLI provider/model/effort overrides on resume must still be logged
		// so the next Restore sees the switch; keep startup noisy then.
		quietStartup = !opts.providerSet && opts.model == "" && opts.effort == ""
	} else {
		info, err := sessions.Create(session.CreateOptions{})
		if err != nil {
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("creating session: %w", err)
		}
		bound, err = sessions.Bind(info.ID)
		if err != nil {
			_ = sessions.CloseAll()
			_ = issueStore.Close()
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("binding session: %w", err)
		}
		sessionID = info.ID
	}
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
	eng := engine.New(engine.Options{
		SessionID:            sessionID,
		Select:               selectProvider,
		Registry:             registry,
		WorkDir:              workDir,
		ProjectRoot:          projectIdentity.Root,
		Instructions:         instructions,
		SystemPrompt:         cfg.SystemPrompt,
		InitialProvider:      initialProvider,
		InitialModel:         initialModel,
		InitialEffort:        initialEffort,
		InitialAutonomy:      initialAutonomy,
		Agents:               agents,
		InitialAgent:         initialAgent,
		InitialMessages:      initialMessages,
		InitialPriority:      initialPriority,
		InitialTitled:        initialTitled,
		InitialPhaseWorkflow: initialPhaseWF,
		InitialPhaseIndex:    initialPhaseIndex,
		InitialAlwaysGrants:  initialAlways,
		QuietStartup:         quietStartup,
		Workflows:            workflows,
		Rules:                permissionLayers(cfg.Permissions, opts.dangerouslySkipPermissions),
		Hooks:                hookDefs,
		HookRules:            cfg.HookRules(),
		LookupContextWindow: func(providerName, model string) int {
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
		},
		PersistProjectRule: func(rule permission.Rule) error {
			return config.AppendProjectPermission(workDir, rule)
		},
		PersistSessionMeta: func(m protocol.SessionMeta) error {
			_, err := session.UpdateMeta(sessionDir, sessionID, func(meta *session.Meta) {
				if m.PRURL != "" {
					meta.PRURL = m.PRURL
				}
				if m.PRNumber != 0 {
					meta.PRNumber = m.PRNumber
				}
			})
			return err
		},
		OpenChildSession: func(parentID, childID, title string) (string, error) {
			info, err := sessions.Create(session.CreateOptions{
				ID:              childID,
				ParentSessionID: parentID,
				Title:           title,
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

	agentNames := make([]string, len(agents))
	for i, a := range agents {
		agentNames[i] = a.Name
	}
	// local.New wraps the real backend stores in the host.Services contract;
	// the TUI never sees auth/config/models/history/memory/issues directly.
	services := local.New(authStore, historyStore, memoryStore, issueStore, agentNames, skills, customStore)
	services.Files = local.NewFiles(workDir)
	services.Sessions = local.NewSessions(sessions)

	return &assembled{
		eng:       eng,
		sessions:  sessions,
		store:     bound,
		sessionID: sessionID,
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
	}, nil
}

// resumeOpened is the product of openResumeSession.
type resumeOpened struct {
	id     string
	bound  session.Bound
	replay []protocol.Event
}

// openResumeSession opens an existing root session, binds it, and loads the
// event log for engine.Restore + TUI replay. Child (subagent) sessions are
// rejected — resume is for root transcripts only.
func openResumeSession(sessions *session.Manager, id string) (resumeOpened, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return resumeOpened{}, fmt.Errorf("session: id is empty")
	}
	info, err := sessions.Get(id)
	if err != nil {
		return resumeOpened{}, fmt.Errorf("session: %w", err)
	}
	if info.ParentSessionID != "" {
		return resumeOpened{}, fmt.Errorf("session %q is a subagent transcript; resume a root session", id)
	}
	if _, err := sessions.Open(id); err != nil {
		return resumeOpened{}, fmt.Errorf("session: opening: %w", err)
	}
	bound, err := sessions.Bind(id)
	if err != nil {
		_ = sessions.CloseAll()
		return resumeOpened{}, fmt.Errorf("session: binding: %w", err)
	}
	replay, err := sessions.Replay(id)
	if err != nil {
		_ = bound.Close()
		return resumeOpened{}, fmt.Errorf("session: replaying: %w", err)
	}
	return resumeOpened{id: id, bound: bound, replay: replay}, nil
}

// run is the interactive composition root: assemble backend, then hand the
// host.Services bundle to the TUI frontend. When the user picks another
// session in /session, the TUI quits with PendingResume set and this loop
// reopens that session with full engine.Restore (not transcript-only view).
// Historical events are seeded into the TUI via Options.Replay (side-effect
// free); only the live engine stream is consumed for applyEvent.
func run(opts cliOptions, stdout, stderr io.Writer) (runErr error) {
	warnedDangerous := false
	for {
		a, err := assemble(opts, false)
		if err != nil {
			return err
		}
		if !warnedDangerous {
			writeDangerousPermissionsWarning(stderr, opts.dangerouslySkipPermissions)
			warnedDangerous = true
		}

		// runSession takes ownership of store (closes it). If setup fails before
		// that handoff, close here without double-closing.
		storeOwned := false
		closeAssembled := func() {
			if !storeOwned {
				if cerr := a.store.Close(); cerr != nil && runErr == nil {
					runErr = fmt.Errorf("closing session store: %w", cerr)
				}
			}
			if a.sessions != nil {
				_ = a.sessions.CloseAll()
			}
			if err := a.issuesClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project issues: %w", err)
			}
			if err := a.memoryClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project memory: %w", err)
			}
			if err := a.historyClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing prompt history: %w", err)
			}
		}

		var pendingResume string
		storeOwned = true
		sessionPath := a.store.Path()
		err = runSession(context.Background(), a.eng.Run, a.eng.Events(), a.store, func(live <-chan protocol.Event) error {
			restore := tui.EnableEnhancedKeys(stdout)
			defer restore()
			// Detect bg once before the program owns stdin — glamour/lipgloss OSC 11
			// replies must not race into the composer (#52).
			tui.PinAppearance()
			vimMode := tui.VimModePane
			if mode, ok := tui.ParseVimMode(a.cfg.VimMode); ok {
				vimMode = mode
			}
			themeID := theme.BuiltinID
			var themePtr *theme.Theme
			if a.cfg.Theme != "" {
				if entry, ok := theme.Lookup(theme.Catalog(a.workDir), a.cfg.Theme); ok {
					th := entry.Theme
					themePtr = &th
					themeID = entry.ID
				}
			}
			program := tea.NewProgram(tui.New(a.eng.Ops(), live, a.services, tui.Options{
				DangerouslySkipPermissions: opts.dangerouslySkipPermissions,
				Theme:                      themePtr,
				ThemeID:                    themeID,
				SessionID:                  a.sessionID,
				WorkDir:                    a.workDir,
				FirstRun:                   a.firstRun,
				VimMode:                    vimMode,
				Replay:                     a.replay,
			}), tea.WithAltScreen(), tea.WithOutput(stdout), tea.WithInput(tui.WrapInput(os.Stdin)), tea.WithReportFocus(), tea.WithMouseCellMotion())
			final, runProgErr := program.Run()
			if m, ok := final.(tui.Model); ok {
				pendingResume = m.PendingResume()
			}
			return runProgErr
		})
		closeAssembled()
		if err != nil {
			return err
		}
		if runErr != nil {
			return runErr
		}
		if pendingResume == "" {
			fmt.Fprintln(stdout, "session log:", sessionPath)
			return nil
		}
		// Restart with the chosen session: durable resume, not transcript-only.
		opts.continueSession = false
		opts.sessionID = pendingResume
	}
}

// runExec is the headless one-shot composition root: same engine and session
// log as the TUI, but streams assistant text to stdout and exits after one turn.
func runExec(opts cliOptions, prompt string, stdout, stderr io.Writer) (runErr error) {
	a, err := assemble(opts, true)
	if err != nil {
		return err
	}
	defer func() {
		if err := a.issuesClose(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing project issues: %w", err)
		}
		if err := a.memoryClose(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing project memory: %w", err)
		}
		if err := a.historyClose(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing prompt history: %w", err)
		}
	}()

	storeOwned := false
	defer func() {
		if !storeOwned {
			if cerr := a.store.Close(); cerr != nil && runErr == nil {
				runErr = fmt.Errorf("closing session store: %w", cerr)
			}
		}
	}()

	writeDangerousPermissionsWarning(stderr, opts.dangerouslySkipPermissions)

	storeOwned = true
	return runSession(context.Background(), a.eng.Run, a.eng.Events(), a.store, func(events <-chan protocol.Event) error {
		return runHeadlessFrontend(a.eng.Ops(), events, prompt, stdout, stderr)
	})
}

// isFreshStrikeHome reports a first-run install: no global config file and no
// real credentials for anthropic/openai/xai. echo does not count as configured.
func isFreshStrikeHome(store *auth.Store) bool {
	if path := config.GlobalPath(); path != "" {
		if _, err := os.Stat(path); err == nil {
			return false
		}
	}
	for _, provider := range []string{"anthropic", "openai", "xai"} {
		if auth.Describe(provider, store) != "none" {
			return false
		}
	}
	return true
}

func indexAgent(agents []engine.Agent, name string) int {
	for i, a := range agents {
		if a.Name == name {
			return i
		}
	}
	return -1
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
