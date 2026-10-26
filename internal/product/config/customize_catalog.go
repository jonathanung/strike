package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/internal/integrate/plugin"
	"github.com/jonathanung/strike/pkg/redact"
)

const (
	scopeGlobal    = "global"
	scopeWorkspace = "workspace"
)

// Customize origins are the layers the Customize page shows.
const (
	OriginBuiltin   = "builtin"
	OriginGlobal    = "global"
	OriginWorkspace = "workspace"
	OriginPlugin    = "plugin"
)

// SecretKeep is the only env/header value the Customize API returns.
// Sending it back on save keeps the value already on disk.
const SecretKeep = "••••"

// CustomizeCatalog is the read model for the Customize page.
// Env values, header values, and other secrets are never included.
type CustomizeCatalog struct {
	WorkDir    string            `json:"workDir,omitempty"`
	Skills     []CustomizeSkill  `json:"skills"`
	Agents     []CustomizeAgent  `json:"agents"`
	MCP        []CustomizeMCP    `json:"mcp"`
	Plugins    []CustomizePlugin `json:"plugins"`
	Hooks      []CustomizeHook   `json:"hooks"`
	Rules      []CustomizeRule   `json:"rules"`
	Roster     CustomizeRoster   `json:"roster"`
	HookEvents []string          `json:"hookEvents"`
	Warnings   []string          `json:"warnings,omitempty"`
}

// CustomizeSkill is one winning skill ($skill / slash name).
type CustomizeSkill struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Origin      string `json:"origin"`
	Scope       string `json:"scope,omitempty"`
	Path        string `json:"path,omitempty"`
	PluginID    string `json:"pluginId,omitempty"`
	Editable    bool   `json:"editable"`
	Overrides   string `json:"overrides,omitempty"`
}

// CustomizeAgent is one winning agent persona.
type CustomizeAgent struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Origin      string   `json:"origin"`
	Scope       string   `json:"scope,omitempty"`
	Path        string   `json:"path,omitempty"`
	PluginID    string   `json:"pluginId,omitempty"`
	Editable    bool     `json:"editable"`
	Overrides   string   `json:"overrides,omitempty"`
	Model       string   `json:"model,omitempty"`
	Provider    string   `json:"provider,omitempty"`
	Effort      string   `json:"effort,omitempty"`
	Harness     string   `json:"harness,omitempty"`
	Handoff     string   `json:"handoff,omitempty"`
	Permissions []string `json:"permissions,omitempty"`
}

// CustomizeMCP is one server definition. Env and header values are omitted.
type CustomizeMCP struct {
	Name      string   `json:"name"`
	Origin    string   `json:"origin"`
	Scope     string   `json:"scope,omitempty"`
	Path      string   `json:"path,omitempty"`
	PluginID  string   `json:"pluginId,omitempty"`
	Editable  bool     `json:"editable"`
	Active    bool     `json:"active"`
	Type      string   `json:"type,omitempty"`
	Command   string   `json:"command,omitempty"`
	URL       string   `json:"url,omitempty"`
	EnvKeys   []string `json:"envKeys,omitempty"`
	State     string   `json:"state,omitempty"`
	Error     string   `json:"error,omitempty"`
	ToolCount int      `json:"toolCount,omitempty"`
	Tools     []string `json:"tools,omitempty"`
}

// CustomizePlugin is an installed plugin. Package files are not edited here.
type CustomizePlugin struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Scope     string `json:"scope,omitempty"`
	Origin    string `json:"origin"`
	Enabled   bool   `json:"enabled"`
	Status    string `json:"status,omitempty"`
	Agents    int    `json:"agents"`
	Skills    int    `json:"skills"`
	Hooks     int    `json:"hooks"`
	MCP       int    `json:"mcp"`
	LoadError string `json:"loadError,omitempty"`
	Editable  bool   `json:"editable"`
}

// CustomizeHook is one hook entry in a config file or a plugin manifest.
type CustomizeHook struct {
	Scope     string `json:"scope,omitempty"`
	Origin    string `json:"origin"`
	Index     int    `json:"index"`
	Event     string `json:"event,omitempty"`
	Matcher   string `json:"matcher,omitempty"`
	Action    string `json:"action,omitempty"`
	Message   string `json:"message,omitempty"`
	Command   string `json:"command,omitempty"`
	TimeoutMs int    `json:"timeoutMs,omitempty"`
	PluginID  string `json:"pluginId,omitempty"`
	Editable  bool   `json:"editable"`
	BaseHash  string `json:"baseHash,omitempty"`
	Ignored   bool   `json:"ignored,omitempty"`
}

// CustomizeRule is one instruction file (AGENTS.md / CLAUDE.md).
type CustomizeRule struct {
	Name     string `json:"name"`
	Origin   string `json:"origin"`
	Scope    string `json:"scope,omitempty"`
	Path     string `json:"path,omitempty"`
	Excerpt  string `json:"excerpt,omitempty"`
	Editable bool   `json:"editable"`
	Exists   bool   `json:"exists"`
	// Abs is the on-disk path. It is not sent to the client.
	Abs string `json:"-"`
}

// CustomizeRoster is the session stall threshold and handoff gate.
// budget.stall on a live child is the Activity flag once StallAfterS is exceeded.
type CustomizeRoster struct {
	HandoffRequired   bool   `json:"handoffRequired"`
	HandoffSource     string `json:"handoffSource"`
	GlobalHandoff     string `json:"globalHandoff"`
	WorkspaceHandoff  string `json:"workspaceHandoff"`
	StallAfterS       int    `json:"stallAfterS"`
	StallSource       string `json:"stallSource"`
	GlobalStall       int    `json:"globalStall"`
	WorkspaceStall    int    `json:"workspaceStall"`
	GlobalBaseHash    string `json:"globalBaseHash"`
	WorkspaceBaseHash string `json:"workspaceBaseHash"`
	Note              string `json:"note,omitempty"`
}

// MCPStatusView is connection state joined onto catalog rows by name.
// The customize API does not import the host package.
type MCPStatusView struct {
	Name      string
	State     string
	Error     string
	ToolCount int
	Tools     []string
}

// CustomizeCatalog reads the files the agent already loads.
// workDir is the daemon working directory (workspace scope).
func LoadCustomize(workDir string) (*CustomizeCatalog, error) {
	cat := &CustomizeCatalog{
		WorkDir:    workDir,
		Skills:     []CustomizeSkill{},
		Agents:     []CustomizeAgent{},
		MCP:        []CustomizeMCP{},
		Plugins:    []CustomizePlugin{},
		Hooks:      []CustomizeHook{},
		Rules:      []CustomizeRule{},
		HookEvents: append([]string(nil), tool.KnownLifecycleEvents...),
	}
	var err error
	if cat.Skills, err = catalogSkills(workDir); err != nil {
		return nil, err
	}
	if cat.Agents, err = catalogAgents(workDir); err != nil {
		return nil, err
	}
	mcpRows, mcpWarn := catalogMCP(workDir)
	cat.MCP = mcpRows
	cat.Warnings = append(cat.Warnings, mcpWarn...)
	plugins, hooks, plugWarn := catalogPlugins(workDir)
	cat.Plugins = plugins
	cat.Hooks = append(cat.Hooks, hooks...)
	cat.Warnings = append(cat.Warnings, plugWarn...)
	fileHooks, hookWarn := catalogFileHooks(workDir)
	cat.Hooks = append(fileHooks, cat.Hooks...)
	cat.Warnings = append(cat.Warnings, hookWarn...)
	cat.Rules = catalogRules(workDir)
	roster, rosterWarn := catalogRoster(workDir)
	cat.Roster = roster
	cat.Warnings = append(cat.Warnings, rosterWarn...)
	if cat.Skills == nil {
		cat.Skills = []CustomizeSkill{}
	}
	if cat.Agents == nil {
		cat.Agents = []CustomizeAgent{}
	}
	if cat.MCP == nil {
		cat.MCP = []CustomizeMCP{}
	}
	if cat.Plugins == nil {
		cat.Plugins = []CustomizePlugin{}
	}
	if cat.Hooks == nil {
		cat.Hooks = []CustomizeHook{}
	}
	if cat.Rules == nil {
		cat.Rules = []CustomizeRule{}
	}
	if cat.HookEvents == nil {
		cat.HookEvents = []string{}
	}
	return cat, nil
}

// AttachMCPStatus copies connection state onto active rows with the same name.
func AttachMCPStatus(servers []CustomizeMCP, statuses []MCPStatusView) {
	byName := map[string]MCPStatusView{}
	for _, st := range statuses {
		byName[st.Name] = st
	}
	for i := range servers {
		st, ok := byName[servers[i].Name]
		if !ok || !servers[i].Active {
			continue
		}
		servers[i].State = st.State
		servers[i].Error = redact.String(st.Error)
		servers[i].ToolCount = st.ToolCount
		servers[i].Tools = append([]string(nil), st.Tools...)
	}
}

func catalogSkills(workDir string) ([]CustomizeSkill, error) {
	loaded, err := LoadSkillsWithError(workDir)
	if err != nil {
		return nil, err
	}
	plugins := DiscoverPlugins(workDir).Plugins
	builtin := map[string]struct{}{}
	for _, s := range BuiltinSkills() {
		builtin[s.Name] = struct{}{}
	}
	out := make([]CustomizeSkill, 0, len(loaded))
	for _, s := range loaded {
		item := CustomizeSkill{Name: s.Name, Description: s.Description}
		fillSkillOrigin(&item, s, workDir, plugins)
		if _, ok := builtin[s.Name]; ok && item.Origin != OriginBuiltin {
			item.Overrides = OriginBuiltin
		}
		out = append(out, item)
	}
	return out, nil
}

func fillSkillOrigin(item *CustomizeSkill, s Skill, workDir string, plugins []plugin.Plugin) {
	if s.Builtin || s.Path == "" {
		item.Origin = OriginBuiltin
		return
	}
	item.Path = displayPath(s.Path, workDir)
	if id := pluginIDForPath(s.Path, plugins); id != "" {
		item.Origin = OriginPlugin
		item.PluginID = id
		return
	}
	if underDir(s.Path, workDir) {
		item.Origin = OriginWorkspace
		item.Scope = scopeWorkspace
		item.Editable = underStrikeKind(s.Path, projectRoot(workDir), "skills")
		return
	}
	item.Origin = OriginGlobal
	item.Scope = scopeGlobal
	item.Editable = underStrikeKind(s.Path, GlobalRoot(), "skills")
}

func catalogAgents(workDir string) ([]CustomizeAgent, error) {
	disk, paths, pluginIDs, err := loadDiskAgentsTracked(workDir)
	if err != nil {
		return nil, err
	}
	plugins := DiscoverPlugins(workDir).Plugins
	builtin := map[string]struct{}{}
	for _, a := range BuiltinAgents() {
		builtin[a.Name] = struct{}{}
	}
	merged := mergeAgents(BuiltinAgents(), disk)
	out := make([]CustomizeAgent, 0, len(merged))
	for _, a := range merged {
		item := CustomizeAgent{
			Name:        a.Name,
			Description: a.Description,
			Model:       a.Model,
			Provider:    a.Provider,
			Effort:      string(a.Effort),
			Harness:     a.Harness,
			Handoff:     handoffLabel(a.ChildHandoffRequired),
			Permissions: permissionLines(a.Permissions),
		}
		path := paths[a.Name]
		if path == "" {
			item.Origin = OriginBuiltin
		} else {
			item.Path = displayPath(path, workDir)
			id := pluginIDs[a.Name]
			if id == "" {
				id = pluginIDForPath(path, plugins)
			}
			switch {
			case id != "":
				item.Origin = OriginPlugin
				item.PluginID = id
			case underDir(path, workDir):
				item.Origin = OriginWorkspace
				item.Scope = scopeWorkspace
				item.Editable = underStrikeKind(path, projectRoot(workDir), "agents")
			default:
				item.Origin = OriginGlobal
				item.Scope = scopeGlobal
				item.Editable = underStrikeKind(path, GlobalRoot(), "agents")
			}
		}
		if _, ok := builtin[a.Name]; ok && item.Origin != OriginBuiltin {
			item.Overrides = OriginBuiltin
		}
		out = append(out, item)
	}
	return out, nil
}

func loadDiskAgentsTracked(workDir string) ([]Agent, map[string]string, map[string]string, error) {
	byName := map[string]Agent{}
	paths := map[string]string{}
	pluginIDs := map[string]string{}
	var order []string
	put := func(a Agent, path, pluginID string) {
		if _, exists := byName[a.Name]; !exists {
			order = append(order, a.Name)
		}
		byName[a.Name] = a
		paths[a.Name] = path
		if pluginID != "" {
			pluginIDs[a.Name] = pluginID
		} else {
			delete(pluginIDs, a.Name)
		}
	}
	globalRoots, projectRoots := splitAgentRoots(workDir)
	loadRoot := func(src agentSource) error {
		for _, path := range markdownFiles(src.dir) {
			agent, err := parseAgentFile(path)
			if err != nil {
				if src.strict {
					return err
				}
				continue
			}
			if agent == nil {
				continue
			}
			put(*agent, path, "")
		}
		return nil
	}
	for _, src := range globalRoots {
		if err := loadRoot(src); err != nil {
			return nil, nil, nil, err
		}
	}
	pres := DiscoverPlugins(workDir)
	applyTrackedPlugins(pres.Plugins, plugin.ScopeGlobal, put)
	for _, src := range projectRoots {
		if err := loadRoot(src); err != nil {
			return nil, nil, nil, err
		}
	}
	applyTrackedPlugins(pres.Plugins, plugin.ScopeProject, put)
	agents := make([]Agent, 0, len(order))
	for _, name := range order {
		agents = append(agents, byName[name])
	}
	return agents, paths, pluginIDs, nil
}

func applyTrackedPlugins(plugins []plugin.Plugin, scope plugin.Scope, put func(Agent, string, string)) {
	for _, p := range plugins {
		if p.Source != scope {
			continue
		}
		for _, ref := range p.Agents {
			agent, err := parseAgentFile(ref.AbsPath)
			if err != nil || agent == nil {
				continue
			}
			put(*agent, ref.AbsPath, p.ID)
		}
	}
}

func catalogMCP(workDir string) ([]CustomizeMCP, []string) {
	var warnings []string
	var out []CustomizeMCP
	globalPath := GlobalMCPFilePath()
	globalServers, _ := readMCPLayer(globalPath, &warnings)
	projectPath := ProjectMCPFilePath(workDir)
	projectExists := projectPath != "" && customizeFileExists(projectPath)
	projectServers, _ := readMCPLayer(projectPath, &warnings)
	// A present project file replaces the global server map, including {}.
	projectWins := projectExists
	for _, name := range sortedKeys(globalServers) {
		out = append(out, mcpRow(name, globalServers[name], scopeGlobal, OriginGlobal, globalPath, !projectWins, true))
	}
	if projectExists {
		for _, name := range sortedKeys(projectServers) {
			out = append(out, mcpRow(name, projectServers[name], scopeWorkspace, OriginWorkspace, projectPath, true, true))
		}
	}
	activeNames := map[string]struct{}{}
	for _, row := range out {
		if row.Active {
			activeNames[row.Name] = struct{}{}
		}
	}
	installed, _, err := plugin.ListInstalled(plugin.ListOptions{WorkDir: workDir})
	if err != nil {
		warnings = append(warnings, "plugins: "+redact.String(err.Error()))
		return out, warnings
	}
	for _, inst := range installed {
		if !inst.Enabled || inst.Manifest == nil {
			continue
		}
		for i, raw := range inst.Manifest.Contributions.MCP {
			row, ok := pluginMCPRow(inst, i, raw)
			if !ok {
				continue
			}
			if _, taken := activeNames[row.Name]; taken {
				row.Active = false
			} else {
				activeNames[row.Name] = struct{}{}
			}
			out = append(out, row)
		}
	}
	return out, warnings
}

func readMCPLayer(path string, warnings *[]string) (map[string]MCPServer, bool) {
	if path == "" || !customizeFileExists(path) {
		return nil, false
	}
	mc, err := ReadMCPFile(path)
	if err != nil {
		*warnings = append(*warnings, "mcp "+displayPath(path, "")+": "+redact.String(err.Error()))
		return nil, false
	}
	if mc.Servers == nil {
		return map[string]MCPServer{}, true
	}
	return mc.Servers, true
}

func mcpRow(name string, srv MCPServer, scope, origin, path string, active, editable bool) CustomizeMCP {
	typ := strings.TrimSpace(srv.Type)
	if typ == "" {
		if srv.URL != "" {
			typ = "http"
		} else {
			typ = "stdio"
		}
	}
	row := CustomizeMCP{
		Name:     name,
		Origin:   origin,
		Scope:    scope,
		Path:     displayPath(path, ""),
		Editable: editable,
		Active:   active,
		Type:     typ,
		Command:  redact.String(srv.Command),
		EnvKeys:  mapKeys(srv.Env),
	}
	if redact.Findings(srv.URL) != nil {
		row.URL = ""
	} else {
		row.URL = srv.URL
	}
	return row
}

func pluginMCPRow(inst plugin.InstalledPlugin, index int, raw []byte) (CustomizeMCP, bool) {
	var body struct {
		Name    string            `json:"name"`
		Type    string            `json:"type"`
		Command string            `json:"command"`
		URL     string            `json:"url"`
		Env     map[string]string `json:"env"`
	}
	if err := unmarshalJSONC(raw, &body); err != nil {
		return CustomizeMCP{}, false
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = fmt.Sprintf("%s-%d", inst.ID, index)
	}
	scope := scopeGlobal
	origin := OriginPlugin
	if inst.Scope == plugin.ScopeProject {
		scope = scopeWorkspace
	}
	row := mcpRow(name, MCPServer{Type: body.Type, Command: body.Command, URL: body.URL, Env: body.Env}, scope, origin, inst.Root, true, false)
	row.PluginID = inst.ID
	row.Editable = false
	return row, true
}

func catalogPlugins(workDir string) ([]CustomizePlugin, []CustomizeHook, []string) {
	installed, _, err := plugin.ListInstalled(plugin.ListOptions{WorkDir: workDir})
	if err != nil {
		return []CustomizePlugin{}, nil, []string{"plugins: " + redact.String(err.Error())}
	}
	out := make([]CustomizePlugin, 0, len(installed))
	var hooks []CustomizeHook
	for _, inst := range installed {
		scope := scopeGlobal
		if inst.Scope == plugin.ScopeProject {
			scope = scopeWorkspace
		}
		item := CustomizePlugin{
			ID:        inst.ID,
			Name:      inst.Name,
			Scope:     scope,
			Origin:    OriginPlugin,
			Enabled:   inst.Enabled,
			LoadError: redact.String(inst.LoadError),
			Editable:  true, // enable / disable only
		}
		if inst.Name == "" {
			item.Name = inst.ID
		}
		switch {
		case inst.LoadError != "":
			item.Status = "invalid"
		case inst.Enabled:
			item.Status = "enabled"
		default:
			item.Status = "disabled"
		}
		if inst.Manifest != nil {
			c := inst.Manifest.Contributions
			item.Agents = len(c.Agents)
			item.Skills = len(c.Skills)
			item.Hooks = len(c.Hooks)
			item.MCP = len(c.MCP)
			for i, raw := range c.Hooks {
				hooks = append(hooks, pluginHookRow(inst, scope, i, raw))
			}
		}
		out = append(out, item)
	}
	return out, hooks, nil
}

func pluginHookRow(inst plugin.InstalledPlugin, scope string, index int, raw []byte) CustomizeHook {
	var h Hook
	row := CustomizeHook{
		Scope:    scope,
		Origin:   OriginPlugin,
		Index:    index,
		PluginID: inst.ID,
		Editable: false,
	}
	if err := unmarshalJSONC(raw, &h); err != nil {
		row.Ignored = true
		row.Message = "ignored: invalid hook"
		return row
	}
	row.Event = h.Event
	row.Matcher = h.Matcher
	row.Action = h.Action
	row.Message = redact.String(h.Message)
	row.Command = redact.String(h.Command)
	row.TimeoutMs = h.TimeoutMs
	if !h.IsShell() && !h.IsRule() {
		row.Ignored = true
	}
	return row
}

func catalogFileHooks(workDir string) ([]CustomizeHook, []string) {
	var warnings []string
	var out []CustomizeHook
	if rows, warn := hooksInFile(GlobalPath(), scopeGlobal, OriginGlobal); warn != "" {
		warnings = append(warnings, warn)
	} else {
		out = append(out, rows...)
	}
	if workDir != "" {
		if rows, warn := hooksInFile(ProjectPath(workDir), scopeWorkspace, OriginWorkspace); warn != "" {
			warnings = append(warnings, warn)
		} else {
			out = append(out, rows...)
		}
	}
	return out, warnings
}

func hooksInFile(path, scope, origin string) ([]CustomizeHook, string) {
	if path == "" || !customizeFileExists(path) {
		return nil, ""
	}
	cfg, err := read(path)
	if err != nil {
		return nil, "hooks " + displayPath(path, "") + ": " + redact.String(err.Error())
	}
	hash, _ := fileHash(path)
	out := make([]CustomizeHook, 0, len(cfg.Hooks))
	for i, h := range cfg.Hooks {
		secret := hookHasSecret(h)
		out = append(out, CustomizeHook{
			Scope:     scope,
			Origin:    origin,
			Index:     i,
			Event:     h.Event,
			Matcher:   redact.String(h.Matcher),
			Action:    h.Action,
			Message:   redact.String(h.Message),
			Command:   redact.String(h.Command),
			TimeoutMs: h.TimeoutMs,
			Editable:  !secret,
			BaseHash:  hash,
		})
	}
	return out, ""
}

func catalogRules(workDir string) []CustomizeRule {
	var out []CustomizeRule
	out = append(out, ruleRow(globalAgentsPath(), "Global rules", OriginGlobal, scopeGlobal, true))
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		claude := filepath.Join(home, ".claude", "CLAUDE.md")
		if customizeFileExists(claude) {
			out = append(out, ruleRow(claude, "CLAUDE.md", OriginGlobal, scopeGlobal, false))
		}
	}
	if workDir != "" {
		root := gitRootOrSelf(workDir)
		out = append(out, discoveredRules(root, workDir)...)
		// Ensure the working-directory AGENTS.md row exists even when absent.
		want := filepath.Join(workDir, AgentsMDName)
		if !ruleListed(out, want) {
			out = append(out, ruleRow(want, "Workspace rules", OriginWorkspace, scopeWorkspace, true))
		}
	}
	return out
}

func discoveredRules(root, workDir string) []CustomizeRule {
	var dirs []string
	dir := filepath.Clean(workDir)
	root = filepath.Clean(root)
	for i := 0; i < 12; i++ {
		dirs = append(dirs, dir)
		if dir == root {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir || !underDir(parent, root) && parent != root {
			break
		}
		dir = parent
	}
	// Root first, then deeper, matching instruction load order.
	var out []CustomizeRule
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		for _, name := range instructionNames {
			path := filepath.Join(d, name)
			editable := name == AgentsMDName && filepath.Clean(d) == filepath.Clean(workDir)
			if !editable && !customizeFileExists(path) {
				continue
			}
			if editable && !customizeFileExists(path) && name == AgentsMDName {
				out = append(out, ruleRow(path, "Workspace rules", OriginWorkspace, scopeWorkspace, true))
				continue
			}
			if !customizeFileExists(path) {
				continue
			}
			label := name
			if editable {
				label = "Workspace rules"
			}
			out = append(out, ruleRow(path, label, OriginWorkspace, scopeWorkspace, editable))
		}
	}
	return out
}

func ruleListed(rows []CustomizeRule, path string) bool {
	clean := filepath.Clean(path)
	for _, row := range rows {
		if row.Abs != "" && filepath.Clean(row.Abs) == clean {
			return true
		}
	}
	return false
}

func ruleRow(path, name, origin, scope string, editable bool) CustomizeRule {
	row := CustomizeRule{
		Name:     name,
		Origin:   origin,
		Scope:    scope,
		Path:     displayPath(path, ""),
		Abs:      path,
		Editable: editable,
		Exists:   customizeFileExists(path),
	}
	if row.Exists {
		data, err := os.ReadFile(path)
		if err == nil {
			row.Excerpt = excerpt(string(data))
			if len(redact.Findings(string(data))) > 0 {
				row.Editable = false
			}
		}
	}
	return row
}

func catalogRoster(workDir string) (CustomizeRoster, []string) {
	var warnings []string
	roster := CustomizeRoster{
		HandoffRequired:   true,
		HandoffSource:     "default",
		GlobalHandoff:     "inherit",
		WorkspaceHandoff:  "inherit",
		StallSource:       "default",
		GlobalBaseHash:    "missing",
		WorkspaceBaseHash: "missing",
		Note:              "Activity shows budget.stall when a child runs longer than stallAfterS. schema:handoff is required unless this gate or the agent frontmatter says optional.",
	}
	if path := GlobalPath(); path != "" {
		if hash, err := fileHash(path); err == nil {
			roster.GlobalBaseHash = hash
		}
		if cfg, err := readOptional(path); err != nil {
			warnings = append(warnings, "global config: "+redact.String(err.Error()))
		} else {
			roster.GlobalHandoff = handoffLabel(cfg.Session.ChildHandoffRequired)
			roster.GlobalStall = cfg.Session.AgentBudget.StallAfterS
		}
	}
	if workDir != "" {
		path := ProjectPath(workDir)
		if hash, err := fileHash(path); err == nil {
			roster.WorkspaceBaseHash = hash
		}
		if cfg, err := readOptional(path); err != nil {
			warnings = append(warnings, "workspace config: "+redact.String(err.Error()))
		} else {
			roster.WorkspaceHandoff = handoffLabel(cfg.Session.ChildHandoffRequired)
			roster.WorkspaceStall = cfg.Session.AgentBudget.StallAfterS
		}
	}
	if cfg, err := Load(workDir); err != nil {
		warnings = append(warnings, "effective config: "+redact.String(err.Error()))
	} else {
		if cfg.Session.ChildHandoffRequired != nil {
			roster.HandoffRequired = *cfg.Session.ChildHandoffRequired
		}
		roster.StallAfterS = cfg.Session.AgentBudget.StallAfterS
	}
	switch {
	case roster.WorkspaceHandoff != "inherit":
		roster.HandoffSource = OriginWorkspace
	case roster.GlobalHandoff != "inherit":
		roster.HandoffSource = OriginGlobal
	default:
		roster.HandoffSource = "default"
	}
	switch {
	case roster.WorkspaceStall != 0:
		roster.StallSource = OriginWorkspace
	case roster.GlobalStall != 0:
		roster.StallSource = OriginGlobal
	default:
		roster.StallSource = "default"
	}
	return roster, warnings
}

func readOptional(path string) (Config, error) {
	cfg, err := read(path)
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	return cfg, err
}

func handoffLabel(v *bool) string {
	if v == nil {
		return "inherit"
	}
	if *v {
		return "required"
	}
	return "optional"
}

func permissionLines(rs permission.Ruleset) []string {
	if len(rs) == 0 {
		return nil
	}
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Permission+" "+r.Pattern+" "+string(r.Action))
	}
	return out
}

func customizeFileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

func fileHash(path string) (string, error) {
	if path == "" || !customizeFileExists(path) {
		return "missing", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hashBytes(data), nil
}

func underDir(path, root string) bool {
	if path == "" || root == "" {
		return false
	}
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func underStrikeKind(path, root, kind string) bool {
	if root == "" {
		return false
	}
	return underDir(path, filepath.Join(root, kind))
}

func pluginIDForPath(path string, plugins []plugin.Plugin) string {
	for _, p := range plugins {
		if p.Root != "" && underDir(path, p.Root) {
			return p.ID
		}
	}
	return ""
}

func displayPath(path, workDir string) string {
	if path == "" {
		return ""
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if underDir(path, home) {
			rel, err := filepath.Rel(home, path)
			if err == nil {
				return filepath.ToSlash(filepath.Join("~", rel))
			}
		}
	}
	if workDir != "" && underDir(path, workDir) {
		rel, err := filepath.Rel(workDir, path)
		if err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(path)
}

func excerpt(s string) string {
	s = redact.String(s)
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > 140 {
		return string(runes[:140]) + "…"
	}
	return s
}

func mapKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeys(m map[string]MCPServer) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func unmarshalJSONC(data []byte, dst any) error {
	stripped, err := stripJSONC(data)
	if err != nil {
		return err
	}
	if len(bytesTrimSpace(stripped)) == 0 {
		return nil
	}
	return json.Unmarshal(stripped, dst)
}

func gitRootOrSelf(start string) string {
	dir := filepath.Clean(start)
	for i := 0; i < 12; i++ {
		git := filepath.Join(dir, ".git")
		if st, err := os.Stat(git); err == nil && (st.IsDir() || st.Mode().IsRegular()) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return filepath.Clean(start)
}

func globalAgentsPath() string {
	root := GlobalRoot()
	if root == "" {
		return ""
	}
	return filepath.Join(root, AgentsMDName)
}
