package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/internal/auth"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/providers"
	"github.com/jonathanung/strike-cli/providers/factory"
)

// workflowTestCompleter, when non-nil, overrides provider-backed completion in
// tests (deterministic model output without network).
var workflowTestCompleter config.TextCompleter

func runWorkflowGenerate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("workflow generate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	nameHint := fs.String("name", "", "")
	providerName := fs.String("provider", "", "")
	modelName := fs.String("model", "", "")
	save := fs.Bool("save", false, "")
	global := fs.Bool("global", false, "")
	project := fs.Bool("project", false, "")
	force := fs.Bool("force", false, "")
	yes := fs.Bool("yes", false, "")
	printJSON := fs.Bool("json", false, "")
	// Standard flag parse: flags before intent; remaining args are intent words.
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "strike workflow generate:", err)
		fmt.Fprint(stderr, workflowUsage)
		return 2
	}
	intent := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if intent == "" {
		fmt.Fprintln(stderr, "strike workflow generate: require <intent...>")
		fmt.Fprint(stderr, workflowUsage)
		return 2
	}
	if *save {
		if *global == *project {
			fmt.Fprintln(stderr, "strike workflow generate: --save requires exactly one of --global or --project")
			return 2
		}
		if !*yes {
			fmt.Fprintln(stderr, "strike workflow generate: --save requires --yes (explicit confirmation; drafts never auto-save)")
			return 2
		}
	} else if *global || *project || *force || *yes {
		fmt.Fprintln(stderr, "strike workflow generate: --global/--project/--force/--yes only apply with --save")
		return 2
	}

	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "strike workflow generate:", err)
		return 1
	}
	agents, err := config.LoadAgentsWithError(workDir)
	if err != nil {
		fmt.Fprintln(stderr, "strike workflow generate: loading agents:", err)
		return 1
	}
	agentNames := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.Name != "" {
			agentNames = append(agentNames, a.Name)
		}
	}
	known := config.AgentNameSet(agents)

	complete := workflowTestCompleter
	if complete == nil {
		complete, err = buildWorkflowCompleter(workDir, *providerName, *modelName)
		if err != nil {
			fmt.Fprintln(stderr, "strike workflow generate:", err)
			return 1
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	draft, err := config.GenerateWorkflowDraft(ctx, complete, config.GenerateDraftOpts{
		Intent:      intent,
		NameHint:    *nameHint,
		Agents:      agentNames,
		KnownAgents: known,
	})
	if err != nil {
		fmt.Fprintln(stderr, "strike workflow generate:", err)
		return 1
	}

	rev := config.ReviewWorkflowDraft(draft, config.DraftReviewOpts{
		Baseline:    []permission.Ruleset{permission.Defaults()},
		KnownAgents: known,
	})
	fmt.Fprint(stdout, config.FormatDraftReview(rev))
	if *printJSON || !draft.Valid() {
		if len(draft.Raw) > 0 {
			fmt.Fprintln(stdout, "\n--- draft JSON ---")
			fmt.Fprintln(stdout, strings.TrimSpace(string(draft.Raw)))
		}
	} else if draft.Valid() {
		raw, err := config.FormatWorkflow(draft.Workflow)
		if err == nil {
			fmt.Fprintln(stdout, "\n--- canonical JSON ---")
			fmt.Fprint(stdout, string(raw))
		}
	}

	if !draft.Valid() {
		fmt.Fprintln(stderr, "strike workflow generate: draft invalid — not saved; fix JSON and use save-draft")
		return 1
	}
	if !*save {
		fmt.Fprintln(stdout, "draft not saved (pass --save --yes --global|--project after review)")
		return 0
	}

	scope := "global"
	wd := ""
	if *project {
		scope = "project"
		wd = workDir
	}
	path, err := config.SaveWorkflowDraft(draft, config.SaveDraftOpts{
		Scope:   scope,
		WorkDir: wd,
		Force:   *force,
		Confirm: true, // --yes already required above
	})
	if err != nil {
		if errors.Is(err, config.ErrWorkflowExists) {
			fmt.Fprintln(stderr, "strike workflow generate:", err)
			fmt.Fprintln(stderr, "re-run with --force after confirming overwrite")
			return 1
		}
		fmt.Fprintln(stderr, "strike workflow generate:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (not activated)\n", path)
	return 0
}

func runWorkflowSaveDraft(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("workflow save-draft", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	global := fs.Bool("global", false, "")
	project := fs.Bool("project", false, "")
	force := fs.Bool("force", false, "")
	yes := fs.Bool("yes", false, "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "strike workflow save-draft:", err)
		fmt.Fprint(stderr, workflowUsage)
		return 2
	}
	if *global == *project {
		fmt.Fprintln(stderr, "strike workflow save-draft: require exactly one of --global or --project")
		return 2
	}
	if !*yes {
		fmt.Fprintln(stderr, "strike workflow save-draft: require --yes (explicit confirmation)")
		return 2
	}
	posArgs := fs.Args()
	if len(posArgs) > 1 {
		fmt.Fprintln(stderr, "strike workflow save-draft: at most one path (or - for stdin)")
		return 2
	}
	pathArg := "-"
	if len(posArgs) == 1 {
		pathArg = posArgs[0]
	}
	var data []byte
	var err error
	if pathArg == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(pathArg)
	}
	if err != nil {
		fmt.Fprintln(stderr, "strike workflow save-draft:", err)
		return 1
	}

	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "strike workflow save-draft:", err)
		return 1
	}
	agents, err := config.LoadAgentsWithError(workDir)
	if err != nil {
		fmt.Fprintln(stderr, "strike workflow save-draft: loading agents:", err)
		return 1
	}
	known := config.AgentNameSet(agents)
	draft := config.DraftFromJSON(data, "edit")
	draft.Revalidate(known)
	rev := config.ReviewWorkflowDraft(draft, config.DraftReviewOpts{
		Baseline:    []permission.Ruleset{permission.Defaults()},
		KnownAgents: known,
	})
	fmt.Fprint(stdout, config.FormatDraftReview(rev))
	if !draft.Valid() {
		fmt.Fprintln(stderr, "strike workflow save-draft: draft invalid — not saved")
		return 1
	}
	scope := "global"
	wd := ""
	if *project {
		scope = "project"
		wd = workDir
	}
	path, err := config.SaveWorkflowDraft(draft, config.SaveDraftOpts{
		Scope:   scope,
		WorkDir: wd,
		Force:   *force,
		Confirm: true,
	})
	if err != nil {
		if errors.Is(err, config.ErrWorkflowExists) {
			fmt.Fprintln(stderr, "strike workflow save-draft:", err)
			fmt.Fprintln(stderr, "re-run with --force after confirming overwrite")
			return 1
		}
		fmt.Fprintln(stderr, "strike workflow save-draft:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (not activated)\n", path)
	return 0
}

func buildWorkflowCompleter(workDir, providerName, modelName string) (config.TextCompleter, error) {
	providerName = config.CanonicalProviderID(strings.TrimSpace(providerName))
	cfg, _ := config.Load(workDir)
	if providerName == "" {
		if strings.TrimSpace(cfg.Provider) != "" {
			providerName = config.CanonicalProviderID(cfg.Provider)
		} else {
			providerName = "echo"
		}
	}
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		if strings.TrimSpace(cfg.Model) != "" && (cfg.Provider == "" || config.CanonicalProviderID(cfg.Provider) == providerName) {
			modelName = cfg.Model
		} else {
			modelName = config.DefaultModel(providerName)
		}
	}
	p, err := selectWorkflowProvider(providerName)
	if err != nil {
		return nil, err
	}
	if modelName == "" {
		modelName = config.DefaultModel(providerName)
	}
	return providerTextCompleter(p, modelName), nil
}

func selectWorkflowProvider(name string) (provider.Provider, error) {
	name = config.CanonicalProviderID(name)
	store, err := auth.OpenStore(auth.DefaultPath())
	if err != nil {
		if name == "echo" {
			p, _, selErr := providers.Select(name, factory.Options{})
			return p, selErr
		}
		return nil, fmt.Errorf("opening auth store: %w", err)
	}
	p, _, err := providers.Select(name, factory.Options{
		Store:        store,
		DefaultModel: config.DefaultModel,
	})
	return p, err
}

func providerTextCompleter(p provider.Provider, model string) config.TextCompleter {
	return func(ctx context.Context, system, user string) (string, error) {
		ch, err := p.Stream(ctx, provider.Request{
			Model:  model,
			System: system,
			Messages: []provider.Message{
				{Role: provider.RoleUser, Text: user},
			},
			MaxTokens: 4096,
		})
		if err != nil {
			return "", err
		}
		var b strings.Builder
		for ev := range ch {
			switch ev.Type {
			case provider.EventTextDelta:
				b.WriteString(ev.Text)
			case provider.EventError:
				if ev.Err != nil {
					return "", ev.Err
				}
				return "", fmt.Errorf("provider stream error")
			case provider.EventDone:
				// ok
			}
		}
		return b.String(), nil
	}
}
