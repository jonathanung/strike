package lsp

import (
	"fmt"
	"os/exec"
	"strings"
)

// InstallHint is optional guidance when a configured language server binary
// is missing. Never auto-installs; operators must approve any install action.
type InstallHint struct {
	Server  string `json:"server"`
	Command string `json:"command"`
	Missing bool   `json:"missing"`
	// Guidance is human-readable install advice (package names / docs).
	Guidance string `json:"guidance,omitempty"`
	// SuggestedCommands are explicit, operator-approved install commands.
	// Strike never runs these automatically.
	SuggestedCommands []string `json:"suggestedCommands,omitempty"`
}

// knownInstallers maps common LSP commands to install guidance.
var knownInstallers = map[string]InstallHint{
	"gopls": {
		Guidance:          "Install the Go language server (gopls).",
		SuggestedCommands: []string{"go install golang.org/x/tools/gopls@latest"},
	},
	"typescript-language-server": {
		Guidance:          "Install the TypeScript language server via npm.",
		SuggestedCommands: []string{"npm install -g typescript-language-server typescript"},
	},
	"pyright-langserver": {
		Guidance:          "Install Pyright via npm or pip.",
		SuggestedCommands: []string{"npm install -g pyright", "pip install pyright"},
	},
	"pyright": {
		Guidance:          "Install Pyright via npm or pip.",
		SuggestedCommands: []string{"npm install -g pyright", "pip install pyright"},
	},
	"rust-analyzer": {
		Guidance:          "Install rust-analyzer (rustup component or package manager).",
		SuggestedCommands: []string{"rustup component add rust-analyzer", "brew install rust-analyzer"},
	},
	"clangd": {
		Guidance:          "Install clangd from LLVM or your OS package manager.",
		SuggestedCommands: []string{"brew install llvm", "sudo apt install clangd"},
	},
}

// LookPath reports whether command is findable on PATH (or absolute and exists).
func LookPath(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	_, err := exec.LookPath(command)
	return err == nil
}

// MissingInstallHints returns install guidance for configured servers whose
// binaries are not on PATH. Does not start servers or run installers.
func (m *Manager) MissingInstallHints() []InstallHint {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []InstallHint
	for name, cfg := range m.cfgs {
		cmd := strings.TrimSpace(cfg.Command)
		if cmd == "" || LookPath(cmd) {
			continue
		}
		h := InstallHint{
			Server:   name,
			Command:  cmd,
			Missing:  true,
			Guidance: fmt.Sprintf("Language server %q (%s) is not installed or not on PATH.", name, cmd),
		}
		base := filepathBase(cmd)
		if known, ok := knownInstallers[base]; ok {
			if known.Guidance != "" {
				h.Guidance = known.Guidance
			}
			h.SuggestedCommands = append([]string(nil), known.SuggestedCommands...)
		}
		out = append(out, h)
	}
	// stable order by server name
	sortHints(out)
	return out
}

func filepathBase(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

func sortHints(h []InstallHint) {
	for i := 0; i < len(h); i++ {
		for j := i + 1; j < len(h); j++ {
			if h[j].Server < h[i].Server {
				h[i], h[j] = h[j], h[i]
			}
		}
	}
}

// FormatInstallHints renders operator-facing text for /lsp missing servers.
func FormatInstallHints(hints []InstallHint) string {
	if len(hints) == 0 {
		return "All configured language servers are available on PATH."
	}
	var b strings.Builder
	b.WriteString("Missing language servers (install explicitly; strike never auto-installs):\n")
	for _, h := range hints {
		fmt.Fprintf(&b, "- %s (%s): %s\n", h.Server, h.Command, h.Guidance)
		for _, c := range h.SuggestedCommands {
			fmt.Fprintf(&b, "    $ %s\n", c)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
