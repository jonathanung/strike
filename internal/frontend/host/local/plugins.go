package local

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/integrate/plugin"
	"github.com/jonathanung/strike-cli/internal/product/version"
)

// NewPlugins builds a host.Plugins backed by internal/integrate/plugin lifecycle APIs.
// workDir scopes project installs; empty still allows global operations.
func NewPlugins(workDir string) host.Plugins {
	return pluginsAdapter{workDir: strings.TrimSpace(workDir)}
}

// NewPluginsForTest is like NewPlugins but pins roots for unit tests.
func NewPluginsForTest(workDir, globalRoot, projectRoot string) host.Plugins {
	return pluginsAdapter{
		workDir:     strings.TrimSpace(workDir),
		globalRoot:  strings.TrimSpace(globalRoot),
		projectRoot: strings.TrimSpace(projectRoot),
	}
}

type pluginsAdapter struct {
	workDir     string
	globalRoot  string
	projectRoot string
	strikeVer   string
}

func (a pluginsAdapter) strikeVersion() string {
	if a.strikeVer != "" {
		return a.strikeVer
	}
	return version.Version
}

func (a pluginsAdapter) manageOpts(id, scope string) plugin.EnableOptions {
	return plugin.EnableOptions{
		ID:          strings.TrimSpace(id),
		Scope:       parsePluginScope(scope),
		WorkDir:     a.workDir,
		GlobalRoot:  a.globalRoot,
		ProjectRoot: a.projectRoot,
	}
}

func parsePluginScope(s string) plugin.Scope {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "global":
		return plugin.ScopeGlobal
	case "project":
		return plugin.ScopeProject
	default:
		return ""
	}
}

func pluginScopeString(s plugin.Scope) string {
	switch s {
	case plugin.ScopeGlobal:
		return host.PluginScopeGlobal
	case plugin.ScopeProject:
		return host.PluginScopeProject
	default:
		return string(s)
	}
}

func (a pluginsAdapter) List() ([]host.PluginInfo, error) {
	report, err := plugin.Doctor(plugin.DoctorOptions{
		WorkDir:       a.workDir,
		GlobalRoot:    a.globalRoot,
		ProjectRoot:   a.projectRoot,
		StrikeVersion: a.strikeVersion(),
	})
	if err != nil {
		return nil, err
	}
	byID := map[string][]string{}
	var globalFindings []string
	for _, f := range report.Findings {
		msg := strings.TrimSpace(f.String())
		if msg == "" {
			continue
		}
		if f.PluginID != "" {
			byID[f.PluginID] = append(byID[f.PluginID], msg)
		} else {
			globalFindings = append(globalFindings, msg)
		}
	}
	out := make([]host.PluginInfo, 0, len(report.Plugins))
	for _, dp := range report.Plugins {
		info := doctorPluginToInfo(dp)
		info.Findings = append(info.Findings, byID[dp.ID]...)
		// Attach unscoped findings once on the first row only.
		if len(out) == 0 {
			info.Findings = append(info.Findings, globalFindings...)
		}
		out = append(out, info)
	}
	return out, nil
}

func (a pluginsAdapter) Inspect(id, scope string) (host.PluginInfo, error) {
	id = strings.TrimSpace(id)
	report, err := plugin.Doctor(plugin.DoctorOptions{
		ID:            id,
		WorkDir:       a.workDir,
		GlobalRoot:    a.globalRoot,
		ProjectRoot:   a.projectRoot,
		StrikeVersion: a.strikeVersion(),
	})
	if err != nil {
		return host.PluginInfo{}, err
	}
	wantScope := parsePluginScope(scope)
	var match *plugin.DoctorPlugin
	for i := range report.Plugins {
		p := &report.Plugins[i]
		if p.ID != id {
			continue
		}
		if wantScope != "" && p.Scope != wantScope {
			continue
		}
		if match == nil || p.Scope == plugin.ScopeProject {
			match = p
		}
	}
	if match == nil {
		return host.PluginInfo{}, fmt.Errorf("plugin %q not found", id)
	}
	info := doctorPluginToInfo(*match)
	for _, f := range report.Findings {
		if f.PluginID != "" && f.PluginID != match.ID {
			continue
		}
		if msg := strings.TrimSpace(f.String()); msg != "" {
			info.Findings = append(info.Findings, msg)
		}
	}
	return info, nil
}

func doctorPluginToInfo(dp plugin.DoctorPlugin) host.PluginInfo {
	info := host.PluginInfo{
		ID:           dp.ID,
		Version:      dp.Version,
		Name:         dp.Name,
		DisplayName:  dp.DisplayName,
		Format:       string(dp.Format),
		Schema:       dp.Schema,
		Scope:        pluginScopeString(dp.Scope),
		Enabled:      dp.Enabled,
		Digest:       dp.Digest,
		TrustState:   dp.TrustState,
		Agents:       dp.Contributions.Agents,
		Skills:       dp.Contributions.Skills,
		Workflows:    dp.Contributions.Workflows,
		Themes:       dp.Contributions.Themes,
		Providers:    dp.Contributions.Providers,
		Hooks:        dp.Contributions.Hooks,
		Panes:        dp.Contributions.Panes,
		Capabilities: append([]string(nil), dp.Capabilities...),
	}
	if dp.Enabled {
		info.Status = "enabled"
	} else {
		info.Status = "disabled"
	}
	for _, f := range dp.Findings {
		if f.Code == "malformed" || f.Severity == plugin.SeverityError {
			info.Status = "invalid"
			if info.LoadError == "" {
				info.LoadError = f.Message
			}
		}
		if msg := strings.TrimSpace(f.String()); msg != "" {
			info.Findings = append(info.Findings, msg)
		}
	}
	if dp.Source != nil {
		info.SourceType = string(dp.Source.Type)
		info.SourceLabel = dp.Source.String()
	}
	for _, m := range dp.Contributions.MCP {
		info.MCP = append(info.MCP, host.PluginMCP{
			Name:       m.Name,
			Transport:  m.Transport,
			Command:    m.Command,
			Args:       append([]string(nil), m.Args...),
			EnvKeys:    append([]string(nil), m.EnvKeys...),
			URL:        m.URL,
			HeaderKeys: append([]string(nil), m.HeaderKeys...),
		})
	}
	for _, h := range dp.Contributions.Harnesses {
		info.Harnesses = append(info.Harnesses, host.PluginHarness{
			Name:    h.Name,
			Command: h.Command,
			Args:    append([]string(nil), h.Args...),
		})
	}
	hasProcessPanes := false
	if info.Panes > 0 && dp.Root != "" {
		if mm, _, err := plugin.ReadManifest(dp.Root); err == nil {
			hasProcessPanes = plugin.HasProcessPanes(mm, dp.Root)
		}
	}
	info.HasExecutable = len(info.MCP) > 0 || len(info.Harnesses) > 0 || info.Hooks > 0 || hasProcessPanes
	if info.HasExecutable || info.Panes > 0 {
		var caps []string
		for _, m := range info.MCP {
			switch strings.ToLower(strings.TrimSpace(m.Transport)) {
			case "http":
				caps = append(caps, plugin.CapMCPHTTP)
			default:
				caps = append(caps, plugin.CapMCPStdio)
			}
		}
		if len(info.Harnesses) > 0 {
			caps = append(caps, plugin.CapHarnesses)
		}
		if info.Hooks > 0 {
			// Doctor counts all hooks; shell hooks are the trust-relevant subset.
			caps = append(caps, plugin.CapHooksCommand)
		}
		if info.Panes > 0 {
			caps = append(caps, plugin.CapPanes)
		}
		if hasProcessPanes {
			caps = append(caps, plugin.CapPanesProcess)
		}
		info.Capabilities = uniqueSorted(append(info.Capabilities, caps...))
	}
	if info.TrustState == "" {
		if info.HasExecutable {
			info.TrustState = host.PluginTrustNone
		} else {
			info.TrustState = host.PluginTrustPassiveOnly
		}
	}
	return info
}

func uniqueSorted(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	// Stable-ish: already insertion order; sort for determinism.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func (a pluginsAdapter) Enable(id, scope string) error {
	return plugin.Enable(a.manageOpts(id, scope))
}

func (a pluginsAdapter) Disable(id, scope string) error {
	return plugin.Disable(a.manageOpts(id, scope))
}

func (a pluginsAdapter) Remove(id, scope string, confirm bool) error {
	return plugin.Remove(plugin.RemoveOptions{
		ID:          strings.TrimSpace(id),
		Scope:       parsePluginScope(scope),
		WorkDir:     a.workDir,
		GlobalRoot:  a.globalRoot,
		ProjectRoot: a.projectRoot,
		Confirm:     confirm,
	})
}

func (a pluginsAdapter) TrustPreview(id, scope string) (host.PluginTrustPreview, error) {
	info, err := a.Inspect(id, scope)
	if err != nil {
		return host.PluginTrustPreview{}, err
	}
	if info.LoadError != "" {
		return host.PluginTrustPreview{}, fmt.Errorf("plugin %q: %s", id, info.LoadError)
	}
	if !info.HasExecutable {
		return host.PluginTrustPreview{}, fmt.Errorf("plugin %q has no executable contributions to trust", id)
	}
	// Prefer live Trust validation path for digest.
	ip, err := plugin.Inspect(a.manageOpts(id, scope))
	if err != nil {
		return host.PluginTrustPreview{}, err
	}
	digest := info.Digest
	if d, err := plugin.ComputeDigest(ip.Root); err == nil {
		digest = d
	}
	caps := info.Capabilities
	if ip.Manifest != nil {
		caps = plugin.InferCapabilitiesAt(*ip.Manifest, ip.Root)
	}
	prev := host.PluginTrustPreview{
		ID:           info.ID,
		Scope:        info.Scope,
		Digest:       digest,
		Capabilities: caps,
		MCP:          append([]host.PluginMCP(nil), info.MCP...),
		Harnesses:    append([]host.PluginHarness(nil), info.Harnesses...),
		Hooks:        info.Hooks,
	}
	prev.ReviewLines = trustReviewLines(prev)
	return prev, nil
}

func trustReviewLines(p host.PluginTrustPreview) []string {
	var lines []string
	lines = append(lines, fmt.Sprintf("Grant executable trust for %s?", p.ID))
	if p.Digest != "" {
		lines = append(lines, "digest: "+p.Digest)
	}
	if len(p.Capabilities) > 0 {
		lines = append(lines, "capabilities: "+strings.Join(p.Capabilities, ", "))
	}
	if len(p.MCP) > 0 {
		lines = append(lines, fmt.Sprintf("MCP servers (%d) — contribution type: mcp:", len(p.MCP)))
		for _, m := range p.MCP {
			cmd := strings.TrimSpace(m.Command)
			if cmd == "" {
				cmd = strings.TrimSpace(m.URL)
			}
			line := "  - " + m.Name
			if m.Transport != "" {
				line += " [" + m.Transport + "]"
			}
			if cmd != "" {
				line += " command: " + cmd
			}
			if len(m.Args) > 0 {
				line += " " + strings.Join(m.Args, " ")
			}
			if len(m.EnvKeys) > 0 {
				line += " (env keys: " + strings.Join(m.EnvKeys, ", ") + ")"
			}
			if len(m.HeaderKeys) > 0 {
				line += " (header keys: " + strings.Join(m.HeaderKeys, ", ") + ")"
			}
			lines = append(lines, line)
		}
	}
	if len(p.Harnesses) > 0 {
		lines = append(lines, fmt.Sprintf("harnesses (%d) — contribution type: harnesses:", len(p.Harnesses)))
		for _, h := range p.Harnesses {
			line := "  - " + h.Name
			if h.Command != "" {
				line += " command: " + h.Command
			}
			if len(h.Args) > 0 {
				line += " " + strings.Join(h.Args, " ")
			}
			lines = append(lines, line)
		}
	}
	if p.Hooks > 0 {
		lines = append(lines, fmt.Sprintf("hooks: %d — contribution type: hooks (shell hooks require trust)", p.Hooks))
	}
	lines = append(lines, "Passive contributions load without this grant.")
	lines = append(lines, "Changes apply on next Strike launch.")
	return lines
}

func (a pluginsAdapter) Trust(id, scope string) error {
	_, err := plugin.Trust(plugin.TrustOptions{
		ID:            strings.TrimSpace(id),
		Scope:         parsePluginScope(scope),
		WorkDir:       a.workDir,
		GlobalRoot:    a.globalRoot,
		ProjectRoot:   a.projectRoot,
		StrikeVersion: a.strikeVersion(),
	})
	return err
}

func (a pluginsAdapter) Untrust(id, scope string) error {
	return plugin.Untrust(plugin.TrustOptions{
		ID:            strings.TrimSpace(id),
		Scope:         parsePluginScope(scope),
		WorkDir:       a.workDir,
		GlobalRoot:    a.globalRoot,
		ProjectRoot:   a.projectRoot,
		StrikeVersion: a.strikeVersion(),
	})
}

func (a pluginsAdapter) Search(ctx context.Context, registry, query string) ([]host.PluginCatalogHit, error) {
	reg := strings.TrimSpace(registry)
	if reg == "" {
		return nil, fmt.Errorf("registry URL required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cat, _, err := plugin.FetchCatalog(ctx, nil, reg)
	if err != nil {
		return nil, err
	}
	hits := cat.Search(query)
	out := make([]host.PluginCatalogHit, 0, len(hits))
	for _, h := range hits {
		hit := host.PluginCatalogHit{
			ID:           h.ID,
			Name:         h.Name,
			Description:  h.Description,
			Version:      h.Version.Version,
			Registry:     h.Registry,
			Capabilities: append([]string(nil), h.Version.Capabilities...),
		}
		if hit.Registry == "" {
			hit.Registry = cat.Registry
		}
		if hit.Registry == "" {
			hit.Registry = reg
		}
		out = append(out, hit)
	}
	return out, nil
}

func (a pluginsAdapter) Install(ctx context.Context, source, scope, registry string) (host.PluginInstallResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sc := parsePluginScope(scope)
	if sc == "" {
		sc = plugin.ScopeGlobal
	}
	localPath, gitURL, catPkg, catVer, err := plugin.ParseInstallSource(strings.TrimSpace(source))
	if err != nil {
		return host.PluginInstallResult{}, err
	}
	opts := plugin.InstallOptions{
		Scope:         sc,
		WorkDir:       a.workDir,
		GlobalRoot:    a.globalRoot,
		ProjectRoot:   a.projectRoot,
		StrikeVersion: a.strikeVersion(),
		LocalPath:     localPath,
		GitURL:        gitURL,
	}
	if catPkg != "" {
		opts.CatalogPackage = catPkg
		opts.CatalogVersion = catVer
		opts.CatalogRegistry = strings.TrimSpace(registry)
		if opts.CatalogRegistry == "" {
			return host.PluginInstallResult{}, fmt.Errorf("catalog install requires a registry URL")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res, err := plugin.Install(ctx, opts)
	if err != nil {
		return host.PluginInstallResult{}, err
	}
	return host.PluginInstallResult{
		ID:      res.ID,
		Version: res.Version,
		Scope:   pluginScopeString(res.Scope),
		Digest:  res.Digest,
		Enabled: res.Enabled,
	}, nil
}

func (a pluginsAdapter) CheckOutdated(ctx context.Context, registry string) ([]host.PluginInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	items, err := plugin.CheckOutdated(ctx, plugin.OutdatedOptions{
		WorkDir:       a.workDir,
		GlobalRoot:    a.globalRoot,
		ProjectRoot:   a.projectRoot,
		Registry:      strings.TrimSpace(registry),
		StrikeVersion: a.strikeVersion(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]host.PluginInfo, 0, len(items))
	for _, it := range items {
		info, err := a.Inspect(it.Installed.ID, pluginScopeString(it.Installed.Scope))
		if err != nil {
			info = host.PluginInfo{
				ID:      it.Installed.ID,
				Version: it.Installed.Version,
				Scope:   pluginScopeString(it.Installed.Scope),
				Enabled: it.Installed.Enabled,
			}
			if it.Installed.Enabled {
				info.Status = "enabled"
			} else {
				info.Status = "disabled"
			}
		}
		info.UpdateAvailable = it.Latest.Version
		out = append(out, info)
	}
	return out, nil
}

func (a pluginsAdapter) PreviewUpdate(ctx context.Context, id, scope, registry string) (host.PluginUpdateReview, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	review, _, err := plugin.PreviewUpdate(ctx, plugin.UpdateOptions{
		ID:            strings.TrimSpace(id),
		Scope:         parsePluginScope(scope),
		WorkDir:       a.workDir,
		GlobalRoot:    a.globalRoot,
		ProjectRoot:   a.projectRoot,
		Registry:      strings.TrimSpace(registry),
		StrikeVersion: a.strikeVersion(),
	})
	if err != nil {
		return host.PluginUpdateReview{}, err
	}
	return host.PluginUpdateReview{
		ID:                review.ID,
		OldVersion:        review.OldVersion,
		NewVersion:        review.NewVersion,
		OldDigest:         review.OldDigest,
		NewDigest:         review.NewDigest,
		SourceLabel:       review.NewSource.String(),
		CapabilityAdded:   append([]string(nil), review.CapabilityAdded...),
		CapabilityRemoved: append([]string(nil), review.CapabilityRemoved...),
		ContribAdded:      append([]string(nil), review.ContribAdded...),
		ContribRemoved:    append([]string(nil), review.ContribRemoved...),
		ExecutableChanged: review.ExecutableChanged,
		ExecutableDiffs:   append([]string(nil), review.ExecutableDiffs...),
		TrustInvalidated:  review.TrustInvalidated,
		HadTrust:          review.HadTrust,
		Summary:           review.Format(),
	}, nil
}

func (a pluginsAdapter) Update(ctx context.Context, id, scope, registry string, confirm bool) (host.PluginInstallResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	res, err := plugin.Update(ctx, plugin.UpdateOptions{
		ID:            strings.TrimSpace(id),
		Scope:         parsePluginScope(scope),
		WorkDir:       a.workDir,
		GlobalRoot:    a.globalRoot,
		ProjectRoot:   a.projectRoot,
		Registry:      strings.TrimSpace(registry),
		StrikeVersion: a.strikeVersion(),
		Confirm:       confirm,
	})
	if err != nil {
		return host.PluginInstallResult{}, err
	}
	return host.PluginInstallResult{
		ID:      res.Install.ID,
		Version: res.Install.Version,
		Scope:   pluginScopeString(res.Install.Scope),
		Digest:  res.Install.Digest,
		Enabled: res.Install.Enabled,
	}, nil
}
