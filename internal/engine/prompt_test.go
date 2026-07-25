package engine_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestDefaultSystemPromptMatchesOpencodeStyle(t *testing.T) {
	p := engine.DefaultSystemPrompt
	for _, want := range []string{
		"You are strike",
		"# Tone and style",
		"# Tool usage policy",
		"# Code References",
		"NEVER commit changes unless the user explicitly asks",
		"/help",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("DefaultSystemPrompt missing %q", want)
		}
	}
}

func TestPlanSystemPromptIsReadOnly(t *testing.T) {
	p := engine.PlanSystemPrompt
	for _, want := range []string{
		"plan mode",
		"MUST NOT make any edits",
		"READ-ONLY",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("PlanSystemPrompt missing %q", want)
		}
	}
}

func TestSystemPromptComposesEnvironmentAndInstructions(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "pkg")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	prov := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})
	eng := engine.New(engine.Options{
		SessionID: "s1",
		Select: func(string) (provider.Provider, string, error) {
			return prov, "model-a", nil
		},
		Registry:     tool.NewRegistry(),
		WorkDir:      work,
		ProjectRoot:  root,
		Instructions: []string{"Instructions from: /tmp/AGENTS.md\nUse make test."},
		Agents: []engine.Agent{{
			Name:   "build",
			Prompt: "BASE_PROMPT_MARKER",
		}},
		InitialProvider: "scripted",
		InitialModel:    "model-a",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}

	var req provider.Request
	deadline := time.After(5 * time.Second)
	for req.System == "" {
		select {
		case req = <-prov.requests:
		case ev := <-eng.Events():
			if err, ok := ev.(protocol.EngineError); ok {
				t.Fatalf("engine error: %s", err.Message)
			}
		case <-deadline:
			t.Fatal("timeout waiting for Stream request")
		}
	}

	sys := req.System
	for _, want := range []string{
		"BASE_PROMPT_MARKER",
		"Working directory: " + work,
		"Workspace root folder: " + root,
		"Is directory a git repo: yes",
		"You are powered by the model named model-a",
		"scripted/model-a",
		"Instructions from: /tmp/AGENTS.md",
		"Use make test.",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt missing %q\n---\n%s", want, sys)
		}
	}
	baseIdx := strings.Index(sys, "BASE_PROMPT_MARKER")
	envIdx := strings.Index(sys, "<env>")
	instIdx := strings.Index(sys, "Instructions from:")
	if !(baseIdx >= 0 && envIdx > baseIdx && instIdx > envIdx) {
		t.Fatalf("layer order wrong: base=%d env=%d inst=%d", baseIdx, envIdx, instIdx)
	}
}
