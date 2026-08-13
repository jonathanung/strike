package plugin

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Agent Plugins 1.0.0 canonical schema identifiers (do not fetch at load).
const (
	APSPluginSchemaV1 = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	APSMCPSchemaV1    = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
)

// ManifestFormat is the on-disk package format.
type ManifestFormat string

const (
	FormatAPS    ManifestFormat = "aps"
	FormatLegacy ManifestFormat = "legacy"
)

const (
	placeholderRoot = "${PLUGIN_ROOT}"
	placeholderData = "${PLUGIN_DATA}"
	strikeCLINs     = "com.strike.cli"
)

var apsSchemaRE = regexp.MustCompile(`^https://agent-plugins\.org/schemas/([^/]+)/plugin\.schema\.json$`)

// StrikeCLIExtension is the parsed extensions.com.strike.cli object (APS.2
// reads metadata only; contribution directory loading is APS.3 / #1144).
type StrikeCLIExtension struct {
	DisplayName  string
	Strike       StrikeRange
	Capabilities []string
	Digest       string
}

// PluginDataDir returns the Strike-managed PLUGIN_DATA directory for an
// installed plugin instance (docs/plugins.md §2.2).
func PluginDataDir(scope Scope, name string, opts Options) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var strikeRoot string
	switch scope {
	case ScopeProject:
		strikeRoot = opts.ProjectRoot
		if strikeRoot == "" && opts.WorkDir != "" {
			strikeRoot = defaultProjectRoot(opts.WorkDir)
		}
	default:
		strikeRoot = opts.GlobalRoot
		if strikeRoot == "" {
			strikeRoot = defaultGlobalRoot()
		}
	}
	if strikeRoot == "" {
		return ""
	}
	return filepath.Join(strikeRoot, "plugin-data", name)
}

// EnsurePluginDataDir creates PLUGIN_DATA (0755) when path is non-empty.
func EnsurePluginDataDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return fmt.Errorf("empty plugin data directory")
	}
	return os.MkdirAll(dir, 0o755)
}

// ValidateAPSName checks Agent Plugins §5.5 name constraints.
func ValidateAPSName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("name exceeds 64 characters")
	}
	if strings.Contains(name, "--") || strings.Contains(name, "..") {
		return fmt.Errorf("name %q must not contain '--' or '..'", name)
	}
	if !apsNameCharsetOK(name) {
		return fmt.Errorf("name %q is not a valid Agent Plugins name", name)
	}
	return nil
}

func apsNameCharsetOK(name string) bool {
	if name == "" {
		return false
	}
	alnum := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
	}
	rs := []rune(name)
	if !alnum(rs[0]) || !alnum(rs[len(rs)-1]) {
		return false
	}
	for _, r := range rs {
		if alnum(r) || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// ValidatePluginKey accepts APS name or legacy Strike id (lockfile / install key).
func ValidatePluginKey(id string) error {
	id = strings.TrimSpace(id)
	if ValidateAPSName(id) == nil {
		return nil
	}
	return ValidatePluginID(id)
}

func isAPSPluginSchemaURL(schema string) bool {
	return apsSchemaRE.MatchString(strings.TrimSpace(schema))
}

func parseAPSManifest(raw map[string]json.RawMessage) (Manifest, []Diagnostic, error) {
	var diags []Diagnostic
	schema := jsonRawString(raw["$schema"])
	if schema != APSPluginSchemaV1 {
		if isAPSPluginSchemaURL(schema) {
			return Manifest{}, nil, unsupportedSchemaError{schema: schema}
		}
		return Manifest{}, nil, fmt.Errorf("$schema must be %s", APSPluginSchemaV1)
	}

	known := map[string]struct{}{
		"$schema": {}, "name": {}, "version": {}, "description": {},
		"author": {}, "homepage": {}, "repository": {}, "license": {},
		"keywords": {}, "extensions": {},
	}
	for k := range raw {
		if _, ok := known[k]; ok {
			continue
		}
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Code:     "unknown_field",
			Message:  fmt.Sprintf("ignoring unknown top-level field %q", k),
		})
	}

	name, err := requireJSONString(raw, "name")
	if err != nil {
		return Manifest{}, diags, err
	}
	if err := ValidateAPSName(name); err != nil {
		return Manifest{}, diags, err
	}

	m := Manifest{
		Schema: schema,
		ID:     name,
		Name:   name,
		Format: FormatAPS,
	}

	if v, ok := raw["version"]; ok {
		s, err := decodeJSONString(v, "version")
		if err != nil {
			return Manifest{}, diags, err
		}
		m.Version = s
	}
	if v, ok := raw["description"]; ok {
		s, err := decodeJSONString(v, "description")
		if err != nil {
			return Manifest{}, diags, err
		}
		m.Description = s
	}
	for _, key := range []string{"homepage", "repository", "license"} {
		if v, ok := raw[key]; ok {
			if _, err := decodeJSONString(v, key); err != nil {
				return Manifest{}, diags, err
			}
		}
	}
	if v, ok := raw["keywords"]; ok {
		if err := decodeJSONStringSlice(v, "keywords"); err != nil {
			return Manifest{}, diags, err
		}
	}
	if v, ok := raw["author"]; ok {
		if err := validateAPSAuthor(v); err != nil {
			return Manifest{}, diags, err
		}
	}
	if v, ok := raw["extensions"]; ok {
		ext, extDiags, err := parseAPSExtensions(v)
		diags = append(diags, extDiags...)
		if err != nil {
			return Manifest{}, diags, err
		}
		if ext != nil {
			m.StrikeCLI = ext
			if dn := strings.TrimSpace(ext.DisplayName); dn != "" {
				m.Name = dn
			}
			m.Strike = ext.Strike
			m.Capabilities = append([]string(nil), ext.Capabilities...)
			m.Digest = ext.Digest
		}
	}
	return m, diags, nil
}

func parseAPSExtensions(raw json.RawMessage) (*StrikeCLIExtension, []Diagnostic, error) {
	raw = bytesTrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "malformed",
			Message:  "extensions is not an object; ignoring",
		}}, nil
	}
	if raw[0] != '{' {
		return nil, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "malformed",
			Message:  "extensions is not an object; ignoring",
		}}, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "malformed",
			Message:  "extensions is not an object; ignoring",
		}}, nil
	}
	strikeRaw, ok := obj[strikeCLINs]
	if !ok {
		return nil, nil, nil
	}
	return parseStrikeCLIExtension(strikeRaw)
}

func parseStrikeCLIExtension(raw json.RawMessage) (*StrikeCLIExtension, []Diagnostic, error) {
	var diags []Diagnostic
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Code:     "malformed",
			Message:  "extensions.com.strike.cli is not an object; ignoring Strike extension metadata",
		})
		return nil, diags, nil
	}
	known := map[string]struct{}{
		"displayName": {}, "strike": {}, "capabilities": {}, "digest": {},
	}
	for k := range obj {
		if _, ok := known[k]; ok {
			continue
		}
		diags = append(diags, Diagnostic{
			Severity: SeverityWarning,
			Code:     "unknown_field",
			Message:  fmt.Sprintf("ignoring unknown extensions.com.strike.cli field %q", k),
		})
	}
	ext := &StrikeCLIExtension{}
	if v, ok := obj["displayName"]; ok {
		s, err := decodeJSONString(v, "extensions.com.strike.cli.displayName")
		if err != nil {
			diags = append(diags, Diagnostic{Severity: SeverityWarning, Code: "malformed", Message: err.Error()})
		} else {
			s = strings.TrimSpace(s)
			if s != "" && len(s) > 80 {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Code:     "malformed",
					Message:  "extensions.com.strike.cli.displayName exceeds 80 characters; ignoring",
				})
			} else {
				ext.DisplayName = s
			}
		}
	}
	if v, ok := obj["strike"]; ok {
		var sr StrikeRange
		if err := json.Unmarshal(v, &sr); err != nil {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Code:     "malformed",
				Message:  "extensions.com.strike.cli.strike has invalid type; ignoring Strike range",
			})
		} else {
			if min := strings.TrimSpace(sr.Min); min != "" && !bundleVersionRE.MatchString(min) {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Code:     "malformed",
					Message:  fmt.Sprintf("extensions.com.strike.cli.strike.min %q is not valid semver; ignoring Strike range", sr.Min),
				})
			} else if max := strings.TrimSpace(sr.Max); max != "" && !bundleVersionRE.MatchString(max) {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Code:     "malformed",
					Message:  fmt.Sprintf("extensions.com.strike.cli.strike.max %q is not valid semver; ignoring Strike range", sr.Max),
				})
			} else {
				ext.Strike = sr
			}
		}
	}
	if v, ok := obj["capabilities"]; ok {
		var caps []string
		if err := json.Unmarshal(v, &caps); err != nil {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Code:     "malformed",
				Message:  "extensions.com.strike.cli.capabilities has invalid type; ignoring",
			})
		} else {
			ext.Capabilities = caps
		}
	}
	if v, ok := obj["digest"]; ok {
		s, err := decodeJSONString(v, "extensions.com.strike.cli.digest")
		if err != nil {
			diags = append(diags, Diagnostic{Severity: SeverityWarning, Code: "malformed", Message: err.Error()})
		} else if strings.TrimSpace(s) != "" {
			if err := validateDigestString(s); err != nil {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Code:     "malformed",
					Message:  "extensions.com.strike.cli.digest: " + err.Error() + "; ignoring",
				})
			} else {
				ext.Digest = strings.TrimSpace(s)
			}
		}
	}
	return ext, diags, nil
}

func validateAPSAuthor(raw json.RawMessage) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fmt.Errorf("author must be an object")
	}
	allowed := map[string]struct{}{"name": {}, "email": {}, "url": {}}
	for k, v := range obj {
		if _, ok := allowed[k]; !ok {
			return fmt.Errorf("author.%s is not permitted", k)
		}
		if _, err := decodeJSONString(v, "author."+k); err != nil {
			return err
		}
	}
	return nil
}

func discoverAPSSkills(root string, base Diagnostic) ([]FileRef, []Diagnostic) {
	var diags []Diagnostic
	skillsDir := filepath.Join(root, "skills")
	st, err := os.Lstat(skillsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		d := base
		d.Severity = SeverityWarning
		d.Code = "malformed"
		d.Path = "skills"
		d.Message = err.Error()
		return nil, []Diagnostic{d}
	}
	resolved, err := confinedExistingPath(root, skillsDir)
	if err != nil {
		d := base
		d.Severity = SeverityError
		d.Code = "path"
		d.Path = "skills"
		d.Message = err.Error()
		return nil, []Diagnostic{d}
	}
	st, err = os.Stat(resolved)
	if err != nil || !st.IsDir() {
		d := base
		d.Severity = SeverityWarning
		d.Code = "malformed"
		d.Path = "skills"
		d.Message = "skills is not a directory; skipping skills"
		return nil, []Diagnostic{d}
	}

	entries, err := os.ReadDir(resolved)
	if err != nil {
		d := base
		d.Severity = SeverityWarning
		d.Code = "malformed"
		d.Path = "skills"
		d.Message = err.Error()
		return nil, []Diagnostic{d}
	}

	var refs []FileRef
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		child := filepath.Join(resolved, name)
		info, err := os.Stat(child)
		if err != nil || !info.IsDir() {
			continue
		}
		rel := filepath.ToSlash(filepath.Join("skills", name, "SKILL.md"))
		abs, err := ResolveUnderRoot(root, rel)
		if err != nil {
			d := base
			d.Severity = SeverityWarning
			d.Code = "path"
			d.Path = rel
			d.Message = err.Error()
			diags = append(diags, d)
			continue
		}
		st, err := os.Stat(abs)
		if err != nil || st.IsDir() {
			d := base
			d.Severity = SeverityWarning
			d.Code = "malformed"
			d.Path = rel
			if err != nil {
				d.Message = "skipping invalid skill: SKILL.md not found"
			} else {
				d.Message = "skipping invalid skill: SKILL.md is not a regular file"
			}
			diags = append(diags, d)
			continue
		}
		if !st.Mode().IsRegular() {
			d := base
			d.Severity = SeverityWarning
			d.Code = "malformed"
			d.Path = rel
			d.Message = "skipping invalid skill: SKILL.md is not a regular file"
			diags = append(diags, d)
			continue
		}
		refs = append(refs, FileRef{
			PluginID: base.PluginID,
			Version:  base.Version,
			Source:   base.Source,
			RelPath:  rel,
			AbsPath:  abs,
		})
	}
	return refs, diags
}

func confinedExistingPath(root, path string) (string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolved
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if !isUnder(rootAbs, abs) {
		return "", fmt.Errorf("path %q escapes plugin root", path)
	}
	return abs, nil
}

type apsMCPFile struct {
	disabled bool
	servers  []apsMCPServer
}

type apsMCPServer struct {
	Name      string
	Type      string
	Command   string
	Args      []string
	Env       map[string]string
	Cwd       string
	URL       string
	Headers   map[string]string
	Skip      bool
	SkipCode  string
	SkipMsg   string
	Transport string // stdio | http after mapping
}

func loadAPSMCPFile(root string) (apsMCPFile, []Diagnostic, error) {
	path := filepath.Join(root, "mcp.json")
	st, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return apsMCPFile{}, nil, nil
		}
		return apsMCPFile{}, nil, err
	}
	abs, err := confinedExistingPath(root, path)
	if err != nil {
		return apsMCPFile{disabled: true}, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "path",
			Path:     "mcp.json",
			Message:  err.Error() + "; MCP disabled for this plugin",
		}}, nil
	}
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() {
		msg := "mcp.json is not a regular file; MCP disabled for this plugin"
		if err != nil {
			msg = err.Error() + "; MCP disabled for this plugin"
		}
		return apsMCPFile{disabled: true}, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "malformed",
			Path:     "mcp.json",
			Message:  msg,
		}}, nil
	}
	_ = st

	data, err := os.ReadFile(abs)
	if err != nil {
		return apsMCPFile{disabled: true}, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "malformed",
			Path:     "mcp.json",
			Message:  err.Error() + "; MCP disabled for this plugin",
		}}, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return apsMCPFile{disabled: true}, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "malformed",
			Path:     "mcp.json",
			Message:  "mcp.json is not valid JSON; MCP disabled for this plugin",
		}}, nil
	}
	schema := jsonRawString(obj["$schema"])
	if schema != APSMCPSchemaV1 {
		return apsMCPFile{disabled: true}, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "schema_version",
			Path:     "mcp.json",
			Message:  fmt.Sprintf("unsupported or mismatched mcp.json $schema %q; MCP disabled for this plugin", schema),
		}}, nil
	}
	if _, ok := obj["mcpServers"]; !ok {
		return apsMCPFile{disabled: true}, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "malformed",
			Path:     "mcp.json",
			Message:  "mcp.json missing mcpServers; MCP disabled for this plugin",
		}}, nil
	}
	for k := range obj {
		if k == "$schema" || k == "mcpServers" {
			continue
		}
		return apsMCPFile{disabled: true}, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "malformed",
			Path:     "mcp.json",
			Message:  fmt.Sprintf("unknown top-level mcp.json field %q; MCP disabled for this plugin", k),
		}}, nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(obj["mcpServers"], &servers); err != nil {
		return apsMCPFile{disabled: true}, []Diagnostic{{
			Severity: SeverityWarning,
			Code:     "malformed",
			Path:     "mcp.json",
			Message:  "mcpServers must be an object; MCP disabled for this plugin",
		}}, nil
	}

	var names []string
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []apsMCPServer
	var diags []Diagnostic
	for _, name := range names {
		srv, d := parseAPSServer(name, servers[name])
		if d.Message != "" {
			diags = append(diags, d)
		}
		out = append(out, srv)
	}
	return apsMCPFile{servers: out}, diags, nil
}

func parseAPSServer(name string, raw json.RawMessage) (apsMCPServer, Diagnostic) {
	base := Diagnostic{
		Severity: SeverityWarning,
		Code:     "malformed",
		Path:     "mcp.json",
	}
	srv := apsMCPServer{Name: name}
	if name == "" || !validMCPServerName(name) {
		srv.Skip = true
		srv.SkipCode = "malformed"
		srv.SkipMsg = fmt.Sprintf("mcp server %q: invalid name; skipped", name)
		base.Message = srv.SkipMsg
		return srv, base
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		srv.Skip = true
		srv.SkipMsg = fmt.Sprintf("mcp server %q: not an object; skipped", name)
		base.Message = srv.SkipMsg
		return srv, base
	}
	typ := jsonRawString(obj["type"])
	srv.Type = typ
	switch typ {
	case "stdio":
		return parseAPSStdioServer(name, obj, srv)
	case "streamable-http":
		return parseAPSHTTPServer(name, obj, srv, "http")
	case "sse":
		srv.Skip = true
		srv.SkipCode = "unsupported_transport"
		srv.SkipMsg = fmt.Sprintf("mcp server %q: transport %q not supported; skipped", name, typ)
		base.Code = "unsupported_transport"
		base.Message = srv.SkipMsg
		return srv, base
	default:
		srv.Skip = true
		srv.SkipCode = "unsupported_transport"
		srv.SkipMsg = fmt.Sprintf("mcp server %q: unsupported transport %q; skipped", name, typ)
		base.Code = "unsupported_transport"
		base.Message = srv.SkipMsg
		return srv, base
	}
}

func parseAPSStdioServer(name string, obj map[string]json.RawMessage, srv apsMCPServer) (apsMCPServer, Diagnostic) {
	base := Diagnostic{Severity: SeverityWarning, Code: "malformed", Path: "mcp.json"}
	allowed := map[string]struct{}{"type": {}, "command": {}, "args": {}, "env": {}, "cwd": {}}
	for k := range obj {
		if _, ok := allowed[k]; !ok {
			srv.Skip = true
			srv.SkipMsg = fmt.Sprintf("mcp server %q: unknown field %q; skipped", name, k)
			base.Message = srv.SkipMsg
			return srv, base
		}
	}
	cmd, err := requireMapString(obj, "command")
	if err != nil {
		srv.Skip = true
		srv.SkipMsg = fmt.Sprintf("mcp server %q: %v; skipped", name, err)
		base.Message = srv.SkipMsg
		return srv, base
	}
	if strings.ContainsAny(cmd, " \t") {
		srv.Skip = true
		srv.SkipMsg = fmt.Sprintf("mcp server %q: command must be a single token; skipped", name)
		base.Message = srv.SkipMsg
		return srv, base
	}
	srv.Command = cmd
	if v, ok := obj["args"]; ok {
		var args []string
		if err := json.Unmarshal(v, &args); err != nil {
			srv.Skip = true
			srv.SkipMsg = fmt.Sprintf("mcp server %q: args must be an array of strings; skipped", name)
			base.Message = srv.SkipMsg
			return srv, base
		}
		srv.Args = args
	}
	if v, ok := obj["env"]; ok {
		var env map[string]string
		if err := json.Unmarshal(v, &env); err != nil {
			srv.Skip = true
			srv.SkipMsg = fmt.Sprintf("mcp server %q: env must be an object of strings; skipped", name)
			base.Message = srv.SkipMsg
			return srv, base
		}
		if _, ok := env["PLUGIN_ROOT"]; ok {
			srv.Skip = true
			srv.SkipMsg = fmt.Sprintf("mcp server %q: env must not contain PLUGIN_ROOT; skipped", name)
			base.Message = srv.SkipMsg
			return srv, base
		}
		if _, ok := env["PLUGIN_DATA"]; ok {
			srv.Skip = true
			srv.SkipMsg = fmt.Sprintf("mcp server %q: env must not contain PLUGIN_DATA; skipped", name)
			base.Message = srv.SkipMsg
			return srv, base
		}
		srv.Env = env
	}
	if v, ok := obj["cwd"]; ok {
		s, err := decodeJSONString(v, "cwd")
		if err != nil {
			srv.Skip = true
			srv.SkipMsg = fmt.Sprintf("mcp server %q: cwd must be a string; skipped", name)
			base.Message = srv.SkipMsg
			return srv, base
		}
		if err := validateAPSCWDForm(s); err != nil {
			srv.Skip = true
			srv.SkipMsg = fmt.Sprintf("mcp server %q: %v; skipped", name, err)
			base.Message = srv.SkipMsg
			return srv, base
		}
		srv.Cwd = s
	}
	srv.Transport = "stdio"
	return srv, Diagnostic{}
}

func parseAPSHTTPServer(name string, obj map[string]json.RawMessage, srv apsMCPServer, transport string) (apsMCPServer, Diagnostic) {
	base := Diagnostic{Severity: SeverityWarning, Code: "malformed", Path: "mcp.json"}
	allowed := map[string]struct{}{"type": {}, "url": {}, "headers": {}}
	for k := range obj {
		if _, ok := allowed[k]; !ok {
			srv.Skip = true
			srv.SkipMsg = fmt.Sprintf("mcp server %q: unknown field %q; skipped", name, k)
			base.Message = srv.SkipMsg
			return srv, base
		}
	}
	u, err := requireMapString(obj, "url")
	if err != nil {
		srv.Skip = true
		srv.SkipMsg = fmt.Sprintf("mcp server %q: %v; skipped", name, err)
		base.Message = srv.SkipMsg
		return srv, base
	}
	if err := validateAPSMCPURL(u); err != nil {
		srv.Skip = true
		srv.SkipMsg = fmt.Sprintf("mcp server %q: %v; skipped", name, err)
		base.Message = srv.SkipMsg
		return srv, base
	}
	srv.URL = u
	if v, ok := obj["headers"]; ok {
		var headers map[string]string
		if err := json.Unmarshal(v, &headers); err != nil {
			srv.Skip = true
			srv.SkipMsg = fmt.Sprintf("mcp server %q: headers must be an object of strings; skipped", name)
			base.Message = srv.SkipMsg
			return srv, base
		}
		seen := map[string]string{}
		for k := range headers {
			lk := strings.ToLower(k)
			if prev, ok := seen[lk]; ok {
				srv.Skip = true
				srv.SkipMsg = fmt.Sprintf("mcp server %q: duplicate header %q/%q; skipped", name, prev, k)
				base.Message = srv.SkipMsg
				return srv, base
			}
			seen[lk] = k
		}
		srv.Headers = headers
	}
	srv.Transport = transport
	return srv, Diagnostic{}
}

func validateAPSCWDForm(cwd string) error {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return fmt.Errorf("cwd is empty")
	}
	switch {
	case cwd == placeholderRoot || strings.HasPrefix(cwd, placeholderRoot+"/"):
		return nil
	case cwd == placeholderData || strings.HasPrefix(cwd, placeholderData+"/"):
		return nil
	case strings.HasPrefix(cwd, "./"):
		return nil
	default:
		return fmt.Errorf("cwd %q is not ./, ${PLUGIN_ROOT}, or ${PLUGIN_DATA} rooted", cwd)
	}
}

func validateAPSMCPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url must be http or https")
	}
	if !u.IsAbs() {
		return fmt.Errorf("url must be absolute")
	}
	if u.User != nil {
		return fmt.Errorf("url must not contain user information")
	}
	if u.Fragment != "" {
		return fmt.Errorf("url must not contain a fragment")
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("non-loopback http url requires https")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	h := strings.TrimSpace(host)
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func resolveAPSCommand(root, command string) (string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return "", fmt.Errorf("empty command")
	}
	if strings.ContainsRune(command, os.PathSeparator) || strings.Contains(command, "/") || strings.Contains(command, `\`) {
		if !strings.HasPrefix(command, "./") {
			return "", fmt.Errorf("plugin-relative command must start with ./")
		}
		rel := strings.TrimPrefix(command, "./")
		if err := validateRelPathSyntax(rel); err != nil {
			return "", err
		}
		return ResolveUnderRoot(root, rel)
	}
	// Bare executable name: platform search; do not resolve under plugin root.
	if strings.Contains(command, "..") {
		return "", fmt.Errorf("bare command must not contain '..'")
	}
	return command, nil
}

func expandPluginPlaceholders(s, pluginRoot, pluginData string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if strings.HasPrefix(s[i:], placeholderRoot) {
			b.WriteString(pluginRoot)
			i += len(placeholderRoot)
			continue
		}
		if strings.HasPrefix(s[i:], placeholderData) {
			b.WriteString(pluginData)
			i += len(placeholderData)
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func resolveAPSCWD(root, dataDir, cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		return root, nil
	}
	expanded := expandPluginPlaceholders(cwd, root, dataDir)
	switch {
	case cwd == placeholderData || strings.HasPrefix(cwd, placeholderData+"/"):
		return confineUnder(dataDir, expanded, "PLUGIN_DATA")
	case strings.HasPrefix(cwd, "./"):
		rel := strings.TrimPrefix(cwd, "./")
		return ResolveUnderRoot(root, rel)
	default:
		return confineUnder(root, expanded, "plugin root")
	}
}

func confineUnder(base, path, label string) (string, error) {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(baseAbs); err == nil {
		baseAbs = resolved
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	if !isUnder(baseAbs, clean) && baseAbs != clean {
		return "", fmt.Errorf("path %q escapes %s", path, label)
	}
	if fi, err := os.Lstat(clean); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || fi.IsDir() {
			if resolved, err := filepath.EvalSymlinks(clean); err == nil {
				if !isUnder(baseAbs, resolved) && baseAbs != resolved {
					return "", fmt.Errorf("path %q escapes %s via symlink", path, label)
				}
				return resolved, nil
			}
		}
	}
	return clean, nil
}

func resolvedPluginRoot(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return root
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func apsHasExecutableMCP(root string) bool {
	f, _, err := loadAPSMCPFile(root)
	if err != nil || f.disabled {
		return false
	}
	for _, s := range f.servers {
		if !s.Skip {
			return true
		}
	}
	return false
}

func inferAPSMCPCaps(root string, set map[string]struct{}) {
	f, _, err := loadAPSMCPFile(root)
	if err != nil || f.disabled {
		return
	}
	for _, s := range f.servers {
		if s.Skip {
			continue
		}
		switch s.Transport {
		case "http":
			set[CapMCPHTTP] = struct{}{}
		default:
			set[CapMCPStdio] = struct{}{}
		}
	}
}

func jsonRawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func requireJSONString(raw map[string]json.RawMessage, key string) (string, error) {
	v, ok := raw[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	return decodeJSONString(v, key)
}

func requireMapString(obj map[string]json.RawMessage, key string) (string, error) {
	v, ok := obj[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	return decodeJSONString(v, key)
}

func decodeJSONString(raw json.RawMessage, key string) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return s, nil
}

func decodeJSONStringSlice(raw json.RawMessage, key string) error {
	var ss []string
	if err := json.Unmarshal(raw, &ss); err != nil {
		return fmt.Errorf("%s must be an array of strings", key)
	}
	return nil
}

type unsupportedSchemaError struct {
	schema string
}

func (e unsupportedSchemaError) Error() string {
	return fmt.Sprintf("unsupported Agent Plugins $schema %q", e.schema)
}
