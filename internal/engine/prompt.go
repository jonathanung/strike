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

//go:embed prompt/default.txt
var defaultSystemPrompt string

//go:embed prompt/plan.txt
var planSystemPrompt string

// DefaultSystemPrompt is the built-in build-agent baseline, adapted from
// opencode's default session prompt for strike's tool set and slash commands.
var DefaultSystemPrompt = strings.TrimSpace(defaultSystemPrompt) + "\n"

// PlanSystemPrompt is the built-in plan-agent baseline (read-only planning).
var PlanSystemPrompt = strings.TrimSpace(planSystemPrompt) + "\n"

// system returns the composed system prompt for the next provider request:
// agent baseline, then environment, then project/global instruction files.
// Matches opencode's layered assembly (agent/provider prompt + env + AGENTS.md).
func (e *Engine) system() string {
	base := e.agent.Prompt
	if base == "" {
		base = DefaultSystemPrompt
	}
	parts := []string{strings.TrimSpace(base)}
	if env := e.environmentPrompt(); env != "" {
		parts = append(parts, env)
	}
	for _, inst := range e.opts.Instructions {
		if s := strings.TrimSpace(inst); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n") + "\n"
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
