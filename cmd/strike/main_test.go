package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

const expectedUsage = `Usage:
  strike [options]
  strike exec [options] <prompt>
  strike rpc [options]
  strike acp [options]
  strike serve [options]
  strike mcp-serve [options]
  strike auth <command> [arguments]
  strike plugin <command> [arguments]
  strike container <command> [arguments]
  strike eval <command> [arguments]
  strike restore [options]
  strike workflow <command> [arguments]
  strike version
  strike upgrade

Options:
  --provider <provider>              provider to use (anthropic|openai|xai|google|kimi|deepseek|echo; gemini=google alias); overrides config
  --model <model>                    model id; overrides config
  --effort <level>                   reasoning effort (off|low|medium|high|xhigh|max); overrides config
  --auto, --dangerously-skip-permissions skip configured permission prompts (agent profile denies still apply)
  --sandbox <mode>                   OS process sandbox for bash (off|read-only|workspace-write); overrides config
  --i-know                           allow permissionMode yolo when sandbox is off (explicit override)
  --continue                         resume the most recent root session (model history + selections)
  --session <id>                     resume a specific session by id (model history + selections)
  --worktree                         run this session in an isolated git worktree under .strike/worktrees/
  --turn-timeout <duration>          root-turn wall-clock deadline (e.g. 30m, 1h, 1800s); off/0 disables; overrides session.turnTimeoutS
  --launch-inside-container          build/start the project container and re-exec strike inside it (E12.4)
  --container-rebuild                with --launch-inside-container: rebuild when the live container is stale (E12.6)
  --container-attach-stale           with --launch-inside-container: attach to a stale live container (E12.6)
  --container-cancel                 with --launch-inside-container: cancel when the live container is stale (E12.6)
  --telemetry                        show local system metrics pane (CPU/RAM/disk); on by default
  --upgrade                          download and install the latest GitHub Release
  --version                          print version and exit
  -h, --help                         show help
`

func TestParseCLIOptionsDefaults(t *testing.T) {
	opts, err := parseCLIOptions(nil)
	if err != nil {
		t.Fatalf("parseCLIOptions(nil): %v", err)
	}
	if opts != (cliOptions{}) {
		t.Fatalf("parseCLIOptions(nil) = %+v, want zero-value defaults", opts)
	}
}

func TestParseCLIOptionsValueFormsAndProviderExplicitness(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want cliOptions
	}{
		{
			name: "separated",
			args: []string{"--provider", "openai", "--model", "gpt-test"},
			want: cliOptions{provider: "openai", model: "gpt-test", providerSet: true},
		},
		{
			name: "equals",
			args: []string{"--provider=xai", "--model=grok-test"},
			want: cliOptions{provider: "xai", model: "grok-test", providerSet: true},
		},
		{
			name: "flag-looking model value is consumed",
			args: []string{"--model", "-provider"},
			want: cliOptions{model: "-provider"},
		},
		{
			name: "model does not mark provider explicit",
			args: []string{"--model=custom"},
			want: cliOptions{model: "custom"},
		},
		{
			name: "empty provider has no effective explicit override",
			args: []string{"--provider="},
			want: cliOptions{},
		},
		{
			name: "continue flag",
			args: []string{"--continue"},
			want: cliOptions{continueSession: true},
		},
		{
			name: "session flag",
			args: []string{"--session", "abc-123"},
			want: cliOptions{sessionID: "abc-123"},
		},
		{
			name: "session equals",
			args: []string{"--session=abc-123"},
			want: cliOptions{sessionID: "abc-123"},
		},
		{
			name: "worktree flag",
			args: []string{"--worktree"},
			want: cliOptions{worktree: true},
		},
		{
			name: "turn-timeout duration",
			args: []string{"--turn-timeout", "45m"},
			want: cliOptions{turnTimeout: "45m", turnTimeoutSet: true},
		},
		{
			name: "turn-timeout off",
			args: []string{"--turn-timeout=off"},
			want: cliOptions{turnTimeout: "off", turnTimeoutSet: true},
		},
		{
			name: "telemetry flag",
			args: []string{"--telemetry"},
			want: cliOptions{telemetry: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCLIOptions(tt.args)
			if err != nil {
				t.Fatalf("parseCLIOptions(%q): %v", tt.args, err)
			}
			if got != tt.want {
				t.Fatalf("parseCLIOptions(%q) = %+v, want %+v", tt.args, got, tt.want)
			}
		})
	}
}

func TestParseCLIOptionsRejectsContinueWithSession(t *testing.T) {
	_, err := parseCLIOptions([]string{"--continue", "--session", "abc"})
	if err == nil || !strings.Contains(err.Error(), "cannot combine") {
		t.Fatalf("err = %v, want cannot combine", err)
	}
}

func TestRunCLIEmptyProviderDoesNotOverrideOrEagerlyValidateConfiguredProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".strike")
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config"), []byte(`{"provider":"configured-provider"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", "invalid\nname.md"), []byte("agent prompt"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"--provider="}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d, want later startup failure exit 1; stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "loading agents:") {
		t.Errorf("stderr = %q, want proof startup reached post-provider preflight", stderr.String())
	}
	if strings.Contains(stderr.String(), "unknown provider") {
		t.Errorf("empty --provider triggered eager configured-provider validation: %q", stderr.String())
	}
}

func TestParseCLIOptionsDangerousBooleanForms(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{args: []string{"--dangerously-skip-permissions"}, want: true},
		{args: []string{"--dangerously-skip-permissions=true"}, want: true},
		{args: []string{"--dangerously-skip-permissions=false"}, want: false},
		{args: []string{"--auto"}, want: true},
		{args: []string{"--auto=true"}, want: true},
		{args: []string{"--auto=false"}, want: false},
		{args: []string{"--auto", "--dangerously-skip-permissions"}, want: true},
	}
	for _, tt := range tests {
		opts, err := parseCLIOptions(tt.args)
		if err != nil {
			t.Fatalf("parseCLIOptions(%q): %v", tt.args, err)
		}
		if opts.dangerouslySkipPermissions != tt.want {
			t.Errorf("parseCLIOptions(%q) dangerouslySkipPermissions = %t, want %t", tt.args, opts.dangerouslySkipPermissions, tt.want)
		}
	}
}

func TestParseCLIOptionsSandboxAndIKnow(t *testing.T) {
	opts, err := parseCLIOptions([]string{"--sandbox", "read-only", "--i-know"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.sandbox != "read-only" {
		t.Errorf("sandbox = %q", opts.sandbox)
	}
	if !opts.iKnow {
		t.Error("want iKnow true")
	}
	if _, err := parseCLIOptions([]string{"--sandbox", "nope"}); err == nil {
		t.Fatal("want unknown sandbox error")
	}
	mode, err := resolveSandboxMode("off", "workspace-write", false)
	if err != nil || mode != "workspace-write" {
		t.Fatalf("CLI overrides config: mode=%q err=%v", mode, err)
	}
	mode, err = resolveSandboxMode("read-only", "", false)
	if err != nil || mode != "read-only" {
		t.Fatalf("config only: mode=%q err=%v", mode, err)
	}
	mode, err = resolveSandboxMode("", "", false)
	if err != nil || mode != "workspace-write" {
		t.Fatalf("default: mode=%q err=%v", mode, err)
	}
	// Managed lock: CLI cannot loosen enterprise sandbox.
	mode, err = resolveSandboxMode("read-only", "off", true)
	if err != nil || mode != "read-only" {
		t.Fatalf("managed lock: mode=%q err=%v", mode, err)
	}
}

func TestParseCLIOptionsRejectsAliasesAndAbbreviations(t *testing.T) {
	tests := []struct {
		args    []string
		wantErr string
	}{
		{args: []string{"-provider", "echo"}, wantErr: "flag provided but not defined: -provider"},
		{args: []string{"-provider=echo"}, wantErr: "flag provided but not defined: -provider"},
		{args: []string{"-model", "value"}, wantErr: "flag provided but not defined: -model"},
		{args: []string{"-dangerously-skip-permissions"}, wantErr: "flag provided but not defined: -dangerously-skip-permissions"},
		{args: []string{"-auto"}, wantErr: "flag provided but not defined: -auto"},
		{args: []string{"--prov", "echo"}, wantErr: "flag provided but not defined: -prov"},
		{args: []string{"-p", "echo"}, wantErr: "flag provided but not defined: -p"},
		{args: []string{"--dangerously-skip"}, wantErr: "flag provided but not defined: -dangerously-skip"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, "_"), func(t *testing.T) {
			_, err := parseCLIOptions(tt.args)
			if err == nil {
				t.Fatalf("parseCLIOptions(%q) succeeded, want unknown-flag error", tt.args)
			}
			if err.Error() != tt.wantErr {
				t.Errorf("parseCLIOptions(%q) error = %q, want exact Go-style diagnostic %q", tt.args, err, tt.wantErr)
			}
		})
	}
}

func TestHelpIsClassifiedAndRunCLIPrintsOneStableUsage(t *testing.T) {
	for _, arg := range []string{"--help", "-h"} {
		t.Run(arg, func(t *testing.T) {
			_, err := parseCLIOptions([]string{arg})
			if !errors.Is(err, flag.ErrHelp) {
				t.Errorf("parseCLIOptions(%q) error = %v, want flag.ErrHelp", arg, err)
			}

			var stdout, stderr bytes.Buffer
			if code := runCLI([]string{arg}, &stdout, &stderr); code != 0 {
				t.Errorf("runCLI(%q) exit = %d, want 0", arg, code)
			}
			if got := stdout.String(); got != expectedUsage {
				t.Errorf("stdout usage changed:\n--- got ---\n%s--- want ---\n%s", got, expectedUsage)
			}
			if strings.Count(stdout.String(), "Usage:\n") != 1 {
				t.Errorf("usage rendered %d times, want once", strings.Count(stdout.String(), "Usage:\n"))
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestWriteUsageUsesCanonicalOptionsAndProviders(t *testing.T) {
	var out bytes.Buffer
	writeUsage(&out)
	if out.String() != expectedUsage {
		t.Fatalf("usage changed:\n--- got ---\n%s--- want ---\n%s", out.String(), expectedUsage)
	}
	for _, text := range []string{"strike auth <command>", "--provider <provider>", "--model <model>", "anthropic", "openai", "xai", "google", "echo", "gemini=google alias"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("usage does not contain %q", text)
		}
	}
	if strings.Contains(out.String(), "--dangerously-skip-permissions <") || strings.Contains(out.String(), "--auto <") {
		t.Error("boolean dangerous option has a value placeholder")
	}
	if !strings.Contains(out.String(), "--auto") || !strings.Contains(out.String(), "--dangerously-skip-permissions") {
		t.Error("usage must list both --auto and --dangerously-skip-permissions")
	}
	if strings.Contains(out.String(), "-provider") && !strings.Contains(out.String(), "--provider") {
		t.Error("provider is not rendered with its canonical double-dash name")
	}
}

func TestRunCLIUsageErrorsHaveOneDiagnosticAndNoWarning(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "unknown flag", args: []string{"--unknown"}},
		{name: "provider missing value", args: []string{"--provider"}},
		{name: "model missing value", args: []string{"--model"}},
		{name: "positional argument", args: []string{"chat"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := runCLI(tt.args, &stdout, &stderr); code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if stdout.Len() != 0 {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if strings.Count(stderr.String(), "strike:") != 1 {
				t.Errorf("diagnostic count = %d, want 1; stderr=%q", strings.Count(stderr.String(), "strike:"), stderr.String())
			}
			if strings.Count(stderr.String(), "Usage:\n") != 1 {
				t.Errorf("usage count = %d, want 1; stderr=%q", strings.Count(stderr.String(), "Usage:\n"), stderr.String())
			}
			if strings.Contains(stderr.String(), dangerousPermissionsWarning) {
				t.Errorf("unexpected dangerous warning: %q", stderr.String())
			}
		})
	}
}

func TestRunCLIFirstArgumentAuthUsesSuppliedOutputDestinations(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantExit   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "bare auth prints auth usage successfully",
			args:       []string{"auth"},
			wantStdout: authUsage + "\n",
		},
		{
			name:       "logout missing provider is a safe usage error",
			args:       []string{"auth", "logout"},
			wantExit:   1,
			wantStderr: "strike: usage: strike auth logout <provider>\n",
		},
		{
			name:       "login missing provider is a safe usage error",
			args:       []string{"auth", "login"},
			wantExit:   1,
			wantStderr: "strike: usage: strike auth login <anthropic|openai|xai|google|kimi|deepseek> [--api-key] [--device]\n",
		},
		{
			name:       "unknown command separates usage and diagnostic",
			args:       []string{"auth", "not-a-command"},
			wantExit:   1,
			wantStdout: authUsage + "\n",
			wantStderr: "strike: unknown auth command \"not-a-command\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			var stdout, stderr bytes.Buffer
			if code := runCLI(tt.args, &stdout, &stderr); code != tt.wantExit {
				t.Errorf("exit = %d, want %d", code, tt.wantExit)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("supplied stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if stderr.String() != tt.wantStderr {
				t.Errorf("supplied stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
			if _, err := os.Stat(filepath.Join(home, ".strike")); !os.IsNotExist(err) {
				t.Errorf("auth usage/error path created state: stat error = %v", err)
			}
		})
	}
}

func TestPermissionLayersPreserveDefaultsConfiguredRulesAndBackingArray(t *testing.T) {
	backing := make(permission.Ruleset, 1, 4)
	backing[0] = permission.Rule{Permission: "write", Pattern: "*.go", Action: permission.Deny}
	spare := backing[:cap(backing)]
	spare[1] = permission.Rule{Permission: "bash", Pattern: "git *", Action: permission.Ask}
	before := append(permission.Ruleset(nil), spare...)

	for _, dangerous := range []bool{false, true} {
		layers := permissionLayers(backing, dangerous)
		wantLen := 2
		if dangerous {
			wantLen = 3
		}
		if len(layers) != wantLen {
			t.Fatalf("permissionLayers(_, %t) has %d layers, want %d", dangerous, len(layers), wantLen)
		}
		if !reflect.DeepEqual(layers[0], permission.Defaults()) {
			t.Errorf("default layer = %#v, want %#v", layers[0], permission.Defaults())
		}
		if !reflect.DeepEqual(layers[1], backing) {
			t.Errorf("configured layer = %#v, want %#v", layers[1], backing)
		}
		if dangerous {
			want := permission.Ruleset{{Permission: "*", Pattern: "*", Action: permission.Allow}}
			if !reflect.DeepEqual(layers[2], want) {
				t.Errorf("dangerous final layer = %#v, want %#v", layers[2], want)
			}
		}
		layers[1][0].Action = permission.Allow
		if !reflect.DeepEqual(spare, before) {
			t.Fatalf("permissionLayers(_, %t) changed configured backing array: got %#v, want %#v", dangerous, spare, before)
		}
	}
}

func TestDangerousPermissionServiceAllowsAllPatternsSynchronouslyWithoutEvents(t *testing.T) {
	configured := permission.Ruleset{
		{Permission: "*", Pattern: "*", Action: permission.Deny},
		{Permission: "bash", Pattern: "*", Action: permission.Ask},
	}
	events := make(chan protocol.Event, 16)
	service := permission.New(func(event protocol.Event) { events <- event }, permissionLayers(configured, true)...)

	requests := []tool.AskRequest{
		{Permission: "read", Patterns: []string{"README.md"}},
		{Permission: "write", Patterns: []string{"dir/file.go"}},
		{Permission: "bash", Patterns: []string{"git status"}},
		{Permission: "unknown", Patterns: []string{"anything"}},
		{Permission: "bash", Patterns: []string{"slashless"}},
		{Permission: "write", Patterns: []string{""}},
		{Permission: "unknown"},
		{Permission: "bash", Patterns: []string{"one", "nested/two", ""}},
	}
	for _, req := range requests {
		if err := askWithin(t, service, req); err != nil {
			t.Errorf("Ask(%q, %q) = %v, want nil", req.Permission, req.Patterns, err)
		}
	}
	// Audit PermissionDecided is OK; must not emit PermissionAsked/Resolved.
	for {
		select {
		case ev := <-events:
			switch ev.(type) {
			case protocol.PermissionAsked, protocol.PermissionResolved:
				t.Fatalf("dangerous allow emitted prompt event %T", ev)
			case protocol.PermissionDecided:
				// ok
			default:
				t.Fatalf("unexpected event %T", ev)
			}
		default:
			goto doneDangerous
		}
	}
doneDangerous:
}

func TestNormalPermissionLayersStillAskAndDeny(t *testing.T) {
	configured := permission.Ruleset{{Permission: "bash", Pattern: "rm *", Action: permission.Deny}}
	layers := permissionLayers(configured, false)
	if got := permission.Evaluate("read", "README.md", layers...); got != permission.Allow {
		t.Errorf("read action = %q, want allow", got)
	}
	if got := permission.Evaluate("write", "file.go", layers...); got != permission.Ask {
		t.Errorf("write action = %q, want ask", got)
	}
	if got := permission.Evaluate("bash", "rm -rf build", layers...); got != permission.Deny {
		t.Errorf("configured bash action = %q, want deny", got)
	}

	events := make(chan protocol.Event, 4)
	service := permission.New(func(event protocol.Event) { events <- event }, layers...)
	err := askWithin(t, service, tool.AskRequest{Permission: "bash", Patterns: []string{"rm -rf build"}})
	var denied *permission.DeniedError
	if !errors.As(err, &denied) {
		t.Errorf("denied Ask error = %v, want *permission.DeniedError", err)
	}
	for {
		select {
		case ev := <-events:
			switch ev.(type) {
			case protocol.PermissionAsked, protocol.PermissionResolved:
				t.Errorf("rule-denied Ask emitted prompt event %T", ev)
			case protocol.PermissionDecided:
				// audit ok
			default:
				t.Errorf("unexpected event %T", ev)
			}
		default:
			return
		}
	}
}

func TestWarningTextAndPreflightFailuresDoNotWarn(t *testing.T) {
	const wantWarning = "WARNING: --dangerously-skip-permissions is enabled; configured permission asks are skipped for this invocation. Active agent permission denies still apply. Workflow phase permission widening is auto-accepted; hard sandbox and path protections are unchanged."
	if dangerousPermissionsWarning != wantWarning {
		t.Errorf("warning = %q, want %q", dangerousPermissionsWarning, wantWarning)
	}

	homeFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(homeFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeFile)
	for _, args := range [][]string{
		nil,
		{"--dangerously-skip-permissions"},
		{"--dangerously-skip-permissions=false"},
		{"--auto"},
		{"--auto=false"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runCLI(args, &stdout, &stderr); code != 1 {
			t.Errorf("runCLI(%q) exit = %d, want startup failure exit 1", args, code)
		}
		if strings.Contains(stderr.String(), dangerousPermissionsWarning) {
			t.Errorf("runCLI(%q) warned before successful preflight: %q", args, stderr.String())
		}
	}
}

func TestWriteDangerousPermissionsWarning(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		want    string
	}{
		{
			name:    "enabled writes exactly one line",
			enabled: true,
			want:    dangerousPermissionsWarning + "\n",
		},
		{
			name: "disabled writes nothing",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			writeDangerousPermissionsWarning(&out, tt.enabled)
			if out.String() != tt.want {
				t.Errorf("writeDangerousPermissionsWarning(_, %t) = %q, want %q", tt.enabled, out.String(), tt.want)
			}
		})
	}
}

func askWithin(t *testing.T, service *permission.Service, req tool.AskRequest) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- service.Ask(context.Background(), req)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatalf("Ask(%q, %q) did not return synchronously", req.Permission, req.Patterns)
		return nil
	}
}

func TestRunCLIVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	for _, args := range [][]string{{"version"}, {"--version"}} {
		stdout.Reset()
		stderr.Reset()
		if code := runCLI(args, &stdout, &stderr); code != 0 {
			t.Fatalf("args=%v exit=%d stderr=%q", args, code, stderr.String())
		}
		if stdout.Len() == 0 {
			t.Fatalf("args=%v: empty version stdout", args)
		}
		if stderr.Len() != 0 {
			t.Fatalf("args=%v stderr=%q", args, stderr.String())
		}
	}
}

func TestUpgradeCLIOptionsDoesNotReexec(t *testing.T) {
	var stdout bytes.Buffer
	opts := upgradeCLIOptions(&stdout)
	if !opts.NoExec {
		t.Fatal("CLI upgrade must set NoExec so strike does not relaunch after upgrade")
	}
	if opts.Stdout != &stdout {
		t.Fatal("CLI upgrade must forward stdout for progress messages")
	}
}

func TestParseTurnTimeoutFlag(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"off", -1, false},
		{"none", -1, false},
		{"0", -1, false},
		{"", -1, false},
		{"30m", 1800, false},
		{"1h", 3600, false},
		{"90s", 90, false},
		{"120", 120, false},
		{"bogus", 0, true},
	}
	for _, tt := range tests {
		got, err := parseTurnTimeoutFlag(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("parseTurnTimeoutFlag(%q) err=nil, want error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseTurnTimeoutFlag(%q): %v", tt.in, err)
		}
		if got != tt.want {
			t.Fatalf("parseTurnTimeoutFlag(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestResolveRootTurnTimeout(t *testing.T) {
	// Default when unset.
	d := resolveRootTurnTimeout(config.Config{}, cliOptions{})
	if d != 30*time.Minute {
		t.Fatalf("default = %v, want 30m", d)
	}
	// Config seconds.
	d = resolveRootTurnTimeout(config.Config{Session: config.SessionConfig{TurnTimeoutS: 60}}, cliOptions{})
	if d != time.Minute {
		t.Fatalf("config 60s = %v", d)
	}
	// Config disable.
	d = resolveRootTurnTimeout(config.Config{Session: config.SessionConfig{TurnTimeoutS: -1}}, cliOptions{})
	if d != 0 {
		t.Fatalf("config off = %v, want 0", d)
	}
	// CLI overrides config.
	d = resolveRootTurnTimeout(
		config.Config{Session: config.SessionConfig{TurnTimeoutS: 60}},
		cliOptions{turnTimeout: "off", turnTimeoutSet: true},
	)
	if d != 0 {
		t.Fatalf("cli off override = %v", d)
	}
	d = resolveRootTurnTimeout(
		config.Config{Session: config.SessionConfig{TurnTimeoutS: -1}},
		cliOptions{turnTimeout: "10s", turnTimeoutSet: true},
	)
	if d != 10*time.Second {
		t.Fatalf("cli 10s override = %v", d)
	}
}
