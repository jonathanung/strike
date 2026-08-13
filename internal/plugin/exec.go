package plugin

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// MCPEntry is one contributions.mcp object (aligned with config MCP servers).
type MCPEntry struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// HarnessEntry is one contributions.harnesses object.
type HarnessEntry struct {
	Name          string            `json:"name"`
	Command       string            `json:"command"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Mode          string            `json:"mode,omitempty"`
	MaxConcurrent int               `json:"maxConcurrent,omitempty"`
	IdleTimeoutMs int               `json:"idleTimeoutMs,omitempty"`
	MaxRestarts   int               `json:"maxRestarts,omitempty"`
}

// HookEntry is one contributions.hooks object (shell or declarative).
type HookEntry struct {
	Event     string `json:"event"`
	Matcher   string `json:"matcher,omitempty"`
	Type      string `json:"type,omitempty"` // command|shell optional alias
	Action    string `json:"action,omitempty"`
	Message   string `json:"message,omitempty"`
	Command   string `json:"command,omitempty"`
	TimeoutMs int    `json:"timeoutMs,omitempty"`
}

// IsShell reports a command/shell hook.
func (h HookEntry) IsShell() bool {
	if strings.TrimSpace(h.Action) != "" {
		return false
	}
	if strings.TrimSpace(h.Command) != "" {
		return true
	}
	t := strings.ToLower(strings.TrimSpace(h.Type))
	return t == "command" || t == "shell"
}

// IsRule reports a declarative action hook.
func (h HookEntry) IsRule() bool {
	return strings.TrimSpace(h.Action) != "" && strings.TrimSpace(h.Command) == ""
}

// CompiledMCP is a path-resolved MCP server ready for the MCP manager.
type CompiledMCP struct {
	PluginID  string
	Version   string
	Scope     Scope
	Name      string
	Transport string // stdio|http
	Command   string // absolute when relative was resolved
	Args      []string
	Env       map[string]string
	Cwd       string // subprocess working directory; empty inherits
	URL       string
	Headers   map[string]string
}

// CompiledHarness is a path-resolved external harness registration.
type CompiledHarness struct {
	PluginID      string
	Version       string
	Scope         Scope
	Name          string
	Command       string
	Args          []string
	Env           map[string]string
	Mode          string
	MaxConcurrent int
	IdleTimeoutMs int
	MaxRestarts   int
}

// CompiledHook is a hook contribution with command path resolved when shell.
type CompiledHook struct {
	PluginID  string
	Version   string
	Scope     Scope
	Event     string
	Matcher   string
	Action    string
	Message   string
	Command   string // absolute path or reviewed absolute; empty for declarative
	TimeoutMs int
	// Trusted is false only for declarative hooks from enabled untrusted plugins
	// (declarative still loads). Shell hooks are omitted unless trusted.
	Trusted bool
}

// ExecutableSet is the compiled trusted (and declarative) executable surface
// from enabled plugins in Discover merge order.
type ExecutableSet struct {
	MCP       []CompiledMCP
	Harnesses []CompiledHarness
	// Hooks in §4.1 plugin order: global plugins (id asc) then project (id asc),
	// each plugin's hooks in manifest order. Caller interleaves with user hooks.
	GlobalHooks  []CompiledHook
	ProjectHooks []CompiledHook
	Diagnostics  []Diagnostic
	// MCPByName / HarnessByName map public name → plugin id (for diagnostics).
	MCPByName     map[string]string
	HarnessByName map[string]string
}

// CompileExecutables discovers enabled plugins and activates executable
// contributions only when a matching trust record is present (shell hooks,
// MCP, harnesses). Declarative hooks load for any enabled plugin.
//
// Name collisions between plugins fail closed for that contribution.
// userMCP / userHarnesses names are reserved: plugin entries with the same
// name are skipped with a diagnostic (user config wins).
func CompileExecutables(opts Options, userMCP, userHarnesses map[string]struct{}) ExecutableSet {
	var out ExecutableSet
	out.MCPByName = map[string]string{}
	out.HarnessByName = map[string]string{}
	mcpBlocked := map[string]struct{}{}
	harnessBlocked := map[string]struct{}{}
	if userMCP == nil {
		userMCP = map[string]struct{}{}
	}
	if userHarnesses == nil {
		userHarnesses = map[string]struct{}{}
	}

	globalRoot := opts.GlobalRoot
	if globalRoot == "" {
		globalRoot = defaultGlobalRoot()
	}
	projectRoot := opts.ProjectRoot
	if projectRoot == "" && opts.WorkDir != "" {
		projectRoot = defaultProjectRoot(opts.WorkDir)
	}
	globalLock, projectLock, lockDiags := loadLockfiles(globalRoot, projectRoot)
	out.Diagnostics = append(out.Diagnostics, lockDiags...)

	disc := Discover(opts)
	// Discover parse/skip/deprecation diagnostics are printed by config.Load.
	// CompileExecutables only reports activation (trust, collision, command/cwd).

	for _, p := range disc.Plugins {
		// Install-scope lockfile owns provenance/trust for that install.
		entry, _ := lockEntryFor(p.ID, p.Source, globalLock, projectLock)
		// Always recompute live digest so file edits invalidate trust even when
		// the lockfile digest field is stale.
		digest, digErr := ComputeDigest(p.Root)
		if digErr != nil {
			d := Diagnostic{
				Severity: SeverityError,
				Code:     "digest",
				Message:  digErr.Error(),
				PluginID: p.ID,
				Version:  p.Version,
				Source:   p.Source,
				Path:     p.Root,
			}
			out.Diagnostics = append(out.Diagnostics, d)
			continue
		}
		source := entry.Source
		caps := InferCapabilitiesAt(p.Manifest, p.Root)
		match := MatchTrust(entry.Trust, digest, source, caps)

		base := Diagnostic{
			PluginID: p.ID,
			Version:  p.Version,
			Source:   p.Source,
			Path:     p.Root,
		}

		if HasExecutableContributionsAt(p.Manifest, p.Root) && !match.OK {
			d := base
			d.Severity = SeverityInfo
			d.Code = "executable_untrusted"
			d.Message = match.Reason
			if d.Message == "" {
				d.Message = "executable contributions blocked until trust is granted"
			}
			out.Diagnostics = append(out.Diagnostics, d)
		}

		// Declarative hooks: enabled plugin only (passive-class).
		// Shell hooks / MCP / harnesses: require trust.
		trusted := match.OK

		if trusted {
			mcpList, mcpDiags := compileMCP(p, opts, userMCP, out.MCPByName)
			out.Diagnostics = append(out.Diagnostics, mcpDiags...)
			for _, cm := range mcpList {
				if _, blocked := mcpBlocked[cm.Name]; blocked {
					out.Diagnostics = append(out.Diagnostics, Diagnostic{
						Severity:  SeverityError,
						Code:      "collision",
						Collision: cm.Name,
						Message:   fmt.Sprintf("mcp server %q previously collided; skipped", cm.Name),
						PluginID:  p.ID,
						Version:   p.Version,
						Source:    p.Source,
					})
					continue
				}
				if prev, ok := out.MCPByName[cm.Name]; ok && prev != p.ID {
					// Fail closed: drop earlier registration and block the name.
					out.MCP = filterMCPByName(out.MCP, cm.Name)
					delete(out.MCPByName, cm.Name)
					mcpBlocked[cm.Name] = struct{}{}
					out.Diagnostics = append(out.Diagnostics, Diagnostic{
						Severity:  SeverityError,
						Code:      "collision",
						Collision: cm.Name,
						Message:   fmt.Sprintf("mcp server %q collides with plugin %s; both skipped for this name", cm.Name, prev),
						PluginID:  p.ID,
						Version:   p.Version,
						Source:    p.Source,
					})
					continue
				}
				out.MCPByName[cm.Name] = p.ID
				out.MCP = append(out.MCP, cm)
			}

			harList, harDiags := compileHarnesses(p, userHarnesses, out.HarnessByName)
			out.Diagnostics = append(out.Diagnostics, harDiags...)
			for _, ch := range harList {
				if _, blocked := harnessBlocked[ch.Name]; blocked {
					out.Diagnostics = append(out.Diagnostics, Diagnostic{
						Severity:  SeverityError,
						Code:      "collision",
						Collision: ch.Name,
						Message:   fmt.Sprintf("harness %q previously collided; skipped", ch.Name),
						PluginID:  p.ID,
						Version:   p.Version,
						Source:    p.Source,
					})
					continue
				}
				if prev, ok := out.HarnessByName[ch.Name]; ok && prev != p.ID {
					out.Harnesses = filterHarnessByName(out.Harnesses, ch.Name)
					delete(out.HarnessByName, ch.Name)
					harnessBlocked[ch.Name] = struct{}{}
					out.Diagnostics = append(out.Diagnostics, Diagnostic{
						Severity:  SeverityError,
						Code:      "collision",
						Collision: ch.Name,
						Message:   fmt.Sprintf("harness %q collides with plugin %s; both skipped for this name", ch.Name, prev),
						PluginID:  p.ID,
						Version:   p.Version,
						Source:    p.Source,
					})
					continue
				}
				out.HarnessByName[ch.Name] = p.ID
				out.Harnesses = append(out.Harnesses, ch)
			}
		}

		hooks, hookDiags := compileHooks(p, trusted)
		out.Diagnostics = append(out.Diagnostics, hookDiags...)
		switch p.Source {
		case ScopeGlobal:
			out.GlobalHooks = append(out.GlobalHooks, hooks...)
		default:
			out.ProjectHooks = append(out.ProjectHooks, hooks...)
		}
	}
	return out
}

func lockEntryFor(id string, scope Scope, global, project Lockfile) (LockfileEntry, bool) {
	// Install-scope lockfile owns provenance/trust for that install.
	switch scope {
	case ScopeProject:
		if e, ok := project.Plugins[id]; ok {
			return e, true
		}
	case ScopeGlobal:
		if e, ok := global.Plugins[id]; ok {
			return e, true
		}
	}
	// Fallback: either lock may hold the entry after manual edits.
	if e, ok := project.Plugins[id]; ok {
		return e, true
	}
	if e, ok := global.Plugins[id]; ok {
		return e, true
	}
	return LockfileEntry{}, false
}

func compileMCP(p Plugin, opts Options, userNames map[string]struct{}, claimed map[string]string) ([]CompiledMCP, []Diagnostic) {
	if p.Manifest.Format == FormatAPS {
		return compileAPSMCP(p, opts, userNames)
	}
	return compileLegacyMCP(p, userNames, claimed)
}

func compileAPSMCP(p Plugin, opts Options, userNames map[string]struct{}) ([]CompiledMCP, []Diagnostic) {
	var out []CompiledMCP
	var diags []Diagnostic
	base := Diagnostic{PluginID: p.ID, Version: p.Version, Source: p.Source, Path: p.Root}

	file, _, err := loadAPSMCPFile(p.Root)
	if err != nil || file.disabled {
		// Parse/schema/skip diagnostics already emitted by Discover/loadOne.
		return nil, nil
	}

	pluginRoot := resolvedPluginRoot(p.Root)
	dataDir := PluginDataDir(p.Source, p.ID, opts)

	for _, e := range file.servers {
		if e.Skip {
			continue
		}
		name := e.Name
		if _, ok := userNames[name]; ok {
			d := base
			d.Severity = SeverityWarning
			d.Code = "collision"
			d.Collision = name
			d.Message = fmt.Sprintf("mcp server %q skipped: user config overrides plugin", name)
			diags = append(diags, d)
			continue
		}
		cm := CompiledMCP{
			PluginID:  p.ID,
			Version:   p.Version,
			Scope:     p.Source,
			Name:      name,
			Transport: e.Transport,
			URL:       e.URL,
			Headers:   copyStringMap(e.Headers),
		}
		if e.Transport == "http" {
			out = append(out, cm)
			continue
		}

		cmd, err := resolveAPSCommand(pluginRoot, e.Command)
		if err != nil {
			d := base
			d.Severity = SeverityError
			d.Code = "path"
			d.Message = fmt.Sprintf("mcp %q command: %v", name, err)
			diags = append(diags, d)
			continue
		}
		if dataDir != "" {
			if err := EnsurePluginDataDir(dataDir); err != nil {
				d := base
				d.Severity = SeverityError
				d.Code = "path"
				d.Message = fmt.Sprintf("mcp %q PLUGIN_DATA: %v", name, err)
				diags = append(diags, d)
				continue
			}
		}
		dataAbs := dataDir
		if dataAbs != "" {
			if a, err := filepath.Abs(dataAbs); err == nil {
				dataAbs = a
			}
		}
		cwd, err := resolveAPSCWD(pluginRoot, dataAbs, e.Cwd)
		if err != nil {
			d := base
			d.Severity = SeverityError
			d.Code = "path"
			d.Message = fmt.Sprintf("mcp %q cwd: %v", name, err)
			diags = append(diags, d)
			continue
		}
		args := make([]string, len(e.Args))
		for i, a := range e.Args {
			args[i] = expandPluginPlaceholders(a, pluginRoot, dataAbs)
		}
		env := map[string]string{}
		for k, v := range e.Env {
			env[k] = expandPluginPlaceholders(v, pluginRoot, dataAbs)
		}
		// Spec §9.1: overlay configured env, then set PLUGIN_ROOT / PLUGIN_DATA.
		env["PLUGIN_ROOT"] = pluginRoot
		env["PLUGIN_DATA"] = dataAbs
		cm.Command = cmd
		cm.Args = args
		cm.Env = env
		cm.Cwd = cwd
		cm.Transport = "stdio"
		out = append(out, cm)
	}
	return out, diags
}

func compileLegacyMCP(p Plugin, userNames map[string]struct{}, claimed map[string]string) ([]CompiledMCP, []Diagnostic) {
	var out []CompiledMCP
	var diags []Diagnostic
	base := Diagnostic{PluginID: p.ID, Version: p.Version, Source: p.Source}

	for i, raw := range p.Manifest.Contributions.MCP {
		e, err := parseMCPEntry(raw)
		if err != nil {
			d := base
			d.Severity = SeverityError
			d.Code = "malformed"
			d.Message = fmt.Sprintf("mcp[%d]: %v", i, err)
			diags = append(diags, d)
			continue
		}
		name := strings.TrimSpace(e.Name)
		if name == "" || !validMCPServerName(name) {
			d := base
			d.Severity = SeverityError
			d.Code = "malformed"
			d.Message = fmt.Sprintf("mcp[%d]: invalid server name %q", i, name)
			diags = append(diags, d)
			continue
		}
		if _, ok := userNames[name]; ok {
			d := base
			d.Severity = SeverityWarning
			d.Code = "collision"
			d.Collision = name
			d.Message = fmt.Sprintf("mcp server %q skipped: user config overrides plugin", name)
			diags = append(diags, d)
			continue
		}
		// Cross-plugin collisions are resolved by CompileExecutables using claimed.
		_ = claimed

		transport := normalizeMCPTransport(e.Transport, e.URL)
		cm := CompiledMCP{
			PluginID:  p.ID,
			Version:   p.Version,
			Scope:     p.Source,
			Name:      name,
			Transport: transport,
			Args:      append([]string(nil), e.Args...),
			Env:       copyStringMap(e.Env),
			URL:       strings.TrimSpace(e.URL),
			Headers:   copyStringMap(e.Headers),
		}
		switch transport {
		case "http":
			if cm.URL == "" {
				d := base
				d.Severity = SeverityError
				d.Code = "malformed"
				d.Message = fmt.Sprintf("mcp %q: url required for http transport", name)
				diags = append(diags, d)
				continue
			}
		default:
			cmd, err := resolveCommand(p.Root, e.Command)
			if err != nil {
				d := base
				d.Severity = SeverityError
				d.Code = "path"
				d.Message = fmt.Sprintf("mcp %q command: %v", name, err)
				diags = append(diags, d)
				continue
			}
			cm.Command = cmd
			cm.Transport = "stdio"
		}
		out = append(out, cm)
	}
	return out, diags
}

func compileHarnesses(p Plugin, userNames map[string]struct{}, claimed map[string]string) ([]CompiledHarness, []Diagnostic) {
	var out []CompiledHarness
	var diags []Diagnostic
	base := Diagnostic{PluginID: p.ID, Version: p.Version, Source: p.Source}
	_ = claimed

	raws := p.Manifest.Contributions.Harnesses
	if p.Manifest.Format == FormatAPS {
		if skipStrikeCLI(p.Manifest) {
			return nil, nil
		}
		var extra []Diagnostic
		raws, extra = loadStrikeCLIHarnessRaws(p.Root, base)
		diags = append(diags, extra...)
	}

	for i, raw := range raws {
		e, err := parseHarnessEntry(raw)
		if err != nil {
			d := base
			d.Severity = SeverityError
			d.Code = "malformed"
			d.Message = fmt.Sprintf("harnesses[%d]: %v", i, err)
			diags = append(diags, d)
			continue
		}
		name := strings.TrimSpace(e.Name)
		if name == "" {
			d := base
			d.Severity = SeverityError
			d.Code = "malformed"
			d.Message = fmt.Sprintf("harnesses[%d]: name is required", i)
			diags = append(diags, d)
			continue
		}
		if _, ok := userNames[name]; ok {
			d := base
			d.Severity = SeverityWarning
			d.Code = "collision"
			d.Collision = name
			d.Message = fmt.Sprintf("harness %q skipped: user config overrides plugin", name)
			diags = append(diags, d)
			continue
		}
		cmd, err := resolveCommand(p.Root, e.Command)
		if err != nil {
			d := base
			d.Severity = SeverityError
			d.Code = "path"
			d.Message = fmt.Sprintf("harness %q command: %v", name, err)
			diags = append(diags, d)
			continue
		}
		ch := CompiledHarness{
			PluginID:      p.ID,
			Version:       p.Version,
			Scope:         p.Source,
			Name:          name,
			Command:       cmd,
			Args:          append([]string(nil), e.Args...),
			Env:           copyStringMap(e.Env),
			Mode:          strings.TrimSpace(e.Mode),
			MaxConcurrent: e.MaxConcurrent,
			IdleTimeoutMs: e.IdleTimeoutMs,
			MaxRestarts:   e.MaxRestarts,
		}
		out = append(out, ch)
	}
	return out, diags
}

func compileHooks(p Plugin, trusted bool) ([]CompiledHook, []Diagnostic) {
	var out []CompiledHook
	var diags []Diagnostic
	base := Diagnostic{PluginID: p.ID, Version: p.Version, Source: p.Source}

	raws := p.Manifest.Contributions.Hooks
	if p.Manifest.Format == FormatAPS {
		if skipStrikeCLI(p.Manifest) {
			return nil, nil
		}
		var extra []Diagnostic
		raws, extra = loadStrikeCLIHookRaws(p.Root, base)
		diags = append(diags, extra...)
	}

	for i, raw := range raws {
		e, err := parseHookEntry(raw)
		if err != nil {
			d := base
			d.Severity = SeverityError
			d.Code = "malformed"
			d.Message = fmt.Sprintf("hooks[%d]: %v", i, err)
			diags = append(diags, d)
			continue
		}
		if e.IsShell() {
			if !trusted {
				// Shell hooks stay inactive without trust (already diagnosed).
				continue
			}
			cmd, err := resolveCommand(p.Root, e.Command)
			if err != nil {
				d := base
				d.Severity = SeverityError
				d.Code = "path"
				d.Message = fmt.Sprintf("hooks[%d] command: %v", i, err)
				diags = append(diags, d)
				continue
			}
			out = append(out, CompiledHook{
				PluginID:  p.ID,
				Version:   p.Version,
				Scope:     p.Source,
				Event:     strings.TrimSpace(e.Event),
				Matcher:   e.Matcher,
				Command:   cmd,
				TimeoutMs: e.TimeoutMs,
				Trusted:   true,
			})
			continue
		}
		if e.IsRule() {
			// Declarative: enablement only.
			out = append(out, CompiledHook{
				PluginID: p.ID,
				Version:  p.Version,
				Scope:    p.Source,
				Event:    strings.TrimSpace(e.Event),
				Matcher:  e.Matcher,
				Action:   strings.TrimSpace(e.Action),
				Message:  e.Message,
				Trusted:  true, // not an executable trust gate
			})
			continue
		}
		d := base
		d.Severity = SeverityError
		d.Code = "malformed"
		d.Message = fmt.Sprintf("hooks[%d]: need command or action", i)
		diags = append(diags, d)
	}
	return out, diags
}

// resolveCommand resolves a relative command under plugin root; absolute paths
// are allowed as reviewed entry text (digest covers scripts; abs binaries are
// part of the trusted entry).
func resolveCommand(root, command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("empty command")
	}
	if filepath.IsAbs(command) {
		return command, nil
	}
	// Reject path escape in relative form.
	if err := validateRelPathSyntax(command); err != nil {
		return "", err
	}
	return ResolveUnderRoot(root, command)
}

func parseMCPEntry(raw json.RawMessage) (MCPEntry, error) {
	var e MCPEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return MCPEntry{}, err
	}
	return e, nil
}

func parseHarnessEntry(raw json.RawMessage) (HarnessEntry, error) {
	var e HarnessEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return HarnessEntry{}, err
	}
	if strings.TrimSpace(e.Command) == "" {
		return HarnessEntry{}, fmt.Errorf("command is required")
	}
	return e, nil
}

func parseHookEntry(raw json.RawMessage) (HookEntry, error) {
	var e HookEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		return HookEntry{}, err
	}
	if strings.TrimSpace(e.Event) == "" {
		return HookEntry{}, fmt.Errorf("event is required")
	}
	return e, nil
}

func normalizeMCPTransport(t, url string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	switch t {
	case "", "stdio":
		if strings.TrimSpace(url) != "" && t == "" {
			return "http"
		}
		return "stdio"
	case "http", "streamable-http", "streamable_http", "sse":
		return "http"
	default:
		return t
	}
}

func validMCPServerName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for i, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		if r == '_' || r == '-' {
			if i == 0 {
				return false
			}
			continue
		}
		return false
	}
	r := rune(name[0])
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if strings.TrimSpace(k) == "" {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func filterMCPByName(in []CompiledMCP, name string) []CompiledMCP {
	out := in[:0]
	for _, m := range in {
		if m.Name != name {
			out = append(out, m)
		}
	}
	return out
}

func filterHarnessByName(in []CompiledHarness, name string) []CompiledHarness {
	out := in[:0]
	for _, h := range in {
		if h.Name != name {
			out = append(out, h)
		}
	}
	return out
}
