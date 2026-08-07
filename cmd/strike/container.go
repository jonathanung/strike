package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/container"
	"github.com/jonathanung/strike-cli/internal/version"
)

const containerUsage = `Manage native containerization (Dockerfile eject, status).

Usage:
  strike container eject [--out Dockerfile.devcontainer] [--force] [--dockerfile path]
  strike container drift [--dockerfile path]
  strike container help

eject:
  Materialize Dockerfile.devcontainer from layered container config (E12.3).
  Writes a generated header with strike-config-hash for drift detection.
  Default output is Dockerfile.devcontainer in the current directory (commit it).
  --force overwrites when the on-disk hash does not match current config.
  --dockerfile uses a hand-edited Dockerfile as the body (still stamps the hash).

drift:
  Exit 0 if the ejected file matches current config hash; 1 on drift or missing file.
`

func runContainerCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, containerUsage)
		return 0
	}
	switch args[0] {
	case "eject":
		return runContainerEject(args[1:], stdout, stderr)
	case "drift":
		return runContainerDrift(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "strike container: unknown command %q\n", args[0])
		fmt.Fprint(stderr, containerUsage)
		return 2
	}
}

func runContainerEject(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("container eject", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", container.DefaultEjectName, "output path (default Dockerfile.devcontainer)")
	force := fs.Bool("force", false, "overwrite when config hash drifted")
	dockerfile := fs.String("dockerfile", "", "hand-edited Dockerfile path (body source)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	rt := cfg.Container.ToRuntime(version.Version)
	if *dockerfile != "" {
		rt.Dockerfile = *dockerfile
	}
	res, err := container.Eject(rt, cwd, container.EjectOpts{
		Out:     *out,
		Force:   *force,
		Version: version.Version,
	})
	if err != nil {
		if errors.Is(err, container.ErrDockerfileDrift) {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	rel := res.Path
	if r, err := filepath.Rel(cwd, res.Path); err == nil {
		rel = r
	}
	switch {
	case res.Unchanged:
		fmt.Fprintf(stdout, "unchanged %s (hash %s)\n", rel, res.Hash[:12])
	case res.Wrote && res.Drifted:
		fmt.Fprintf(stdout, "wrote %s (overwrote drift; hash %s)\n", rel, res.Hash[:12])
	case res.Wrote:
		fmt.Fprintf(stdout, "wrote %s (hash %s)\n", rel, res.Hash[:12])
	}
	return 0
}

func runContainerDrift(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("container drift", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("dockerfile", container.DefaultEjectName, "Dockerfile path to check")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	rt := cfg.Container.ToRuntime(version.Version)
	drifted, have, want, err := container.CheckDrift(rt, cwd, *path, version.Version)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stderr, "strike: %s not found (run strike container eject)\n", *path)
			return 1
		}
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if drifted {
		fmt.Fprintf(stdout, "drift: have %s want %s\n", short(have), short(want))
		return 1
	}
	fmt.Fprintf(stdout, "ok %s\n", short(want))
	return 0
}

func short(h string) string {
	if h == "" {
		return "(none)"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
