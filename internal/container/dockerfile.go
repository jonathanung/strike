package container

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MinimalDockerfile renders a strike-oriented Dockerfile from Config.
// Harness-install branches from Zone are omitted; strike is the guest agent.
// Project dep detection (node/python/…) is extended by the /devcontainer skill (E12.5).
// Prefer `strike container eject` to materialize Dockerfile.devcontainer.
func MinimalDockerfile(cfg Config, hostUID int) string {
	base := cfg.baseImage()
	shell := cfg.shell()
	mount := cfg.mountPath()
	// shell basename for useradd -s
	shellPath := shell
	if !strings.HasPrefix(shellPath, "/") {
		shellPath = "/bin/" + shellPath
	}
	var b strings.Builder
	b.WriteString("# syntax=docker/dockerfile:1\n")
	fmt.Fprintf(&b, "FROM %s\n\n", base)
	b.WriteString("ARG DEBIAN_FRONTEND=noninteractive\n")
	fmt.Fprintf(&b, "ARG HOST_UID=%d\n\n", hostUID)
	b.WriteString("# Base system packages (cached layer)\n")
	b.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends \\\n")
	b.WriteString("    ca-certificates \\\n")
	b.WriteString("    curl \\\n")
	b.WriteString("    git \\\n")
	b.WriteString("    sudo \\\n")
	b.WriteString("    && rm -rf /var/lib/apt/lists/*\n")
	if len(cfg.AptPackages) > 0 {
		b.WriteString("\n# User-specified apt packages\n")
		b.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends \\\n")
		for _, p := range cfg.AptPackages {
			fmt.Fprintf(&b, "    %s \\\n", p)
		}
		b.WriteString("    && rm -rf /var/lib/apt/lists/*\n")
	}
	// Optional project-oriented deps via config (skill may set these later).
	if cfg.NeedsNode {
		ver := cfg.NodeVersion
		if ver == "" {
			ver = "22"
		}
		fmt.Fprintf(&b, "\n# Node.js %s\n", ver)
		fmt.Fprintf(&b, "RUN curl -fsSL https://deb.nodesource.com/setup_%s.x | bash - \\\n", ver)
		b.WriteString("    && apt-get install -y nodejs \\\n")
		b.WriteString("    && rm -rf /var/lib/apt/lists/*\n")
	}
	if cfg.NeedsPython {
		ver := cfg.PythonVersion
		if ver == "" {
			ver = "3"
		}
		fmt.Fprintf(&b, "\n# Python %s\n", ver)
		b.WriteString("RUN apt-get update && apt-get install -y --no-install-recommends \\\n")
		fmt.Fprintf(&b, "    python%s python3-pip \\\n", ver)
		b.WriteString("    && rm -rf /var/lib/apt/lists/*\n")
	}
	if len(cfg.NpmPackages) > 0 {
		b.WriteString("\n# Global npm packages\n")
		fmt.Fprintf(&b, "RUN npm install -g %s\n", strings.Join(cfg.NpmPackages, " "))
	}
	if len(cfg.PipPackages) > 0 {
		b.WriteString("\n# Pip packages\n")
		fmt.Fprintf(&b, "RUN pip install --break-system-packages %s\n", strings.Join(cfg.PipPackages, " "))
	}
	// Hardcoded strike install placeholder: binary is bind-mounted or copied at launch (E12.4).
	b.WriteString("\n# strike CLI is provided at launch (bind-mount or copy); not baked here.\n")
	if hostUID > 0 {
		b.WriteString("\n# Non-root user matching host UID\n")
		b.WriteString("RUN set -eux; \\\n")
		b.WriteString("    if ! getent passwd strike >/dev/null; then \\\n")
		fmt.Fprintf(&b, "      useradd --create-home --shell %s --uid \"${HOST_UID}\" strike; \\\n", shellPath)
		b.WriteString("    fi; \\\n")
		b.WriteString("    echo 'strike ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/strike; \\\n")
		b.WriteString("    chmod 0440 /etc/sudoers.d/strike\n")
		b.WriteString("USER strike\n")
		b.WriteString("WORKDIR /home/strike\n")
	}
	fmt.Fprintf(&b, "\nENV STRIKE_WORKSPACE=%s\n", mount)
	fmt.Fprintf(&b, "WORKDIR %s\n", mount)
	fmt.Fprintf(&b, "SHELL [%q, \"-c\"]\n", shellPath)
	b.WriteString("CMD [\"sleep\", \"infinity\"]\n")
	return b.String()
}

// ResolveDockerfileBody returns Dockerfile text from path override or template.
func ResolveDockerfileBody(cfg Config, repoDir string, hostUID int) (string, error) {
	if cfg.Dockerfile != "" {
		path := cfg.Dockerfile
		if !filepath.IsAbs(path) {
			path = filepath.Join(repoDir, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read Dockerfile %s: %w", path, err)
		}
		return string(data), nil
	}
	return MinimalDockerfile(cfg, hostUID), nil
}

// HostUID returns the current user id for non-root container mapping (0 on failure).
func HostUID() int {
	// os/user would work; keep dependency-free via env common in CI.
	if s := os.Getenv("SUDO_UID"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return osGetuid()
}
