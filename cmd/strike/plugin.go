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

const pluginUsage = `Manage local and Git plugin installs (lifecycle + trust).

Usage:
  strike plugin list [--scope global|project]
  strike plugin inspect <id> [--scope global|project]
  strike plugin install <path-or-git-url> [options]
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
  --force                  Replace an existing install of the same id

Scopes:
  global   ~/.strike/plugins/<id>/  + ~/.strike/plugins.lock.json
  project  ./.strike/plugins/<id>/  + ./.strike/plugins.lock.json  (cwd)

Notes:
  - Install is atomic: failed validation leaves no partially enabled plugin.
  - Git installs always pin a full commit SHA in the lockfile.
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
		if plugin.HasExecutableContributions(*p.Manifest) {
			caps := plugin.InferCapabilities(*p.Manifest)
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
		}
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
	force := fs.Bool("force", false, "replace existing install")
	flagArgs, pos := splitPluginArgs(args, map[string]bool{
		"scope": true, "ref": true, "commit": true, "subdir": true,
	})
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if len(pos) < 1 {
		fmt.Fprintln(stderr, "usage: strike plugin install <path-or-git-url> [--scope global|project] [--ref] [--commit] [--subdir] [--force]")
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
	localPath, gitURL, err := plugin.ParseInstallSource(pos[0])
	if err != nil {
		fmt.Fprintln(stderr, "strike plugin install:", err)
		return 2
	}
	opts := pluginDiscoverOpts()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := plugin.Install(ctx, plugin.InstallOptions{
		Scope:     scope,
		WorkDir:   opts.WorkDir,
		LocalPath: localPath,
		GitURL:    gitURL,
		GitRef:    *ref,
		GitCommit: *commit,
		GitSubdir: *subdir,
		Force:     *force,
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
	fmt.Fprintln(stdout, "  enabled: true (passive contributions load on next launch)")
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
