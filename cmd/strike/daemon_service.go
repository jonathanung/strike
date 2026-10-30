package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/daemon/unit"
	"github.com/jonathanung/strike/internal/frontend/server"
)

// daemonCommander runs service-manager commands. Tests pass a fake so
// launchctl and systemctl are never executed. A nil commander uses exec.
type daemonCommander interface {
	CombinedOutput(name string, args ...string) ([]byte, error)
}

type execDaemonCommander struct{}

func (execDaemonCommander) CombinedOutput(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("%s timed out after 30s", name)
	}
	return out, err
}

// daemonHost is the OS view install/uninstall/status switch on.
// GOOS is runtime.GOOS in production and an explicit value in tests.
type daemonHost struct {
	GOOS string
	Home string
	XDG  string
	UID  int
	Exe  string
}

func currentDaemonHost() (daemonHost, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return daemonHost{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return daemonHost{}, err
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	return daemonHost{
		GOOS: runtime.GOOS,
		Home: home,
		XDG:  os.Getenv("XDG_CONFIG_HOME"),
		UID:  os.Getuid(),
		Exe:  exe,
	}, nil
}

type daemonInstallOpts struct {
	addr         string
	allowPrivate bool
	start        bool
}

func parseDaemonInstallArgs(args []string) (daemonInstallOpts, int, error) {
	opts := daemonInstallOpts{addr: "127.0.0.1:8787"}
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--allow-private-bind":
			opts.allowPrivate = true
		case args[i] == "--start":
			opts.start = true
		case args[i] == "--addr" && i+1 < len(args):
			i++
			opts.addr = args[i]
		case strings.HasPrefix(args[i], "--addr="):
			opts.addr = strings.TrimPrefix(args[i], "--addr=")
		default:
			return opts, 2, fmt.Errorf("unknown daemon install flag %q", args[i])
		}
	}
	return opts, 0, nil
}

func installDaemon(host daemonHost, opts daemonInstallOpts, cmd daemonCommander, stdout, stderr io.Writer) int {
	if !unit.Supported(host.GOOS) {
		fmt.Fprintln(stderr, "strike:", unit.UnsupportedMessage(host.GOOS))
		return 1
	}
	if _, err := server.ResolveDaemonBindAddr(opts.addr, opts.allowPrivate); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 2
	}
	def, err := unit.DefinitionPath(host.GOOS, host.Home, host.XDG)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	tokenPath := unit.TokenPath(host.Home)
	if _, _, err := loadOrMintTokenFile(tokenPath); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if host.GOOS == "darwin" {
		if err := os.MkdirAll(unit.LogDir(host.Home), 0o755); err != nil {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
	}
	if err := os.MkdirAll(filepath.Dir(def), 0o755); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	var body string
	switch host.GOOS {
	case "linux":
		body = unit.RenderSystemd(host.Exe, tokenPath, opts.addr, opts.allowPrivate)
	case "darwin":
		body = unit.RenderLaunchd(host.Exe, tokenPath, opts.addr, host.Home, opts.allowPrivate)
	default:
		fmt.Fprintln(stderr, "strike:", unit.UnsupportedMessage(host.GOOS))
		return 1
	}
	if err := os.WriteFile(def, []byte(body), 0o644); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if err := os.Chmod(def, 0o644); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if !opts.start {
		writeInstallHint(stdout, host.GOOS, def, tokenPath, opts)
		return 0
	}
	var ran [][]string
	if host.GOOS == "darwin" {
		// bootstrap fails if this label is already loaded. Drop it first.
		// A missing job on the first start is not worth a warning.
		pre, err := unit.PreRemoveArgv(host.GOOS, host.UID)
		if err != nil {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
		_ = runServiceCommands(cmd, pre, true, io.Discard)
		ran = append(ran, pre...)
	}
	argv, err := unit.StartArgv(host.GOOS, host.UID, def)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if err := runServiceCommands(cmd, argv, false, stderr); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		fmt.Fprintln(stderr, "Start it manually:")
		manual, _ := unit.ManualStartLines(host.GOOS, def)
		for _, line := range manual {
			fmt.Fprintln(stderr, "  "+line)
		}
		writeInstallHeader(stdout, def, tokenPath)
		return 1
	}
	ran = append(ran, argv...)
	writeInstallHeader(stdout, def, tokenPath)
	fmt.Fprintln(stdout, "Started with:")
	for _, a := range ran {
		fmt.Fprintf(stdout, "  %s\n", formatArgv(a))
	}
	if code := verifyStartedDaemon(opts.addr, tokenPath, stdout, stderr); code != 0 {
		return code
	}
	fmt.Fprintf(stdout, "Then open http://%s/ in a browser.\n", opts.addr)
	fmt.Fprintln(stdout, "Sign the browser in with the one-time link from `strike gateway url`, or paste the token from `strike daemon token` in the Gateways panel.")
	return 0
}

// daemonProbeBudget bounds how long install --start waits for the started
// daemon to answer with the installed token. Tests shrink both values.
var (
	daemonProbeBudget   = 3 * time.Second
	daemonProbeInterval = 200 * time.Millisecond
)

// verifyStartedDaemon checks that the daemon now answering at addr accepts
// the token in tokenPath — the file the installed unit passes to serve via
// --token-file (#1679). A daemon that answers but rejects the token means
// something else holds the address with another credential: that is a hard
// failure with remediation text. A daemon that does not answer yet is only
// a warning; the service manager may still be starting it.
func verifyStartedDaemon(addr, tokenPath string, stdout, stderr io.Writer) int {
	tok, err := readDaemonToken(tokenPath)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	base := "http://" + dialableAddr(addr)
	deadline := time.Now().Add(daemonProbeBudget)
	for {
		status, _, perr := daemonAuthedJSON(http.MethodGet, base+"/v1/gateway", tok, nil)
		if perr == nil && status == http.StatusOK {
			fmt.Fprintf(stdout, "Verified: the daemon at %s accepts the token in %s.\n", base, tokenPath)
			return 0
		}
		if time.Now().After(deadline) {
			if perr == nil {
				fmt.Fprintf(stderr, "strike: the daemon answering at %s does not accept the token in %s (HTTP %d).\n%s\n", base, tokenPath, status, daemonTokenMismatchHint(addr, tokenPath))
				return 1
			}
			fmt.Fprintf(stderr, "strike: could not verify the daemon at %s yet (%v). It may still be starting; check `strike daemon status`.\n", base, perr)
			return 0
		}
		time.Sleep(daemonProbeInterval)
	}
}

// dialableAddr turns a bind address into one a local client can dial:
// an unspecified host means this machine.
func dialableAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// daemonTokenMismatchHint is the remediation printed when a daemon rejects
// the token from the daemon token file (#1679).
func daemonTokenMismatchHint(where, tokenPath string) string {
	return fmt.Sprintf("A different strike serve is probably already using %s with another token (for example a foreground `strike serve` started by hand), so the installed daemon could not take over the address.\nFix: stop that process, then run `strike daemon install --start` again.\nIf the running daemon IS the installed one, its token source disagrees with %s: run `strike daemon logout` to rotate the file token (a daemon started with --token-file picks the new value up on the next request and revokes paired browsers), then sign the browser in again with `strike gateway url`.", where, tokenPath)
}

func writeInstallHeader(stdout io.Writer, def, tokenPath string) {
	fmt.Fprintf(stdout, "Wrote %s\nToken: %s (0600; print with: strike daemon token)\n", def, tokenPath)
}

func writeInstallHint(stdout io.Writer, goos, def, tokenPath string, opts daemonInstallOpts) {
	writeInstallHeader(stdout, def, tokenPath)
	fmt.Fprintln(stdout, "Recommended:")
	fmt.Fprintf(stdout, "  %s\n", recommendStart(opts))
	fmt.Fprintln(stdout, "Or manually:")
	manual, err := unit.ManualStartLines(goos, def)
	if err != nil {
		return
	}
	for _, line := range manual {
		fmt.Fprintf(stdout, "  %s\n", line)
	}
	fmt.Fprintf(stdout, "Then open http://%s/ in a browser.\n", opts.addr)
}

func recommendStart(opts daemonInstallOpts) string {
	parts := []string{"strike daemon install --start"}
	if opts.addr != "" && opts.addr != "127.0.0.1:8787" {
		parts = append(parts, "--addr "+unit.QuoteArg(opts.addr))
	}
	if opts.allowPrivate {
		parts = append(parts, "--allow-private-bind")
	}
	return strings.Join(parts, " ")
}

func formatArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = unit.QuoteArg(a)
	}
	return strings.Join(parts, " ")
}

func daemonUninstall(stdout, stderr io.Writer) int {
	host, err := currentDaemonHost()
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return uninstallDaemon(host, nil, stdout, stderr)
}

func uninstallDaemon(host daemonHost, cmd daemonCommander, stdout, stderr io.Writer) int {
	if !unit.Supported(host.GOOS) {
		fmt.Fprintln(stderr, "strike:", unit.UnsupportedMessage(host.GOOS))
		return 1
	}
	def, err := unit.DefinitionPath(host.GOOS, host.Home, host.XDG)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	pre, err := unit.PreRemoveArgv(host.GOOS, host.UID)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	// Not loaded is normal. Keep going and delete the file.
	_ = runServiceCommands(cmd, pre, true, stderr)
	if err := os.Remove(def); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	post, err := unit.PostRemoveArgv(host.GOOS)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if err := runServiceCommands(cmd, post, false, stderr); err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	fmt.Fprintf(stdout, "Removed %s\nRun:\n", def)
	lines, err := unit.ManualRemoveLines(host.GOOS)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	for _, line := range lines {
		fmt.Fprintf(stdout, "  %s\n", line)
	}
	return 0
}

func daemonStatus(stdout, stderr io.Writer) int {
	host, err := currentDaemonHost()
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	return statusDaemon(host, nil, stdout, stderr)
}

func statusDaemon(host daemonHost, cmd daemonCommander, stdout, stderr io.Writer) int {
	if !unit.Supported(host.GOOS) {
		fmt.Fprintln(stderr, "strike:", unit.UnsupportedMessage(host.GOOS))
		return 1
	}
	def, err := unit.DefinitionPath(host.GOOS, host.Home, host.XDG)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if _, err := os.Stat(def); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stdout, "not installed (run: strike daemon install)")
		return 0
	} else if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	argv, err := unit.StatusArgv(host.GOOS, host.UID)
	if err != nil {
		fmt.Fprintln(stderr, "strike:", err)
		return 1
	}
	if cmd == nil {
		cmd = execDaemonCommander{}
	}
	out, runErr := cmd.CombinedOutput(argv[0], argv[1:]...)
	fmt.Fprintf(stdout, "installed: %s\n$ %s\n", def, formatArgv(argv))
	if len(out) > 0 {
		if _, err := stdout.Write(out); err != nil {
			fmt.Fprintln(stderr, "strike:", err)
			return 1
		}
		if out[len(out)-1] != '\n' {
			fmt.Fprintln(stdout)
		}
	}
	if runErr != nil {
		if len(out) == 0 {
			fmt.Fprintln(stderr, "strike:", runErr)
		}
		return 1
	}
	return 0
}

func runServiceCommands(cmd daemonCommander, argvs [][]string, ignoreErr bool, stderr io.Writer) error {
	if cmd == nil {
		cmd = execDaemonCommander{}
	}
	for _, argv := range argvs {
		if len(argv) == 0 {
			continue
		}
		out, err := cmd.CombinedOutput(argv[0], argv[1:]...)
		if err == nil {
			continue
		}
		msg := strings.TrimSpace(string(out))
		var wrapped error
		if msg != "" {
			wrapped = fmt.Errorf("%s: %s", formatArgv(argv), msg)
		} else {
			wrapped = fmt.Errorf("%s: %w", formatArgv(argv), err)
		}
		if ignoreErr {
			fmt.Fprintln(stderr, "strike:", wrapped)
			continue
		}
		return wrapped
	}
	return nil
}
