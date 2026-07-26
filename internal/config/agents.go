package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

// Agent is a named persona: a system prompt with optional provider/model/
// effort pins. Defined as markdown files with frontmatter in agents/ folders:
//
//	---
//	description: reviews code for correctness
//	model: gpt-5.5
//	provider: openai
//	effort: high
//	permission.write: deny
//	permission.edit: deny
//	---
//	You are a meticulous code reviewer...
//
// The filename (sans .md) is the agent name unless frontmatter sets one.
type Agent struct {
	Name        string
	Description string
	Provider    string
	Model       string
	Effort      protocol.Effort
	Prompt      string
	Permissions permission.Ruleset
}

// Skill is a reusable prompt template invoked as a slash command
// (/name args). $ARGUMENTS in the template is replaced with the command's
// arguments; without the placeholder, arguments are appended.
type Skill struct {
	Name        string
	Description string
	Template    string
}

var reservedSkillNames = map[string]struct{}{
	"provider":         {},
	"model":            {},
	"effort":           {},
	"auth":             {},
	"agent":            {},
	"fast":             {},
	"vim":              {},
	"md-read":          {},
	"theme":            {},
	"layout":           {},
	"split":            {},
	"compact":          {},
	"fork":             {},
	"undo":             {},
	"rewind":           {},
	"help":             {},
	"keys":             {},
	"memory":           {},
	"issues":           {},
	"session":          {},
	"context":          {},
	"effective-prompt": {},
	"upgrade":          {},
}

// ValidateSkillName rejects names that cannot be represented safely as slash commands.
func ValidateSkillName(name string) error {
	if err := validateConfigIdentifier(name, "skill"); err != nil {
		return err
	}
	if _, reserved := reservedSkillNames[name]; reserved {
		return fmt.Errorf("skill name %q is reserved", name)
	}
	for _, r := range name {
		if r == '/' {
			return fmt.Errorf("skill name %q contains '/'", name)
		}
		if unicode.IsSpace(r) {
			return fmt.Errorf("skill name %q contains whitespace or a control character", name)
		}
	}
	return nil
}

// ValidateAgentName rejects names that cannot be selected or displayed safely.
func ValidateAgentName(name string) error {
	if err := validateConfigIdentifier(name, "agent"); err != nil {
		return err
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("agent name %q has leading or trailing whitespace", name)
	}
	return nil
}

func validateConfigIdentifier(name, kind string) error {
	if name == "" {
		return fmt.Errorf("%s name is empty", kind)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("%s name is not valid UTF-8", kind)
	}
	for _, r := range name {
		if r <= '\u001f' || r >= '\u007f' && r <= '\u009f' {
			detail := "a control character"
			if kind == "skill" {
				detail = "whitespace or a control character"
			}
			return fmt.Errorf("%s name %q contains %s", kind, name, detail)
		}
	}
	return nil
}

// LoadAgents reads agents from discovery roots (see agentDiscoveryRoots).
// A later root overrides an earlier one with the same name.
func LoadAgents(workDir string) []Agent {
	agents, err := LoadAgentsWithError(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading agents: %v\n", err)
		return nil
	}
	return agents
}

// LoadAgentsWithError merges built-in agents with disk agents from all
// discovery roots. Later layers override earlier ones with the same name
// (builtins < global strike < global claude/opencode < project strike <
// project claude/opencode). build remains first when present.
// Strike-native roots fail hard on invalid files; external trees warn and skip.
func LoadAgentsWithError(workDir string) ([]Agent, error) {
	disk, err := loadDiskAgents(workDir)
	if err != nil {
		return nil, err
	}
	return mergeAgents(BuiltinAgents(), disk), nil
}

func loadDiskAgents(workDir string) ([]Agent, error) {
	byName := map[string]Agent{}
	var order []string
	for _, src := range agentDiscoveryRoots(workDir) {
		for _, path := range markdownFiles(src.dir) {
			agent, err := parseAgentFile(path)
			if err != nil {
				if src.strict {
					return nil, err
				}
				fmt.Fprintf(os.Stderr, "loading agents: skip %s: %v\n", path, err)
				continue
			}
			if agent == nil {
				continue
			}
			if _, exists := byName[agent.Name]; !exists {
				order = append(order, agent.Name)
			}
			byName[agent.Name] = *agent
		}
	}
	agents := make([]Agent, 0, len(order))
	for _, name := range order {
		agents = append(agents, byName[name])
	}
	return agents, nil
}

func parseAgentFile(path string) (*Agent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	meta, body, nestedPerms := parseFrontmatter(string(data))
	if body == "" {
		return nil, nil
	}
	name := strings.TrimSuffix(filepath.Base(path), ".md")
	if meta["name"] != "" {
		name = meta["name"]
	}
	if err := ValidateAgentName(name); err != nil {
		return nil, fmt.Errorf("load agent %s: %w", path, err)
	}
	effort, ok := protocol.ParseEffort(meta["effort"])
	if !ok {
		return nil, fmt.Errorf("load agent %s: unknown effort %q", path, meta["effort"])
	}
	perms, err := parseAgentPermissions(meta, nestedPerms)
	if err != nil {
		return nil, fmt.Errorf("load agent %s: %w", path, err)
	}
	provider, model := resolveAgentModel(meta["provider"], meta["model"])
	return &Agent{
		Name:        name,
		Description: meta["description"],
		Provider:    provider,
		Model:       model,
		Effort:      effort,
		Prompt:      body,
		Permissions: perms,
	}, nil
}

// resolveAgentModel maps strike provider+model pins and OpenCode-style
// "provider/model" model ids. Explicit provider wins over a slash prefix.
func resolveAgentModel(provider, model string) (string, string) {
	if provider == "" && strings.Contains(model, "/") {
		p, m, ok := strings.Cut(model, "/")
		if ok && p != "" && m != "" {
			return p, m
		}
	}
	return provider, model
}

// LoadSkills reads skills from discovery roots (see skillDiscoveryRoots).
// Invalid names are reported clearly on stderr; callers that need to handle
// the error directly can use LoadSkillsWithError.
func LoadSkills(workDir string) []Skill {
	skills, err := LoadSkillsWithError(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading skills: %v\n", err)
		return nil
	}
	return skills
}

// LoadSkillsWithError merges built-in shipping skills with disk skills from
// all discovery roots. Later layers override earlier ones with the same name
// (builtins < global strike < global claude/opencode < project strike <
// project claude/opencode). Strike-native roots fail hard on invalid files;
// external trees warn and skip.
func LoadSkillsWithError(workDir string) ([]Skill, error) {
	byName := map[string]Skill{}
	var order []string
	for _, src := range skillDiscoveryRoots(workDir) {
		for _, path := range markdownSkillFiles(src.dir) {
			skill, err := parseSkillFile(path)
			if err != nil {
				if src.strict {
					return nil, err
				}
				fmt.Fprintf(os.Stderr, "loading skills: skip %s: %v\n", path, err)
				continue
			}
			if skill == nil {
				continue
			}
			if _, exists := byName[skill.Name]; !exists {
				order = append(order, skill.Name)
			}
			byName[skill.Name] = *skill
		}
	}
	disk := make([]Skill, 0, len(order))
	for _, name := range order {
		disk = append(disk, byName[name])
	}
	return mergeSkills(BuiltinSkills(), disk), nil
}

func parseSkillFile(path string) (*Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil
	}
	meta, body, _ := parseFrontmatter(string(data))
	if body == "" {
		return nil, nil
	}
	name := skillNameFromPath(path)
	if meta["name"] != "" {
		name = meta["name"]
	}
	if err := ValidateSkillName(name); err != nil {
		return nil, fmt.Errorf("load skill %s: %w", path, err)
	}
	return &Skill{Name: name, Description: meta["description"], Template: body}, nil
}

// Render substitutes the skill's arguments into its template.
func (s Skill) Render(args string) string {
	if strings.Contains(s.Template, "$ARGUMENTS") {
		return strings.ReplaceAll(s.Template, "$ARGUMENTS", args)
	}
	if args != "" {
		return s.Template + "\n\nArguments: " + args
	}
	return s.Template
}

// parseAgentPermissions builds a ruleset from agent frontmatter.
// Compact keys (permission.<name>: <action>) and nested Claude/OpenCode
// permission maps are collected first, sorted by permission name then pattern;
// an optional permissions JSON array is appended after.
func parseAgentPermissions(meta map[string]string, nested permission.Ruleset) (permission.Ruleset, error) {
	var compact permission.Ruleset
	for key, value := range meta {
		name, ok := strings.CutPrefix(key, "permission.")
		if !ok {
			continue
		}
		if name == "" {
			continue
		}
		compact = append(compact, permission.Rule{
			Permission: name,
			Pattern:    "*",
			Action:     permission.Action(value),
		})
	}
	compact = append(compact, nested...)
	sort.SliceStable(compact, func(i, j int) bool {
		if compact[i].Permission != compact[j].Permission {
			return compact[i].Permission < compact[j].Permission
		}
		return compact[i].Pattern < compact[j].Pattern
	})

	var rs permission.Ruleset
	rs = append(rs, compact...)
	if raw := meta["permissions"]; raw != "" {
		var extra permission.Ruleset
		if err := json.Unmarshal([]byte(raw), &extra); err != nil {
			return nil, fmt.Errorf("permissions: %w", err)
		}
		rs = append(rs, extra...)
	}
	if err := permission.ValidateRuleset(rs); err != nil {
		return nil, err
	}
	return rs, nil
}

func markdownFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files
}

// parseFrontmatter splits "---\nkey: value\n---\nbody". Returns nil meta
// and the whole input when there is no frontmatter block.
// Nested Claude/OpenCode permission blocks become nestedPerms; other nested
// maps and unknown keys are ignored. Flat keys remain in meta.
func parseFrontmatter(data string) (meta map[string]string, body string, nestedPerms permission.Ruleset) {
	rest, found := strings.CutPrefix(data, "---\n")
	if !found {
		return nil, strings.TrimSpace(data), nil
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, strings.TrimSpace(data), nil
	}
	meta = map[string]string{}
	lines := strings.Split(rest[:end], "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := countLeadingSpaces(line)
		trimmed := strings.TrimSpace(line)
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if indent == 0 && key == "permission" && value == "" {
			var more int
			nestedPerms, more = parseNestedPermissionBlock(lines[i+1:])
			i += more
			continue
		}
		if indent > 0 {
			// Nested under an unrecognized parent — skip.
			continue
		}
		meta[key] = value
	}
	body = rest[end+len("\n---"):]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	} else {
		body = ""
	}
	return meta, strings.TrimSpace(body), nestedPerms
}

// parseNestedPermissionBlock reads indented permission entries after
// "permission:". Simple "tool: action" and nested "tool:\n  pattern: action"
// forms are mapped; unknown structure is skipped. Returns rules and how many
// lines were consumed.
func parseNestedPermissionBlock(lines []string) (permission.Ruleset, int) {
	var rs permission.Ruleset
	consumed := 0
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			consumed++
			continue
		}
		indent := countLeadingSpaces(line)
		if indent == 0 {
			break
		}
		consumed++
		if indent != 2 && indent != 1 {
			// Only one level of nesting under permission is recognized beyond
			// the tool key; deeper/odd indentation is skipped.
		}
		trimmed := strings.TrimSpace(line)
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		tool := strings.TrimSpace(key)
		tool = strings.Trim(tool, `"'`)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if tool == "" {
			continue
		}
		if value != "" {
			if permission.ValidAction(permission.Action(value)) {
				rs = append(rs, permission.Rule{
					Permission: tool,
					Pattern:    "*",
					Action:     permission.Action(value),
				})
			}
			continue
		}
		// Nested pattern map under this tool.
		for j := i + 1; j < len(lines); j++ {
			sub := lines[j]
			if strings.TrimSpace(sub) == "" {
				consumed++
				i = j
				continue
			}
			subIndent := countLeadingSpaces(sub)
			if subIndent <= indent {
				break
			}
			consumed++
			i = j
			st := strings.TrimSpace(sub)
			pk, pv, ok := strings.Cut(st, ":")
			if !ok {
				continue
			}
			pattern := strings.TrimSpace(pk)
			pattern = strings.Trim(pattern, `"'`)
			action := strings.TrimSpace(pv)
			action = strings.Trim(action, `"'`)
			if pattern == "" || !permission.ValidAction(permission.Action(action)) {
				continue
			}
			rs = append(rs, permission.Rule{
				Permission: tool,
				Pattern:    pattern,
				Action:     permission.Action(action),
			})
		}
	}
	return rs, consumed
}

func countLeadingSpaces(s string) int {
	n := 0
	for _, r := range s {
		if r == ' ' {
			n++
			continue
		}
		if r == '\t' {
			n += 2
			continue
		}
		break
	}
	return n
}
