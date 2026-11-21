package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jonathanung/strike/harness/tool"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jonathanung/strike/pkg/redact"
)

// SkillInfo is the tool-facing skill payload.
type SkillInfo struct {
	Name        string
	Description string
	Template    string
	// Path is the absolute skill file path (empty for builtins).
	Path string
}

// SkillLoader can refresh the skill catalog without process restart.
// Optional: when nil, the tool uses a fixed snapshot.
type SkillLoader interface {
	// Skills returns the current catalog snapshot.
	Skills() []SkillInfo
	// Reload re-reads skills from disk. May be nil-op when unsupported.
	Reload() error
}

type skillTool struct {
	mu     sync.Mutex
	skills []SkillInfo
	loader SkillLoader
	desc   string
	// session, when set, supplies extra skills for one root session (Powers
	// packs active there, #1943). They are listed and loadable only in that
	// session; a base skill of the same name always wins.
	session func(rootSession string) []SkillInfo
}

// SessionSkillSetter is implemented by the skill tool. SetSessionSkills wires
// the per-session skill source after the registry is built (Powers are
// assembled later). Nil clears it.
type SessionSkillSetter interface {
	SetSessionSkills(fn func(rootSession string) []SkillInfo)
}

// SetSessionSkills sets the per-session skill source.
func (t *skillTool) SetSessionSkills(fn func(rootSession string) []SkillInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.session = fn
}

// forSession returns the base catalog plus skills supplied for rootSession,
// skipping any whose name the base catalog already has.
func (t *skillTool) forSession(rootSession string) []SkillInfo {
	base := t.snapshot()
	t.mu.Lock()
	fn := t.session
	t.mu.Unlock()
	if fn == nil || strings.TrimSpace(rootSession) == "" {
		return base
	}
	extra := fn(rootSession)
	if len(extra) == 0 {
		return base
	}
	seen := make(map[string]bool, len(base)+len(extra))
	for _, s := range base {
		seen[s.Name] = true
	}
	for _, s := range extra {
		if s.Name == "" || seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		base = append(base, s)
	}
	return base
}

// DescriptionFor implements tool.SessionDescriber: the description lists the
// skills available in rootSession, Powers skills included.
func (t *skillTool) DescriptionFor(rootSession string) string {
	t.mu.Lock()
	fn := t.session
	t.mu.Unlock()
	if fn == nil {
		return ""
	}
	return skillDescription(t.forSession(rootSession))
}

// NewSkill returns a tool that loads named skill templates into the conversation.
// The skills slice is copied so the caller cannot mutate the tool's catalog later.
func NewSkill(skills []SkillInfo) tool.Tool {
	return NewSkillWithLoader(skills, nil)
}

// NewSkillWithLoader wires an optional reloadable catalog.
func NewSkillWithLoader(skills []SkillInfo, loader SkillLoader) tool.Tool {
	cp := make([]SkillInfo, len(skills))
	copy(cp, skills)
	t := &skillTool{skills: cp, loader: loader}
	t.rebuildDesc()
	return t
}

func (t *skillTool) snapshot() []SkillInfo {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.loader != nil {
		if err := t.loader.Reload(); err == nil {
			s := t.loader.Skills()
			t.skills = make([]SkillInfo, len(s))
			copy(t.skills, s)
			t.rebuildDescLocked()
		}
	}
	out := make([]SkillInfo, len(t.skills))
	copy(out, t.skills)
	return out
}

func (t *skillTool) rebuildDesc() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rebuildDescLocked()
}

func (t *skillTool) rebuildDescLocked() {
	t.desc = skillDescription(t.skills)
}

func skillDescription(skills []SkillInfo) string {
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Name)
	}
	var list string
	if len(names) == 0 {
		list = "(none loaded)"
	} else {
		list = strings.Join(names, ", ")
	}
	return fmt.Sprintf(`Load a specialized skill when the task matches one of the available skills.

Use this tool to inject the skill's instructions into the current conversation. The output may contain detailed workflow guidance. Optional resource paths load adjacent files under the skill's source root (traversal-protected).

Available skills: %s

The skill name must match one of the available skills. Optional arguments are substituted for $ARGUMENTS in the template (or appended if the placeholder is missing).`, list)
}

func (t *skillTool) Name() string { return "skill" }

func (t *skillTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectRead, tool.IdempotencySafeRetry)
}

func (t *skillTool) Description() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.desc
}

func (t *skillTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string", "description": "The name of the skill from available skills"},
			"arguments": {"type": "string", "description": "Optional arguments substituted for $ARGUMENTS in the skill template"},
			"resource": {"type": "string", "description": "Optional relative path to an adjacent file under the skill source root (no .. escape)"}
		},
		"required": ["name"]
	}`)
}

type skillArgs struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Resource  string `json:"resource"`
}

func (t *skillTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	var a skillArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Name) == "" {
		return tool.Result{}, fmt.Errorf("name is required")
	}

	var root string
	if tc != nil {
		root = tc.RootSessionID
		if root == "" {
			root = tc.SessionID
		}
	}
	skills := t.forSession(root)
	var found *SkillInfo
	names := make([]string, 0, len(skills))
	for i := range skills {
		names = append(names, skills[i].Name)
		if skills[i].Name == a.Name {
			found = &skills[i]
		}
	}
	if found == nil {
		if len(names) == 0 {
			return tool.Result{}, fmt.Errorf("unknown skill %q (no skills loaded)", a.Name)
		}
		return tool.Result{}, fmt.Errorf("unknown skill %q (available: %s)", a.Name, strings.Join(names, ", "))
	}

	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "skill",
		Patterns:   []string{a.Name},
		Always:     []string{a.Name},
	}); err != nil {
		return tool.Result{}, err
	}

	rendered := renderSkillTemplate(found.Template, a.Arguments)
	out := fmt.Sprintf("# Skill: %s\n\n%s", found.Name, rendered)

	if res := strings.TrimSpace(a.Resource); res != "" {
		body, err := readAdjacentSkillResource(*found, res)
		if err != nil {
			return tool.Result{}, fmt.Errorf("skill resource: %w", err)
		}
		out += fmt.Sprintf("\n\n## Resource: %s\n\n%s", res, redact.String(string(body)))
	}

	meta, _ := json.Marshal(map[string]any{"name": found.Name, "resource": a.Resource})
	return tool.Result{
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

// readAdjacentSkillResource enforces the same root/traversal rules as config.ResolveSkillResource.
const maxSkillResourceBytes = 256 << 10

func readAdjacentSkillResource(sk SkillInfo, rel string) ([]byte, error) {
	root := skillSourceRoot(sk.Path)
	if root == "" {
		return nil, fmt.Errorf("skill %q has no source root", sk.Name)
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return nil, fmt.Errorf("empty resource path")
	}
	if filepath.IsAbs(rel) {
		return nil, fmt.Errorf("absolute resource paths are not allowed")
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("resource path escapes skill root")
	}
	full := filepath.Join(root, clean)
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return nil, err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	sep := string(filepath.Separator)
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+sep) {
		return nil, fmt.Errorf("resource path escapes skill root")
	}
	// Use os.ReadFile via open - need import os
	return readFileBounded(fullAbs, maxSkillResourceBytes)
}

func skillSourceRoot(path string) string {
	p := strings.TrimSpace(path)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	base := filepath.Base(abs)
	dir := filepath.Dir(abs)
	if strings.EqualFold(base, "SKILL.md") || strings.EqualFold(base, "skill.md") {
		return dir
	}
	return dir
}

func readFileBounded(path string, max int64) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("resource is a directory")
	}
	if fi.Size() > max {
		return nil, fmt.Errorf("resource exceeds %d bytes", max)
	}
	return os.ReadFile(path)
}
