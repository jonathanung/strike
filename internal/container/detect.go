package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DetectedDeps summarizes project dependency signals found under repoDir.
// Used by `strike container detect` and the /devcontainer skill (E12.5).
type DetectedDeps struct {
	// Markers lists relative paths that triggered detection (sorted).
	Markers []string `json:"markers"`

	// Languages / toolchains
	Go     bool `json:"go,omitempty"`
	Node   bool `json:"node,omitempty"`
	Python bool `json:"python,omitempty"`
	Rust   bool `json:"rust,omitempty"`
	Nix    bool `json:"nix,omitempty"`
	Make   bool `json:"make,omitempty"`

	// Suggested versions (best-effort; empty = product default).
	NodeVersion   string `json:"nodeVersion,omitempty"`
	PythonVersion string `json:"pythonVersion,omitempty"`
	GoVersion     string `json:"goVersion,omitempty"`

	// Suggested apt packages (deduped, sorted).
	AptPackages []string `json:"aptPackages,omitempty"`

	// Suggested runtime Config flags for Dockerfile branches.
	NeedsNode   bool `json:"needsNode,omitempty"`
	NeedsPython bool `json:"needsPython,omitempty"`
	NeedsGo     bool `json:"needsGo,omitempty"`
	NeedsRust   bool `json:"needsRust,omitempty"`
}

// nodeVersionRe matches engines.node ranges like ">=18" or "20.x".
var nodeVersionRe = regexp.MustCompile(`(?:>=?\s*)?(\d{1,2})`)

// goVersionRe matches `go 1.22` lines in go.mod.
var goVersionRe = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)\s*$`)

// DetectProjectDeps scans repoDir for common dependency manifests.
// Missing files are ignored; the result is always non-nil.
func DetectProjectDeps(repoDir string) DetectedDeps {
	var d DetectedDeps
	apt := map[string]struct{}{}
	mark := func(rel string) {
		d.Markers = append(d.Markers, rel)
	}
	addApt := func(pkgs ...string) {
		for _, p := range pkgs {
			p = strings.TrimSpace(p)
			if p != "" {
				apt[p] = struct{}{}
			}
		}
	}
	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(repoDir, name))
		return err == nil
	}
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(repoDir, name))
		if err != nil {
			return ""
		}
		return string(data)
	}

	// Go
	if exists("go.mod") {
		mark("go.mod")
		d.Go = true
		d.NeedsGo = true
		if m := goVersionRe.FindStringSubmatch(read("go.mod")); len(m) == 2 {
			d.GoVersion = m[1]
		}
		addApt("golang-go", "gcc", "libc6-dev", "make")
	}

	// Node
	if exists("package.json") {
		mark("package.json")
		d.Node = true
		d.NeedsNode = true
		d.NodeVersion = detectNodeVersion(read("package.json"))
		addApt() // node installed via nodesource in Dockerfile branch
	}
	if exists("package-lock.json") {
		mark("package-lock.json")
	}
	if exists("pnpm-lock.yaml") {
		mark("pnpm-lock.yaml")
		d.Node = true
		d.NeedsNode = true
	}
	if exists("yarn.lock") {
		mark("yarn.lock")
		d.Node = true
		d.NeedsNode = true
	}

	// Python
	for _, name := range []string{"requirements.txt", "pyproject.toml", "Pipfile", "setup.py", "environment.yml"} {
		if exists(name) {
			mark(name)
			d.Python = true
			d.NeedsPython = true
		}
	}
	if d.Python {
		d.PythonVersion = "3"
		addApt("python3", "python3-pip", "python3-venv")
	}

	// Rust
	if exists("Cargo.toml") {
		mark("Cargo.toml")
		d.Rust = true
		d.NeedsRust = true
		addApt("curl", "build-essential", "pkg-config")
	}

	// Nix
	if exists("flake.nix") {
		mark("flake.nix")
		d.Nix = true
		// Nix inside Docker is heavy; surface marker only — skill asks user.
	}
	if exists("shell.nix") {
		mark("shell.nix")
		d.Nix = true
	}

	// Make
	if exists("Makefile") || exists("makefile") {
		if exists("Makefile") {
			mark("Makefile")
		} else {
			mark("makefile")
		}
		d.Make = true
		addApt("make")
	}

	// Sort markers + apt
	sort.Strings(d.Markers)
	if len(apt) > 0 {
		d.AptPackages = make([]string, 0, len(apt))
		for p := range apt {
			d.AptPackages = append(d.AptPackages, p)
		}
		sort.Strings(d.AptPackages)
	}
	return d
}

func detectNodeVersion(packageJSON string) string {
	if packageJSON == "" {
		return "22"
	}
	var meta struct {
		Engines struct {
			Node string `json:"node"`
		} `json:"engines"`
		Volta struct {
			Node string `json:"node"`
		} `json:"volta"`
	}
	if err := json.Unmarshal([]byte(packageJSON), &meta); err != nil {
		return "22"
	}
	for _, cand := range []string{meta.Volta.Node, meta.Engines.Node} {
		if cand == "" {
			continue
		}
		if m := nodeVersionRe.FindStringSubmatch(cand); len(m) == 2 {
			return m[1]
		}
	}
	return "22"
}

// ApplyDetected merges detection into cfg (does not override non-zero user choices
// for versions when already set; always ORs boolean Needs* flags).
func ApplyDetected(cfg Config, d DetectedDeps) Config {
	if d.NeedsNode {
		cfg.NeedsNode = true
		if cfg.NodeVersion == "" && d.NodeVersion != "" {
			cfg.NodeVersion = d.NodeVersion
		}
	}
	if d.NeedsPython {
		cfg.NeedsPython = true
		if cfg.PythonVersion == "" && d.PythonVersion != "" {
			cfg.PythonVersion = d.PythonVersion
		}
	}
	// Merge apt suggestions without duplicates.
	if len(d.AptPackages) > 0 {
		seen := map[string]struct{}{}
		for _, p := range cfg.AptPackages {
			seen[p] = struct{}{}
		}
		for _, p := range d.AptPackages {
			if _, ok := seen[p]; ok {
				continue
			}
			// Skip pure language runtimes that Dockerfile branches install.
			if p == "python3" || p == "python3-pip" || p == "python3-venv" || p == "golang-go" {
				// Still useful when Needs* branches are off; include when not using branch.
				if (p == "python3" || p == "python3-pip" || p == "python3-venv") && cfg.NeedsPython {
					continue
				}
				if p == "golang-go" && d.NeedsGo {
					// Go toolchain often installed via official image or apt; keep make/gcc.
					continue
				}
			}
			cfg.AptPackages = append(cfg.AptPackages, p)
			seen[p] = struct{}{}
		}
	}
	return cfg
}

// SuggestedContainerJSON returns a partial container config object suitable for
// writing into .strike/container.json after user confirmation.
func (d DetectedDeps) SuggestedContainerJSON() map[string]any {
	out := map[string]any{}
	if len(d.AptPackages) > 0 {
		// Filter language runtimes handled by needs* branches.
		pkgs := make([]string, 0, len(d.AptPackages))
		for _, p := range d.AptPackages {
			if d.NeedsPython && (p == "python3" || p == "python3-pip" || p == "python3-venv") {
				continue
			}
			if d.NeedsGo && p == "golang-go" {
				continue
			}
			pkgs = append(pkgs, p)
		}
		if len(pkgs) > 0 {
			out["packages"] = pkgs
		}
	}
	if d.NeedsNode {
		out["needsNode"] = true
		if d.NodeVersion != "" {
			out["nodeVersion"] = d.NodeVersion
		}
	}
	if d.NeedsPython {
		out["needsPython"] = true
		if d.PythonVersion != "" {
			out["pythonVersion"] = d.PythonVersion
		}
	}
	if d.NeedsGo {
		out["needsGo"] = true
		if d.GoVersion != "" {
			out["goVersion"] = d.GoVersion
		}
	}
	if d.NeedsRust {
		out["needsRust"] = true
	}
	return out
}
