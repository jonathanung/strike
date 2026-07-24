package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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

type cliOptions struct {
	provider                   string
	model                      string
	dangerouslySkipPermissions bool
	providerSet                bool
}

type optionSpec struct {
	names       []string
	valueName   string
	description string
	register    func(*flag.FlagSet, *cliOptions)
}

var optionSpecs = []optionSpec{
	{
		names:       []string{"provider"},
		valueName:   "provider",
		description: "provider to use (anthropic|openai|xai|echo); overrides config",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.StringVar(&opts.provider, "provider", "", "")
		},
	},
	{
		names:       []string{"model"},
		valueName:   "model",
		description: "model id; overrides config",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.StringVar(&opts.model, "model", "", "")
		},
	},
	{
		names:       []string{"dangerously-skip-permissions"},
		description: "allow all tool calls without permission checks for this invocation",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.dangerouslySkipPermissions, "dangerously-skip-permissions", false, "")
		},
	},
	{
		names:       []string{"h", "help"},
		description: "show help",
	},
}

const dangerousPermissionsWarning = "WARNING: --dangerously-skip-permissions is enabled; all tool calls will be allowed without permission checks for this invocation only."

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "auth" {
		if err := runAuth(args[1:], stdout); err != nil {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
		return 0
	}

	opts, err := parseCLIOptions(args)
	if err != nil {
		if err == flag.ErrHelp {
			writeUsage(stdout)
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		writeUsage(stderr)
		return 2
	}
	if err := run(opts, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return 0
}

func parseCLIOptions(args []string) (cliOptions, error) {
	var opts cliOptions
	fs := flag.NewFlagSet("strike", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	for _, spec := range optionSpecs {
		if spec.register != nil {
			spec.register(fs, &opts)
		}
	}
	if err := prevalidateCLIOptions(args, fs); err != nil {
		return cliOptions{}, err
	}
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "provider" && opts.provider != "" {
			opts.providerSet = true
		}
	})
	if fs.NArg() != 0 {
		return cliOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return opts, nil
}

func prevalidateCLIOptions(args []string, fs *flag.FlagSet) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			return flag.ErrHelp
		}
		if arg == "--" || len(arg) < 2 || arg[0] != '-' {
			return nil
		}

		prefixLen := 1
		if len(arg) > 2 && arg[1] == '-' {
			prefixLen = 2
		}
		name := arg[prefixLen:]
		if equals := strings.IndexByte(name, '='); equals >= 0 {
			name = name[:equals]
		}
		f := fs.Lookup(name)
		if f == nil {
			return nil
		}
		if prefixLen == 1 {
			return fmt.Errorf("flag provided but not defined: -%s", name)
		}
		if !isBoolFlag(f) && len(name)+prefixLen == len(arg) && i+1 < len(args) {
			i++
		}
	}
	return nil
}

func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  strike [options]")
	fmt.Fprintln(w, "  strike auth <command> [arguments]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	for _, spec := range optionSpecs {
		var spelling string
		if len(spec.names) == 2 && spec.names[0] == "h" {
			spelling = "-h, --help"
		} else {
			spelling = "--" + spec.names[0]
			if spec.valueName != "" {
				spelling += " <" + spec.valueName + ">"
			}
		}
		fmt.Fprintf(w, "  %-34s %s\n", spelling, spec.description)
	}
}

func permissionLayers(configured permission.Ruleset, dangerouslySkip bool) []permission.Ruleset {
	layers := []permission.Ruleset{
		permission.Defaults(),
		append(permission.Ruleset(nil), configured...),
	}
	if dangerouslySkip {
		layers = append(layers, permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.Allow}})
	}
	return layers
}

func writeDangerousPermissionsWarning(w io.Writer, enabled bool) {
	if enabled {
		fmt.Fprintln(w, dangerousPermissionsWarning)
	}
}

func run(opts cliOptions, stdout, stderr io.Writer) (runErr error) {

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
	if opts.providerSet && opts.provider != "" {
		cfg.Provider = opts.provider
	}
	if opts.model != "" {
		cfg.Model = opts.model
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
	if opts.providerSet && opts.provider != "" {
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
		Rules:           permissionLayers(cfg.Permissions, opts.dangerouslySkipPermissions),
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
	writeDangerousPermissionsWarning(stderr, opts.dangerouslySkipPermissions)
	go eng.Run(ctx)

	agentNames := make([]string, len(agents))
	for i, a := range agents {
		agentNames[i] = a.Name
	}
	program := tea.NewProgram(tui.New(eng.Ops(), events, authStore, agentNames, skills, tui.Options{
		History:                    historyStore,
		DangerouslySkipPermissions: opts.dangerouslySkipPermissions,
	}), tea.WithAltScreen(), tea.WithOutput(stdout))
	if _, err := program.Run(); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "session log:", store.Path())
	return nil
}
