package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/safefile"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/pkg/redact"
)

// CustomizeChange is one proposed edit. Preview never writes.
// Apply writes only when Confirm is true and DiffHash matches a fresh preview.
type CustomizeChange struct {
	Confirm     bool   `json:"confirm"`
	Kind        string `json:"kind"`
	Op          string `json:"op"`
	Scope       string `json:"scope"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	BaseHash    string `json:"baseHash"`
	DiffHash    string `json:"diffHash"`

	Model    string `json:"model"`
	Provider string `json:"provider"`
	Effort   string `json:"effort"`
	Harness  string `json:"harness"`
	Handoff  string `json:"handoff"`

	Index *int `json:"index,omitempty"`

	MCP  *CustomizeMCPDraft  `json:"mcp,omitempty"`
	Hook *CustomizeHookDraft `json:"hook,omitempty"`

	StallOp       string `json:"stallOp"`
	StallSeconds  int    `json:"stallSeconds"`
	RosterHandoff string `json:"rosterHandoff"`
}

// CustomizeMCPDraft is a server edit. Env and header values may be SecretKeep
// to leave the stored secret in place. Values are never copied into diffs.
type CustomizeMCPDraft struct {
	Type        string          `json:"type"`
	Command     string          `json:"command"`
	Args        []string        `json:"args"`
	URL         string          `json:"url"`
	KeepURL     bool            `json:"keepUrl"`
	KeepCommand bool            `json:"keepCommand"`
	Cwd         string          `json:"cwd"`
	Env         []CustomizePair `json:"env"`
	Headers     []CustomizePair `json:"headers"`
}

// CustomizePair is one env or header entry.
type CustomizePair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// CustomizeHookDraft is a declarative rule or a shell hook, never both.
type CustomizeHookDraft struct {
	Event      string `json:"event"`
	Matcher    string `json:"matcher"`
	Action     string `json:"action"`
	Message    string `json:"message"`
	Command    string `json:"command"`
	TimeoutMs  int    `json:"timeoutMs"`
	FailClosed *bool  `json:"failClosed,omitempty"`
}

// CustomizeResult is a redacted diff. DiffHash is what Apply must echo.
type CustomizeResult struct {
	OK       bool     `json:"ok"`
	Diff     string   `json:"diff"`
	DiffHash string   `json:"diffHash"`
	BaseHash string   `json:"baseHash"`
	Warnings []string `json:"warnings,omitempty"`
	Wrote    bool     `json:"wrote,omitempty"`
	Path     string   `json:"path,omitempty"`
}

// CustomizeDoc is one item opened in the editor. Secrets are masked.
type CustomizeDoc struct {
	Kind           string              `json:"kind"`
	Name           string              `json:"name"`
	Scope          string              `json:"scope,omitempty"`
	Origin         string              `json:"origin"`
	Path           string              `json:"path,omitempty"`
	Editable       bool                `json:"editable"`
	BaseHash       string              `json:"baseHash"`
	Description    string              `json:"description,omitempty"`
	Body           string              `json:"body,omitempty"`
	Model          string              `json:"model,omitempty"`
	Provider       string              `json:"provider,omitempty"`
	Effort         string              `json:"effort,omitempty"`
	Harness        string              `json:"harness,omitempty"`
	Handoff        string              `json:"handoff,omitempty"`
	Permissions    []string            `json:"permissions,omitempty"`
	SecretRedacted bool                `json:"secretRedacted,omitempty"`
	MCP            *CustomizeMCPDraft  `json:"mcp,omitempty"`
	Hook           *CustomizeHookDraft `json:"hook,omitempty"`
	Index          *int                `json:"index,omitempty"`
}

const (
	maxCustomizeBytes = 256 * 1024
	maxStallSeconds   = 86400
)

var customizeWriteMu sync.Mutex

// PreviewCustomize validates a change and returns a redacted diff.
// It does not write.
func PreviewCustomize(workDir string, ch CustomizeChange) (CustomizeResult, error) {
	ch.Confirm = false
	return previewCustomize(workDir, ch)
}

// ApplyCustomize re-validates and writes when Confirm and DiffHash match.
func ApplyCustomize(workDir string, ch CustomizeChange) (CustomizeResult, error) {
	if !ch.Confirm {
		return CustomizeResult{}, fmt.Errorf("confirm is required to write")
	}
	customizeWriteMu.Lock()
	defer customizeWriteMu.Unlock()
	res, err := previewCustomize(workDir, ch)
	if err != nil {
		return CustomizeResult{}, err
	}
	if ch.DiffHash == "" || ch.DiffHash != res.DiffHash {
		return CustomizeResult{}, fmt.Errorf("diff is stale; preview again")
	}
	if err := writeCustomize(workDir, ch); err != nil {
		return CustomizeResult{}, err
	}
	res.Wrote = true
	res.OK = true
	return res, nil
}

func previewCustomize(workDir string, ch CustomizeChange) (CustomizeResult, error) {
	switch ch.Kind {
	case "skill":
		return previewSkill(workDir, ch)
	case "agent":
		return previewAgent(workDir, ch)
	case "mcp":
		return previewMCP(workDir, ch)
	case "hook":
		return previewHook(workDir, ch)
	case "rule":
		return previewRule(workDir, ch)
	case "roster":
		return previewRoster(workDir, ch)
	default:
		return CustomizeResult{}, fmt.Errorf("unknown kind %q", ch.Kind)
	}
}

func writeCustomize(workDir string, ch CustomizeChange) error {
	switch ch.Kind {
	case "skill":
		return writeSkill(workDir, ch)
	case "agent":
		return writeAgentChange(workDir, ch)
	case "mcp":
		return writeMCP(workDir, ch)
	case "hook":
		return writeHook(workDir, ch)
	case "rule":
		return writeRule(workDir, ch)
	case "roster":
		return writeRoster(workDir, ch)
	default:
		return fmt.Errorf("unknown kind %q", ch.Kind)
	}
}

func previewSkill(workDir string, ch CustomizeChange) (CustomizeResult, error) {
	path, err := skillWritePath(workDir, ch)
	if err != nil {
		return CustomizeResult{}, err
	}
	before, hash, err := readChecked(path, ch.BaseHash)
	if err != nil {
		return CustomizeResult{}, err
	}
	var after string
	if ch.Op == "delete" {
		if hash == "missing" {
			return CustomizeResult{}, fmt.Errorf("skill %q is not a workspace or global file", ch.Name)
		}
	} else {
		after, err = renderSkill(ch.Name, ch.Description, ch.Body)
		if err != nil {
			return CustomizeResult{}, err
		}
	}
	return diffResult(path, workDir, hash, before, after, nil), nil
}

func writeSkill(workDir string, ch CustomizeChange) error {
	path, err := skillWritePath(workDir, ch)
	if err != nil {
		return err
	}
	if _, _, err := readChecked(path, ch.BaseHash); err != nil {
		return err
	}
	if ch.Op == "delete" {
		return removeFile(path)
	}
	body, err := renderSkill(ch.Name, ch.Description, ch.Body)
	if err != nil {
		return err
	}
	return safefile.WriteFile(path, []byte(body), 0o644)
}

func previewAgent(workDir string, ch CustomizeChange) (CustomizeResult, error) {
	path, err := agentWritePath(workDir, ch)
	if err != nil {
		return CustomizeResult{}, err
	}
	before, hash, err := readChecked(path, ch.BaseHash)
	if err != nil {
		return CustomizeResult{}, err
	}
	var after string
	if ch.Op == "delete" {
		if hash == "missing" {
			return CustomizeResult{}, fmt.Errorf("agent %q is not a workspace or global file", ch.Name)
		}
	} else {
		after, err = renderAgent(ch, before)
		if err != nil {
			return CustomizeResult{}, err
		}
	}
	return diffResult(path, workDir, hash, before, after, nil), nil
}

func writeAgentChange(workDir string, ch CustomizeChange) error {
	path, err := agentWritePath(workDir, ch)
	if err != nil {
		return err
	}
	before, _, err := readChecked(path, ch.BaseHash)
	if err != nil {
		return err
	}
	if ch.Op == "delete" {
		return removeFile(path)
	}
	body, err := renderAgent(ch, before)
	if err != nil {
		return err
	}
	return safefile.WriteFile(path, []byte(body), 0o644)
}

func renderSkill(name, description, body string) (string, error) {
	name = strings.TrimSpace(name)
	if err := safeIdent(name, "skill"); err != nil {
		return "", err
	}
	if err := ValidateSkillName(name); err != nil {
		return "", err
	}
	description = strings.TrimSpace(description)
	if err := oneLine(description, "description"); err != nil {
		return "", err
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("skill body is empty")
	}
	if err := rejectSecrets("skill", description+"\n"+body); err != nil {
		return "", err
	}
	text := joinFrontmatter("", map[string]string{"name": name, "description": description}, nil, body)
	if _, err := parseSkillBytes(name, text); err != nil {
		return "", err
	}
	return text, nil
}

func renderAgent(ch CustomizeChange, before string) (string, error) {
	name := strings.TrimSpace(ch.Name)
	if err := safeIdent(name, "agent"); err != nil {
		return "", err
	}
	if err := ValidateAgentName(name); err != nil {
		return "", err
	}
	description := strings.TrimSpace(ch.Description)
	if err := oneLine(description, "description"); err != nil {
		return "", err
	}
	for _, field := range []struct{ name, val string }{
		{"model", ch.Model},
		{"provider", ch.Provider},
		{"effort", ch.Effort},
		{"harness", ch.Harness},
	} {
		if err := oneLine(strings.TrimSpace(field.val), field.name); err != nil {
			return "", err
		}
	}
	if strings.TrimSpace(ch.Body) == "" {
		return "", fmt.Errorf("agent prompt is empty")
	}
	if err := rejectSecrets("agent", description+"\n"+ch.Model+"\n"+ch.Provider+"\n"+ch.Body); err != nil {
		return "", err
	}
	if err := validateAgentHarness(strings.TrimSpace(ch.Harness)); err != nil {
		return "", err
	}
	effort := strings.TrimSpace(ch.Effort)
	if effort != "" {
		if _, ok := parseEffort(effort); !ok {
			return "", fmt.Errorf("unknown effort %q", effort)
		}
	}
	handoff, clearHandoff, err := agentHandoff(ch.Handoff)
	if err != nil {
		return "", err
	}
	set := map[string]string{}
	if description != "" {
		set["description"] = description
	}
	if strings.TrimSpace(ch.Model) != "" {
		set["model"] = strings.TrimSpace(ch.Model)
	}
	if strings.TrimSpace(ch.Provider) != "" {
		set["provider"] = strings.TrimSpace(ch.Provider)
	}
	if effort != "" {
		set["effort"] = effort
	}
	if strings.TrimSpace(ch.Harness) != "" {
		set["harness"] = strings.TrimSpace(ch.Harness)
	}
	if handoff != "" {
		set["childHandoffRequired"] = handoff
	}
	var clear []string
	if description == "" {
		clear = append(clear, "description")
	}
	if strings.TrimSpace(ch.Model) == "" {
		clear = append(clear, "model")
	}
	if strings.TrimSpace(ch.Provider) == "" {
		clear = append(clear, "provider")
	}
	if effort == "" {
		clear = append(clear, "effort")
	}
	if strings.TrimSpace(ch.Harness) == "" {
		clear = append(clear, "harness")
	}
	if clearHandoff {
		clear = append(clear, "childHandoffRequired")
	}
	text := joinFrontmatter(before, set, clear, ch.Body)
	parsed, err := parseAgentBytes(name, text)
	if err != nil {
		return "", err
	}
	if before != "" {
		old, err := parseAgentBytes(name, before)
		if err == nil && old != nil && parsed != nil && !sameRules(old.Permissions, parsed.Permissions) {
			return "", fmt.Errorf("permission rules would change; this page does not edit them")
		}
	} else if parsed != nil && len(parsed.Permissions) > 0 {
		return "", fmt.Errorf("permission rules would change; this page does not edit them")
	}
	return text, nil
}

func agentHandoff(label string) (set string, clear bool, err error) {
	switch strings.TrimSpace(label) {
	case "", "inherit":
		return "", true, nil
	case "required":
		return "true", false, nil
	case "optional":
		return "false", false, nil
	default:
		return "", false, fmt.Errorf("handoff must be inherit, required, or optional")
	}
}

func parseEffort(v string) (string, bool) {
	e, ok := protocol.ParseEffort(v)
	if !ok {
		return "", false
	}
	return string(e), true
}

func previewMCP(workDir string, ch CustomizeChange) (CustomizeResult, error) {
	path, before, next, hash, err := mcpNext(workDir, ch)
	if err != nil {
		return CustomizeResult{}, err
	}
	diff := mcpDiff(ch.Name, before, next)
	res := resultFromDiff(displayPath(path, workDir), hash, diff, nil)
	if commented, err := fileHasComments(path); err == nil && commented {
		res.Warnings = append(res.Warnings, "Saving rewrites this file as JSON and drops comments.")
	}
	return res, nil
}

func writeMCP(workDir string, ch CustomizeChange) error {
	path, _, next, _, err := mcpNext(workDir, ch)
	if err != nil {
		return err
	}
	raw, err := marshalMCPFile(path, next)
	if err != nil {
		return err
	}
	if err := safefile.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	// Secrets may live in this file. Tighten a pre-existing looser mode.
	return os.Chmod(path, 0o600)
}

func mcpNext(workDir string, ch CustomizeChange) (string, map[string]MCPServer, map[string]MCPServer, string, error) {
	name := strings.TrimSpace(ch.Name)
	if err := safeIdent(name, "mcp server"); err != nil {
		return "", nil, nil, "", err
	}
	path, err := mcpWritePath(workDir, ch.Scope)
	if err != nil {
		return "", nil, nil, "", err
	}
	current, hash, err := readMCPChecked(path, ch.BaseHash)
	if err != nil {
		return "", nil, nil, "", err
	}
	next := cloneMCPServers(current)
	if next == nil {
		next = map[string]MCPServer{}
	}
	if ch.Op == "delete" {
		if _, ok := next[name]; !ok {
			return "", nil, nil, "", fmt.Errorf("mcp server %q is not in this file", name)
		}
		delete(next, name)
		return path, current, next, hash, nil
	}
	if ch.MCP == nil {
		return "", nil, nil, "", fmt.Errorf("mcp object is required")
	}
	old := next[name]
	srv, err := applyMCPDraft(old, *ch.MCP)
	if err != nil {
		return "", nil, nil, "", err
	}
	next[name] = srv
	return path, current, next, hash, nil
}

func applyMCPDraft(old MCPServer, d CustomizeMCPDraft) (MCPServer, error) {
	next := old
	next.Type = strings.ToLower(strings.TrimSpace(d.Type))
	if d.KeepCommand {
		// Command, args, and cwd stay on disk. A secret-shaped command is
		// never echoed back, so the client cannot resubmit it.
	} else {
		next.Command = strings.TrimSpace(d.Command)
		next.Args = append([]string(nil), d.Args...)
		next.Cwd = strings.TrimSpace(d.Cwd)
	}
	if !d.KeepURL {
		next.URL = strings.TrimSpace(d.URL)
	}
	env, err := mergePairs(old.Env, d.Env, "env")
	if err != nil {
		return MCPServer{}, err
	}
	headers, err := mergePairs(old.Headers, d.Headers, "header")
	if err != nil {
		return MCPServer{}, err
	}
	next.Env = env
	next.Headers = headers
	if !d.KeepCommand {
		if err := rejectSecrets("mcp command", next.Command+"\n"+strings.Join(next.Args, "\n")+"\n"+next.Cwd); err != nil {
			return MCPServer{}, err
		}
	}
	if next.Type == "" {
		if next.URL != "" && next.Command == "" {
			next.Type = "http"
		} else {
			next.Type = "stdio"
		}
	}
	switch next.Type {
	case "stdio":
		if next.Command == "" {
			return MCPServer{}, fmt.Errorf("stdio mcp server needs a command")
		}
	case "http":
		if strings.TrimSpace(next.URL) == "" {
			return MCPServer{}, fmt.Errorf("http mcp server needs a url")
		}
	default:
		return MCPServer{}, fmt.Errorf("mcp type must be stdio or http")
	}
	return next, nil
}

func mergePairs(old map[string]string, pairs []CustomizePair, label string) (map[string]string, error) {
	if pairs == nil {
		return old, nil
	}
	out := map[string]string{}
	seen := map[string]struct{}{}
	for _, p := range pairs {
		key := strings.TrimSpace(p.Key)
		if key == "" {
			return nil, fmt.Errorf("%s key is empty", label)
		}
		if strings.ContainsAny(key, "\r\n=") {
			return nil, fmt.Errorf("%s key %q is invalid", label, key)
		}
		if _, dup := seen[key]; dup {
			return nil, fmt.Errorf("duplicate %s key %q", label, key)
		}
		seen[key] = struct{}{}
		val := p.Value
		if val == SecretKeep || val == redact.Placeholder || val == "" {
			prev, ok := old[key]
			if !ok || prev == "" {
				return nil, fmt.Errorf("%s %s needs a value", label, key)
			}
			out[key] = prev
			continue
		}
		out[key] = val
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func mcpDiff(name string, before, after map[string]MCPServer) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mcp server %s\n", name)
	old, oldOK := before[name]
	new, newOK := after[name]
	if oldOK && !newOK {
		fmt.Fprintf(&b, "- %s (%s)\n", name, mcpType(old))
		return b.String()
	}
	if !oldOK && newOK {
		fmt.Fprintf(&b, "+ %s (%s)\n", name, mcpType(new))
		if new.Command != "" {
			if redact.Findings(new.Command) != nil {
				b.WriteString("+ command (set)\n")
			} else {
				fmt.Fprintf(&b, "+ command %s\n", new.Command)
			}
		}
		if new.URL != "" && redact.Findings(new.URL) == nil {
			fmt.Fprintf(&b, "+ url %s\n", new.URL)
		} else if new.URL != "" {
			b.WriteString("+ url (set)\n")
		}
		for _, k := range mapKeys(new.Env) {
			fmt.Fprintf(&b, "+ env %s (set)\n", k)
		}
		for _, k := range mapKeys(new.Headers) {
			fmt.Fprintf(&b, "+ header %s (set)\n", k)
		}
		return b.String()
	}
	writeField(&b, "type", mcpType(old), mcpType(new))
	writeMaybeSecret(&b, "command", old.Command, new.Command)
	writeMaybeSecret(&b, "args", strings.Join(old.Args, " "), strings.Join(new.Args, " "))
	writeMaybeSecret(&b, "cwd", old.Cwd, new.Cwd)
	writeSecretField(&b, "url", old.URL, new.URL)
	diffMap(&b, "env", old.Env, new.Env)
	diffMap(&b, "header", old.Headers, new.Headers)
	return b.String()
}

func mcpType(s MCPServer) string {
	if s.Type != "" {
		return s.Type
	}
	if s.URL != "" {
		return "http"
	}
	return "stdio"
}

func writeField(b *strings.Builder, label, old, new string) {
	if old == new {
		return
	}
	fmt.Fprintf(b, "%s: %q -> %q\n", label, old, new)
}

func writeMaybeSecret(b *strings.Builder, label, old, new string) {
	if redact.Findings(old) != nil || redact.Findings(new) != nil {
		writeSecretField(b, label, old, new)
		return
	}
	writeField(b, label, old, new)
}

func writeSecretField(b *strings.Builder, label, old, new string) {
	if old == new {
		return
	}
	fmt.Fprintf(b, "%s: %s -> %s\n", label, secretState(old), secretState(new))
}

func diffMap(b *strings.Builder, label string, old, new map[string]string) {
	keys := map[string]struct{}{}
	for k := range old {
		keys[k] = struct{}{}
	}
	for k := range new {
		keys[k] = struct{}{}
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		o, oOK := old[k]
		n, nOK := new[k]
		switch {
		case oOK && !nOK:
			fmt.Fprintf(b, "%s %s: (set) -> (removed)\n", label, k)
		case !oOK && nOK:
			fmt.Fprintf(b, "%s %s: (unset) -> (set)\n", label, k)
		case o != n:
			fmt.Fprintf(b, "%s %s: (set) -> (updated)\n", label, k)
		}
	}
}

func secretState(v string) string {
	if v == "" {
		return "(empty)"
	}
	if redact.Findings(v) != nil {
		return "(set)"
	}
	return "(set)"
}

func previewHook(workDir string, ch CustomizeChange) (CustomizeResult, error) {
	path, before, after, hash, warnings, err := hookNext(workDir, ch)
	if err != nil {
		return CustomizeResult{}, err
	}
	diff := unifiedDiff(displayPath(path, workDir), hooksText(before), hooksText(after))
	return resultFromDiff(displayPath(path, workDir), hash, diff, warnings), nil
}

func writeHook(workDir string, ch CustomizeChange) error {
	path, _, after, _, _, err := hookNext(workDir, ch)
	if err != nil {
		return err
	}
	return patchConfigKeys(path, map[string]json.RawMessage{
		"hooks": mustRaw(after),
	}, len(after) == 0)
}

func hookNext(workDir string, ch CustomizeChange) (string, []Hook, []Hook, string, []string, error) {
	path, err := configWritePath(workDir, ch.Scope)
	if err != nil {
		return "", nil, nil, "", nil, err
	}
	obj, hash, warnings, err := readConfigObject(path, ch.BaseHash)
	if err != nil {
		return "", nil, nil, "", nil, err
	}
	current := hooksFromObject(obj)
	if raw, ok := obj.get("hooks"); ok {
		var rawHooks []Hook
		if json.Unmarshal(raw, &rawHooks) == nil && len(rawHooks) != len(current) {
			warnings = append(warnings, "Saving drops invalid hook entries.")
		}
	}
	next := append([]Hook(nil), current...)
	switch ch.Op {
	case "delete":
		if ch.Index == nil || *ch.Index < 0 || *ch.Index >= len(next) {
			return "", nil, nil, "", nil, fmt.Errorf("hook index is out of range")
		}
		next = append(next[:*ch.Index], next[*ch.Index+1:]...)
	case "", "upsert":
		if ch.Hook == nil {
			return "", nil, nil, "", nil, fmt.Errorf("hook object is required")
		}
		hook, err := normalizeHook(*ch.Hook)
		if err != nil {
			return "", nil, nil, "", nil, err
		}
		if ch.Index == nil {
			next = append(next, hook)
		} else if *ch.Index < 0 || *ch.Index > len(next) {
			return "", nil, nil, "", nil, fmt.Errorf("hook index is out of range")
		} else if *ch.Index == len(next) {
			next = append(next, hook)
		} else {
			if hookHasSecret(next[*ch.Index]) {
				return "", nil, nil, "", nil, fmt.Errorf("hook contains a credential-shaped value; edit it outside Customize")
			}
			next[*ch.Index] = hook
		}
	default:
		return "", nil, nil, "", nil, fmt.Errorf("op must be upsert or delete")
	}
	return path, current, next, hash, warnings, nil
}

func normalizeHook(d CustomizeHookDraft) (Hook, error) {
	h := Hook{
		Event:      strings.TrimSpace(d.Event),
		Matcher:    strings.TrimSpace(d.Matcher),
		Action:     strings.TrimSpace(d.Action),
		Message:    d.Message,
		Command:    d.Command,
		TimeoutMs:  d.TimeoutMs,
		FailClosed: d.FailClosed,
	}
	if strings.Contains(h.Matcher, "\n") || strings.Contains(h.Event, "\n") {
		return Hook{}, fmt.Errorf("hook event and matcher must be one line")
	}
	if h.TimeoutMs < 0 || h.TimeoutMs > 120000 {
		return Hook{}, fmt.Errorf("timeoutMs must be 0..120000")
	}
	if err := rejectSecrets("hook", h.Message+"\n"+h.Command+"\n"+h.Matcher); err != nil {
		return Hook{}, err
	}
	switch {
	case h.IsRule():
		if err := permission.ValidateHookRule(permission.HookRule{
			Event: h.Event, Matcher: h.Matcher, Action: h.Action, Message: h.Message,
		}); err != nil {
			return Hook{}, err
		}
	case h.IsShell():
		if !permission.ValidHookEvent(h.Event) {
			return Hook{}, fmt.Errorf("unknown event %q", h.Event)
		}
	default:
		return Hook{}, fmt.Errorf("hook needs an action or a command, not both")
	}
	return h, nil
}

func hookHasSecret(h Hook) bool {
	return len(redact.Findings(h.Message+"\n"+h.Command+"\n"+h.Matcher)) > 0
}

func hooksText(hooks []Hook) string {
	if len(hooks) == 0 {
		return ""
	}
	safe := make([]Hook, len(hooks))
	for i, h := range hooks {
		h.Command = redact.String(h.Command)
		h.Message = redact.String(h.Message)
		h.Matcher = redact.String(h.Matcher)
		safe[i] = h
	}
	raw, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw) + "\n"
}

func previewRule(workDir string, ch CustomizeChange) (CustomizeResult, error) {
	path, err := ruleWritePath(workDir, ch.Scope)
	if err != nil {
		return CustomizeResult{}, err
	}
	before, hash, err := readChecked(path, ch.BaseHash)
	if err != nil {
		return CustomizeResult{}, err
	}
	var after string
	if ch.Op == "delete" {
		if hash == "missing" {
			return CustomizeResult{}, fmt.Errorf("rules file is already absent")
		}
	} else {
		after, err = renderRule(ch.Body)
		if err != nil {
			return CustomizeResult{}, err
		}
	}
	return diffResult(path, workDir, hash, before, after, nil), nil
}

func writeRule(workDir string, ch CustomizeChange) error {
	path, err := ruleWritePath(workDir, ch.Scope)
	if err != nil {
		return err
	}
	if _, _, err := readChecked(path, ch.BaseHash); err != nil {
		return err
	}
	if ch.Op == "delete" {
		return removeFile(path)
	}
	body, err := renderRule(ch.Body)
	if err != nil {
		return err
	}
	return safefile.WriteFile(path, []byte(body), 0o644)
}

func renderRule(body string) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("rules body is empty")
	}
	if err := boundedText("rules", body); err != nil {
		return "", err
	}
	if err := rejectSecrets("rules", body); err != nil {
		return "", err
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body, nil
}

func previewRoster(workDir string, ch CustomizeChange) (CustomizeResult, error) {
	path, before, after, hash, warnings, err := rosterNext(workDir, ch)
	if err != nil {
		return CustomizeResult{}, err
	}
	diff := unifiedDiff(displayPath(path, workDir)+" session", rosterText(before), rosterText(after))
	return resultFromDiff(displayPath(path, workDir), hash, diff, warnings), nil
}

type rosterView struct {
	ChildHandoffRequired *bool `json:"childHandoffRequired,omitempty"`
	StallAfterS          int   `json:"stallAfterS,omitempty"`
}

func rosterNext(workDir string, ch CustomizeChange) (string, rosterView, rosterView, string, []string, error) {
	path, err := configWritePath(workDir, ch.Scope)
	if err != nil {
		return "", rosterView{}, rosterView{}, "", nil, err
	}
	obj, hash, warnings, err := readConfigObject(path, ch.BaseHash)
	if err != nil {
		return "", rosterView{}, rosterView{}, "", nil, err
	}
	session := jsonObject{}
	if raw, ok := obj.get("session"); ok {
		parsed, err := parseJSONObject(raw)
		if err != nil {
			return "", rosterView{}, rosterView{}, "", nil, fmt.Errorf("session: %w", err)
		}
		session = parsed
	}
	before := rosterFromSession(session)
	if err := applyRosterEdits(&session, ch); err != nil {
		return "", rosterView{}, rosterView{}, "", nil, err
	}
	after := rosterFromSession(session)
	return path, before, after, hash, warnings, nil
}

func rosterFromSession(session jsonObject) rosterView {
	var view rosterView
	if raw, ok := session.get("childHandoffRequired"); ok {
		val := strings.TrimSpace(string(raw))
		switch val {
		case "true":
			v := true
			view.ChildHandoffRequired = &v
		case "false":
			v := false
			view.ChildHandoffRequired = &v
		}
	}
	if raw, ok := session.get("agentBudget"); ok {
		var budget AgentBudgetConfig
		if err := json.Unmarshal(raw, &budget); err == nil {
			view.StallAfterS = budget.StallAfterS
		}
	}
	return view
}

func rosterText(v rosterView) string {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return ""
	}
	return string(raw) + "\n"
}

// writeRoster re-derives the session object. rosterNext's session edits are
// applied again inside patch by rebuilding from the same change.
func writeRoster(workDir string, ch CustomizeChange) error {
	path, err := configWritePath(workDir, ch.Scope)
	if err != nil {
		return err
	}
	obj, _, _, err := readConfigObject(path, ch.BaseHash)
	if err != nil {
		return err
	}
	session := jsonObject{}
	if raw, ok := obj.get("session"); ok {
		parsed, err := parseJSONObject(raw)
		if err != nil {
			return err
		}
		session = parsed
	}
	if err := applyRosterEdits(&session, ch); err != nil {
		return err
	}
	raw, err := session.marshalRaw()
	if err != nil {
		return err
	}
	return patchConfigKeys(path, map[string]json.RawMessage{"session": raw}, false)
}

func applyRosterEdits(session *jsonObject, ch CustomizeChange) error {
	stallOp := strings.TrimSpace(ch.StallOp)
	if stallOp == "" {
		stallOp = "unchanged"
	}
	switch stallOp {
	case "unchanged":
	case "clear":
		if budget, ok := session.get("agentBudget"); ok {
			bobj, err := parseJSONObject(budget)
			if err != nil {
				return err
			}
			bobj.delete("stallAfterS")
			if len(bobj.keys) == 0 {
				session.delete("agentBudget")
			} else {
				raw, err := bobj.marshalRaw()
				if err != nil {
					return err
				}
				session.set("agentBudget", raw)
			}
		}
	case "set":
		if ch.StallSeconds < 1 || ch.StallSeconds > maxStallSeconds {
			return fmt.Errorf("stall seconds must be 1..%d", maxStallSeconds)
		}
		bobj := jsonObject{}
		if budget, ok := session.get("agentBudget"); ok {
			parsed, err := parseJSONObject(budget)
			if err != nil {
				return err
			}
			bobj = parsed
		}
		bobj.set("stallAfterS", json.RawMessage(strconv.Itoa(ch.StallSeconds)))
		raw, err := bobj.marshalRaw()
		if err != nil {
			return err
		}
		session.set("agentBudget", raw)
	default:
		return fmt.Errorf("stallOp must be unchanged, set, or clear")
	}
	switch strings.TrimSpace(ch.RosterHandoff) {
	case "", "unchanged":
	case "inherit":
		session.delete("childHandoffRequired")
	case "required":
		session.set("childHandoffRequired", json.RawMessage("true"))
	case "optional":
		session.set("childHandoffRequired", json.RawMessage("false"))
	default:
		return fmt.Errorf("rosterHandoff must be unchanged, inherit, required, or optional")
	}
	return nil
}

func skillWritePath(workDir string, ch CustomizeChange) (string, error) {
	name := strings.TrimSpace(ch.Name)
	if err := safeIdent(name, "skill"); err != nil {
		return "", err
	}
	root, err := scopeRoot(ch.Scope, workDir)
	if err != nil {
		return "", err
	}
	return strikeDocPath(root, "skills", name)
}

func agentWritePath(workDir string, ch CustomizeChange) (string, error) {
	name := strings.TrimSpace(ch.Name)
	if err := safeIdent(name, "agent"); err != nil {
		return "", err
	}
	root, err := scopeRoot(ch.Scope, workDir)
	if err != nil {
		return "", err
	}
	return strikeDocPath(root, "agents", name)
}

func strikeDocPath(root, kind, name string) (string, error) {
	dir := filepath.Join(root, kind)
	nested := filepath.Join(dir, name, "SKILL.md")
	if kind == "skills" && customizeFileExists(nested) {
		if !underDir(nested, dir) {
			return "", fmt.Errorf("path escapes %s", kind)
		}
		return nested, nil
	}
	path := filepath.Join(dir, name+".md")
	if !underDir(path, dir) {
		return "", fmt.Errorf("path escapes %s", kind)
	}
	return path, nil
}

func mcpWritePath(workDir, scope string) (string, error) {
	root, err := scopeRoot(scope, workDir)
	if err != nil {
		return "", err
	}
	return firstExisting(filepath.Join(root, "mcp.jsonc"), filepath.Join(root, "mcp.json")), nil
}

func configWritePath(workDir, scope string) (string, error) {
	root, err := scopeRoot(scope, workDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "config"), nil
}

func ruleWritePath(workDir, scope string) (string, error) {
	switch normalizeScope(scope) {
	case scopeGlobal:
		path := globalAgentsPath()
		if path == "" {
			return "", fmt.Errorf("cannot resolve ~/.strike")
		}
		return path, nil
	case scopeWorkspace:
		if strings.TrimSpace(workDir) == "" {
			return "", fmt.Errorf("no workspace directory")
		}
		return filepath.Join(workDir, AgentsMDName), nil
	default:
		return "", fmt.Errorf("scope must be global or workspace")
	}
}

func scopeRoot(scope, workDir string) (string, error) {
	switch normalizeScope(scope) {
	case scopeGlobal:
		root := GlobalRoot()
		if root == "" {
			return "", fmt.Errorf("cannot resolve ~/.strike")
		}
		return root, nil
	case scopeWorkspace:
		if strings.TrimSpace(workDir) == "" {
			return "", fmt.Errorf("no workspace directory")
		}
		return projectRoot(workDir), nil
	default:
		return "", fmt.Errorf("scope must be global or workspace")
	}
}

func normalizeScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "project", "workspace":
		return scopeWorkspace
	case "global":
		return scopeGlobal
	default:
		return strings.TrimSpace(scope)
	}
}

func readChecked(path, want string) (string, string, error) {
	hash, err := fileHash(path)
	if err != nil {
		return "", "", err
	}
	if want != "" && want != hash {
		return "", "", fmt.Errorf("file changed; preview again")
	}
	if hash == "missing" {
		return "", hash, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	if len(redact.Findings(string(data))) > 0 {
		return "", "", fmt.Errorf("file contains a credential-shaped value; edit it outside Customize")
	}
	return string(data), hash, nil
}

func readMCPChecked(path, want string) (map[string]MCPServer, string, error) {
	hash, err := fileHash(path)
	if err != nil {
		return nil, "", err
	}
	if want != "" && want != hash {
		return nil, "", fmt.Errorf("file changed; preview again")
	}
	if hash == "missing" {
		return map[string]MCPServer{}, hash, nil
	}
	mc, err := ReadMCPFile(path)
	if err != nil {
		return nil, "", err
	}
	if mc.Servers == nil {
		mc.Servers = map[string]MCPServer{}
	}
	return mc.Servers, hash, nil
}

func removeFile(path string) error {
	if err := safefile.CheckLeaf(path, false); err != nil {
		return err
	}
	return os.Remove(path)
}

func rejectSecrets(label, text string) error {
	if len(redact.Findings(text)) > 0 {
		return fmt.Errorf("%s contains a credential-shaped value; put secrets in an MCP env or header field", label)
	}
	return boundedText(label, text)
}

func boundedText(label, text string) error {
	if strings.Contains(text, "\x00") {
		return fmt.Errorf("%s contains a NUL byte", label)
	}
	if !utf8OK(text) {
		return fmt.Errorf("%s is not valid UTF-8", label)
	}
	if len(text) > maxCustomizeBytes {
		return fmt.Errorf("%s is too large", label)
	}
	return nil
}

func oneLine(val, label string) error {
	if strings.ContainsAny(val, "\r\n") {
		return fmt.Errorf("%s must be one line", label)
	}
	return nil
}

func safeIdent(name, kind string) error {
	if name == "" {
		return fmt.Errorf("%s name is empty", kind)
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return fmt.Errorf("%s name %q contains a path separator", kind, name)
	}
	return nil
}

func utf8OK(s string) bool {
	return strings.ToValidUTF8(s, "") == s
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func diffResult(path, workDir, hash, before, after string, warnings []string) CustomizeResult {
	diff := unifiedDiff(displayPath(path, workDir), before, after)
	return resultFromDiff(displayPath(path, workDir), hash, diff, warnings)
}

func resultFromDiff(path, hash, diff string, warnings []string) CustomizeResult {
	if strings.TrimSpace(diff) == "" {
		diff = "(no changes)\n"
	}
	return CustomizeResult{
		OK:       true,
		Diff:     diff,
		DiffHash: hashBytes([]byte(diff)),
		BaseHash: hash,
		Warnings: warnings,
		Path:     path,
	}
}

func unifiedDiff(name, before, after string) string {
	if before == after {
		return ""
	}
	bLines := splitKeep(before)
	aLines := splitKeep(after)
	prefix := 0
	for prefix < len(bLines) && prefix < len(aLines) && bLines[prefix] == aLines[prefix] {
		prefix++
	}
	bEnd, aEnd := len(bLines), len(aLines)
	for bEnd > prefix && aEnd > prefix && bLines[bEnd-1] == aLines[aEnd-1] {
		bEnd--
		aEnd--
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "--- %s\n+++ %s\n", name, name)
	for _, line := range bLines[prefix:bEnd] {
		fmt.Fprintf(&buf, "-%s\n", line)
	}
	for _, line := range aLines[prefix:aEnd] {
		fmt.Fprintf(&buf, "+%s\n", line)
	}
	return buf.String()
}

func splitKeep(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return parts
}

func joinFrontmatter(existing string, set map[string]string, clear []string, body string) string {
	clearSet := map[string]struct{}{}
	for _, k := range clear {
		clearSet[k] = struct{}{}
	}
	lines, _ := existingFrontmatter(existing)
	used := map[string]struct{}{}
	var out []string
	for _, line := range lines {
		key, ok := frontKey(line)
		if !ok {
			out = append(out, line)
			continue
		}
		if _, drop := clearSet[key]; drop {
			used[key] = struct{}{}
			continue
		}
		if val, ok := set[key]; ok {
			if val != "" {
				out = append(out, key+": "+val)
			}
			used[key] = struct{}{}
			continue
		}
		out = append(out, line)
	}
	keys := make([]string, 0, len(set))
	for k, v := range set {
		if v == "" {
			continue
		}
		if _, ok := used[k]; ok {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+": "+set[k])
	}
	var b strings.Builder
	b.WriteString("---\n")
	for _, line := range out {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString("---\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteByte('\n')
	return b.String()
}

func existingFrontmatter(text string) ([]string, bool) {
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, false
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil, false
	}
	return strings.Split(rest[:end], "\n"), true
}

func frontKey(line string) (string, bool) {
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return "", false
	}
	trim := strings.TrimSpace(line)
	if trim == "" || strings.HasPrefix(trim, "#") {
		return "", false
	}
	key, _, ok := strings.Cut(trim, ":")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(key), true
}

func parseSkillBytes(name, text string) (*Skill, error) {
	return parseViaTemp(name+".md", text, parseSkillFile)
}

func parseAgentBytes(name, text string) (*Agent, error) {
	return parseViaTemp(name+".md", text, parseAgentFile)
}

func parseViaTemp[T any](filename, text string, parse func(string) (*T, error)) (*T, error) {
	dir, err := os.MkdirTemp("", "strike-customize-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return nil, err
	}
	return parse(path)
}

func sameRules(a, b permission.Ruleset) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

type jsonObject struct {
	keys []string
	val  map[string]json.RawMessage
}

func (o jsonObject) get(key string) (json.RawMessage, bool) {
	if o.val == nil {
		return nil, false
	}
	raw, ok := o.val[key]
	return raw, ok
}

func (o *jsonObject) set(key string, raw json.RawMessage) {
	if o.val == nil {
		o.val = map[string]json.RawMessage{}
	}
	if _, ok := o.val[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.val[key] = raw
}

func (o *jsonObject) delete(key string) {
	if o.val == nil {
		return
	}
	if _, ok := o.val[key]; !ok {
		return
	}
	delete(o.val, key)
	keys := o.keys[:0]
	for _, k := range o.keys {
		if k != key {
			keys = append(keys, k)
		}
	}
	o.keys = keys
}

func parseJSONObject(raw []byte) (jsonObject, error) {
	stripped, err := stripJSONC(raw)
	if err != nil {
		return jsonObject{}, err
	}
	stripped = bytesTrimSpace(stripped)
	obj := jsonObject{val: map[string]json.RawMessage{}}
	if len(stripped) == 0 || string(stripped) == "null" {
		return obj, nil
	}
	dec := json.NewDecoder(bytes.NewReader(stripped))
	tok, err := dec.Token()
	if err != nil {
		return jsonObject{}, err
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return jsonObject{}, fmt.Errorf("expected a JSON object")
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return jsonObject{}, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return jsonObject{}, fmt.Errorf("expected a string key")
		}
		var val json.RawMessage
		if err := dec.Decode(&val); err != nil {
			return jsonObject{}, err
		}
		obj.set(key, val)
	}
	if _, err := dec.Token(); err != nil {
		return jsonObject{}, err
	}
	return obj, nil
}

func (o jsonObject) marshal() ([]byte, error) {
	raw, err := o.marshalRaw()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

func (o jsonObject) marshalRaw() (json.RawMessage, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, key := range o.keys {
		raw, ok := o.val[key]
		if !ok {
			continue
		}
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(bytes.TrimSpace(raw))
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func readConfigObject(path, want string) (jsonObject, string, []string, error) {
	hash, err := fileHash(path)
	if err != nil {
		return jsonObject{}, "", nil, err
	}
	if want != "" && want != hash {
		return jsonObject{}, "", nil, fmt.Errorf("file changed; preview again")
	}
	var warnings []string
	if hash == "missing" {
		return jsonObject{val: map[string]json.RawMessage{}}, hash, warnings, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return jsonObject{}, "", nil, err
	}
	if commented, err := bytesHaveComments(data); err == nil && commented {
		warnings = append(warnings, "Saving rewrites this file as JSON and drops comments.")
	}
	obj, err := parseJSONObject(data)
	if err != nil {
		return jsonObject{}, "", nil, err
	}
	return obj, hash, warnings, nil
}

func fileHasComments(path string) (bool, error) {
	if !customizeFileExists(path) {
		return false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return bytesHaveComments(data)
}

func bytesHaveComments(data []byte) (bool, error) {
	stripped, err := stripJSONC(data)
	if err != nil {
		return false, err
	}
	return !bytes.Equal(bytesTrimSpace(data), bytesTrimSpace(stripped)), nil
}

func hooksFromObject(obj jsonObject) []Hook {
	raw, ok := obj.get("hooks")
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var hooks []Hook
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return nil
	}
	valid := make([]Hook, 0, len(hooks))
	for _, h := range hooks {
		switch {
		case h.IsShell() && strings.TrimSpace(h.Event) != "":
			valid = append(valid, h)
		case h.IsRule():
			rule := permission.HookRule{Event: h.Event, Matcher: h.Matcher, Action: h.Action, Message: h.Message}
			if permission.ValidateHookRule(rule) == nil {
				valid = append(valid, h)
			}
		}
	}
	return valid
}

func patchConfigKeys(path string, updates map[string]json.RawMessage, dropHooks bool) error {
	obj, _, _, err := readConfigObject(path, "")
	if err != nil {
		return err
	}
	// Caller already checked the hash. Re-read without the want so a matching
	// preview cannot race: Apply holds customizeWriteMu.
	for key, raw := range updates {
		if key == "hooks" && dropHooks {
			obj.delete("hooks")
			continue
		}
		obj.set(key, raw)
	}
	raw, err := obj.marshal()
	if err != nil {
		return err
	}
	return safefile.WriteFile(path, raw, 0o644)
}

func mustRaw(v any) json.RawMessage {
	switch t := v.(type) {
	case jsonObject:
		raw, err := t.marshalRaw()
		if err != nil {
			return json.RawMessage("null")
		}
		return raw
	case []Hook:
		if len(t) == 0 {
			return json.RawMessage("[]")
		}
		raw, err := json.Marshal(t)
		if err != nil {
			return json.RawMessage("[]")
		}
		return raw
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return json.RawMessage("null")
		}
		return raw
	}
}

func marshalMCPFile(path string, servers map[string]MCPServer) ([]byte, error) {
	obj := jsonObject{val: map[string]json.RawMessage{}}
	if customizeFileExists(path) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		parsed, err := parseJSONObject(data)
		if err != nil {
			return nil, err
		}
		obj = parsed
	}
	wrapped := false
	if _, ok := obj.get("servers"); ok {
		wrapped = true
	} else if len(obj.keys) == 0 {
		wrapped = true
	}
	encoded := map[string]json.RawMessage{}
	names := sortedKeys(servers)
	for _, name := range names {
		raw, err := json.Marshal(servers[name])
		if err != nil {
			return nil, err
		}
		encoded[name] = raw
	}
	if wrapped {
		inner := jsonObject{val: map[string]json.RawMessage{}}
		for _, name := range names {
			inner.set(name, encoded[name])
		}
		raw, err := inner.marshalRaw()
		if err != nil {
			return nil, err
		}
		obj.set("servers", raw)
		// Drop bare server keys that are no longer present when the file was mixed.
	} else {
		// Bare server map: replace server entries, keep unknown non-server keys
		// only when they are not previous server names. Previous names that were
		// removed must go.
		prev, err := ReadMCPFile(path)
		if err != nil && customizeFileExists(path) {
			return nil, err
		}
		for name := range prev.Servers {
			if _, ok := servers[name]; !ok {
				obj.delete(name)
			}
		}
		for _, name := range names {
			obj.set(name, encoded[name])
		}
	}
	return obj.marshal()
}

// CustomizeDoc loads one editor document. scope selects the file to edit
// when the winning layer is not editable; pass the winning scope otherwise.
func OpenCustomizeDoc(workDir, kind, name, scope string, index int, hasIndex bool) (CustomizeDoc, error) {
	switch kind {
	case "skill":
		return docSkill(workDir, name, scope)
	case "agent":
		return docAgent(workDir, name, scope)
	case "mcp":
		return docMCP(workDir, name, scope)
	case "hook":
		if !hasIndex {
			return CustomizeDoc{}, fmt.Errorf("hook index is required")
		}
		return docHook(workDir, scope, index)
	case "rule":
		return docRule(workDir, scope)
	default:
		return CustomizeDoc{}, fmt.Errorf("unknown kind %q", kind)
	}
}

func docSkill(workDir, name, scope string) (CustomizeDoc, error) {
	loaded, err := LoadSkillsWithError(workDir)
	if err != nil {
		return CustomizeDoc{}, err
	}
	var found *Skill
	for i := range loaded {
		if loaded[i].Name == name {
			found = &loaded[i]
			break
		}
	}
	doc := CustomizeDoc{Kind: "skill", Name: name, Origin: OriginBuiltin, BaseHash: "missing", Handoff: "inherit"}
	if scope == "" && found != nil && found.Path != "" {
		if underDir(found.Path, workDir) {
			scope = scopeWorkspace
		} else {
			scope = scopeGlobal
		}
	}
	if scope == "" {
		scope = scopeWorkspace
	}
	doc.Scope = normalizeScope(scope)
	path, err := skillWritePath(workDir, CustomizeChange{Name: name, Scope: doc.Scope})
	if err != nil {
		return CustomizeDoc{}, err
	}
	hash, _ := fileHash(path)
	doc.BaseHash = hash
	doc.Path = displayPath(path, workDir)
	if hash != "missing" {
		data, err := os.ReadFile(path)
		if err != nil {
			return CustomizeDoc{}, err
		}
		if len(redact.Findings(string(data))) > 0 {
			doc.Body = redact.String(string(data))
			doc.SecretRedacted = true
			doc.Editable = false
			doc.Origin = originFromPath(path, workDir)
			return doc, nil
		}
		meta, body, _ := parseFrontmatter(string(data))
		doc.Description = meta["description"]
		doc.Body = body
		doc.Editable = true
		doc.Origin = originFromPath(path, workDir)
		return doc, nil
	}
	if found != nil {
		doc.Description = found.Description
		doc.Body = found.Template
		doc.Origin = OriginBuiltin
		if found.Path != "" {
			doc.Origin = originFromPath(found.Path, workDir)
			doc.Path = displayPath(found.Path, workDir)
		}
		doc.Editable = false
		doc.BaseHash = "missing"
		return doc, nil
	}
	doc.Editable = true
	return doc, nil
}

func docAgent(workDir, name, scope string) (CustomizeDoc, error) {
	agents, err := catalogAgents(workDir)
	if err != nil {
		return CustomizeDoc{}, err
	}
	var item *CustomizeAgent
	for i := range agents {
		if agents[i].Name == name {
			item = &agents[i]
			break
		}
	}
	doc := CustomizeDoc{Kind: "agent", Name: name, Origin: OriginBuiltin, BaseHash: "missing", Handoff: "inherit"}
	if item != nil {
		doc.Description = item.Description
		doc.Model = item.Model
		doc.Provider = item.Provider
		doc.Effort = item.Effort
		doc.Harness = item.Harness
		doc.Handoff = item.Handoff
		doc.Permissions = item.Permissions
		doc.Origin = item.Origin
	}
	if scope == "" && item != nil && item.Scope != "" {
		scope = item.Scope
	}
	if scope == "" {
		scope = scopeWorkspace
	}
	doc.Scope = normalizeScope(scope)
	path, err := agentWritePath(workDir, CustomizeChange{Name: name, Scope: doc.Scope})
	if err != nil {
		return CustomizeDoc{}, err
	}
	hash, _ := fileHash(path)
	doc.BaseHash = hash
	if hash == "missing" {
		doc.Editable = item == nil
		if item != nil {
			doc.Path = item.Path
		}
		if loaded, err := LoadAgentsWithError(workDir); err == nil {
			for _, a := range loaded {
				if a.Name == name {
					doc.Body = a.Prompt
					break
				}
			}
		}
		return doc, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CustomizeDoc{}, err
	}
	doc.Path = displayPath(path, workDir)
	doc.Origin = originFromPath(path, workDir)
	if len(redact.Findings(string(data))) > 0 {
		doc.Body = excerpt(string(data))
		doc.SecretRedacted = true
		doc.Editable = false
		return doc, nil
	}
	meta, body, _ := parseFrontmatter(string(data))
	if meta["description"] != "" {
		doc.Description = meta["description"]
	}
	doc.Body = body
	doc.Editable = underStrikeKind(path, scopeRootMust(doc.Scope, workDir), "agents")
	return doc, nil
}

func docMCP(workDir, name, scope string) (CustomizeDoc, error) {
	scope = normalizeScope(scope)
	if scope != scopeGlobal && scope != scopeWorkspace {
		return CustomizeDoc{}, fmt.Errorf("scope must be global or workspace")
	}
	path, err := mcpWritePath(workDir, scope)
	if err != nil {
		return CustomizeDoc{}, err
	}
	hash, err := fileHash(path)
	if err != nil {
		return CustomizeDoc{}, err
	}
	doc := CustomizeDoc{
		Kind: "mcp", Name: name, Scope: scope, Origin: originForScope(scope),
		Path: displayPath(path, workDir), Editable: true, BaseHash: hash,
		MCP: &CustomizeMCPDraft{Type: "stdio"},
	}
	if hash == "missing" {
		return doc, nil
	}
	mc, err := ReadMCPFile(path)
	if err != nil {
		return CustomizeDoc{}, err
	}
	srv, ok := mc.Servers[name]
	if !ok {
		return doc, nil
	}
	doc.MCP = publicMCP(srv)
	doc.Editable = true
	return doc, nil
}

func secretText(s string) bool {
	return len(redact.Findings(s)) > 0
}

func publicMCP(srv MCPServer) *CustomizeMCPDraft {
	d := &CustomizeMCPDraft{
		Type:    mcpType(srv),
		Command: srv.Command,
		Args:    append([]string(nil), srv.Args...),
		Cwd:     srv.Cwd,
		URL:     srv.URL,
	}
	if redact.Findings(srv.URL) != nil {
		d.URL = ""
		d.KeepURL = true
	}
	locked := secretText(srv.Command) || secretText(srv.Cwd)
	for _, arg := range srv.Args {
		if secretText(arg) {
			locked = true
		}
	}
	if locked {
		d.Command = ""
		d.Args = nil
		d.Cwd = ""
		d.KeepCommand = true
	}
	for _, key := range mapKeys(srv.Env) {
		val := SecretKeep
		if srv.Env[key] == "" {
			val = ""
		}
		d.Env = append(d.Env, CustomizePair{Key: key, Value: val})
	}
	for _, key := range mapKeys(srv.Headers) {
		d.Headers = append(d.Headers, CustomizePair{Key: key, Value: SecretKeep})
	}
	if d.Args == nil {
		d.Args = []string{}
	}
	if d.Env == nil {
		d.Env = []CustomizePair{}
	}
	if d.Headers == nil {
		d.Headers = []CustomizePair{}
	}
	return d
}

func docHook(workDir, scope string, index int) (CustomizeDoc, error) {
	scope = normalizeScope(scope)
	path, err := configWritePath(workDir, scope)
	if err != nil {
		return CustomizeDoc{}, err
	}
	rows, warn := hooksInFile(path, scope, originForScope(scope))
	if warn != "" {
		return CustomizeDoc{}, fmt.Errorf("%s", warn)
	}
	if index < 0 || index >= len(rows) {
		return CustomizeDoc{}, fmt.Errorf("hook index is out of range")
	}
	row := rows[index]
	idx := row.Index
	return CustomizeDoc{
		Kind: "hook", Scope: scope, Origin: row.Origin, Path: displayPath(path, workDir),
		Editable: row.Editable, BaseHash: row.BaseHash, Index: &idx,
		Hook: &CustomizeHookDraft{
			Event: row.Event, Matcher: row.Matcher, Action: row.Action,
			Message: row.Message, Command: row.Command, TimeoutMs: row.TimeoutMs,
		},
	}, nil
}

func docRule(workDir, scope string) (CustomizeDoc, error) {
	path, err := ruleWritePath(workDir, scope)
	if err != nil {
		return CustomizeDoc{}, err
	}
	hash, err := fileHash(path)
	if err != nil {
		return CustomizeDoc{}, err
	}
	doc := CustomizeDoc{
		Kind: "rule", Name: AgentsMDName, Scope: normalizeScope(scope),
		Origin: originForScope(normalizeScope(scope)), Path: displayPath(path, workDir),
		Editable: true, BaseHash: hash,
	}
	if hash == "missing" {
		return doc, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return CustomizeDoc{}, err
	}
	if len(redact.Findings(string(data))) > 0 {
		doc.Body = redact.String(string(data))
		doc.SecretRedacted = true
		doc.Editable = false
		return doc, nil
	}
	doc.Body = string(data)
	return doc, nil
}

func originFromPath(path, workDir string) string {
	if id := pluginIDForPath(path, DiscoverPlugins(workDir).Plugins); id != "" {
		return OriginPlugin
	}
	if underDir(path, workDir) {
		return OriginWorkspace
	}
	return OriginGlobal
}

func originForScope(scope string) string {
	if normalizeScope(scope) == scopeWorkspace {
		return OriginWorkspace
	}
	return OriginGlobal
}

func scopeRootMust(scope, workDir string) string {
	root, err := scopeRoot(scope, workDir)
	if err != nil {
		return ""
	}
	return root
}
