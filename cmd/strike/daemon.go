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

const daemonUsage = `Run strike as a per-user background daemon.

Usage:
  strike daemon install [--addr <host:port>] [--allow-private-bind] [--start]
  strike daemon uninstall
  strike daemon status
  strike daemon token
  strike daemon logout
  strike daemon grant [--ttl 15m] [--base http://127.0.0.1:8787]
  strike daemon revoke [--base http://127.0.0.1:8787]

install writes a service definition and a stable 0600 API token at
~/.strike/daemon.token. Linux writes
$XDG_CONFIG_HOME/systemd/user/strike-daemon.service, or
~/.config/systemd/user/strike-daemon.service when XDG_CONFIG_HOME is unset.
macOS writes ~/Library/LaunchAgents/ca.jonathanung.strike-daemon.plist
(launchd). Windows and other systems are unsupported; run strike serve
manually (WSL2 is supported). --start enables and starts the service.
Without --start, install prints the platform commands and recommends
--start. The daemon is "strike serve --auth --token-file"; the web UI
connects with the token ("strike daemon token" prints it).
logout replaces that file with a new token, drops short-lived grants, and
revokes paired browsers on the next /v1 request. A daemon started with
--token-file loads the new value without a restart; one started with
--token keeps the old value until it is restarted.
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

func runDaemonCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, daemonUsage)
		return 0
	}
	switch args[0] {
	case "install":
		return daemonInstall(args[1:], stdout, stderr)
	case "uninstall":
		return daemonUninstall(stdout, stderr)
	case "status":
		return daemonStatus(stdout, stderr)
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
		fmt.Fprintf(stdout, "Rotated %s\n%s\nA serve process started with --token-file uses this token on the next /v1 request, drops short-lived grants, and revokes paired browsers. A process started with --token keeps the previous value until restart.\n", daemonTokenPath(), tok)
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
	opts, code, err := parseDaemonInstallArgs(args)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return code
	}
	host, err := currentDaemonHost()
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return installDaemon(host, opts, nil, stdout, stderr)
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
