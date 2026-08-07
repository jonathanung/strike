package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/container"
	"github.com/jonathanung/strike-cli/internal/version"
)

const containerUsage = `Manage native containerization (Dockerfile eject, detect, status).

Usage:
  strike container eject [--out Dockerfile.devcontainer] [--force] [--dockerfile path]
  strike container drift [--dockerfile path]
  strike container detect [--json]
  strike container help

eject:
  Materialize Dockerfile.devcontainer from layered container config (E12.3).
  Writes a generated header with strike-config-hash for drift detection.
  Default output is Dockerfile.devcontainer in the current directory (commit it).
  --force overwrites when the on-disk hash does not match current config.
  --dockerfile uses a hand-edited Dockerfile as the body (still stamps the hash).

drift:
  Exit 0 if the ejected file matches current config hash; 1 on drift or missing file.

detect:
  Scan the current directory for dependency manifests (go.mod, package.json,
  requirements.txt, Cargo.toml, flake.nix, Makefile, …) and print a summary.
  Used by the /devcontainer skill (E12.5). --json emits machine-readable output
  including a suggested container config fragment.
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
	case "detect":
		return runContainerDetect(args[1:], stdout, stderr)
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

func runContainerDetect(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("container detect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "emit JSON (deps + suggested config fragment)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	deps := container.DetectProjectDeps(cwd)
	if *asJSON {
		payload := struct {
			container.DetectedDeps
			Suggested map[string]any `json:"suggested"`
		}{
			DetectedDeps: deps,
			Suggested:    deps.SuggestedContainerJSON(),
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(payload); err != nil {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
		return 0
	}
	if len(deps.Markers) == 0 {
		fmt.Fprintln(stdout, "no dependency manifests detected")
		return 0
	}
	fmt.Fprintf(stdout, "markers: %s\n", strings.Join(deps.Markers, ", "))
	var langs []string
	if deps.Go {
		langs = append(langs, "go"+suffixVer(deps.GoVersion))
	}
	if deps.Node {
		langs = append(langs, "node"+suffixVer(deps.NodeVersion))
	}
	if deps.Python {
		langs = append(langs, "python"+suffixVer(deps.PythonVersion))
	}
	if deps.Rust {
		langs = append(langs, "rust")
	}
	if deps.Nix {
		langs = append(langs, "nix")
	}
	if deps.Make {
		langs = append(langs, "make")
	}
	if len(langs) > 0 {
		fmt.Fprintf(stdout, "toolchains: %s\n", strings.Join(langs, ", "))
	}
	if len(deps.AptPackages) > 0 {
		fmt.Fprintf(stdout, "suggested apt: %s\n", strings.Join(deps.AptPackages, ", "))
	}
	sug := deps.SuggestedContainerJSON()
	if len(sug) > 0 {
		b, _ := json.MarshalIndent(sug, "", "  ")
		fmt.Fprintf(stdout, "suggested container config:\n%s\n", b)
	}
	fmt.Fprintln(stdout, "next: /devcontainer (or write .strike/container.json then strike container eject)")
	return 0
}

func suffixVer(v string) string {
	if v == "" {
		return ""
	}
	return "@" + v
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
