package skillpropose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/pkg/redact"
)

// Model-assisted drafting (#1958). With the workspace's scan on and its
// model drafting on, each locally mined proposal can be rewritten by the
// operator's configured provider. The model sees only what the local draft
// already holds: redacted openings, tool names, and evidence titles. Its
// reply is redacted again, and the card says which model drafted it and
// from how many sessions. The proposal keeps its id and name, so dismissal
// and the installed-name check work exactly as for a local draft.

// DraftModel is one prose model call through the operator's provider. It
// returns the reply text and the model that wrote it. Implementations must
// not run tools.
type DraftModel func(ctx context.Context, system, prompt string) (text, model string, err error)

// MaxModelDrafts bounds model calls per scan.
const MaxModelDrafts = 5

const (
	maxModelBodyRunes = 6000
	maxModelDescRunes = 200
	modelDraftSystem  = `You turn a workflow a developer repeated across coding sessions into a reusable agent skill.
Reply with one JSON object and nothing else: {"description": "...", "body": "..."}.
description: one sentence saying when to use the skill.
body: Markdown instructions for the agent, with a "## When to use" section and a numbered "## Steps" section.
Use only the facts given. Do not invent tools, files, or commands. Never include secrets or tokens.`
)

// ErrBadDraft is returned when the model's reply is not a usable draft.
var ErrBadDraft = errors.New("model reply is not a usable skill draft")

// modelDraftPrompt is the user message for one proposal.
func modelDraftPrompt(p Proposal) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Skill name: %s\n", p.Name)
	fmt.Fprintf(&b, "Repeated in %d sessions.\n", sessionCount(p))
	fmt.Fprintf(&b, "%s\n", p.Description)
	fmt.Fprintf(&b, "Tools run in order: %s\n", strings.Join(p.Tools, ", "))
	if len(p.Sessions) > 0 {
		b.WriteString("Session titles:\n")
		for i, e := range p.Sessions {
			if i == 10 {
				break
			}
			if t := strings.TrimSpace(e.Title); t != "" {
				fmt.Fprintf(&b, "- %s\n", clip(oneLine(t), 120))
			}
		}
	}
	b.WriteString("\nLocal draft to improve:\n")
	b.WriteString(p.Body)
	return redact.String(b.String())
}

func sessionCount(p Proposal) int {
	if p.Count > 0 {
		return p.Count
	}
	return len(p.Sessions)
}

type modelReply struct {
	Description string `json:"description"`
	Body        string `json:"body"`
}

// parseModelReply reads the JSON object out of a model reply, tolerating a
// code fence or prose around it.
func parseModelReply(text string) (modelReply, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return modelReply{}, ErrBadDraft
	}
	var r modelReply
	if err := json.Unmarshal([]byte(text[start:end+1]), &r); err != nil {
		return modelReply{}, ErrBadDraft
	}
	r.Description = oneLine(r.Description)
	r.Body = strings.TrimSpace(r.Body)
	if r.Description == "" || r.Body == "" {
		return modelReply{}, ErrBadDraft
	}
	return r, nil
}

// ModelDraft asks model to rewrite p and returns the model-drafted
// proposal. On any failure p is returned unchanged with the error, so a
// caller can keep the local draft.
func ModelDraft(ctx context.Context, p Proposal, model DraftModel) (Proposal, error) {
	if model == nil {
		return p, errors.New("no drafting model configured")
	}
	text, name, err := model(ctx, modelDraftSystem, modelDraftPrompt(p))
	if err != nil {
		return p, err
	}
	name = oneLine(redact.String(name))
	if name == "" {
		return p, errors.New("drafting model did not say which model replied")
	}
	r, err := parseModelReply(text)
	if err != nil {
		return p, err
	}
	n := sessionCount(p)
	body := r.Body
	if !strings.Contains(body, "$ARGUMENTS") {
		body += "\n\n## Request\n\n$ARGUMENTS"
	}
	body = fmt.Sprintf("Drafted by %s from %d sessions in this workspace. Review the steps before you rely on them.\n\n", name, n) + body
	out := p
	out.Description = redact.String(clip(r.Description, maxModelDescRunes))
	out.Body = redact.String(clip(body, maxModelBodyRunes))
	if !strings.Contains(out.Body, "$ARGUMENTS") {
		// Clipping cut the placeholder; the skill must still take a request.
		out.Body = clip(out.Body, maxModelBodyRunes-40) + "\n\n## Request\n\n$ARGUMENTS"
	}
	out.Model = name
	out.DraftedBy = name
	return out, nil
}
