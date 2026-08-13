package plugin

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/version"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// DoctorOptions controls Doctor.
type DoctorOptions struct {
	// ID limits to one plugin; empty diagnoses all installed.
	ID            string
	WorkDir       string
	GlobalRoot    string
	ProjectRoot   string
	StrikeVersion string
}

// DoctorReport is a structured diagnosis (safe for printing — no secrets).
type DoctorReport struct {
	Plugins []DoctorPlugin `json:"plugins"`
	// Findings are cross-plugin or lockfile-level issues.
	Findings []Diagnostic `json:"findings,omitempty"`
}

// DoctorPlugin is per-plugin doctor output.
type DoctorPlugin struct {
	ID            string          `json:"id"`
	Version       string          `json:"version,omitempty"`
	Name          string          `json:"name,omitempty"`
	Scope         Scope           `json:"scope"`
	Enabled       bool            `json:"enabled"`
	Root          string          `json:"root"`
	Digest        string          `json:"digest,omitempty"`
	LockDigest    string          `json:"lockDigest,omitempty"`
	Source        *SourceIdentity `json:"source,omitempty"`
	TrustState    string          `json:"trustState"` // none | trusted | stale | n/a-passive-only
	Contributions DoctorContribs  `json:"contributions"`
	Findings      []Diagnostic    `json:"findings,omitempty"`
}

// DoctorContribs summarizes contribution counts and safe executable metadata.
type DoctorContribs struct {
	Agents    int             `json:"agents"`
	Skills    int             `json:"skills"`
	Workflows int             `json:"workflows"`
	Themes    int             `json:"themes"`
	Providers int             `json:"providers"`
	MCP       []DoctorMCP     `json:"mcp,omitempty"`
	Harnesses []DoctorHarness `json:"harnesses,omitempty"`
	Hooks     int             `json:"hooks,omitempty"`
	Panes     int             `json:"panes,omitempty"`
}

// DoctorMCP is MCP contribution metadata without env/header values.
type DoctorMCP struct {
	Name       string   `json:"name"`
	Transport  string   `json:"transport,omitempty"`
	Command    string   `json:"command,omitempty"` // path only
	Args       []string `json:"args,omitempty"`
	EnvKeys    []string `json:"envKeys,omitempty"` // names only — never values
	URL        string   `json:"url,omitempty"`     // redacted if credential-shaped
	HeaderKeys []string `json:"headerKeys,omitempty"`
}

// DoctorHarness is harness metadata (command path only).
type DoctorHarness struct {
	Name    string   `json:"name"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// Doctor inspects installed plugins: paths, provenance, collisions, trust state.
// Never prints secrets or executable environment values.
func Doctor(opts DoctorOptions) (DoctorReport, error) {
	strikeVer := opts.StrikeVersion
	if strikeVer == "" {
		strikeVer = version.Version
	}
	list, lockDiags, err := ListInstalled(ListOptions{
		WorkDir:       opts.WorkDir,
		GlobalRoot:    opts.GlobalRoot,
		ProjectRoot:   opts.ProjectRoot,
		StrikeVersion: strikeVer,
	})
	if err != nil {
		return DoctorReport{}, err
	}

	// Full discovery for collision-style load diagnostics (enabled only).
	disc := Discover(Options{
		WorkDir:       opts.WorkDir,
		GlobalRoot:    opts.GlobalRoot,
		ProjectRoot:   opts.ProjectRoot,
		StrikeVersion: strikeVer,
	})

	var report DoctorReport
	report.Findings = append(report.Findings, lockDiags...)

	wantID := strings.TrimSpace(opts.ID)
	for _, ip := range list {
		if wantID != "" && ip.ID != wantID {
			continue
		}
		dp := doctorOne(ip, strikeVer)
		// Attach discovery diagnostics for this plugin.
		for _, d := range disc.Diagnostics {
			if d.PluginID == ip.ID || (d.PluginID == "" && d.Path != "" && strings.Contains(d.Path, ip.Root)) {
				dp.Findings = append(dp.Findings, scrubDiagnostic(d))
			}
		}
		report.Plugins = append(report.Plugins, dp)
	}

	// Contribution name collisions among enabled plugins (passive names).
	report.Findings = append(report.Findings, findPassiveCollisions(disc.Plugins)...)

	if wantID != "" && len(report.Plugins) == 0 {
		return report, fmt.Errorf("plugin %q not found", wantID)
	}
	return report, nil
}

func doctorOne(ip InstalledPlugin, strikeVer string) DoctorPlugin {
	dp := DoctorPlugin{
		ID:         ip.ID,
		Version:    ip.Version,
		Name:       ip.Name,
		Scope:      ip.Scope,
		Enabled:    ip.Enabled,
		Root:       ip.Root,
		Digest:     ip.Digest,
		Source:     scrubSource(ip.Source),
		TrustState: "n/a-passive-only",
	}
	if ip.LoadError != "" {
		dp.Findings = append(dp.Findings, Diagnostic{
			Severity: SeverityError,
			Code:     "malformed",
			Message:  redact.String(ip.LoadError),
			PluginID: ip.ID,
			Source:   ip.Scope,
			Path:     ip.Root,
		})
		return dp
	}

	// Recompute digest and compare to lock.
	if got, err := ComputeDigest(ip.Root); err != nil {
		dp.Findings = append(dp.Findings, Diagnostic{
			Severity: SeverityError,
			Code:     "digest",
			Message:  err.Error(),
			PluginID: ip.ID,
			Source:   ip.Scope,
		})
	} else {
		dp.Digest = got
		if ip.Digest != "" && ip.Digest != got {
			dp.LockDigest = ip.Digest
			dp.Findings = append(dp.Findings, Diagnostic{
				Severity: SeverityWarning,
				Code:     "digest",
				Message:  fmt.Sprintf("lockfile digest %s differs from computed %s", ip.Digest, got),
				PluginID: ip.ID,
				Source:   ip.Scope,
			})
		}
	}

	m := ip.Manifest
	if m == nil {
		if mm, _, err := ReadManifest(ip.Root); err == nil {
			m = &mm
		}
	}
	if m == nil {
		return dp
	}
	dp.Version = m.Version
	dp.Name = m.Name

	// Passive-run loadOne for path/version issues without requiring enablement.
	if p, diags := loadOne(ip.Root, ip.Scope, strikeVer); p != nil {
		dp.Contributions.Agents = len(p.Agents)
		dp.Contributions.Skills = len(p.Skills)
		dp.Contributions.Workflows = len(p.Workflows)
		dp.Contributions.Themes = len(p.Themes)
		dp.Contributions.Providers = len(p.Providers)
	} else {
		for _, d := range diags {
			if d.Severity == SeverityError {
				dp.Findings = append(dp.Findings, scrubDiagnostic(d))
			}
		}
	}

	// Executable contributions: summarize without secrets; report trust state.
	mcp, harnesses, hooks, panes := summarizeExecutables(m.Contributions)
	if m.Format == FormatAPS {
		mcp = summarizeAPSMCP(ip.Root)
	}
	dp.Contributions.MCP = mcp
	dp.Contributions.Harnesses = harnesses
	dp.Contributions.Hooks = hooks
	dp.Contributions.Panes = panes
	if HasExecutableContributionsAt(*m, ip.Root) {
		caps := InferCapabilitiesAt(*m, ip.Root)
		match := MatchTrust(ip.Trust, dp.Digest, ip.Source, caps)
		dp.TrustState = match.State
		if match.OK {
			dp.Findings = append(dp.Findings, Diagnostic{
				Severity: SeverityInfo,
				Code:     "trust",
				Message:  "executable contributions trusted for current digest and source",
				PluginID: ip.ID,
				Version:  m.Version,
				Source:   ip.Scope,
			})
		} else {
			msg := match.Reason
			if msg == "" {
				msg = "executable contributions present; run: strike plugin trust " + ip.ID
			}
			dp.Findings = append(dp.Findings, Diagnostic{
				Severity: SeverityInfo,
				Code:     "trust",
				Message:  msg,
				PluginID: ip.ID,
				Version:  m.Version,
				Source:   ip.Scope,
			})
		}
	}

	if m.Source != nil {
		// Author-declared source hint only; do not print raw if it looks secret-bearing.
		_ = m.Source
	}
	return dp
}

func summarizeExecutables(c Contributions) (mcp []DoctorMCP, harnesses []DoctorHarness, hooks, panes int) {
	hooks = len(c.Hooks)
	panes = len(c.Panes)
	for _, raw := range c.MCP {
		var m struct {
			Name      string            `json:"name"`
			Transport string            `json:"transport"`
			Command   string            `json:"command"`
			Args      []string          `json:"args"`
			Env       map[string]string `json:"env"`
			URL       string            `json:"url"`
			Headers   map[string]string `json:"headers"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			mcp = append(mcp, DoctorMCP{Name: "(invalid)"})
			continue
		}
		var envKeys []string
		for k := range m.Env {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
		var headerKeys []string
		for k := range m.Headers {
			headerKeys = append(headerKeys, k)
		}
		sort.Strings(headerKeys)
		mcp = append(mcp, DoctorMCP{
			Name:       m.Name,
			Transport:  m.Transport,
			Command:    m.Command,
			Args:       m.Args,
			EnvKeys:    envKeys,
			URL:        scrubURL(m.URL),
			HeaderKeys: headerKeys,
		})
	}
	for _, raw := range c.Harnesses {
		var h struct {
			Name    string   `json:"name"`
			Command string   `json:"command"`
			Args    []string `json:"args"`
		}
		if err := json.Unmarshal(raw, &h); err != nil {
			harnesses = append(harnesses, DoctorHarness{Name: "(invalid)"})
			continue
		}
		harnesses = append(harnesses, DoctorHarness{
			Name:    h.Name,
			Command: h.Command,
			Args:    h.Args,
		})
	}
	return mcp, harnesses, hooks, panes
}

func summarizeAPSMCP(root string) []DoctorMCP {
	f, _, err := loadAPSMCPFile(root)
	if err != nil || f.disabled {
		return nil
	}
	var mcp []DoctorMCP
	for _, s := range f.servers {
		if s.Skip {
			continue
		}
		var envKeys []string
		for k := range s.Env {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
		var headerKeys []string
		for k := range s.Headers {
			headerKeys = append(headerKeys, k)
		}
		sort.Strings(headerKeys)
		mcp = append(mcp, DoctorMCP{
			Name:       s.Name,
			Transport:  s.Transport,
			Command:    s.Command,
			Args:       s.Args,
			EnvKeys:    envKeys,
			URL:        scrubURL(s.URL),
			HeaderKeys: headerKeys,
		})
	}
	return mcp
}

func findPassiveCollisions(plugins []Plugin) []Diagnostic {
	type owner struct {
		pluginID string
		scope    Scope
		path     string
	}
	// name → first owner; subsequent are collisions (higher precedence wins at load).
	seenAgent := map[string]owner{}
	seenSkill := map[string]owner{}
	seenWorkflow := map[string]owner{}
	seenTheme := map[string]owner{}
	var diags []Diagnostic

	note := func(kind, name string, ref FileRef, seen map[string]owner) {
		// Derive contribution public name from filename stem when we only have path.
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(ref.RelPath), filepath.Ext(ref.RelPath))
		}
		o := owner{pluginID: ref.PluginID, scope: ref.Source, path: ref.RelPath}
		if prev, ok := seen[name]; ok {
			diags = append(diags, Diagnostic{
				Severity:  SeverityWarning,
				Code:      "collision",
				Message:   fmt.Sprintf("%s %q from plugin %s shadows %s", kind, name, ref.PluginID, prev.pluginID),
				PluginID:  ref.PluginID,
				Version:   ref.Version,
				Source:    ref.Source,
				Path:      ref.RelPath,
				Collision: name,
			})
			return
		}
		seen[name] = o
	}

	for _, p := range plugins {
		for _, ref := range p.Agents {
			note("agent", "", ref, seenAgent)
		}
		for _, ref := range p.Skills {
			note("skill", "", ref, seenSkill)
		}
		for _, ref := range p.Workflows {
			note("workflow", "", ref, seenWorkflow)
		}
		for _, ref := range p.Themes {
			note("theme", "", ref, seenTheme)
		}
	}
	return diags
}

func scrubSource(s *SourceIdentity) *SourceIdentity {
	if s == nil {
		return nil
	}
	out := *s
	out.URL = scrubURL(out.URL)
	return &out
}

// scrubURL redacts userinfo and credential-shaped spans in URLs.
func scrubURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return u
	}
	if i := strings.Index(u, "://"); i >= 0 {
		rest := u[i+3:]
		if at := strings.Index(rest, "@"); at > 0 {
			// Always redact userinfo (user or user:pass).
			u = u[:i+3] + "[REDACTED]@" + rest[at+1:]
		}
	}
	return redact.String(u)
}

func scrubDiagnostic(d Diagnostic) Diagnostic {
	d.Message = redact.String(d.Message)
	return d
}

// FormatDoctorText renders a human-readable doctor report (no secrets).
func FormatDoctorText(r DoctorReport) string {
	var b strings.Builder
	if len(r.Plugins) == 0 {
		b.WriteString("No plugins installed.\n")
	}
	for _, p := range r.Plugins {
		fmt.Fprintf(&b, "plugin %s", p.ID)
		if p.Version != "" {
			fmt.Fprintf(&b, "@%s", p.Version)
		}
		fmt.Fprintf(&b, " (%s)\n", p.Scope)
		if p.Name != "" {
			fmt.Fprintf(&b, "  name:      %s\n", p.Name)
		}
		fmt.Fprintf(&b, "  enabled:   %v\n", p.Enabled)
		fmt.Fprintf(&b, "  root:      %s\n", p.Root)
		if p.Digest != "" {
			fmt.Fprintf(&b, "  digest:    %s\n", p.Digest)
		}
		if p.Source != nil {
			fmt.Fprintf(&b, "  source:    %s\n", p.Source.String())
		}
		fmt.Fprintf(&b, "  trust:     %s\n", p.TrustState)
		c := p.Contributions
		fmt.Fprintf(&b, "  contribs:  agents=%d skills=%d workflows=%d themes=%d providers=%d\n",
			c.Agents, c.Skills, c.Workflows, c.Themes, c.Providers)
		for _, m := range c.MCP {
			fmt.Fprintf(&b, "    mcp %s transport=%s", m.Name, m.Transport)
			if m.Command != "" {
				fmt.Fprintf(&b, " command=%s", m.Command)
			}
			if len(m.EnvKeys) > 0 {
				fmt.Fprintf(&b, " envKeys=%s", strings.Join(m.EnvKeys, ","))
			}
			if m.URL != "" {
				fmt.Fprintf(&b, " url=%s", m.URL)
			}
			b.WriteByte('\n')
		}
		for _, h := range c.Harnesses {
			fmt.Fprintf(&b, "    harness %s command=%s\n", h.Name, h.Command)
		}
		for _, f := range p.Findings {
			fmt.Fprintf(&b, "  - %s\n", f.String())
		}
		b.WriteByte('\n')
	}
	if len(r.Findings) > 0 {
		b.WriteString("Global findings:\n")
		for _, f := range r.Findings {
			fmt.Fprintf(&b, "  - %s\n", f.String())
		}
	}
	return b.String()
}
