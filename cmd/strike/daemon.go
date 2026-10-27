package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike/internal/frontend/server"
	"github.com/jonathanung/strike/internal/persist/workspace"
	"github.com/jonathanung/strike/internal/trust/usageaudit"
)

const daemonUsage = `Run strike as a per-user background daemon (systemd user service).

Usage:
  strike daemon install [--addr <host:port>] [--allow-private-bind]
  strike daemon uninstall
  strike daemon status
  strike daemon token
  strike daemon logout
  strike daemon grant [--ttl 15m] [--base http://127.0.0.1:8787]
  strike daemon revoke [--base http://127.0.0.1:8787]

install writes ~/.config/systemd/user/strike-daemon.service and a stable
0600 API token at ~/.strike/daemon.token, then prints the systemctl
commands to enable it. The daemon is "strike serve --auth --token-file";
the web UI connects with the token ("strike daemon token" prints it).
logout replaces that file with a new token and drops short-lived grants
on the next /v1 request. A daemon started with --token-file loads the new
value without a restart; one started with --token keeps the old value
until it is restarted.
grant asks the running daemon for a short-lived bearer and prints that
bearer on stdout only. revoke drops those grants and does not rotate the
host token. Neither command prints the host token.
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

// rotateDaemonToken replaces the token file with a new 0600 secret.
// The previous bearer stops working for any serve process that re-reads
// this path. The file is created if it was missing.
func rotateDaemonToken(path string) (string, error) {
	tok, err := server.MintToken()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write token file: %w", err)
	}
	return tok, nil
}

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
	case "logout":
		tok, err := rotateDaemonToken(daemonTokenPath())
		if err != nil {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
		if log, err := usageaudit.Open(usageaudit.DefaultDir()); err != nil {
			fmt.Fprintf(stderr, "strike: usage audit log: %v\n", err)
		} else {
			_, _ = log.Append(usageaudit.Input{Action: usageaudit.ActionToken, Summary: "daemon token file rotated"})
			_ = log.Close()
		}
		fmt.Fprintf(stdout, "Rotated %s\n%s\nA serve process started with --token-file uses this token on the next /v1 request and drops short-lived grants. A process started with --token keeps the previous value until restart.\n", daemonTokenPath(), tok)
		return 0
	case "grant":
		return daemonGrant(args[1:], stdout, stderr)
	case "revoke":
		return daemonRevoke(args[1:], stdout, stderr)
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

func daemonGrant(args []string, stdout, stderr io.Writer) int {
	base := "http://127.0.0.1:8787"
	ttlRaw := "15m"
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--base" && i+1 < len(args):
			i++
			base = args[i]
		case strings.HasPrefix(args[i], "--base="):
			base = strings.TrimPrefix(args[i], "--base=")
		case args[i] == "--ttl" && i+1 < len(args):
			i++
			ttlRaw = args[i]
		case strings.HasPrefix(args[i], "--ttl="):
			ttlRaw = strings.TrimPrefix(args[i], "--ttl=")
		default:
			fmt.Fprintf(stderr, "strike: unknown daemon grant flag %q\n", args[i])
			return 2
		}
	}
	ttl, err := parseGrantTTL(ttlRaw)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 2
	}
	base = daemonBaseURL(base)
	hostToken, _, err := loadOrMintTokenFile(daemonTokenPath())
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	status, body, err := daemonAuthedJSON(http.MethodPost, base+"/v1/gateway/grants", hostToken, map[string]any{
		"ttlSeconds": int(ttl.Seconds()),
	})
	if err != nil {
		fmt.Fprintln(stderr, "strike:", redactSecrets(err.Error(), hostToken))
		return 1
	}
	if status != http.StatusCreated {
		fmt.Fprintf(stderr, "strike: grant failed: %s\n", redactSecrets(string(body), hostToken))
		return 1
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expiresAt"`
	}
	if err := json.Unmarshal(body, &out); err != nil || !validDaemonToken(out.Token) {
		fmt.Fprintln(stderr, "strike: grant response was not a token")
		return 1
	}
	fmt.Fprintln(stdout, out.Token)
	fmt.Fprintf(stderr, "Grant expires %s. The secret is on stdout; do not log that stream.\n", out.ExpiresAt)
	return 0
}

func daemonRevoke(args []string, stdout, stderr io.Writer) int {
	base := "http://127.0.0.1:8787"
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--base" && i+1 < len(args):
			i++
			base = args[i]
		case strings.HasPrefix(args[i], "--base="):
			base = strings.TrimPrefix(args[i], "--base=")
		default:
			fmt.Fprintf(stderr, "strike: unknown daemon revoke flag %q\n", args[i])
			return 2
		}
	}
	base = daemonBaseURL(base)
	hostToken, _, err := loadOrMintTokenFile(daemonTokenPath())
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	status, body, err := daemonAuthedJSON(http.MethodDelete, base+"/v1/gateway/grants", hostToken, nil)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", redactSecrets(err.Error(), hostToken))
		return 1
	}
	if status != http.StatusOK {
		fmt.Fprintf(stderr, "strike: revoke failed: %s\n", redactSecrets(string(body), hostToken))
		return 1
	}
	fmt.Fprintf(stdout, "Revoked short-lived grants on %s\nThe host token file was not changed. strike daemon logout rotates it.\n", base)
	return 0
}
