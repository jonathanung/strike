package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/history"
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/host/local"
	"github.com/jonathanung/strike-cli/internal/memory"
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
// exec frontends: engine, session store, and host services.
type assembled struct {
	eng          *engine.Engine
	store        *session.Store
	sessionID    string
	workDir      string
	cfg          config.Config
	services     host.Services
	firstRun     bool
	historyClose func() error
	memoryClose  func() error
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
	cfg, err := config.Load(workDir)
	if err != nil {
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
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("unknown effort %q (want off, low, medium, high, xhigh, or max)", opts.effort)
		}
		cfg.Effort = level
	}

	authStore, err := auth.OpenStore(auth.DefaultPath())
	if err != nil {
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("opening auth store: %w", err)
	}

	// selectProvider constructs a provider by name, probing credentials so
	// a bad /provider selection fails at select time with a clear message
	// instead of on the first prompt.
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
			return nil, "", fmt.Errorf("unknown provider %q (want anthropic, openai, xai, or echo)", name)
		}
	}

	// An explicit --provider must work or fail loudly; the config default is
	// only a silent best-effort initial selection (pick one in-app with
	// /provider otherwise). Headless exec always requires a usable provider.
	if requireProvider || (opts.providerSet && opts.provider != "") {
		if cfg.Provider == "" {
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, fmt.Errorf("no provider configured (pass --provider or set provider in config)")
		}
		if _, _, err := selectProvider(cfg.Provider); err != nil {
			_ = memoryStore.Close()
			_ = historyStore.Close()
			return nil, err
		}
	}

	// Skills load before the tool registry so the skill tool can advertise
	// available names in its description at construction time.
	skills, err := config.LoadSkillsWithError(workDir)
	if err != nil {
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
		tool.NewNotebookEdit(),
		tool.NewSleep(),
		tool.NewSkill(skillInfos),
		tool.NewQuestion(),
		tool.NewEnterPlanMode(),
		tool.NewExitPlanMode(),
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

	// One session ID shared by the engine (event correlation) and the JSONL
	// filename so transcript identity matches runtime correlation.
	sessionID := session.NewID()
	eng := engine.New(engine.Options{
		SessionID:       sessionID,
		Select:          selectProvider,
		Registry:        registry,
		WorkDir:         workDir,
		ProjectRoot:     projectIdentity.Root,
		Instructions:    instructions,
		SystemPrompt:    cfg.SystemPrompt,
		InitialProvider: cfg.Provider,
		InitialModel:    cfg.Model,
		InitialEffort:   cfg.Effort,
		Agents:          agents,
		InitialAgent:    cfg.DefaultAgent,
		Rules:           permissionLayers(cfg.Permissions, opts.dangerouslySkipPermissions),
	})

	store, err := session.Open(session.DefaultDir(), sessionID)
	if err != nil {
		_ = memoryStore.Close()
		_ = historyStore.Close()
		return nil, fmt.Errorf("opening session store: %w", err)
	}

	agentNames := make([]string, len(agents))
	for i, a := range agents {
		agentNames[i] = a.Name
	}
	// local.New wraps the real backend stores in the host.Services contract;
	// the TUI never sees auth/config/models/history/memory directly.
	services := local.New(authStore, historyStore, memoryStore, agentNames, skills)
	services.Files = local.NewFiles(workDir)

	return &assembled{
		eng:       eng,
		store:     store,
		sessionID: sessionID,
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
	}, nil
}

// run is the interactive composition root: assemble backend, then hand the
// host.Services bundle to the TUI frontend.
func run(opts cliOptions, stdout, stderr io.Writer) (runErr error) {
	a, err := assemble(opts, false)
	if err != nil {
		return err
	}
	defer func() {
		if err := a.memoryClose(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing project memory: %w", err)
		}
		if err := a.historyClose(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing prompt history: %w", err)
		}
	}()

	// runSession takes ownership of store (closes it). If setup fails before
	// that handoff, close here without double-closing.
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
	if err := runSession(context.Background(), a.eng.Run, a.eng.Events(), a.store, func(events <-chan protocol.Event) error {
		restore := tui.EnableEnhancedKeys(stdout)
		defer restore()
		vimMode := tui.VimModePane
		if mode, ok := tui.ParseVimMode(a.cfg.VimMode); ok {
			vimMode = mode
		}
		program := tea.NewProgram(tui.New(a.eng.Ops(), events, a.services, tui.Options{
			DangerouslySkipPermissions: opts.dangerouslySkipPermissions,
			SessionID:                  a.sessionID,
			WorkDir:                    a.workDir,
			FirstRun:                   a.firstRun,
			VimMode:                    vimMode,
		}), tea.WithAltScreen(), tea.WithOutput(stdout), tea.WithInput(tui.WrapInput(os.Stdin)), tea.WithReportFocus(), tea.WithMouseCellMotion())
		_, err := program.Run()
		return err
	}); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "session log:", a.store.Path())
	return nil
}

// runExec is the headless one-shot composition root: same engine and session
// log as the TUI, but streams assistant text to stdout and exits after one turn.
func runExec(opts cliOptions, prompt string, stdout, stderr io.Writer) (runErr error) {
	a, err := assemble(opts, true)
	if err != nil {
		return err
	}
	defer func() {
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
