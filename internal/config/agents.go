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
	"help":             {},
	"keys":             {},
	"memory":           {},
	"issues":           {},
	"session":          {},
	"context":          {},
	"effective-prompt": {},
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

// LoadAgents reads agents/*.md from the global then project .strike roots;
// a project agent overrides a global one with the same name.
func LoadAgents(workDir string) []Agent {
	agents, err := LoadAgentsWithError(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading agents: %v\n", err)
		return nil
	}
	return agents
}

// LoadAgentsWithError merges built-in agents with agents/*.md from the global
// then project .strike roots. Later layers override earlier ones with the same
// name (builtins < global < project). build remains first when present.
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
	for _, dir := range []string{filepath.Join(GlobalRoot(), "agents"), filepath.Join(projectRoot(workDir), "agents")} {
		for _, path := range markdownFiles(dir) {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			meta, body := parseFrontmatter(string(data))
			if body == "" {
				continue
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
			perms, err := parseAgentPermissions(meta)
			if err != nil {
				return nil, fmt.Errorf("load agent %s: %w", path, err)
			}
			if _, exists := byName[name]; !exists {
				order = append(order, name)
			}
			byName[name] = Agent{
				Name:        name,
				Description: meta["description"],
				Provider:    meta["provider"],
				Model:       meta["model"],
				Effort:      effort,
				Prompt:      body,
				Permissions: perms,
			}
		}
	}
	agents := make([]Agent, 0, len(order))
	for _, name := range order {
		agents = append(agents, byName[name])
	}
	return agents, nil
}

// LoadSkills reads skills/*.md. Invalid names are reported clearly on stderr;
// callers that need to handle the error directly can use LoadSkillsWithError.
func LoadSkills(workDir string) []Skill {
	skills, err := LoadSkillsWithError(workDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading skills: %v\n", err)
		return nil
	}
	return skills
}

// LoadSkillsWithError merges built-in shipping skills with skills/*.md from
// the global then project .strike roots. Later layers override earlier ones
// with the same name (builtins < global < project).
func LoadSkillsWithError(workDir string) ([]Skill, error) {
	byName := map[string]Skill{}
	var order []string
	for _, dir := range []string{filepath.Join(GlobalRoot(), "skills"), filepath.Join(projectRoot(workDir), "skills")} {
		for _, path := range markdownFiles(dir) {
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			meta, body := parseFrontmatter(string(data))
			if body == "" {
				continue
			}
			name := strings.TrimSuffix(filepath.Base(path), ".md")
			if meta["name"] != "" {
				name = meta["name"]
			}
			if err := ValidateSkillName(name); err != nil {
				return nil, fmt.Errorf("load skill %s: %w", path, err)
			}
			if _, exists := byName[name]; !exists {
				order = append(order, name)
			}
			byName[name] = Skill{Name: name, Description: meta["description"], Template: body}
		}
	}
	disk := make([]Skill, 0, len(order))
	for _, name := range order {
		disk = append(disk, byName[name])
	}
	return mergeSkills(BuiltinSkills(), disk), nil
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
// Compact keys (permission.<name>: <action>) are collected first, sorted by
// permission name; an optional permissions JSON array is appended after.
func parseAgentPermissions(meta map[string]string) (permission.Ruleset, error) {
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
	sort.Slice(compact, func(i, j int) bool {
		return compact[i].Permission < compact[j].Permission
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
func parseFrontmatter(data string) (map[string]string, string) {
	rest, found := strings.CutPrefix(data, "---\n")
	if !found {
		return nil, strings.TrimSpace(data)
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, strings.TrimSpace(data)
	}
	meta := map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			meta[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	body := rest[end+len("\n---"):]
	if nl := strings.IndexByte(body, '\n'); nl >= 0 {
		body = body[nl+1:]
	} else {
		body = ""
	}
	return meta, strings.TrimSpace(body)
}
