package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/daemon/unit"
)

type fakeCmd struct {
	calls [][]string
	fail  map[string]string
}

func (f *fakeCmd) CombinedOutput(name string, args ...string) ([]byte, error) {
	call := append([]string{name}, append([]string{}, args...)...)
	f.calls = append(f.calls, call)
	key := strings.Join(call, " ")
	if f.fail != nil {
		if msg, ok := f.fail[key]; ok {
			return []byte(msg), errors.New("exit status 1")
		}
		if msg, ok := f.fail[name]; ok {
			return []byte(msg), errors.New("exit status 1")
		}
	}
	return []byte("ok\n"), nil
}

func testHost(t *testing.T, goos string) daemonHost {
	t.Helper()
	home := t.TempDir()
	return daemonHost{
		GOOS: goos,
		Home: home,
		XDG:  filepath.Join(home, "xdg-trap"),
		UID:  501,
		Exe:  "/usr/local/bin/strike",
	}
}

func TestDaemonInstallDarwinPrintsLaunchctl(t *testing.T) {
	host := testHost(t, "darwin")
	cmd := &fakeCmd{fail: map[string]string{"launchctl": "should not run"}}
	var out, errb bytes.Buffer
	if code := installDaemon(host, daemonInstallOpts{addr: "127.0.0.1:8787"}, cmd, &out, &errb); code != 0 {
		t.Fatalf("code = %d stderr=%s", code, errb.String())
	}
	if len(cmd.calls) != 0 {
		t.Fatalf("launchctl called without --start: %#v", cmd.calls)
	}
	plist := filepath.Join(host.Home, "Library", "LaunchAgents", "ca.jonathanung.strike-daemon.plist")
	body, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "Application Support") || strings.Contains(plist, "xdg-trap") {
		t.Fatalf("plist path or body is wrong: %s\n%s", plist, body)
	}
	info, err := os.Stat(filepath.Join(host.Home, ".strike", "daemon.token"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token perm = %v", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(host.Home, ".strike", "logs")); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"Wrote " + plist,
		"Recommended:",
		"strike daemon install --start",
		"launchctl bootstrap gui/$(id -u) " + plist,
		"launchctl kickstart -k gui/$(id -u)/ca.jonathanung.strike-daemon",
		"Then open http://127.0.0.1:8787/ in a browser.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("stdout missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "systemctl") {
		t.Fatalf("darwin install printed systemctl:\n%s", text)
	}
	if strings.Contains(string(body), "<string>/usr/local/bin/strike</string>") == false ||
		!strings.Contains(string(body), "<string>serve</string>") ||
		!strings.Contains(string(body), "<key>RunAtLoad</key>") {
		t.Fatalf("plist body:\n%s", body)
	}
}

func TestDaemonInstallDarwinStartRunsLaunchctl(t *testing.T) {
	host := testHost(t, "darwin")
	host.Exe = "/Users/Ada Lovelace/bin/strike"
	cmd := &fakeCmd{}
	var out, errb bytes.Buffer
	opts := daemonInstallOpts{addr: "100.64.0.5:8787", allowPrivate: true, start: true}
	if code := installDaemon(host, opts, cmd, &out, &errb); code != 0 {
		t.Fatalf("code = %d stderr=%s stdout=%s", code, errb.String(), out.String())
	}
	plist := filepath.Join(host.Home, "Library", "LaunchAgents", "ca.jonathanung.strike-daemon.plist")
	want := [][]string{
		{"launchctl", "bootout", "gui/501/ca.jonathanung.strike-daemon"},
		{"launchctl", "bootstrap", "gui/501", plist},
		{"launchctl", "kickstart", "-k", "gui/501/ca.jonathanung.strike-daemon"},
	}
	if !argvEqual(cmd.calls, want) {
		t.Fatalf("calls = %#v", cmd.calls)
	}
	body, err := os.ReadFile(plist)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "<string>/Users/Ada Lovelace/bin/strike</string>") {
		t.Fatalf("plist did not keep the spaced path:\n%s", body)
	}
	if !strings.Contains(string(body), "<string>--allow-private-bind</string>") {
		t.Fatalf("plist missing private bind:\n%s", body)
	}
	text := out.String()
	for _, want := range []string{
		"launchctl bootout gui/501/ca.jonathanung.strike-daemon",
		"launchctl bootstrap gui/501 " + plist,
		"launchctl kickstart -k gui/501/ca.jonathanung.strike-daemon",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("stdout missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "systemctl") || strings.Contains(text, "$(id -u)") {
		t.Fatalf("--start should print the commands it ran, not the shell form:\n%s", text)
	}
	if !strings.Contains(text, "http://100.64.0.5:8787/") {
		t.Fatalf("stdout missing URL:\n%s", text)
	}
}

func TestDaemonInstallDarwinStartStopsOnBootstrapError(t *testing.T) {
	host := testHost(t, "darwin")
	plist := filepath.Join(host.Home, "Library", "LaunchAgents", "ca.jonathanung.strike-daemon.plist")
	cmd := &fakeCmd{fail: map[string]string{
		"launchctl bootstrap gui/501 " + plist: "service already loaded",
	}}
	var out, errb bytes.Buffer
	if code := installDaemon(host, daemonInstallOpts{addr: "127.0.0.1:8787", start: true}, cmd, &out, &errb); code != 1 {
		t.Fatalf("code = %d stderr=%s", code, errb.String())
	}
	if len(cmd.calls) != 2 || cmd.calls[0][1] != "bootout" || cmd.calls[1][1] != "bootstrap" {
		t.Fatalf("calls = %#v", cmd.calls)
	}
	if !strings.Contains(errb.String(), "service already loaded") || !strings.Contains(errb.String(), "launchctl bootstrap gui/$(id -u)") {
		t.Fatalf("stderr = %s", errb.String())
	}
}

func TestDaemonInstallLinuxUsesXDGNotApplicationSupport(t *testing.T) {
	host := testHost(t, "linux")
	cmd := &fakeCmd{fail: map[string]string{"systemctl": "should not run"}}
	var out, errb bytes.Buffer
	opts := daemonInstallOpts{addr: "100.64.0.5:8787", allowPrivate: true}
	if code := installDaemon(host, opts, cmd, &out, &errb); code != 0 {
		t.Fatalf("code = %d stderr=%s", code, errb.String())
	}
	if len(cmd.calls) != 0 {
		t.Fatalf("systemctl called without --start: %#v", cmd.calls)
	}
	unitPath := filepath.Join(host.XDG, "systemd", "user", "strike-daemon.service")
	body, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(unitPath, "Application Support") || strings.Contains(string(body), "Application Support") {
		t.Fatalf("unit path %s body %s", unitPath, body)
	}
	if _, err := os.Stat(filepath.Join(host.Home, "Library", "LaunchAgents")); !os.IsNotExist(err) {
		t.Fatalf("linux install created LaunchAgents: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Wrote " + unitPath,
		"strike daemon install --start --addr 100.64.0.5:8787 --allow-private-bind",
		"systemctl --user daemon-reload",
		"systemctl --user enable --now strike-daemon",
		"http://100.64.0.5:8787/",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("stdout missing %q\n%s", want, text)
		}
	}
	if strings.Contains(text, "launchctl") {
		t.Fatalf("linux install printed launchctl:\n%s", text)
	}
	if !strings.Contains(string(body), "ExecStart=/usr/local/bin/strike serve --auth --token-file ") ||
		!strings.Contains(string(body), "--allow-private-bind") {
		t.Fatalf("unit:\n%s", body)
	}
	info, err := os.Stat(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("unit perm = %v", info.Mode().Perm())
	}
}

func TestDaemonInstallLinuxStart(t *testing.T) {
	host := testHost(t, "linux")
	cmd := &fakeCmd{}
	var out, errb bytes.Buffer
	if code := installDaemon(host, daemonInstallOpts{addr: "127.0.0.1:8787", start: true}, cmd, &out, &errb); code != 0 {
		t.Fatalf("code = %d stderr=%s", code, errb.String())
	}
	want := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", "strike-daemon"},
	}
	if !argvEqual(cmd.calls, want) {
		t.Fatalf("calls = %#v", cmd.calls)
	}
	if !strings.Contains(out.String(), "systemctl --user daemon-reload") || !strings.Contains(out.String(), "systemctl --user enable --now strike-daemon") {
		t.Fatalf("stdout:\n%s", out.String())
	}
}

func TestDaemonInstallLinuxFallsBackToDotConfig(t *testing.T) {
	host := testHost(t, "linux")
	host.XDG = ""
	var out, errb bytes.Buffer
	if code := installDaemon(host, daemonInstallOpts{addr: "127.0.0.1:8787"}, &fakeCmd{}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	unitPath := filepath.Join(host.Home, ".config", "systemd", "user", "strike-daemon.service")
	if _, err := os.Stat(unitPath); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Application Support") {
		t.Fatalf("stdout:\n%s", out.String())
	}
}

func TestDaemonInstallUnsupported(t *testing.T) {
	for _, goos := range []string{"windows", "freebsd", ""} {
		host := testHost(t, goos)
		cmd := &fakeCmd{fail: map[string]string{"systemctl": "no", "launchctl": "no"}}
		var out, errb bytes.Buffer
		if code := installDaemon(host, daemonInstallOpts{addr: "127.0.0.1:8787", start: true}, cmd, &out, &errb); code != 1 {
			t.Fatalf("%s code = %d", goos, code)
		}
		if len(cmd.calls) != 0 {
			t.Fatalf("%s ran commands %#v", goos, cmd.calls)
		}
		msg := errb.String()
		if !strings.Contains(msg, "unsupported") || !strings.Contains(msg, "strike serve") || !strings.Contains(msg, "WSL2 supported") {
			t.Fatalf("%s stderr = %q", goos, msg)
		}
		entries, err := os.ReadDir(host.Home)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("%s wrote into home: %v", goos, entries)
		}
	}
}

func TestDaemonUninstallAndStatus(t *testing.T) {
	t.Run("darwin", func(t *testing.T) {
		host := testHost(t, "darwin")
		var out, errb bytes.Buffer
		if code := installDaemon(host, daemonInstallOpts{addr: "127.0.0.1:8787"}, &fakeCmd{}, &out, &errb); code != 0 {
			t.Fatal(errb.String())
		}
		cmd := &fakeCmd{}
		out.Reset()
		errb.Reset()
		if code := statusDaemon(host, cmd, &out, &errb); code != 0 {
			t.Fatalf("status code = %d stderr=%s stdout=%s", code, errb.String(), out.String())
		}
		if !argvEqual(cmd.calls, [][]string{{"launchctl", "print", "gui/501/ca.jonathanung.strike-daemon"}}) {
			t.Fatalf("status calls = %#v", cmd.calls)
		}
		if !strings.Contains(out.String(), "installed:") || !strings.Contains(out.String(), "launchctl print gui/501/ca.jonathanung.strike-daemon") || !strings.Contains(out.String(), "ok") {
			t.Fatalf("status stdout:\n%s", out.String())
		}
		cmd = &fakeCmd{fail: map[string]string{"launchctl bootout gui/501/ca.jonathanung.strike-daemon": "No such process"}}
		out.Reset()
		errb.Reset()
		if code := uninstallDaemon(host, cmd, &out, &errb); code != 0 {
			t.Fatalf("uninstall code = %d stderr=%s", code, errb.String())
		}
		plist := filepath.Join(host.Home, "Library", "LaunchAgents", "ca.jonathanung.strike-daemon.plist")
		if _, err := os.Stat(plist); !os.IsNotExist(err) {
			t.Fatalf("plist still present: %v", err)
		}
		if !strings.Contains(out.String(), "Removed "+plist) || !strings.Contains(out.String(), "launchctl bootout gui/$(id -u)/ca.jonathanung.strike-daemon") {
			t.Fatalf("uninstall stdout:\n%s", out.String())
		}
		if !strings.Contains(errb.String(), "No such process") {
			t.Fatalf("stderr = %s", errb.String())
		}
		out.Reset()
		if code := statusDaemon(host, &fakeCmd{fail: map[string]string{"launchctl": "no"}}, &out, &errb); code != 0 {
			t.Fatalf("missing status code = %d", code)
		}
		if !strings.Contains(out.String(), "not installed") {
			t.Fatalf("stdout = %s", out.String())
		}
	})
	t.Run("linux", func(t *testing.T) {
		host := testHost(t, "linux")
		var out, errb bytes.Buffer
		if code := installDaemon(host, daemonInstallOpts{addr: "127.0.0.1:8787"}, &fakeCmd{}, &out, &errb); code != 0 {
			t.Fatal(errb.String())
		}
		cmd := &fakeCmd{}
		out.Reset()
		if code := statusDaemon(host, cmd, &out, &errb); code != 0 {
			t.Fatalf("status = %d %s", code, errb.String())
		}
		if !argvEqual(cmd.calls, [][]string{{"systemctl", "--user", "status", "strike-daemon", "--no-pager"}}) {
			t.Fatalf("status calls = %#v", cmd.calls)
		}
		cmd = &fakeCmd{fail: map[string]string{"systemctl --user disable --now strike-daemon": "not loaded"}}
		out.Reset()
		errb.Reset()
		if code := uninstallDaemon(host, cmd, &out, &errb); code != 0 {
			t.Fatalf("uninstall = %d stderr=%s stdout=%s", code, errb.String(), out.String())
		}
		unitPath := filepath.Join(host.XDG, "systemd", "user", "strike-daemon.service")
		if _, err := os.Stat(unitPath); !os.IsNotExist(err) {
			t.Fatal(err)
		}
		want := [][]string{
			{"systemctl", "--user", "disable", "--now", "strike-daemon"},
			{"systemctl", "--user", "daemon-reload"},
		}
		if !argvEqual(cmd.calls, want) {
			t.Fatalf("calls = %#v", cmd.calls)
		}
		if !strings.Contains(out.String(), "systemctl --user disable --now strike-daemon") || !strings.Contains(out.String(), "systemctl --user daemon-reload") {
			t.Fatalf("stdout:\n%s", out.String())
		}
	})
	t.Run("windows", func(t *testing.T) {
		host := testHost(t, "windows")
		var out, errb bytes.Buffer
		if code := uninstallDaemon(host, &fakeCmd{}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "WSL2 supported") {
			t.Fatalf("uninstall code=%d stderr=%s", code, errb.String())
		}
		errb.Reset()
		if code := statusDaemon(host, &fakeCmd{}, &out, &errb); code != 1 || !strings.Contains(errb.String(), "strike serve") {
			t.Fatalf("status code=%d stderr=%s", code, errb.String())
		}
	})
}

func TestDaemonStatusCommandFailure(t *testing.T) {
	host := testHost(t, "linux")
	var out, errb bytes.Buffer
	if code := installDaemon(host, daemonInstallOpts{addr: "127.0.0.1:8787"}, &fakeCmd{}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	cmd := &fakeCmd{fail: map[string]string{"systemctl": "inactive"}}
	out.Reset()
	if code := statusDaemon(host, cmd, &out, &errb); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out.String(), "installed:") || !strings.Contains(out.String(), "inactive") {
		t.Fatalf("stdout:\n%s", out.String())
	}
}

func TestDaemonInstallCLIUsesHostGOOS(t *testing.T) {
	if !unit.Supported(runtime.GOOS) {
		t.Skip(runtime.GOOS)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	var out, errb bytes.Buffer
	if code := runDaemonCLI([]string{"install", "--addr", "127.0.0.1:8787"}, &out, &errb); code != 0 {
		t.Fatalf("code = %d stderr=%s", code, errb.String())
	}
	switch runtime.GOOS {
	case "linux":
		path := filepath.Join(home, "xdg", "systemd", "user", "strike-daemon.service")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "Application Support") || !strings.Contains(string(body), "ExecStart=") {
			t.Fatalf("unit:\n%s", body)
		}
		if _, err := os.Stat(filepath.Join(home, "Library")); !os.IsNotExist(err) {
			t.Fatalf("linux CLI created Library: %v", err)
		}
		if !strings.Contains(out.String(), "systemctl --user enable --now strike-daemon") || !strings.Contains(out.String(), "strike daemon install --start") {
			t.Fatalf("stdout:\n%s", out.String())
		}
	case "darwin":
		path := filepath.Join(home, "Library", "LaunchAgents", "ca.jonathanung.strike-daemon.plist")
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(home, "xdg", "systemd")); !os.IsNotExist(err) {
			t.Fatalf("darwin CLI wrote systemd under XDG: %v", err)
		}
		if _, err := os.Stat(filepath.Join(home, ".strike", "logs")); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "launchctl bootstrap gui/$(id -u) "+path) || strings.Contains(out.String(), "systemctl") {
			t.Fatalf("stdout:\n%s", out.String())
		}
	}
	info, err := os.Stat(filepath.Join(home, ".strike", "daemon.token"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token perm = %v", info.Mode().Perm())
	}
}

func TestDaemonInstallRejectsBadAddr(t *testing.T) {
	host := testHost(t, "linux")
	var out, errb bytes.Buffer
	if code := installDaemon(host, daemonInstallOpts{addr: "0.0.0.0:8787"}, &fakeCmd{}, &out, &errb); code != 2 {
		t.Fatalf("code = %d stderr=%s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(host.XDG, "systemd")); !os.IsNotExist(err) {
		t.Fatalf("wrote a unit for a rejected address: %v", err)
	}
}

func argvEqual(got, want [][]string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if len(got[i]) != len(want[i]) {
			return false
		}
		for j := range got[i] {
			if got[i][j] != want[i][j] {
				return false
			}
		}
	}
	return true
}
