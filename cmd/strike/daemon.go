package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/frontend/server"
	"github.com/jonathanung/strike-cli/internal/persist/workspace"
)

const daemonUsage = `Run strike as a per-user background daemon (systemd user service).

Usage:
  strike daemon install [--addr <host:port>] [--allow-private-bind]
  strike daemon uninstall
  strike daemon status
  strike daemon token

install writes ~/.config/systemd/user/strike-daemon.service and a stable
0600 API token at ~/.strike/daemon.token, then prints the systemctl
commands to enable it. The daemon is "strike serve --auth"; the web UI
connects with the token ("strike daemon token" prints it).
Default bind is loopback. For phone access over Tailscale, install with
--addr <tailnet-ip>:8787 --allow-private-bind.`

// loadOrMintTokenFile reads a daemon token from path, minting and writing
// one (0600) when missing. Reports whether it created the file.
func loadOrMintTokenFile(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		if tok := strings.TrimSpace(string(data)); tok != "" {
			return tok, false, nil
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("read token file: %w", err)
	}
	tok, err := server.MintToken()
	if err != nil {
		return "", false, fmt.Errorf("minting token: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", false, fmt.Errorf("write token file: %w", err)
	}
	return tok, true, nil
}

func daemonTokenPath() string { return filepath.Join(workspace.DefaultDir(), "daemon.token") }

func systemdUserUnitPath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cfg, "systemd", "user", "strike-daemon.service"), nil
}

// renderSystemdUnit builds the user unit. exe is the absolute strike path.
func renderSystemdUnit(exe, addr string, allowPrivateBind bool) string {
	exec := fmt.Sprintf("%s serve --auth --token-file %s --addr %s", exe, daemonTokenPath(), addr)
	if allowPrivateBind {
		exec += " --allow-private-bind"
	}
	return "[Unit]\n" +
		"Description=Strike daemon (web UI + engine API)\n" +
		"After=network.target\n\n" +
		"[Service]\n" +
		"ExecStart=" + exec + "\n" +
		"Restart=on-failure\n\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
}

func runDaemonCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, daemonUsage)
		return 0
	}
	switch args[0] {
	case "install":
		return daemonInstall(args[1:], stdout, stderr)
	case "uninstall":
		unit, err := systemdUserUnitPath()
		if err != nil {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
		if err := os.Remove(unit); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
		fmt.Fprintf(stdout, "Removed %s\nRun: systemctl --user daemon-reload\n", unit)
		return 0
	case "status":
		unit, err := systemdUserUnitPath()
		if err != nil {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
		if _, err := os.Stat(unit); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stdout, "not installed (run: strike daemon install)")
			return 0
		}
		fmt.Fprintf(stdout, "installed: %s\nCheck: systemctl --user status strike-daemon\n", unit)
		return 0
	case "token":
		tok, _, err := loadOrMintTokenFile(daemonTokenPath())
		if err != nil {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
		fmt.Fprintln(stdout, tok)
		return 0
	default:
		fmt.Fprintf(stderr, "strike: unknown daemon command %q\n%s\n", args[0], daemonUsage)
		return 2
	}
}

func daemonInstall(args []string, stdout, stderr io.Writer) int {
	addr := "127.0.0.1:8787"
	allowPrivate := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--allow-private-bind":
			allowPrivate = true
		case args[i] == "--addr" && i+1 < len(args):
			i++
			addr = args[i]
		case strings.HasPrefix(args[i], "--addr="):
			addr = strings.TrimPrefix(args[i], "--addr=")
		default:
			fmt.Fprintf(stderr, "strike: unknown daemon install flag %q\n", args[i])
			return 2
		}
	}
	if _, err := server.ResolveDaemonBindAddr(addr, allowPrivate); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 2
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	if _, _, err := loadOrMintTokenFile(daemonTokenPath()); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	unit, err := systemdUserUnitPath()
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if err := os.WriteFile(unit, []byte(renderSystemdUnit(exe, addr, allowPrivate)), 0o644); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Wrote %s\nToken: %s (0600; print with: strike daemon token)\nEnable:\n  systemctl --user daemon-reload\n  systemctl --user enable --now strike-daemon\nThen open http://%s/ in a browser.\n", unit, daemonTokenPath(), addr)
	return 0
}
