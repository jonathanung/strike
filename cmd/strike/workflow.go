package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/config"
)

const workflowUsage = `Manage workflow definitions (schema, scaffold, format, validate).

Usage:
  strike workflow scaffold --global|--project <name> [--force]
  strike workflow format [--write] <path>...
  strike workflow validate [path|dir]...
  strike workflow validate --global|--project|--all
  strike workflow help

Workflows are linear phase sequences (schemaVersion 1). Scaffolding and
formatting only write files — they never activate a workflow. Activation is a
separate catalog/runtime step.

Scaffold:
  Requires exactly one of --global or --project. Writes
  <scope>/.strike/workflows/<name>.json (global uses ~/.strike). Refuses to
  overwrite an existing file unless --force is passed (explicit confirmation).

Format:
  Pretty-prints deterministic JSON. With --write, rewrites files in place.
  Without --write, prints to stdout (one file) or reports paths (multiple).

Validate:
  Strict decode (unknown fields rejected), structural checks, and agent
  reference resolution against loaded agents. Reports every actionable error
  in one pass. With no paths, use --global, --project, or --all to validate
  installed workflow directories. Paths may be files or directories of *.json.
`

func runWorkflowCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, workflowUsage)
		return 0
	}
	switch args[0] {
	case "scaffold":
		return runWorkflowScaffold(args[1:], stdout, stderr)
	case "format":
		return runWorkflowFormat(args[1:], stdout, stderr)
	case "validate":
		return runWorkflowValidate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "strike workflow: unknown command %q\n", args[0])
		fmt.Fprint(stderr, workflowUsage)
		return 2
	}
}

func runWorkflowScaffold(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("workflow scaffold", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	global := fs.Bool("global", false, "")
	project := fs.Bool("project", false, "")
	force := fs.Bool("force", false, "")
	// Allow flags before or after <name> (Go's flag stops at first positional).
	flagArgs, posArgs := splitFlagsAndPositionals(args)
	if err := fs.Parse(flagArgs); err != nil {
		fmt.Fprintln(stderr, "strike workflow scaffold:", err)
		fmt.Fprint(stderr, workflowUsage)
		return 2
	}
	if len(posArgs) != 1 {
		fmt.Fprintln(stderr, "strike workflow scaffold: require exactly one <name>")
		fmt.Fprint(stderr, workflowUsage)
		return 2
	}
	if *global == *project {
		fmt.Fprintln(stderr, "strike workflow scaffold: require exactly one of --global or --project")
		return 2
	}
	name := strings.TrimSpace(posArgs[0])
	w, err := config.ScaffoldWorkflow(name)
	if err != nil {
		fmt.Fprintln(stderr, "strike workflow scaffold:", err)
		return 1
	}
	scope := "global"
	workDir := ""
	if *project {
		scope = "project"
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(stderr, "strike workflow scaffold:", err)
			return 1
		}
		workDir = wd
	}
	dir, err := config.WorkflowDir(scope, workDir)
	if err != nil {
		fmt.Fprintln(stderr, "strike workflow scaffold:", err)
		return 1
	}
	path := filepath.Join(dir, name+".json")
	if err := config.WriteWorkflowFile(path, w, *force); err != nil {
		fmt.Fprintln(stderr, "strike workflow scaffold:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (not activated)\n", path)
	return 0
}

func runWorkflowFormat(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("workflow format", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	write := fs.Bool("write", false, "")
	flagArgs, posArgs := splitFlagsAndPositionals(args)
	if err := fs.Parse(flagArgs); err != nil {
		fmt.Fprintln(stderr, "strike workflow format:", err)
		fmt.Fprint(stderr, workflowUsage)
		return 2
	}
	if len(posArgs) == 0 {
		fmt.Fprintln(stderr, "strike workflow format: require at least one <path>")
		fmt.Fprint(stderr, workflowUsage)
		return 2
	}
	paths := posArgs
	if !*write && len(paths) > 1 {
		// Multiple files to stdout would be ambiguous; require --write.
		fmt.Fprintln(stderr, "strike workflow format: multiple paths require --write")
		return 2
	}
	var failed bool
	for _, path := range paths {
		w, err := config.LoadWorkflowFile(path)
		if err != nil {
			fmt.Fprintln(stderr, "strike workflow format:", err)
			failed = true
			continue
		}
		raw, err := config.FormatWorkflow(w)
		if err != nil {
			fmt.Fprintln(stderr, "strike workflow format:", err)
			failed = true
			continue
		}
		if *write {
			if err := config.WriteWorkflowFile(path, w, true); err != nil {
				fmt.Fprintln(stderr, "strike workflow format:", err)
				failed = true
				continue
			}
			fmt.Fprintln(stdout, path)
			continue
		}
		if _, err := stdout.Write(raw); err != nil {
			fmt.Fprintln(stderr, "strike workflow format:", err)
			return 1
		}
	}
	if failed {
		return 1
	}
	return 0
}

func runWorkflowValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("workflow validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	global := fs.Bool("global", false, "")
	project := fs.Bool("project", false, "")
	all := fs.Bool("all", false, "")
	flagArgs, posArgs := splitFlagsAndPositionals(args)
	if err := fs.Parse(flagArgs); err != nil {
		fmt.Fprintln(stderr, "strike workflow validate:", err)
		fmt.Fprint(stderr, workflowUsage)
		return 2
	}

	workDir, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "strike workflow validate:", err)
		return 1
	}

	// Collect target files.
	var files []string
	switch {
	case len(posArgs) > 0:
		if *global || *project || *all {
			fmt.Fprintln(stderr, "strike workflow validate: --global/--project/--all cannot combine with paths")
			return 2
		}
		for _, p := range posArgs {
			expanded, err := expandWorkflowPaths(p)
			if err != nil {
				fmt.Fprintln(stderr, "strike workflow validate:", err)
				return 1
			}
			files = append(files, expanded...)
		}
	case *all:
		for _, scope := range []string{"global", "project"} {
			dir, err := config.WorkflowDir(scope, workDir)
			if err != nil {
				continue
			}
			expanded, err := expandWorkflowPaths(dir)
			if err != nil {
				// Missing dir is fine for --all.
				if os.IsNotExist(err) {
					continue
				}
				fmt.Fprintln(stderr, "strike workflow validate:", err)
				return 1
			}
			files = append(files, expanded...)
		}
	case *global || *project:
		if *global == *project {
			fmt.Fprintln(stderr, "strike workflow validate: use one of --global or --project (or --all)")
			return 2
		}
		scope := "global"
		if *project {
			scope = "project"
		}
		dir, err := config.WorkflowDir(scope, workDir)
		if err != nil {
			fmt.Fprintln(stderr, "strike workflow validate:", err)
			return 1
		}
		expanded, err := expandWorkflowPaths(dir)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(stdout, "ok: no workflows in %s\n", dir)
				return 0
			}
			fmt.Fprintln(stderr, "strike workflow validate:", err)
			return 1
		}
		files = expanded
	default:
		fmt.Fprintln(stderr, "strike workflow validate: require paths or --global/--project/--all")
		fmt.Fprint(stderr, workflowUsage)
		return 2
	}

	if len(files) == 0 {
		fmt.Fprintln(stdout, "ok: no workflow files")
		return 0
	}

	agents, err := config.LoadAgentsWithError(workDir)
	if err != nil {
		fmt.Fprintln(stderr, "strike workflow validate: loading agents:", err)
		return 1
	}
	known := config.AgentNameSet(agents)

	var (
		errCount int
		okCount  int
	)
	// Track names across files for duplicate reporting within this validate run.
	seenName := map[string]string{}
	for _, path := range files {
		w, err := config.LoadWorkflowFile(path)
		if err != nil {
			fmt.Fprintln(stderr, err)
			errCount++
			continue
		}
		var errs config.WorkflowErrors
		if err := config.ValidateWorkflowAgents(w, known); err != nil {
			errs = append(errs, asWFErrors(err)...)
		}
		if prev, ok := seenName[w.Name]; ok {
			errs = append(errs, config.WorkflowError{
				Source: path,
				Path:   "name",
				Msg:    fmt.Sprintf("duplicate workflow %q (also %s)", w.Name, prev),
			})
		} else {
			seenName[w.Name] = path
		}
		if len(errs) > 0 {
			fmt.Fprintln(stderr, errs.Error())
			errCount++
			continue
		}
		fmt.Fprintf(stdout, "ok: %s (%s fingerprint=%s)\n", path, w.Name, shortFP(w.Fingerprint))
		okCount++
	}
	if errCount > 0 {
		fmt.Fprintf(stderr, "strike workflow validate: %d failed, %d ok\n", errCount, okCount)
		return 1
	}
	fmt.Fprintf(stdout, "validated %d workflow(s)\n", okCount)
	return 0
}

func asWFErrors(err error) config.WorkflowErrors {
	if err == nil {
		return nil
	}
	if es, ok := err.(config.WorkflowErrors); ok {
		return es
	}
	if e, ok := err.(config.WorkflowError); ok {
		return config.WorkflowErrors{e}
	}
	return config.WorkflowErrors{{Msg: err.Error()}}
}

func shortFP(fp string) string {
	if len(fp) <= 12 {
		return fp
	}
	return fp[:12]
}

// splitFlagsAndPositionals separates leading-dash tokens (and their values)
// from positional arguments so flags may appear before or after positionals.
func splitFlagsAndPositionals(args []string) (flags, positionals []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			// Boolean flags we define take no value; value flags use --name=value
			// form only in this CLI. Nothing to consume.
			continue
		}
		positionals = append(positionals, a)
	}
	return flags, positionals
}

func expandWorkflowPaths(path string) ([]string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []string{path}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, filepath.Join(path, e.Name()))
	}
	return out, nil
}
