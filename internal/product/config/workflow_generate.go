package config

import (
	"context"
	"fmt"
	"strings"
)

// TextCompleter produces one model completion for workflow draft generation.
// Implementations wrap a provider.Stream (or a deterministic test double).
// The completer must not write workflow files or activate workflows.
type TextCompleter func(ctx context.Context, system, user string) (string, error)

// GenerateDraftOpts configures model-assisted workflow draft generation.
type GenerateDraftOpts struct {
	// Intent is the user's natural-language description of the desired workflow.
	Intent string
	// NameHint is an optional preferred workflow name (still validated).
	NameHint string
	// Agents lists available agent names for the model to pin phases to.
	Agents []string
	// Tools lists available tool/permission names for context (optional).
	Tools []string
	// KnownAgents, when set, is used to validate agent pins after generation.
	// When nil, AgentNameSet(Agents) is used if Agents is non-empty.
	KnownAgents map[string]struct{}
}

// GenerateWorkflowDraft asks the model for a canonical workflow JSON document
// and returns an in-memory draft. It never saves to disk and never activates a
// workflow. Invalid model output remains an editable draft with diagnostics.
func GenerateWorkflowDraft(ctx context.Context, complete TextCompleter, opts GenerateDraftOpts) (WorkflowDraft, error) {
	if complete == nil {
		return WorkflowDraft{}, fmt.Errorf("workflow generate: nil completer")
	}
	intent := strings.TrimSpace(opts.Intent)
	if intent == "" {
		return WorkflowDraft{}, fmt.Errorf("workflow generate: empty intent")
	}
	system := WorkflowDraftSystemPrompt(opts.Agents, opts.Tools)
	user := WorkflowDraftUserPrompt(intent, opts.NameHint)
	text, err := complete(ctx, system, user)
	if err != nil {
		return WorkflowDraft{}, fmt.Errorf("workflow generate: %w", err)
	}
	d := DraftFromModelText(text, "model")
	known := opts.KnownAgents
	if known == nil && len(opts.Agents) > 0 {
		known = map[string]struct{}{}
		for _, a := range opts.Agents {
			a = strings.TrimSpace(a)
			if a != "" {
				known[a] = struct{}{}
			}
		}
	}
	d.Revalidate(known)
	return d, nil
}

// WorkflowDraftSystemPrompt is the system instruction for model generation.
func WorkflowDraftSystemPrompt(agents, tools []string) string {
	var b strings.Builder
	b.WriteString(`You generate strike-cli workflow definition JSON documents.

Output a single JSON object only (optionally inside a ` + "```json" + ` fence). No commentary.

Schema (schemaVersion 1, linear phases only):
{
  "schemaVersion": 1,
  "name": "kebab-or_safe-identifier",
  "description": "short summary",
  "phases": [
    {
      "name": "phase-id",
      "description": "optional",
      "agent": "optional agent pin",
      "context": "optional extra prompt context for this phase",
      "permissions": [
        {"permission": "tool-or-family", "pattern": "*", "action": "allow|ask|deny"}
      ],
      "exit": {"type": "agent|user|check", "command": "required when type is check"}
    }
  ]
}

Rules:
- name and phase names: non-empty identifiers, no path separators or ".." .
- At least one phase. Prefer 2–4 phases for typical flows.
- exit.type defaults to agent when omitted; check requires a non-empty command.
- permissions are optional; prefer deny for write/edit on read-only phases.
- Do not include runtime fields (source, path, fingerprint, active).
- Do not invent agent names outside the available list when one is provided.
- Never assume the workflow will be auto-started; it is a draft for human review.
`)
	if len(agents) > 0 {
		b.WriteString("\nAvailable agents (use only these for phase.agent when set):\n")
		for _, a := range agents {
			a = strings.TrimSpace(a)
			if a != "" {
				fmt.Fprintf(&b, "- %s\n", a)
			}
		}
	}
	if len(tools) > 0 {
		b.WriteString("\nKnown tools/permissions (for permission rules):\n")
		for _, t := range tools {
			t = strings.TrimSpace(t)
			if t != "" {
				fmt.Fprintf(&b, "- %s\n", t)
			}
		}
	}
	return b.String()
}

// WorkflowDraftUserPrompt is the user message carrying intent and optional name.
func WorkflowDraftUserPrompt(intent, nameHint string) string {
	var b strings.Builder
	b.WriteString("Create a workflow for this intent:\n\n")
	b.WriteString(strings.TrimSpace(intent))
	b.WriteByte('\n')
	if hint := strings.TrimSpace(nameHint); hint != "" {
		fmt.Fprintf(&b, "\nPreferred workflow name: %s\n", hint)
	}
	b.WriteString("\nRespond with the workflow JSON only.\n")
	return b.String()
}
