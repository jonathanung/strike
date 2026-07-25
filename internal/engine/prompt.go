package engine

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

//go:embed prompt/shared.txt
var sharedPrompt string

//go:embed prompt/default.txt
var defaultProviderPrompt string

//go:embed prompt/anthropic.txt
var anthropicProviderPrompt string

//go:embed prompt/openai.txt
var openaiProviderPrompt string

//go:embed prompt/xai.txt
var xaiProviderPrompt string

//go:embed prompt/plan.txt
var planPrompt string

func normPrompt(s string) string {
	return strings.TrimSpace(s) + "\n"
}

// SharedSystemPrompt is the always-on baseline (identity, ADHD response
// contract, tools, safety). Provider and agent layers stack on top.
var SharedSystemPrompt = normPrompt(sharedPrompt)

// DefaultSystemPrompt is shared + generic provider notes — used when no
// provider is selected yet and as the build-agent fallback content.
var DefaultSystemPrompt = joinPromptLayers(SharedSystemPrompt, normPrompt(defaultProviderPrompt))

// PlanSystemPrompt is the read-only plan overlay (composed with shared +
// provider at request time for the plan agent).
var PlanSystemPrompt = normPrompt(planPrompt)

// ProviderSystemPrompt returns the provider-specific overlay for a strike
// provider name and/or model id (opencode-style selection).
func ProviderSystemPrompt(provider, model string) string {
	switch providerPromptKind(provider, model) {
	case "anthropic":
		return normPrompt(anthropicProviderPrompt)
	case "openai":
		return normPrompt(openaiProviderPrompt)
	case "xai":
		return normPrompt(xaiProviderPrompt)
	default:
		return normPrompt(defaultProviderPrompt)
	}
}

func providerPromptKind(provider, model string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	m := strings.ToLower(strings.TrimSpace(model))
	switch p {
	case "anthropic":
		return "anthropic"
	case "openai", "chatgpt":
		return "openai"
	case "xai":
		return "xai"
	}
	switch {
	case strings.Contains(m, "claude"):
		return "anthropic"
	case strings.Contains(m, "grok"):
		return "xai"
	case strings.Contains(m, "gpt"),
		strings.Contains(m, "o1"),
		strings.Contains(m, "o3"),
		strings.Contains(m, "o4"):
		return "openai"
	default:
		return "default"
	}
}

func joinPromptLayers(layers ...string) string {
	parts := make([]string, 0, len(layers))
	for _, layer := range layers {
		if s := strings.TrimSpace(layer); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// system returns the composed system prompt for the next provider request:
// shared baseline → provider overlay (or agent/config override) → plan overlay
// → environment → instruction files.
func (e *Engine) system() string {
	parts := []string{SharedSystemPrompt}

	switch {
	case e.agent.Name == "build" && strings.TrimSpace(e.opts.SystemPrompt) != "":
		// Config systemPrompt replaces the provider overlay for build only.
		parts = append(parts, e.opts.SystemPrompt)
	case strings.TrimSpace(e.agent.Prompt) != "":
		// Custom agent (or user-defined build/plan.md) supplies the persona layer.
		parts = append(parts, e.agent.Prompt)
	default:
		parts = append(parts, ProviderSystemPrompt(e.provName, e.model))
	}

	// Built-in plan constraints always apply while the plan agent is active,
	// even when a user plan.md supplies extra persona text.
	if e.agent.Name == "plan" {
		parts = append(parts, PlanSystemPrompt)
	}

	if env := e.environmentPrompt(); env != "" {
		parts = append(parts, env)
	}
	for _, inst := range e.opts.Instructions {
		if s := strings.TrimSpace(inst); s != "" {
			parts = append(parts, s)
		}
	}
	return joinPromptLayers(parts...)
}

func (e *Engine) environmentPrompt() string {
	workDir := e.opts.WorkDir
	if workDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workDir = wd
		}
	}
	root := e.opts.ProjectRoot
	if root == "" {
		root = workDir
	}
	isGit := "no"
	if root != "" {
		if st, err := os.Stat(filepath.Join(root, ".git")); err == nil && (st.IsDir() || st.Mode().IsRegular()) {
			isGit = "yes"
		}
	}
	modelLine := "You are powered by an unset model."
	if e.provName != "" || e.model != "" {
		id := e.model
		if e.provName != "" && e.model != "" {
			id = e.provName + "/" + e.model
		} else if e.provName != "" {
			id = e.provName
		}
		modelLine = fmt.Sprintf("You are powered by the model named %s. The exact model ID is %s", e.model, id)
	}
	return strings.TrimSpace(fmt.Sprintf(`%s
Here is some useful information about the environment you are running in:
<env>
  Working directory: %s
  Workspace root folder: %s
  Is directory a git repo: %s
  Platform: %s
  Today's date: %s
</env>`, modelLine, workDir, root, isGit, runtime.GOOS, time.Now().Format("Mon Jan 2 2006")))
}
