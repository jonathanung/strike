package tui

import (
	"strings"

	"github.com/jonathanung/strike-cli/internal/config"
)

type commandID string

const (
	commandProvider commandID = "provider"
	commandModel    commandID = "model"
	commandAuth     commandID = "auth"
	commandAgent    commandID = "agent"
	commandHelp     commandID = "help"
)

type commandSource string

const (
	commandSourceBuiltin commandSource = "command"
	commandSourceSkill   commandSource = "skill"
)

type commandSpec struct {
	ID          commandID
	Name        string
	Description string
	ArgsHint    string
	Source      commandSource
}

var builtinCommandSpecs = []commandSpec{
	{ID: commandProvider, Name: "/provider", Description: "select a provider and model", ArgsHint: "[name [model]]", Source: commandSourceBuiltin},
	{ID: commandModel, Name: "/model", Description: "select a model for the current provider", ArgsHint: "[model]", Source: commandSourceBuiltin},
	{ID: commandAuth, Name: "/auth", Description: "manage provider authentication", ArgsHint: "[provider]", Source: commandSourceBuiltin},
	{ID: commandAgent, Name: "/agent", Description: "select an agent", ArgsHint: "[name]", Source: commandSourceBuiltin},
	{ID: commandHelp, Name: "/help", Description: "show available commands", Source: commandSourceBuiltin},
}

func commandCatalog(skills []config.Skill) []commandSpec {
	catalog := make([]commandSpec, len(builtinCommandSpecs), len(builtinCommandSpecs)+len(skills))
	copy(catalog, builtinCommandSpecs)
	for _, skill := range skills {
		if err := config.ValidateSkillName(skill.Name); err != nil {
			continue
		}
		argsHint := ""
		if strings.Contains(skill.Template, "$ARGUMENTS") {
			argsHint = "$ARGUMENTS"
		}
		catalog = append(catalog, commandSpec{
			ID:          commandID("skill:" + skill.Name),
			Name:        "/" + sanitizeDisplayData(skill.Name),
			Description: sanitizeDisplayData(skill.Description),
			ArgsHint:    argsHint,
			Source:      commandSourceSkill,
		})
	}
	return catalog
}

func sanitizeDisplayData(value string) string {
	return strings.Map(func(r rune) rune {
		if r <= '\u001f' || r >= '\u007f' && r <= '\u009f' {
			return '\uFFFD'
		}
		return r
	}, value)
}
