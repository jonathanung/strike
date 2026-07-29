package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jonathanung/strike-cli/internal/config"
)

const restoreUsage = `Restore .strike directory structure and repair corrupted metadata.

Usage:
  strike restore [options]

Recreates missing directories under ~/.strike (and optionally ./.strike).
Valid files are never overwritten. Corrupt JSON metadata is moved aside as
<name>.corrupt-<timestamp>; required config is rewritten with safe defaults.
Session logs, memory, issues, goals, and history data are never deleted.

Options:
  --project              also restore ./.strike in the current directory
  --project-dir <path>   restore <path>/.strike (implies project restore for that path)
  -h, --help             show help
`

func runRestoreCLI(args []string, stdout, stderr io.Writer) int {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Fprint(stdout, restoreUsage)
			return 0
		}
	}

	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	project := fs.Bool("project", false, "")
	projectDir := fs.String("project-dir", "", "")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "strike restore:", err)
		fmt.Fprint(stderr, restoreUsage)
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "strike restore: unexpected argument %q\n", fs.Arg(0))
		fmt.Fprint(stderr, restoreUsage)
		return 2
	}

	res, err := config.RestoreGlobalHome()
	if err != nil {
		fmt.Fprintln(stderr, "strike restore:", err)
		return 1
	}
	fmt.Fprint(stdout, config.FormatRestoreReport(res))

	dir := strings.TrimSpace(*projectDir)
	if *project || dir != "" {
		if dir == "" {
			wd, err := os.Getwd()
			if err != nil {
				fmt.Fprintln(stderr, "strike restore:", err)
				return 1
			}
			dir = wd
		}
		pres, err := config.RestoreProjectDir(dir)
		if err != nil {
			fmt.Fprintln(stderr, "strike restore:", err)
			return 1
		}
		fmt.Fprint(stdout, config.FormatRestoreReport(pres))
	}
	return 0
}
