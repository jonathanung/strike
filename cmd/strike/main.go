package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/history"
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
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "auth" {
		if err := runAuth(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "strike:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "strike:", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	provFlag := flag.String("provider", "", "provider to use (anthropic|echo); overrides config")
	modelFlag := flag.String("model", "", "model id; overrides config")
	flag.Parse()

	workDir, err := os.Getwd()
	if err != nil {
		return err
	}
	projectIdentity, err := project.Resolve(context.Background(), workDir)
	if err != nil {
		return fmt.Errorf("resolving project identity: %w", err)
	}
	globalRoot := config.GlobalRoot()
	if globalRoot == "" {
		return fmt.Errorf("opening prompt history: global config root is unavailable")
	}
	historyStore, err := history.Open(globalRoot, projectIdentity.Key)
	if err != nil {
		return fmt.Errorf("opening prompt history: %w", err)
	}
	defer func() {
		if err := historyStore.Close(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing prompt history: %w", err)
		}
	}()
	cfg, err := config.Load(workDir)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	if *provFlag != "" {
		cfg.Provider = *provFlag
	}
	if *modelFlag != "" {
		cfg.Model = *modelFlag
	}

	authStore, err := auth.OpenStore(auth.DefaultPath())
	if err != nil {
		return fmt.Errorf("opening auth store: %w", err)
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
	// /provider otherwise).
	if *provFlag != "" {
		if _, _, err := selectProvider(cfg.Provider); err != nil {
			return err
		}
	}

	registry := tool.NewRegistry(
		tool.NewRead(),
		tool.NewGlob(),
		tool.NewGrep(),
		tool.NewEdit(),
		tool.NewWrite(),
		tool.NewBash(),
	)

	// The built-in build agent comes first (the default, like opencode);
	// user agents from ~/.strike/agents and ./.strike/agents follow.
	buildPrompt := engine.DefaultSystemPrompt
	if cfg.SystemPrompt != "" {
		buildPrompt = cfg.SystemPrompt
	}
	agents := []engine.Agent{{Name: "build", Description: "general coding agent", Prompt: buildPrompt}}
	loadedAgents, err := config.LoadAgentsWithError(workDir)
	if err != nil {
		return fmt.Errorf("loading agents: %w", err)
	}
	for _, a := range loadedAgents {
		if a.Name == "build" {
			agents[0] = engine.Agent(a) // user-defined build overrides the built-in
			continue
		}
		agents = append(agents, engine.Agent(a))
	}
	skills, err := config.LoadSkillsWithError(workDir)
	if err != nil {
		return fmt.Errorf("loading skills: %w", err)
	}

	eng := engine.New(engine.Options{
		Select:          selectProvider,
		Registry:        registry,
		WorkDir:         workDir,
		InitialProvider: cfg.Provider,
		InitialModel:    cfg.Model,
		Agents:          agents,
		InitialAgent:    cfg.DefaultAgent,
		Rules:           []permission.Ruleset{permission.Defaults(), cfg.Permissions},
	})

	store, err := session.Open(session.DefaultDir(), session.NewID())
	if err != nil {
		return fmt.Errorf("opening session store: %w", err)
	}
	defer store.Close()

	// Tee engine events into the session log on their way to the TUI.
	events := make(chan protocol.Event, 256)
	go func() {
		defer close(events)
		for ev := range eng.Events() {
			_ = store.Append(ev)
			events <- ev
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	agentNames := make([]string, len(agents))
	for i, a := range agents {
		agentNames[i] = a.Name
	}
	program := tea.NewProgram(tui.New(eng.Ops(), events, authStore, agentNames, skills, tui.Options{History: historyStore}), tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		return err
	}
	fmt.Println("session log:", store.Path())
	return nil
}
