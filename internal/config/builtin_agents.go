package config

import (
	"embed"
	"io/fs"
	"path"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

//go:embed agents/*.md
var builtinAgentFS embed.FS

// BuiltinAgents returns shipping agent personas. build and plan are always
// first (empty Prompt → shared baseline + provider/plan overlays at request
// time). Additional personas load from embedded agents/*.md. User global and
// project agents override same names via LoadAgentsWithError.
func BuiltinAgents() []Agent {
	base := []Agent{
		{
			Name:        "build",
			Description: "Default coding agent. Full tools subject to permission rules.",
		},
		{
			Name:        "plan",
			Description: "Plan mode. Read-only analysis and implementation plans.",
		},
	}
	return mergeAgents(base, loadEmbeddedAgents())
}

func loadEmbeddedAgents() []Agent {
	entries, err := fs.ReadDir(builtinAgentFS, "agents")
	if err != nil {
		return nil
	}
	agents := make([]Agent, 0, len(entries))
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") {
			continue
		}
		data, err := builtinAgentFS.ReadFile(path.Join("agents", ent.Name()))
		if err != nil {
			continue
		}
		meta, body, nestedPerms := parseFrontmatter(string(data))
		if strings.TrimSpace(body) == "" {
			continue
		}
		name := strings.TrimSuffix(ent.Name(), ".md")
		if meta["name"] != "" {
			name = meta["name"]
		}
		if err := ValidateAgentName(name); err != nil {
			continue
		}
		// build/plan overlays are composed in-engine; ignore accidental embeds.
		if name == "build" || name == "plan" {
			continue
		}
		effort, ok := protocol.ParseEffort(meta["effort"])
		if !ok {
			continue
		}
		perms, err := parseAgentPermissions(meta, nestedPerms)
		if err != nil {
			continue
		}
		provider, model := resolveAgentModel(meta["provider"], meta["model"])
		agents = append(agents, Agent{
			Name:        name,
			Description: meta["description"],
			Provider:    provider,
			Model:       model,
			Effort:      effort,
			Prompt:      body,
			Permissions: perms,
		})
	}
	return agents
}

// mergeAgents overlays later agents onto earlier ones by name, preserving
// first-seen order and appending newly introduced names in overlay order.
func mergeAgents(base, overlay []Agent) []Agent {
	if len(overlay) == 0 {
		return append([]Agent(nil), base...)
	}
	if len(base) == 0 {
		return append([]Agent(nil), overlay...)
	}
	byName := make(map[string]Agent, len(base)+len(overlay))
	order := make([]string, 0, len(base)+len(overlay))
	for _, a := range base {
		if _, ok := byName[a.Name]; !ok {
			order = append(order, a.Name)
		}
		byName[a.Name] = a
	}
	for _, a := range overlay {
		if _, ok := byName[a.Name]; !ok {
			order = append(order, a.Name)
		}
		byName[a.Name] = a
	}
	out := make([]Agent, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out
}
