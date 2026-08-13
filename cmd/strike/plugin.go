package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/plugin"
)

const pluginUsage = `Manage plugin installs (local, Git, catalog) and executable trust.


Usage:
  strike plugin list [--scope global|project]
  strike plugin inspect <id> [--scope global|project]
  strike plugin install <path-or-git-url|catalog:pkg[@ver]> [options]
  strike plugin search <query> --registry <url>
  strike plugin outdated [--registry <url>] [--scope global|project]
  strike plugin update <id> --yes [--registry <url>] [--version <ver>] [--scope global|project]
  strike plugin enable <id> [--scope global|project]
  strike plugin disable <id> [--scope global|project]
  strike plugin trust <id> [--scope global|project]
  strike plugin untrust <id> [--scope global|project]
  strike plugin remove <id> --yes [--scope global|project]
  strike plugin doctor [id]
  strike plugin help

Install options:
  --scope global|project   Install root (default: global)
  --ref <branch|tag>       Git ref used only to resolve the pin (stored; not followed later)
  --commit <sha>           Pin an explicit git commit (full or unique prefix)
  --subdir <path>          Plugin root inside the git repository
  --registry <url>         Catalog base or catalog.json URL (required for catalog: installs)
  --version <semver>       Pin catalog package version (default: latest)
  --force                  Replace an existing install of the same id

Update options:
  --yes                    Confirm after review (required; no unattended updates)
  --registry <url>         Catalog registry override
  --version <semver>       Target version (default: latest newer)
  --force                  Allow reinstall when not strictly newer
  --scope global|project

Scopes:
  global   ~/.strike/plugins/<id>/  + ~/.strike/plugins.lock.json
  project  ./.strike/plugins/<id>/  + ./.strike/plugins.lock.json  (cwd)

Notes:
  - Install is atomic: failed validation leaves no partially enabled plugin.
  - Git installs always pin a full commit SHA in the lockfile.
  - Catalog installs pin immutable version + verified artifact digest; lockfile
    records registry, package, version, artifact URL/digest, and content digest.
  - Catalog metadata cannot enable or execute content; trust is separate (#728).
  - Updates that change digest/source/executable contributions clear prior trust.
  - Failed download, verification, validation, or activation preserves the prior version.
  - disable keeps files; remove deletes files and lockfile entry (--yes required).
  - trust grants executable MCP/harness/shell-hook activation for the current
    content digest + source identity + capability set; untrust revokes it.
  - doctor prints paths, provenance, and trust state; never secrets or env values.

  - Changes apply on next Strike launch (no hot reload).
`

func runPluginCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, pluginUsage)
		return 0
	}
	switch args[0] {
	case "list":
		return runPluginList(args[1:], stdout, stderr)
	case "inspect":
		return runPluginInspect(args[1:], stdout, stderr)
	case "install":
		return runPluginInstall(args[1:], stdout, stderr)
	case "search":
		return runPluginSearch(args[1:], stdout, stderr)
	case "outdated":
		return runPluginOutdated(args[1:], stdout, stderr)
	case "update":
		return runPluginUpdate(args[1:], stdout, stderr)
	case "enable":
		return runPluginEnable(args[1:], stdout, stderr, true)
	case "disable":
		return runPluginEnable(args[1:], stdout, stderr, false)
	case "trust":
		return runPluginTrust(args[1:], stdout, stderr, true)
	case "untrust":
		return runPluginTrust(args[1:], stdout, stderr, false)
	case "remove":
		return runPluginRemove(args[1:], stdout, stderr)
	case "doctor":
		return runPluginDoctor(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "strike plugin: unknown command %q\n", args[0])
		fmt.Fprint(stderr, pluginUsage)
		return 2
	}
}

func pluginDiscoverOpts() plugin.Options {
	wd, err := os.Getwd()
	if err != nil {
		wd = ""
	}
	return plugin.Options{WorkDir: wd}
}

func parsePluginScope(fs *flag.FlagSet) *string {
	return fs.String("scope", "", "global or project")
}

func scopeFromFlag(s string) (plugin.Scope, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	switch strings.ToLower(s) {
	case "global":
		return plugin.ScopeGlobal, nil
	case "project":
		return plugin.ScopeProject, nil
	default:
		return "", fmt.Errorf("scope must be global or project, got %q", s)
	}
}

func runPluginList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scopeFlag := parsePluginScope(fs)
	flagArgs, _ := splitPluginArgs(args, map[string]bool{"scope": true})
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	scope, err := scopeFromFlag(*scopeFlag)
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin list:", err)
		return 2
	}
	opts := pluginDiscoverOpts()
	list, diags, err := plugin.ListInstalled(plugin.ListOptions{
		WorkDir: opts.WorkDir,
		Scope:   scope,
	})
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin list:", err)
		return 1
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "No plugins installed.")
	}
	for _, p := range list {
		state := "enabled"
		if !p.Enabled {
			state = "disabled"
		}
		if p.LoadError != "" {
			state = "invalid"
		}
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s", p.ID, p.Version, p.Scope, state)
		if p.Name != "" {
			fmt.Fprintf(stdout, "\t%s", p.Name)
		}
		fmt.Fprintln(stdout)
	}
	for _, d := range diags {
		fmt.Fprintln(stderr, d.String())
	}
	return 0
}

func runPluginInspect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin inspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scopeFlag := parsePluginScope(fs)
	flagArgs, pos := splitPluginArgs(args, map[string]bool{"scope": true})
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "usage: strike plugin inspect <id> [--scope global|project]")
		return 2
	}
	scope, err := scopeFromFlag(*scopeFlag)
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin inspect:", err)
		return 2
	}
	opts := pluginDiscoverOpts()
	p, err := plugin.Inspect(plugin.EnableOptions{
		ID:      pos[0],
		Scope:   scope,
		WorkDir: opts.WorkDir,
	})
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin inspect:", err)
		return 1
	}
	fmt.Fprintf(stdout, "id:       %s\n", p.ID)
	fmt.Fprintf(stdout, "version:  %s\n", p.Version)
	fmt.Fprintf(stdout, "name:     %s\n", p.Name)
	fmt.Fprintf(stdout, "scope:    %s\n", p.Scope)
	fmt.Fprintf(stdout, "enabled:  %v\n", p.Enabled)
	fmt.Fprintf(stdout, "root:     %s\n", p.Root)
	if p.Digest != "" {
		fmt.Fprintf(stdout, "digest:   %s\n", p.Digest)
	}
	if p.Source != nil {
		fmt.Fprintf(stdout, "source:   %s\n", p.Source.String())
		if p.Source.Type == plugin.SourceGit && p.Source.Commit != "" {
			fmt.Fprintf(stdout, "commit:   %s\n", p.Source.Commit)
		}
		if p.Source.Type == plugin.SourceCatalog {
			if p.Source.Registry != "" {
				fmt.Fprintf(stdout, "registry: %s\n", p.Source.Registry)
			}
			if p.Source.Package != "" {
				fmt.Fprintf(stdout, "package:  %s\n", p.Source.Package)
			}
			if p.Source.Version != "" {
				fmt.Fprintf(stdout, "pinned:   %s\n", p.Source.Version)
			}
			if p.Source.URL != "" {
				fmt.Fprintf(stdout, "artifact: %s\n", p.Source.URL)
			}
			if p.Source.Digest != "" {
				fmt.Fprintf(stdout, "artifactDigest: %s\n", p.Source.Digest)
			}
		}
	}
	if p.LoadError != "" {
		fmt.Fprintf(stdout, "error:    %s\n", p.LoadError)
		return 1
	}
	if p.Manifest != nil {
		c := p.Manifest.Contributions
		fmt.Fprintf(stdout, "contribs: agents=%d skills=%d workflows=%d themes=%d providers=%d mcp=%d harnesses=%d hooks=%d panes=%d\n",
			len(c.Agents), len(c.Skills), len(c.Workflows), len(c.Themes), len(c.Providers),
			len(c.MCP), len(c.Harnesses), len(c.Hooks), len(c.Panes))
		if plugin.HasExecutableContributionsAt(*p.Manifest, p.Root) {
			caps := plugin.InferCapabilitiesAt(*p.Manifest, p.Root)
			digest := p.Digest
			if live, err := plugin.ComputeDigest(p.Root); err == nil {
				digest = live
			}
			match := plugin.MatchTrust(p.Trust, digest, p.Source, caps)
			fmt.Fprintf(stdout, "trust:    %s", match.State)
			if !match.OK && match.Reason != "" {
				fmt.Fprintf(stdout, " (%s)", match.Reason)
			}
			fmt.Fprintln(stdout)
		} else if p.Trust != nil && p.Trust.Digest != "" {
			fmt.Fprintf(stdout, "trust:    granted digest=%s (no executable contributions)\n", p.Trust.Digest)
		} else {
			fmt.Fprintln(stdout, "trust:    n/a-passive-only")
		}
	} else if p.Trust != nil && p.Trust.Digest != "" {
		fmt.Fprintf(stdout, "trust:    granted digest=%s\n", p.Trust.Digest)
	} else {
		fmt.Fprintln(stdout, "trust:    none")
	}
	return 0
}

func runPluginInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scopeFlag := fs.String("scope", "global", "global or project")
	ref := fs.String("ref", "", "git branch or tag to resolve")
	commit := fs.String("commit", "", "git commit SHA to pin")
	subdir := fs.String("subdir", "", "subdirectory inside git repo")
	registry := fs.String("registry", "", "catalog base or catalog.json URL")
	version := fs.String("version", "", "catalog package version pin")
	force := fs.Bool("force", false, "replace existing install")
	flagArgs, pos := splitPluginArgs(args, map[string]bool{
		"scope": true, "ref": true, "commit": true, "subdir": true,
		"registry": true, "version": true,
	})
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "usage: strike plugin install <path|git-url|catalog:pkg[@ver]> [--scope] [--registry] [--version] [--ref] [--commit] [--subdir] [--force]")
		return 2
	}
	scope, err := scopeFromFlag(*scopeFlag)
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin install:", err)
		return 2
	}
	if scope == "" {
		scope = plugin.ScopeGlobal
	}
	localPath, gitURL, catPkg, catVer, err := plugin.ParseInstallSource(pos[0])
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin install:", err)
		return 2
	}
	if catVer == "" {
		catVer = strings.TrimSpace(*version)
	} else if strings.TrimSpace(*version) != "" && catVer != strings.TrimSpace(*version) {
		fmt.Fprintln(stderr, "strike plugin install: conflicting versions in catalog: ref and --version")
		return 2
	}
	if catPkg != "" && strings.TrimSpace(*registry) == "" {
		fmt.Fprintln(stderr, "strike plugin install: catalog installs require --registry <url>")
		return 2
	}
	opts := pluginDiscoverOpts()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := plugin.Install(ctx, plugin.InstallOptions{
		Scope:           scope,
		WorkDir:         opts.WorkDir,
		LocalPath:       localPath,
		GitURL:          gitURL,
		GitRef:          *ref,
		GitCommit:       *commit,
		GitSubdir:       *subdir,
		CatalogRegistry: strings.TrimSpace(*registry),
		CatalogPackage:  catPkg,
		CatalogVersion:  catVer,
		Force:           *force,
	})
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin install:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Installed %s@%s (%s)\n", res.ID, res.Version, res.Scope)
	fmt.Fprintf(stdout, "  root:    %s\n", res.Root)
	fmt.Fprintf(stdout, "  digest:  %s\n", res.Digest)
	fmt.Fprintf(stdout, "  source:  %s\n", res.Source.String())
	if res.Source.Type == plugin.SourceGit {
		fmt.Fprintf(stdout, "  commit:  %s\n", res.Source.Commit)
	}
	if res.Source.Type == plugin.SourceCatalog {
		fmt.Fprintf(stdout, "  artifactDigest: %s\n", res.Source.Digest)
	}
	fmt.Fprintln(stdout, "  enabled: true (passive contributions load on next launch)")
	fmt.Fprintln(stdout, "  trust:   none (catalog/install metadata cannot enable execution)")
	return 0
}

func runPluginSearch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	registry := fs.String("registry", "", "catalog base or catalog.json URL")
	flagArgs, pos := splitPluginArgs(args, map[string]bool{"registry": true})
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if strings.TrimSpace(*registry) == "" {
		fmt.Fprintln(stderr, "usage: strike plugin search <query> --registry <url>")
		return 2
	}
	query := strings.Join(pos, " ")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cat, _, err := plugin.FetchCatalog(ctx, nil, *registry)
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin search:", err)
		return 1
	}
	hits := cat.Search(query)
	if len(hits) == 0 {
		fmt.Fprintln(stdout, "No packages matched.")
		return 0
	}
	for _, h := range hits {
		fmt.Fprintf(stdout, "%s\t%s\t%s", h.ID, h.Version.Version, h.Name)
		if h.Description != "" {
			fmt.Fprintf(stdout, "\t%s", h.Description)
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

func runPluginOutdated(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin outdated", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scopeFlag := parsePluginScope(fs)
	registry := fs.String("registry", "", "default catalog registry when lock entry lacks one")
	flagArgs, _ := splitPluginArgs(args, map[string]bool{"scope": true, "registry": true})
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	scope, err := scopeFromFlag(*scopeFlag)
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin outdated:", err)
		return 2
	}
	opts := pluginDiscoverOpts()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	items, err := plugin.CheckOutdated(ctx, plugin.OutdatedOptions{
		WorkDir:  opts.WorkDir,
		Scope:    scope,
		Registry: strings.TrimSpace(*registry),
	})
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin outdated:", err)
		return 1
	}
	if len(items) == 0 {
		fmt.Fprintln(stdout, "All catalog-sourced plugins are up to date.")
		return 0
	}
	for _, it := range items {
		cur := it.Installed.Version
		if it.Installed.Source != nil && it.Installed.Source.Version != "" {
			cur = it.Installed.Source.Version
		}
		fmt.Fprintf(stdout, "%s\t%s → %s\t%s\n", it.Installed.ID, cur, it.Latest.Version, it.Registry)
	}
	return 0
}

func runPluginUpdate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin update", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scopeFlag := parsePluginScope(fs)
	registry := fs.String("registry", "", "catalog registry override")
	version := fs.String("version", "", "target catalog version")
	yes := fs.Bool("yes", false, "confirm update after review")
	force := fs.Bool("force", false, "allow reinstall when not newer")
	flagArgs, pos := splitPluginArgs(args, map[string]bool{
		"scope": true, "registry": true, "version": true,
	})
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "usage: strike plugin update <id> --yes [--registry] [--version] [--scope] [--force]")
		return 2
	}
	scope, err := scopeFromFlag(*scopeFlag)
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin update:", err)
		return 2
	}
	opts := pluginDiscoverOpts()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	uo := plugin.UpdateOptions{
		ID:       pos[0],
		Scope:    scope,
		WorkDir:  opts.WorkDir,
		Registry: strings.TrimSpace(*registry),
		Version:  strings.TrimSpace(*version),
		Confirm:  *yes,
		Force:    *force,
	}
	// Always show review first (even when --yes is set).
	review, ver, err := plugin.PreviewUpdate(ctx, uo)
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin update:", err)
		return 1
	}
	fmt.Fprint(stdout, review.Format())
	fmt.Fprintf(stdout, "  catalog:  %s@%s\n", review.ID, ver.Version)
	if !*yes {
		fmt.Fprintln(stderr, "strike plugin update: re-run with --yes to apply after reviewing the diff")
		return 2
	}
	res, err := plugin.Update(ctx, uo)
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin update:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Updated %s@%s\n", res.Install.ID, res.Install.Version)
	fmt.Fprintf(stdout, "  root:   %s\n", res.Install.Root)
	fmt.Fprintf(stdout, "  digest: %s\n", res.Install.Digest)
	if res.Review.TrustInvalidated {
		fmt.Fprintln(stdout, "  trust:  invalidated (re-review required for executable activation)")
	}
	return 0
}

func runPluginEnable(args []string, stdout, stderr io.Writer, enable bool) int {
	cmd := "enable"
	if !enable {
		cmd = "disable"
	}
	fs := flag.NewFlagSet("plugin "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	scopeFlag := parsePluginScope(fs)
	flagArgs, pos := splitPluginArgs(args, map[string]bool{"scope": true})
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintf(stderr, "usage: strike plugin %s <id> [--scope global|project]\n", cmd)
		return 2
	}
	scope, err := scopeFromFlag(*scopeFlag)
	if err != nil {
		fmt.Fprintf(stderr, "strike plugin %s: %v\n", cmd, err)
		return 2
	}
	opts := pluginDiscoverOpts()
	eo := plugin.EnableOptions{
		ID:      pos[0],
		Scope:   scope,
		WorkDir: opts.WorkDir,
	}
	var opErr error
	if enable {
		opErr = plugin.Enable(eo)
	} else {
		opErr = plugin.Disable(eo)
	}
	if opErr != nil {
		fmt.Fprintf(stderr, "strike plugin %s: %v\n", cmd, opErr)
		return 1
	}
	if enable {
		fmt.Fprintf(stdout, "Enabled %s\n", pos[0])
	} else {
		fmt.Fprintf(stdout, "Disabled %s (files preserved)\n", pos[0])
	}
	return 0
}

func runPluginRemove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("plugin remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	scopeFlag := parsePluginScope(fs)
	yes := fs.Bool("yes", false, "confirm removal")
	flagArgs, pos := splitPluginArgs(args, map[string]bool{"scope": true})
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "usage: strike plugin remove <id> --yes [--scope global|project]")
		return 2
	}
	scope, err := scopeFromFlag(*scopeFlag)
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin remove:", err)
		return 2
	}
	opts := pluginDiscoverOpts()
	if err := plugin.Remove(plugin.RemoveOptions{
		ID:      pos[0],
		Scope:   scope,
		WorkDir: opts.WorkDir,
		Confirm: *yes,
	}); err != nil {
		fmt.Fprintln(stderr, "strike plugin remove:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Removed %s\n", pos[0])
	return 0
}

func runPluginTrust(args []string, stdout, stderr io.Writer, grant bool) int {
	cmdName := "trust"
	if !grant {
		cmdName = "untrust"
	}
	fs := flag.NewFlagSet("plugin "+cmdName, flag.ContinueOnError)
	fs.SetOutput(stderr)
	scopeFlag := parsePluginScope(fs)
	flagArgs, pos := splitPluginArgs(args, map[string]bool{"scope": true})
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintf(stderr, "usage: strike plugin %s <id> [--scope global|project]\n", cmdName)
		return 2
	}
	scope, err := scopeFromFlag(*scopeFlag)
	if err != nil {
		fmt.Fprintf(stderr, "strike plugin %s: %v\n", cmdName, err)
		return 2
	}
	opts := pluginDiscoverOpts()
	topts := plugin.TrustOptions{
		ID:      pos[0],
		Scope:   scope,
		WorkDir: opts.WorkDir,
	}
	if grant {
		res, err := plugin.Trust(topts)
		if err != nil {
			fmt.Fprintln(stderr, "strike plugin trust:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Trusted %s (%s)\n", res.ID, res.Scope)
		fmt.Fprintf(stdout, "  digest:       %s\n", res.Digest)
		if len(res.Capabilities) > 0 {
			fmt.Fprintf(stdout, "  capabilities: %s\n", strings.Join(res.Capabilities, ", "))
		}
		if res.Source != nil {
			fmt.Fprintf(stdout, "  source:       %s\n", res.Source.String())
		}
		fmt.Fprintln(stdout, "Executable MCP/harness/shell-hook contributions activate on next launch.")
		return 0
	}
	if err := plugin.Untrust(topts); err != nil {
		fmt.Fprintln(stderr, "strike plugin untrust:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Untrusted %s (executables inactive on next launch)\n", pos[0])
	return 0
}

// splitPluginArgs separates flags (including those after positionals) from
// positional args. valueFlags lists flag names that take a following argument
// when written as --name value (not --name=value).
func splitPluginArgs(args []string, valueFlags map[string]bool) (flags, positionals []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				continue // --name=value
			}
			if valueFlags[name] && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positionals = append(positionals, a)
	}
	return flags, positionals
}

func runPluginDoctor(args []string, stdout, stderr io.Writer) int {
	var id string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		id = args[0]
	}
	opts := pluginDiscoverOpts()
	report, err := plugin.Doctor(plugin.DoctorOptions{
		ID:      id,
		WorkDir: opts.WorkDir,
	})
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin doctor:", err)
		return 1
	}
	fmt.Fprint(stdout, plugin.FormatDoctorText(report))
	// Non-zero if any error-severity findings on selected plugins.
	for _, p := range report.Plugins {
		for _, f := range p.Findings {
			if f.Severity == plugin.SeverityError {
				return 1
			}
		}
	}
	for _, f := range report.Findings {
		if f.Severity == plugin.SeverityError {
			return 1
		}
	}
	return 0
}
