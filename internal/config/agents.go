package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Agent is a named persona: a system prompt with optional provider/model
// pins. Defined as markdown files with frontmatter in agents/ folders:
//
//	---
//	description: reviews code for correctness
//	model: gpt-5.5
//	provider: openai
//	---
//	You are a meticulous code reviewer...
//
// The filename (sans .md) is the agent name unless frontmatter sets one.
type Agent struct {
	Name        string
	Description string
	Provider    string
	Model       string
	Prompt      string
}

// Skill is a reusable prompt template invoked as a slash command
// (/name args). $ARGUMENTS in the template is replaced with the command's
// arguments; without the placeholder, arguments are appended.
type Skill struct {
	Name        string
	Description string
	Template    string
}

// LoadAgents reads agents/*.md from the global then project .strike roots;
// a project agent overrides a global one with the same name.
func LoadAgents(workDir string) []Agent {
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
			if _, exists := byName[name]; !exists {
				order = append(order, name)
			}
			byName[name] = Agent{
				Name:        name,
				Description: meta["description"],
				Provider:    meta["provider"],
				Model:       meta["model"],
				Prompt:      body,
			}
		}
	}
	agents := make([]Agent, 0, len(order))
	for _, name := range order {
		agents = append(agents, byName[name])
	}
	return agents
}

// LoadSkills reads skills/*.md from the global then project .strike roots;
// a project skill overrides a global one with the same name.
func LoadSkills(workDir string) []Skill {
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
			if _, exists := byName[name]; !exists {
				order = append(order, name)
			}
			byName[name] = Skill{Name: name, Description: meta["description"], Template: body}
		}
	}
	skills := make([]Skill, 0, len(order))
	for _, name := range order {
		skills = append(skills, byName[name])
	}
	return skills
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
