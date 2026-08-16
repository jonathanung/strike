// Command strike is the CLI entry point. This file owns argument parsing,
// usage text, and the auth/exec/rpc/acp/serve/eval subcommand dispatch only; the
// composition root that assembles the engine, host services, and TUI lives in
// wire.go.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/sandbox"
	"github.com/jonathanung/strike-cli/internal/product/update"
	"github.com/jonathanung/strike-cli/internal/product/version"
)

type cliOptions struct {
	provider                   string
	model                      string
	effort                     string
	sandbox                    string // --sandbox: off|read-only|workspace-write
	iKnow                      bool   // --i-know: allow yolo with sandbox off
	auto                       bool   // --auto: skip permission asks; network.allow stays
	dangerouslySkipPermissions bool   // --dangerously-skip-permissions: skip asks + bypass network.allow
	providerSet                bool
	continueSession            bool
	sessionID                  string  // --session: resume a specific root session
	worktree                   bool    // --worktree: force a git worktree for this session
	turnTimeout                string  // --turn-timeout: root-turn wall-clock deadline (duration or off)
	turnTimeoutSet             bool    // true when --turn-timeout was explicitly passed
	launchInsideContainer      bool    // --launch-inside-container: re-exec strike in managed container
	containerRebuild           bool    // --container-rebuild: replace stale live container
	containerAttachStale       bool    // --container-attach-stale: join drifted container
	containerCancelStale       bool    // --container-cancel: refuse stale container non-interactively
	telemetry                  bool    // --telemetry: show local system metrics pane
	maxCost                    float64 // --max-cost: session cost envelope USD (#577)
	maxCostSet                 bool
	upgrade                    bool
	version                    bool
}

type optionSpec struct {
	names       []string
	valueName   string
	description string
	register    func(*flag.FlagSet, *cliOptions)
}

var optionSpecs = []optionSpec{
	{
		names:       []string{"provider"},
		valueName:   "provider",
		description: "provider to use (anthropic|openai|xai|google|kimi|deepseek|echo; gemini=google alias); overrides config",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.StringVar(&opts.provider, "provider", "", "")
		},
	},
	{
		names:       []string{"model"},
		valueName:   "model",
		description: "model id; overrides config",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.StringVar(&opts.model, "model", "", "")
		},
	},
	{
		names:       []string{"effort"},
		valueName:   "level",
		description: "reasoning effort (off|low|medium|high|xhigh|max); overrides config",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.StringVar(&opts.effort, "effort", "", "")
		},
	},
	{
		names:       []string{"auto"},
		description: "skip configured permission prompts; network.allow and OS sandbox still apply",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.auto, "auto", false, "")
		},
	},
	{
		names:       []string{"dangerously-skip-permissions"},
		description: "skip permission prompts and bypass network.allow (OS sandbox still applies)",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.dangerouslySkipPermissions, "dangerously-skip-permissions", false, "")
		},
	},
	{
		names:       []string{"sandbox"},
		valueName:   "mode",
		description: "OS process sandbox for bash (off|read-only|workspace-write); overrides config",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.StringVar(&opts.sandbox, "sandbox", "", "")
		},
	},
	{
		names:       []string{"i-know"},
		description: "allow permissionMode yolo when sandbox is off (explicit override)",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.iKnow, "i-know", false, "")
		},
	},
	{
		names:       []string{"continue"},
		description: "resume the most recent root session (model history + selections)",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.continueSession, "continue", false, "")
		},
	},
	{
		names:       []string{"session"},
		valueName:   "id",
		description: "resume a specific session by id (model history + selections)",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.StringVar(&opts.sessionID, "session", "", "")
		},
	},
	{
		names:       []string{"worktree"},
		description: "run this session in an isolated git worktree under .strike/worktrees/",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.worktree, "worktree", false, "")
		},
	},
	{
		names:       []string{"turn-timeout"},
		valueName:   "duration",
		description: "root-turn wall-clock deadline (e.g. 30m, 1h, 1800s); off/0 disables; overrides session.turnTimeoutS",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.StringVar(&opts.turnTimeout, "turn-timeout", "", "")
		},
	},
	{
		names:       []string{"launch-inside-container"},
		description: "build/start the project container and re-exec strike inside it (E12.4)",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.launchInsideContainer, "launch-inside-container", false, "")
		},
	},
	{
		names:       []string{"container-rebuild"},
		description: "with --launch-inside-container: rebuild when the live container is stale (E12.6)",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.containerRebuild, "container-rebuild", false, "")
		},
	},
	{
		names:       []string{"container-attach-stale"},
		description: "with --launch-inside-container: attach to a stale live container (E12.6)",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.containerAttachStale, "container-attach-stale", false, "")
		},
	},
	{
		names:       []string{"container-cancel"},
		description: "with --launch-inside-container: cancel when the live container is stale (E12.6)",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.containerCancelStale, "container-cancel", false, "")
		},
	},
	{
		names:       []string{"telemetry"},
		description: "show local system metrics pane (CPU/RAM/disk); on by default",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.telemetry, "telemetry", false, "")
		},
	},
	{
		names:       []string{"max-cost"},
		valueName:   "usd",
		description: "session cost envelope in USD (hard stop; overrides config session.maxSessionCostUSD)",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.Float64Var(&opts.maxCost, "max-cost", 0, "")
		},
	},
	{
		names:       []string{"upgrade"},
		description: "download and install the latest GitHub Release",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.upgrade, "upgrade", false, "")
		},
	},
	{
		names:       []string{"version"},
		description: "print version and exit",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.version, "version", false, "")
		},
	},
	{
		names:       []string{"h", "help"},
		description: "show help",
	},
}

const autoPermissionsWarning = "WARNING: --auto is enabled; configured permission asks are skipped for this invocation. Active agent permission denies still apply. Workflow phase permission widening is auto-accepted; OS sandbox and network.allow are unchanged."

const dangerousPermissionsWarning = "WARNING: --dangerously-skip-permissions is enabled; configured permission asks are skipped and network.allow is bypassed for this invocation. Active agent permission denies still apply. OS sandbox and path protections are unchanged."

// skipPermissionAsks reports whether configured permission prompts are skipped.
func (o cliOptions) skipPermissionAsks() bool {
	return o.auto || o.dangerouslySkipPermissions
}

// skipNetworkAllow reports whether config network.allow is bypassed.
// Only --dangerously-skip-permissions does this; --auto cannot.
func (o cliOptions) skipNetworkAllow() bool {
	return o.dangerouslySkipPermissions
}

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "auth" {
		if err := runAuth(args[1:], stdout); err != nil {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
		return 0
	}
	if len(args) > 0 && args[0] == "exec" {
		return runExecCLI(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "rpc" {
		return runRPCCLI(args[1:], os.Stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "acp" {
		return runACPCLI(args[1:], os.Stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "serve" {
		return runServeCLI(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "mcp-serve" {
		return runMCPServeCLI(args[1:], os.Stdin, stdout, stderr)
	}
	if len(args) > 0 && args[0] == "restore" {
		return runRestoreCLI(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "workflow" {
		return runWorkflowCLI(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "plugin" {
		return runPluginCLI(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "container" {
		return runContainerCLI(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "eval" {
		return runEvalCLI(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "audit" {
		return runAuditCLI(args[1:], stdout, stderr)
	}
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version" || args[0] == "-version") {
		fmt.Fprintln(stdout, version.String())
		return 0
	}
	if len(args) > 0 && args[0] == "upgrade" {
		return runUpgradeCLI(stdout, stderr)
	}

	opts, err := parseCLIOptions(args)
	if err != nil {
		if err == flag.ErrHelp {
			writeUsage(stdout)
			return 0
		}
		fmt.Fprintln(stderr, "strike:", err)
		writeUsage(stderr)
		return 2
	}
	if opts.version {
		fmt.Fprintln(stdout, version.String())
		return 0
	}
	if opts.upgrade {
		return runUpgradeCLI(stdout, stderr)
	}
	if err := run(opts, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return 0
}

// upgradeCLIOptions configures self-update for the CLI path: install only,
// never re-exec (return to the shell). TUI /upgrade keeps default re-exec.
func upgradeCLIOptions(stdout io.Writer) update.Options {
	return update.Options{
		Stdout: stdout,
		NoExec: true,
	}
}

func runUpgradeCLI(stdout, stderr io.Writer) int {
	_, err := update.Upgrade(context.Background(), upgradeCLIOptions(stdout))
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return 0
}

func parseCLIOptions(args []string) (cliOptions, error) {
	var opts cliOptions
	fs := flag.NewFlagSet("strike", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	for _, spec := range optionSpecs {
		if spec.register != nil {
			spec.register(fs, &opts)
		}
	}
	if err := prevalidateCLIOptions(args, fs); err != nil {
		return cliOptions{}, err
	}
	if err := fs.Parse(args); err != nil {
		return cliOptions{}, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "provider" && opts.provider != "" {
			opts.providerSet = true
		}
		if f.Name == "turn-timeout" {
			opts.turnTimeoutSet = true
		}
		if f.Name == "max-cost" {
			opts.maxCostSet = true
		}
	})
	if fs.NArg() != 0 {
		return cliOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	opts.sessionID = strings.TrimSpace(opts.sessionID)
	if opts.continueSession && opts.sessionID != "" {
		return cliOptions{}, fmt.Errorf("cannot combine --continue and --session")
	}
	opts.sandbox = strings.TrimSpace(opts.sandbox)
	if opts.sandbox != "" {
		// Validate early so --help-adjacent typos fail before assemble.
		if _, err := parseSandboxFlag(opts.sandbox); err != nil {
			return cliOptions{}, err
		}
	}
	opts.turnTimeout = strings.TrimSpace(opts.turnTimeout)
	if opts.turnTimeoutSet {
		if _, err := parseTurnTimeoutFlag(opts.turnTimeout); err != nil {
			return cliOptions{}, err
		}
	}
	return opts, nil
}

// parseTurnTimeoutFlag maps a --turn-timeout value to config seconds.
// "off"/"none"/"disable"/empty/"0" → -1 (disabled). Duration strings and plain
// integer seconds are accepted. Returns seconds for ResolveTurnTimeout.
func parseTurnTimeoutFlag(value string) (int, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "", "off", "none", "disable", "disabled", "0":
		return -1, nil
	}
	// Plain integer seconds (no unit).
	if n, err := strconv.Atoi(v); err == nil {
		if n < 0 {
			return -1, nil
		}
		if n == 0 {
			return -1, nil
		}
		return n, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid --turn-timeout %q (want duration like 30m/1h/1800s, or off)", value)
	}
	if d <= 0 {
		return -1, nil
	}
	secs := int(d / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs, nil
}

// parseSandboxFlag validates a --sandbox value and returns the canonical token.
func parseSandboxFlag(value string) (string, error) {
	mode, ok := sandbox.ParseMode(value)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("unknown sandbox %q (want %s)", value, sandbox.ModeNames())
	}
	return mode.String(), nil
}

// resolveSandboxMode picks CLI --sandbox over config, defaulting to workspace-write.
// When managedLocked is true (MDM set sandbox), the CLI flag is ignored so
// operators cannot loosen enterprise policy from the command line.
func resolveSandboxMode(cfgValue, cliValue string, managedLocked bool) (string, error) {
	if !managedLocked && strings.TrimSpace(cliValue) != "" {
		return parseSandboxFlag(cliValue)
	}
	if strings.TrimSpace(cfgValue) == "" {
		return sandbox.DefaultMode.String(), nil
	}
	mode, ok := sandbox.ParseMode(cfgValue)
	if !ok {
		return "", fmt.Errorf("unknown sandbox %q (want %s)", cfgValue, sandbox.ModeNames())
	}
	return mode.String(), nil
}

func prevalidateCLIOptions(args []string, fs *flag.FlagSet) error {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			return flag.ErrHelp
		}
		if arg == "--" || len(arg) < 2 || arg[0] != '-' {
			return nil
		}

		prefixLen := 1
		if len(arg) > 2 && arg[1] == '-' {
			prefixLen = 2
		}
		name := arg[prefixLen:]
		if equals := strings.IndexByte(name, '='); equals >= 0 {
			name = name[:equals]
		}
		f := fs.Lookup(name)
		if f == nil {
			return nil
		}
		if prefixLen == 1 {
			return fmt.Errorf("flag provided but not defined: -%s", name)
		}
		if !isBoolFlag(f) && len(name)+prefixLen == len(arg) && i+1 < len(args) {
			i++
		}
	}
	return nil
}

func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

func writeUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  strike [options]")
	fmt.Fprintln(w, "  strike exec [options] <prompt>")
	fmt.Fprintln(w, "  strike rpc [options]")
	fmt.Fprintln(w, "  strike acp [options]")
	fmt.Fprintln(w, "  strike serve [options]")
	fmt.Fprintln(w, "  strike mcp-serve [options]")
	fmt.Fprintln(w, "  strike auth <command> [arguments]")
	fmt.Fprintln(w, "  strike plugin <command> [arguments]")
	fmt.Fprintln(w, "  strike container <command> [arguments]")
	fmt.Fprintln(w, "  strike eval <command> [arguments]")
	fmt.Fprintln(w, "  strike restore [options]")
	fmt.Fprintln(w, "  strike workflow <command> [arguments]")
	fmt.Fprintln(w, "  strike version")
	fmt.Fprintln(w, "  strike upgrade")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	for _, spec := range optionSpecs {
		var spelling string
		if len(spec.names) == 2 && spec.names[0] == "h" {
			spelling = "-h, --help"
		} else {
			parts := make([]string, 0, len(spec.names))
			for i, name := range spec.names {
				p := "--" + name
				if i == 0 && spec.valueName != "" {
					p += " <" + spec.valueName + ">"
				}
				parts = append(parts, p)
			}
			spelling = strings.Join(parts, ", ")
		}
		fmt.Fprintf(w, "  %-34s %s\n", spelling, spec.description)
	}
}

func permissionLayers(configured permission.Ruleset, dangerouslySkip bool) []permission.Ruleset {
	return permissionLayersWithPreset(configured, "", dangerouslySkip)
}

// permissionLayersWithPreset builds evaluation layers:
// defaults → optional preset → config permissions → optional dangerous allow-all.
func permissionLayersWithPreset(configured permission.Ruleset, presetID string, dangerouslySkip bool) []permission.Ruleset {
	layers := []permission.Ruleset{permission.Defaults()}
	if p, ok := permission.PresetByID(presetID); ok && len(p.Rules) > 0 {
		layers = append(layers, append(permission.Ruleset(nil), p.Rules...))
	}
	layers = append(layers, append(permission.Ruleset(nil), configured...))
	if dangerouslySkip {
		layers = append(layers, permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.Allow}})
	}
	return layers
}

// permissionLayerNames returns stable explain names parallel to permissionLayersWithPreset.
func permissionLayerNames(presetID string, dangerouslySkip bool) []string {
	names := []string{permission.LayerDefaults}
	if _, ok := permission.PresetByID(presetID); ok {
		names = append(names, permission.LayerPreset)
	}
	names = append(names, permission.LayerConfig)
	if dangerouslySkip {
		names = append(names, permission.LayerDangerous)
	}
	return names
}

func writePermissionsModeWarning(w io.Writer, auto, dangerouslySkip bool) {
	if dangerouslySkip {
		fmt.Fprintln(w, dangerousPermissionsWarning)
		return
	}
	if auto {
		fmt.Fprintln(w, autoPermissionsWarning)
	}
}

// sessionNetworkAllow returns the session network.allow list. --dangerously-skip-permissions
// clears it (unrestricted public hosts; SSRF private blocks remain). --auto does not.
func sessionNetworkAllow(cfgAllow []string, skipNetwork bool) []string {
	if skipNetwork {
		return nil
	}
	return sandbox.CloneNetworkAllow(cfgAllow)
}
