// Package unit renders the per-user service definition for the strike daemon.
// Linux gets a systemd user unit. macOS gets a launchd LaunchAgent plist.
// Path resolution and command lines are pure; the CLI writes the file and
// runs systemctl or launchctl.
package unit

import (
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	// Service is the systemd unit name (without .service).
	Service = "strike-daemon"
	// UnitFile is the systemd user unit filename.
	UnitFile = Service + ".service"
	// LaunchdLabel is the launchd job label and kickstart target.
	LaunchdLabel = "ca.jonathanung.strike-daemon"
	// PlistName is the LaunchAgent filename.
	PlistName = LaunchdLabel + ".plist"
)

// UnsupportedMessage is the text for Windows and any other GOOS.
func UnsupportedMessage(goos string) string {
	if strings.TrimSpace(goos) == "" {
		goos = "this OS"
	}
	return fmt.Sprintf("unsupported on %s, run `strike serve` manually (WSL2 supported)", goos)
}

// Supported reports whether install/uninstall/status can manage a service.
func Supported(goos string) bool {
	return goos == "linux" || goos == "darwin"
}

// DefinitionPath is the unit or plist path for goos.
// On Linux, xdgConfigHome overrides ~/.config (XDG_CONFIG_HOME).
// On macOS the LaunchAgent path ignores xdgConfigHome; it is never
// ~/Library/Application Support.
func DefinitionPath(goos, home, xdgConfigHome string) (string, error) {
	switch goos {
	case "linux":
		cfg := strings.TrimSpace(xdgConfigHome)
		if cfg == "" {
			if strings.TrimSpace(home) == "" {
				return "", errors.New("home directory is not set")
			}
			cfg = filepath.Join(home, ".config")
		}
		return filepath.Join(cfg, "systemd", "user", UnitFile), nil
	case "darwin":
		if strings.TrimSpace(home) == "" {
			return "", errors.New("home directory is not set")
		}
		return filepath.Join(home, "Library", "LaunchAgents", PlistName), nil
	default:
		return "", errors.New(UnsupportedMessage(goos))
	}
}

// TokenPath is the stable daemon bearer file. Callers create it mode 0600.
func TokenPath(home string) string {
	return filepath.Join(home, ".strike", "daemon.token")
}

// LogDir is where the LaunchAgent writes stdout and stderr.
func LogDir(home string) string {
	return filepath.Join(home, ".strike", "logs")
}

// ServeArgs is the strike invocation shared by the systemd unit and the
// LaunchAgent. exe is the absolute strike binary.
func ServeArgs(exe, tokenPath, addr string, allowPrivate bool) []string {
	args := []string{exe, "serve", "--auth", "--token-file", tokenPath, "--addr", addr}
	if allowPrivate {
		args = append(args, "--allow-private-bind")
	}
	return args
}

// RenderSystemd builds a user unit. Paths with shell metacharacters are quoted.
func RenderSystemd(exe, tokenPath, addr string, allowPrivate bool) string {
	args := ServeArgs(exe, tokenPath, addr, allowPrivate)
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = systemdQuote(arg)
	}
	return "[Unit]\n" +
		"Description=Strike daemon (web UI + engine API)\n" +
		"After=network.target\n\n" +
		"[Service]\n" +
		"ExecStart=" + strings.Join(quoted, " ") + "\n" +
		"Restart=on-failure\n\n" +
		"[Install]\n" +
		"WantedBy=default.target\n"
}

// LaunchdPathEnv is the PATH set on the LaunchAgent. launchd's default is
// only /usr/bin:/bin, which hides Homebrew and ~/.strike/bin.
func LaunchdPathEnv(home string) string {
	return filepath.Join(home, ".strike", "bin") + ":/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
}

const plistHeader = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`

// RenderLaunchd builds a LaunchAgent plist. home is WorkingDirectory.
// Log files are under LogDir(home). String values are XML-escaped.
func RenderLaunchd(exe, tokenPath, addr, home string, allowPrivate bool) string {
	outLog := filepath.Join(LogDir(home), "strike-daemon.out.log")
	errLog := filepath.Join(LogDir(home), "strike-daemon.err.log")
	var b strings.Builder
	b.WriteString(plistHeader)
	writeKey(&b, "Label")
	writePlistString(&b, LaunchdLabel, 1)
	writeKey(&b, "ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, arg := range ServeArgs(exe, tokenPath, addr, allowPrivate) {
		writePlistString(&b, arg, 2)
	}
	b.WriteString("\t</array>\n")
	writeKey(&b, "RunAtLoad")
	b.WriteString("\t<true/>\n")
	writeKey(&b, "KeepAlive")
	b.WriteString("\t<true/>\n")
	writeKey(&b, "StandardOutPath")
	writePlistString(&b, outLog, 1)
	writeKey(&b, "StandardErrorPath")
	writePlistString(&b, errLog, 1)
	writeKey(&b, "EnvironmentVariables")
	b.WriteString("\t<dict>\n")
	b.WriteString("\t\t<key>PATH</key>\n")
	writePlistString(&b, LaunchdPathEnv(home), 2)
	b.WriteString("\t</dict>\n")
	writeKey(&b, "WorkingDirectory")
	writePlistString(&b, home, 1)
	b.WriteString("</dict>\n</plist>\n")
	return b.String()
}

func writeKey(b *strings.Builder, key string) {
	b.WriteString("\t<key>")
	b.WriteString(key)
	b.WriteString("</key>\n")
}

func writePlistString(b *strings.Builder, s string, indent int) {
	for i := 0; i < indent; i++ {
		b.WriteByte('\t')
	}
	b.WriteString("<string>")
	b.WriteString(plistEscape(s))
	b.WriteString("</string>\n")
}

func plistEscape(s string) string {
	var b strings.Builder
	// strings.Builder.Write does not fail.
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// StartArgv is what `strike daemon install --start` executes.
// Darwin uses the numeric uid. The printed hint uses $(id -u) instead;
// see ManualStartLines.
func StartArgv(goos string, uid int, definitionPath string) ([][]string, error) {
	switch goos {
	case "linux":
		return [][]string{
			{"systemctl", "--user", "daemon-reload"},
			{"systemctl", "--user", "enable", "--now", Service},
		}, nil
	case "darwin":
		domain := fmt.Sprintf("gui/%d", uid)
		return [][]string{
			{"launchctl", "bootstrap", domain, definitionPath},
			{"launchctl", "kickstart", "-k", domain + "/" + LaunchdLabel},
		}, nil
	default:
		return nil, errors.New(UnsupportedMessage(goos))
	}
}

// ManualStartLines are the copy-paste commands printed when --start is not set.
func ManualStartLines(goos, definitionPath string) ([]string, error) {
	switch goos {
	case "linux":
		return []string{
			"systemctl --user daemon-reload",
			"systemctl --user enable --now " + Service,
		}, nil
	case "darwin":
		return []string{
			"launchctl bootstrap gui/$(id -u) " + QuoteArg(definitionPath),
			"launchctl kickstart -k gui/$(id -u)/" + LaunchdLabel,
		}, nil
	default:
		return nil, errors.New(UnsupportedMessage(goos))
	}
}

// PreRemoveArgv stops the service before the definition file is deleted.
// Callers ignore a non-zero exit: the service may never have been loaded.
func PreRemoveArgv(goos string, uid int) ([][]string, error) {
	switch goos {
	case "linux":
		return [][]string{
			{"systemctl", "--user", "disable", "--now", Service},
		}, nil
	case "darwin":
		return [][]string{
			{"launchctl", "bootout", fmt.Sprintf("gui/%d/%s", uid, LaunchdLabel)},
		}, nil
	default:
		return nil, errors.New(UnsupportedMessage(goos))
	}
}

// PostRemoveArgv refreshes systemd after the unit file is gone.
func PostRemoveArgv(goos string) ([][]string, error) {
	switch goos {
	case "linux":
		return [][]string{
			{"systemctl", "--user", "daemon-reload"},
		}, nil
	case "darwin":
		return [][]string{}, nil
	default:
		return nil, errors.New(UnsupportedMessage(goos))
	}
}

// ManualRemoveLines are the copy-paste uninstall commands.
func ManualRemoveLines(goos string) ([]string, error) {
	switch goos {
	case "linux":
		return []string{
			"systemctl --user disable --now " + Service,
			"systemctl --user daemon-reload",
		}, nil
	case "darwin":
		return []string{
			"launchctl bootout gui/$(id -u)/" + LaunchdLabel,
		}, nil
	default:
		return nil, errors.New(UnsupportedMessage(goos))
	}
}

// StatusArgv is the service-manager query for an installed definition.
func StatusArgv(goos string, uid int) ([]string, error) {
	switch goos {
	case "linux":
		return []string{"systemctl", "--user", "status", Service, "--no-pager"}, nil
	case "darwin":
		return []string{"launchctl", "print", fmt.Sprintf("gui/%d/%s", uid, LaunchdLabel)}, nil
	default:
		return nil, errors.New(UnsupportedMessage(goos))
	}
}

// QuoteArg quotes s for a shell when it contains metacharacters.
// Safe values are returned unchanged so printed launchctl lines stay pasteable.
func QuoteArg(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\n\"'\\$`;&|<>()[]{}*?#~") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

func systemdQuote(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\n\"\\$`';") {
		escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ").Replace(s)
		return `"` + escaped + `"`
	}
	return s
}
