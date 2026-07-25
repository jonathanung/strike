package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SkillInfo is the tool-facing skill payload (name, description, template body).
type SkillInfo struct {
	Name        string
	Description string
	Template    string
}

type skillTool struct {
	skills []SkillInfo
	desc   string
}

// NewSkill returns a tool that loads named skill templates into the conversation.
// The skills slice is copied so the caller cannot mutate the tool's catalog later.
func NewSkill(skills []SkillInfo) Tool {
	cp := make([]SkillInfo, len(skills))
	copy(cp, skills)
	names := make([]string, 0, len(cp))
	for _, s := range cp {
		names = append(names, s.Name)
	}
	var list string
	if len(names) == 0 {
		list = "(none loaded)"
	} else {
		list = strings.Join(names, ", ")
	}
	desc := fmt.Sprintf(`Load a specialized skill when the task matches one of the available skills.

Use this tool to inject the skill's instructions into the current conversation. The output may contain detailed workflow guidance.

Available skills: %s

The skill name must match one of the available skills. Optional arguments are substituted for $ARGUMENTS in the template (or appended if the placeholder is missing).`, list)
	return &skillTool{skills: cp, desc: desc}
}

func (t *skillTool) Name() string { return "skill" }

func (t *skillTool) Description() string { return t.desc }

func (t *skillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "The name of the skill from available skills"},
			"arguments": {"type": "string", "description": "Optional arguments substituted for $ARGUMENTS in the skill template"}
		},
		"required": ["name"]
	}`)
}

type skillArgs struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (t *skillTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a skillArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Name) == "" {
		return Result{}, fmt.Errorf("name is required")
	}

	var found *SkillInfo
	names := make([]string, 0, len(t.skills))
	for i := range t.skills {
		names = append(names, t.skills[i].Name)
		if t.skills[i].Name == a.Name {
			found = &t.skills[i]
		}
	}
	if found == nil {
		if len(names) == 0 {
			return Result{}, fmt.Errorf("unknown skill %q (no skills loaded)", a.Name)
		}
		return Result{}, fmt.Errorf("unknown skill %q (available: %s)", a.Name, strings.Join(names, ", "))
	}

	if err := tc.Ask(ctx, AskRequest{
		Permission: "skill",
		Patterns:   []string{a.Name},
		Always:     []string{a.Name},
	}); err != nil {
		return Result{}, err
	}

	rendered := renderSkillTemplate(found.Template, a.Arguments)
	out := fmt.Sprintf("# Skill: %s\n\n%s", found.Name, rendered)
	meta, _ := json.Marshal(map[string]any{"name": found.Name})
	return Result{
		Title:    fmt.Sprintf("skill %s", found.Name),
		Output:   out,
		Metadata: meta,
	}, nil
}

func renderSkillTemplate(template, args string) string {
	if strings.Contains(template, "$ARGUMENTS") {
		return strings.ReplaceAll(template, "$ARGUMENTS", args)
	}
	if args != "" {
		return template + "\n\nArguments: " + args
	}
	return template
}
