// Command strike is the CLI entry point. This file owns argument parsing,
// usage text, and the auth subcommand dispatch only; the composition root that
// assembles the engine, host services, and TUI lives in wire.go.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
)

type cliOptions struct {
	provider                   string
	model                      string
	effort                     string
	dangerouslySkipPermissions bool
	providerSet                bool
	continueSession            bool
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
		description: "provider to use (anthropic|openai|xai|echo); overrides config",
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
		names:       []string{"dangerously-skip-permissions"},
		description: "skip configured permission prompts (agent profile denies still apply)",
		register: func(fs *flag.FlagSet, opts *cliOptions) {
			fs.BoolVar(&opts.dangerouslySkipPermissions, "dangerously-skip-permissions", false, "")
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
		names:       []string{"h", "help"},
		description: "show help",
	},
}

const dangerousPermissionsWarning = "WARNING: --dangerously-skip-permissions is enabled; configured permission asks are skipped for this invocation. Active agent permission denies still apply."

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
	if err := run(opts, stdout, stderr); err != nil {
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
	})
	if fs.NArg() != 0 {
		return cliOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	return opts, nil
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
	fmt.Fprintln(w, "  strike auth <command> [arguments]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Options:")
	for _, spec := range optionSpecs {
		var spelling string
		if len(spec.names) == 2 && spec.names[0] == "h" {
			spelling = "-h, --help"
		} else {
			spelling = "--" + spec.names[0]
			if spec.valueName != "" {
				spelling += " <" + spec.valueName + ">"
			}
		}
		fmt.Fprintf(w, "  %-34s %s\n", spelling, spec.description)
	}
}

func permissionLayers(configured permission.Ruleset, dangerouslySkip bool) []permission.Ruleset {
	layers := []permission.Ruleset{
		permission.Defaults(),
		append(permission.Ruleset(nil), configured...),
	}
	if dangerouslySkip {
		layers = append(layers, permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.Allow}})
	}
	return layers
}

func writeDangerousPermissionsWarning(w io.Writer, enabled bool) {
	if enabled {
		fmt.Fprintln(w, dangerousPermissionsWarning)
	}
}
