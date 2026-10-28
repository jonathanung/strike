package unit

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefinitionPath(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		home    string
		xdg     string
		want    string
		wantErr bool
	}{
		{
			name: "linux default config",
			goos: "linux",
			home: "/home/tester",
			want: "/home/tester/.config/systemd/user/strike-daemon.service",
		},
		{
			name: "linux xdg",
			goos: "linux",
			home: "/home/tester",
			xdg:  "/xdg/config",
			want: "/xdg/config/systemd/user/strike-daemon.service",
		},
		{
			name: "linux home that looks like macos still uses .config",
			goos: "linux",
			home: "/Users/jonathan",
			want: "/Users/jonathan/.config/systemd/user/strike-daemon.service",
		},
		{
			name: "darwin ignores xdg and application support",
			goos: "darwin",
			home: "/Users/jonathan",
			xdg:  "/Users/jonathan/Library/Application Support",
			want: "/Users/jonathan/Library/LaunchAgents/ca.jonathanung.strike-daemon.plist",
		},
		{
			name:    "darwin requires home",
			goos:    "darwin",
			wantErr: true,
		},
		{
			name:    "linux requires home without xdg",
			goos:    "linux",
			wantErr: true,
		},
		{
			name:    "windows",
			goos:    "windows",
			home:    `C:\Users\tester`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DefinitionPath(tt.goos, tt.home, tt.xdg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("path = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("path = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "Application Support") {
				t.Fatalf("path uses Application Support: %s", got)
			}
		})
	}
}

func TestUnsupportedMessage(t *testing.T) {
	got := UnsupportedMessage("windows")
	for _, want := range []string{"unsupported on windows", "strike serve", "WSL2 supported"} {
		if !strings.Contains(got, want) {
			t.Fatalf("message %q missing %q", got, want)
		}
	}
	if Supported("windows") || Supported("freebsd") || !Supported("linux") || !Supported("darwin") {
		t.Fatal("Supported mismatch")
	}
}

func TestRenderSystemdUnitGolden(t *testing.T) {
	got := RenderSystemd("/usr/local/bin/strike", "/home/tester/.strike/daemon.token", "100.64.0.5:8787", true)
	compareGolden(t, "strike-daemon.service", got)
	for _, want := range []string{
		"ExecStart=/usr/local/bin/strike serve --auth --token-file /home/tester/.strike/daemon.token --addr 100.64.0.5:8787 --allow-private-bind",
		"WantedBy=default.target",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("unit missing %q", want)
		}
	}
}

func TestRenderSystemdQuotesSpaces(t *testing.T) {
	got := RenderSystemd("/Program Files/strike", "/home/a b/.strike/daemon.token", "127.0.0.1:8787", false)
	for _, want := range []string{
		`"/Program Files/strike"`,
		`"/home/a b/.strike/daemon.token"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("unit missing %q\n%s", want, got)
		}
	}
}

func TestRenderLaunchdPlistGolden(t *testing.T) {
	got := RenderLaunchd(
		"/usr/local/bin/strike",
		"/Users/tester/.strike/daemon.token",
		"127.0.0.1:8787",
		"/Users/tester",
		false,
	)
	compareGolden(t, "ca.jonathanung.strike-daemon.plist", got)
	for _, want := range []string{
		"<key>Label</key>",
		"<string>ca.jonathanung.strike-daemon</string>",
		"<string>serve</string>",
		"<string>--auth</string>",
		"<string>--token-file</string>",
		"<string>/Users/tester/.strike/daemon.token</string>",
		"<string>--addr</string>",
		"<string>127.0.0.1:8787</string>",
		"<true/>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"/Users/tester/.strike/logs/strike-daemon.out.log",
		"/Users/tester/.strike/logs/strike-daemon.err.log",
		"/Users/tester/.strike/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"<key>WorkingDirectory</key>",
		"<string>/Users/tester</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q", want)
		}
	}
	if strings.Contains(got, "--allow-private-bind") {
		t.Fatal("default plist includes --allow-private-bind")
	}
}

func TestLaunchdMatchesSystemdServeArgs(t *testing.T) {
	exe := "/usr/local/bin/strike"
	token := "/home/tester/.strike/daemon.token"
	addr := "100.64.0.5:8787"
	args := ServeArgs(exe, token, addr, true)
	unit := RenderSystemd(exe, token, addr, true)
	plist := RenderLaunchd(exe, token, addr, "/home/tester", true)
	for _, arg := range args {
		if !strings.Contains(unit, arg) {
			t.Errorf("systemd unit missing %q", arg)
		}
		needle := "<string>" + arg + "</string>"
		if !strings.Contains(plist, needle) {
			t.Errorf("plist missing %q", needle)
		}
	}
}

func TestLaunchdPlistEscapesSpaces(t *testing.T) {
	exe := `/Users/A&B/My Apps/strike`
	home := `/Users/Ada Lovelace`
	token := home + "/.strike/daemon.token"
	got := RenderLaunchd(exe, token, "127.0.0.1:8787", home, true)
	compareGolden(t, "ca.jonathanung.strike-daemon-spaces.plist", got)
	if strings.Contains(got, "A&B") {
		t.Fatalf("raw ampersand in plist:\n%s", got)
	}
	if !strings.Contains(got, "<string>/Users/A&amp;B/My Apps/strike</string>") {
		t.Fatalf("escaped exe missing:\n%s", got)
	}
	if !strings.Contains(got, "<string>/Users/Ada Lovelace</string>") {
		t.Fatalf("working directory missing:\n%s", got)
	}
	dec := xml.NewDecoder(strings.NewReader(got))
	var sawExe, sawHome, sawPrivate bool
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch v := tok.(type) {
		case xml.CharData:
			s := string(v)
			switch s {
			case exe:
				sawExe = true
			case home:
				sawHome = true
			case "--allow-private-bind":
				sawPrivate = true
			}
		}
	}
	if !sawExe || !sawHome || !sawPrivate {
		t.Fatalf("xml round trip exe=%v home=%v private=%v", sawExe, sawHome, sawPrivate)
	}
}

func TestCommands(t *testing.T) {
	plist := "/Users/tester/Library/LaunchAgents/ca.jonathanung.strike-daemon.plist"
	start, err := StartArgv("darwin", 501, plist)
	if err != nil {
		t.Fatal(err)
	}
	wantStart := [][]string{
		{"launchctl", "bootstrap", "gui/501", plist},
		{"launchctl", "kickstart", "-k", "gui/501/ca.jonathanung.strike-daemon"},
	}
	if !sameArgv(start, wantStart) {
		t.Fatalf("darwin start = %#v", start)
	}
	manual, err := ManualStartLines("darwin", plist)
	if err != nil {
		t.Fatal(err)
	}
	if manual[0] != "launchctl bootstrap gui/$(id -u) "+plist {
		t.Fatalf("manual bootstrap = %q", manual[0])
	}
	if manual[1] != "launchctl kickstart -k gui/$(id -u)/ca.jonathanung.strike-daemon" {
		t.Fatalf("manual kickstart = %q", manual[1])
	}
	spaced := manualMust(t, "darwin", "/Users/Ada Lovelace/Library/LaunchAgents/ca.jonathanung.strike-daemon.plist")
	if spaced[0] != "launchctl bootstrap gui/$(id -u) '/Users/Ada Lovelace/Library/LaunchAgents/ca.jonathanung.strike-daemon.plist'" {
		t.Fatalf("quoted bootstrap = %q", spaced[0])
	}
	linux, err := StartArgv("linux", 0, "/ignored")
	if err != nil {
		t.Fatal(err)
	}
	wantLinux := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "--now", "strike-daemon"},
	}
	if !sameArgv(linux, wantLinux) {
		t.Fatalf("linux start = %#v", linux)
	}
	boot, err := PreRemoveArgv("darwin", 501)
	if err != nil {
		t.Fatal(err)
	}
	if !sameArgv(boot, [][]string{{"launchctl", "bootout", "gui/501/ca.jonathanung.strike-daemon"}}) {
		t.Fatalf("bootout = %#v", boot)
	}
	lines, err := ManualRemoveLines("darwin")
	if err != nil || lines[0] != "launchctl bootout gui/$(id -u)/ca.jonathanung.strike-daemon" {
		t.Fatalf("manual bootout = %#v err=%v", lines, err)
	}
	st, err := StatusArgv("darwin", 501)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(st, " ") != "launchctl print gui/501/ca.jonathanung.strike-daemon" {
		t.Fatalf("status = %#v", st)
	}
	lst, err := StatusArgv("linux", 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(lst, " ") != "systemctl --user status strike-daemon --no-pager" {
		t.Fatalf("linux status = %#v", lst)
	}
	if _, err := StartArgv("windows", 0, ""); err == nil {
		t.Fatal("windows start succeeded")
	}
}

func manualMust(t *testing.T, goos, path string) []string {
	t.Helper()
	lines, err := ManualStartLines(goos, path)
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

func sameArgv(got, want [][]string) bool {
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

func compareGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (UPDATE_GOLDEN=1 to write): %v\n--- got ---\n%s", name, err, got)
	}
	if got != string(want) {
		t.Fatalf("golden %s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}
