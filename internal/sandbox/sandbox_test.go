package sandbox

import (
	"bytes"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		in   string
		want Mode
		ok   bool
	}{
		{"", DefaultMode, true},
		{"off", ModeOff, true},
		{"OFF", ModeOff, true},
		{"read-only", ModeReadOnly, true},
		{"readonly", ModeReadOnly, true},
		{"workspace-write", ModeWorkspaceWrite, true},
		{"write", ModeWorkspaceWrite, true},
		{"nope", 0, false},
		{"  read_only ", ModeReadOnly, true},
	}
	for _, tc := range cases {
		got, ok := ParseMode(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("ParseMode(%q) = (%v, %v), want (%v, %v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	if ModeOff.String() != "off" || ModeReadOnly.String() != "read-only" || ModeWorkspaceWrite.String() != "workspace-write" {
		t.Fatalf("String() mismatch: %q %q %q", ModeOff, ModeReadOnly, ModeWorkspaceWrite)
	}
}

func TestCheckYoloSandbox(t *testing.T) {
	if err := CheckYoloSandbox("yolo", "off", false); err == nil {
		t.Fatal("expected error for yolo+off without iKnow")
	}
	if err := CheckYoloSandbox("yolo", "off", true); err != nil {
		t.Fatalf("iKnow should allow: %v", err)
	}
	if err := CheckYoloSandbox("yolo", "workspace-write", false); err != nil {
		t.Fatalf("sandboxed yolo ok: %v", err)
	}
	if err := CheckYoloSandbox("default", "off", false); err != nil {
		t.Fatalf("non-yolo ok: %v", err)
	}
	if err := CheckYoloSandbox("yolo", "", false); err != nil {
		t.Fatalf("empty sandbox defaults to workspace-write: %v", err)
	}
}

func TestWrapModeOffUnchanged(t *testing.T) {
	ResetWarnForTest()
	forceSetAvailabilityForTest(availInfo{ok: true, name: "bwrap"})
	argv := []string{"bash", "-c", "echo hi"}
	res := WrapResult(argv, Policy{Mode: ModeOff, WorkDir: t.TempDir()})
	if res.Applied || res.Degraded {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Argv) != 3 || res.Argv[0] != "bash" {
		t.Fatalf("argv = %#v", res.Argv)
	}
	// Defensive copy: mutating result must not touch input.
	res.Argv[0] = "nope"
	if argv[0] != "bash" {
		t.Fatal("Wrap must clone argv")
	}
}

func TestWrapEmptyArgv(t *testing.T) {
	ResetWarnForTest()
	forceSetAvailabilityForTest(availInfo{ok: true, name: "bwrap"})
	if got := Wrap(nil, Policy{Mode: ModeWorkspaceWrite, WorkDir: "/tmp"}); got != nil {
		t.Fatalf("got %#v", got)
	}
	if got := Wrap([]string{}, Policy{Mode: ModeWorkspaceWrite, WorkDir: "/tmp"}); len(got) != 0 {
		t.Fatalf("got %#v", got)
	}
}

func TestWrapDegradedWhenUnavailable(t *testing.T) {
	ResetWarnForTest()
	var buf bytes.Buffer
	SetWarnWriter(&buf)
	t.Cleanup(func() { SetWarnWriter(nil) })
	forceSetAvailabilityForTest(availInfo{
		warn: "test backend missing; bash runs unsandboxed",
	})
	argv := []string{"bash", "-c", "true"}
	res := WrapResult(argv, Policy{Mode: ModeWorkspaceWrite, WorkDir: t.TempDir()})
	if res.Applied || !res.Degraded {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Argv) != 3 || res.Argv[0] != "bash" {
		t.Fatalf("argv = %#v", res.Argv)
	}
	if !strings.Contains(buf.String(), "strike: warning:") {
		t.Fatalf("warning = %q", buf.String())
	}
	// Second wrap must not re-warn.
	buf.Reset()
	_ = Wrap(argv, Policy{Mode: ModeWorkspaceWrite, WorkDir: t.TempDir()})
	if buf.Len() != 0 {
		t.Fatalf("second warning = %q", buf.String())
	}
}

func TestWarnUnavailableOnce(t *testing.T) {
	ResetWarnForTest()
	var buf bytes.Buffer
	SetWarnWriter(&buf)
	t.Cleanup(func() { SetWarnWriter(nil) })
	forceSetAvailabilityForTest(availInfo{warn: "no sandbox here"})
	WarnUnavailable()
	WarnUnavailable()
	if n := strings.Count(buf.String(), "strike: warning:"); n != 1 {
		t.Fatalf("warnings = %d, body=%q", n, buf.String())
	}
}

func TestWarnUnavailableSilentWhenOK(t *testing.T) {
	ResetWarnForTest()
	var buf bytes.Buffer
	SetWarnWriter(&buf)
	t.Cleanup(func() { SetWarnWriter(nil) })
	forceSetAvailabilityForTest(availInfo{ok: true, name: "bwrap"})
	WarnUnavailable()
	if buf.Len() != 0 {
		t.Fatalf("unexpected warning %q", buf.String())
	}
}

func TestWrapLinuxArgvShape(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux bwrap argv shape")
	}
	ResetWarnForTest()
	forceSetAvailabilityForTest(availInfo{ok: true, name: "bwrap"})
	wd := t.TempDir()
	// Ensure real path for comparison.
	realWD, err := filepath.EvalSymlinks(wd)
	if err != nil {
		realWD = wd
	}
	realWD, err = filepath.Abs(realWD)
	if err != nil {
		t.Fatal(err)
	}

	res := WrapResult([]string{"bash", "-c", "echo hi"}, Policy{
		Mode:    ModeWorkspaceWrite,
		WorkDir: wd,
	})
	if !res.Applied || res.Backend != "bwrap" {
		t.Fatalf("result = %+v", res)
	}
	argv := res.Argv
	// bwrap --ro-bind / / --bind WD WD [--bind shared...] --dev /dev --proc /proc --die-with-parent -- bash -c echo hi
	// Default (NoNetwork zero) shares host networking — no --unshare-net.
	// Launcher may be absolute (preferred) or bare "bwrap". Shared temp/cache
	// binds are host-dependent so assert structure, not a fixed argv length.
	if len(argv) < 14 {
		t.Fatalf("argv too short: %#v", argv)
	}
	if base := filepath.Base(argv[0]); base != "bwrap" {
		t.Fatalf("launcher = %q", argv[0])
	}
	joined := strings.Join(argv, "\x00")
	if !strings.HasPrefix(joined, argv[0]+"\x00--ro-bind\x00/\x00/\x00") {
		t.Fatalf("missing ro-bind /: %#v", argv)
	}
	if !strings.Contains(joined, "\x00--bind\x00"+realWD+"\x00"+realWD+"\x00") {
		t.Fatalf("missing workspace bind: %#v", argv)
	}
	// At least one shared temp bind when /tmp exists.
	if st, err := os.Stat("/tmp"); err == nil && st.IsDir() {
		if !strings.Contains(joined, "\x00--bind\x00/tmp\x00/tmp\x00") {
			t.Fatalf("workspace-write should bind /tmp: %#v", argv)
		}
	}
	if !strings.Contains(joined, "\x00--dev\x00/dev\x00--proc\x00/proc\x00") {
		t.Fatalf("missing dev/proc: %#v", argv)
	}
	// Product default: bare Policy keeps host networking (issue #750).
	if strings.Contains(joined, "\x00--unshare-net\x00") {
		t.Fatalf("default Policy must omit --unshare-net: %#v", argv)
	}
	if !strings.Contains(joined, "\x00--die-with-parent\x00--\x00bash\x00-c\x00echo hi") {
		t.Fatalf("missing trailer: %#v", argv)
	}

	// Read-only: no workdir bind; temp shared binds still allowed.
	ro := Wrap([]string{"true"}, Policy{Mode: ModeReadOnly, WorkDir: wd})
	joined = strings.Join(ro, "\x00")
	if strings.Contains(joined, "\x00--bind\x00"+realWD+"\x00") {
		t.Fatalf("read-only must not --bind workdir: %#v", ro)
	}
	if filepath.Base(ro[0]) != "bwrap" || ro[len(ro)-1] != "true" {
		t.Fatalf("ro argv = %#v", ro)
	}

	// Explicit NoNetwork: --unshare-net present.
	netOff := Wrap([]string{"true"}, Policy{Mode: ModeReadOnly, WorkDir: wd, NoNetwork: true})
	if !strings.Contains("\x00"+strings.Join(netOff, "\x00")+"\x00", "\x00--unshare-net\x00") {
		t.Fatalf("NoNetwork policy must include --unshare-net: %#v", netOff)
	}

	// Deny path remounted read-only after workspace bind.
	deny := filepath.Join(wd, "secret")
	if err := os.Mkdir(deny, 0o700); err != nil {
		t.Fatal(err)
	}
	realDeny, err := filepath.EvalSymlinks(deny)
	if err != nil {
		realDeny = deny
	}
	realDeny, _ = filepath.Abs(realDeny)
	denyArgv := Wrap([]string{"true"}, Policy{
		Mode:           ModeWorkspaceWrite,
		WorkDir:        wd,
		DenyWritePaths: []string{deny},
	})
	joined = strings.Join(denyArgv, "\x00")
	if !strings.Contains(joined, "\x00--ro-bind\x00"+realDeny+"\x00"+realDeny) &&
		!strings.Contains(joined, "\x00--ro-bind\x00"+deny+"\x00"+deny) {
		t.Fatalf("deny path missing ro-bind: %#v", denyArgv)
	}

	// NoWorkspaceWrite: no --bind workdir (shared temp binds may remain).
	nw := Wrap([]string{"true"}, Policy{
		Mode:             ModeWorkspaceWrite,
		WorkDir:          wd,
		NoWorkspaceWrite: true,
	})
	nwJoined := strings.Join(nw, "\x00")
	if strings.Contains(nwJoined, "\x00--bind\x00"+realWD+"\x00") {
		t.Fatalf("NoWorkspaceWrite must not --bind workdir: %#v", nw)
	}
}

func TestSharedWritablePathsAndIsShared(t *testing.T) {
	if !IsSharedWritablePath("/dev/null") {
		t.Fatal("/dev/null should be shared writable")
	}
	if !IsSharedWritablePath("/tmp/foo") {
		t.Fatal("/tmp/foo should be shared writable")
	}
	if IsSharedWritablePath("/etc/passwd") {
		t.Fatal("/etc/passwd must not be shared writable")
	}
	// Fixture home must not sit under /tmp (which is itself shared-writable).
	realHome, err := os.UserHomeDir()
	if err != nil || realHome == "" || IsSharedWritablePath(realHome) {
		t.Skip("need a non-shared-writable real home for over-broad root tests")
	}
	home, err := os.MkdirTemp(realHome, ".strike-sandbox-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	// Create workspace before rewriting TMPDIR (t.TempDir follows TMPDIR).
	wd := t.TempDir()
	// Over-broad env roots must not open the whole home tree.
	t.Setenv("TMPDIR", home)
	t.Setenv("XDG_CACHE_HOME", home)
	if IsSharedWritablePath(filepath.Join(home, "Documents", "secret")) {
		t.Fatal("TMPDIR/XDG_CACHE_HOME=$HOME must not make home contents shared-writable")
	}
	tmpUnderHome := filepath.Join(home, "tmpdir")
	if err := os.MkdirAll(tmpUnderHome, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("TMPDIR", tmpUnderHome)
	cacheFile := filepath.Join(home, ".cache", "go-build", "x")
	if !IsSharedWritablePath(cacheFile) {
		t.Fatalf("cache path %q should be shared writable", cacheFile)
	}
	if !isSafeSharedRoot("/tmp", home) || isSafeSharedRoot(home, home) || isSafeSharedRoot("/", home) {
		t.Fatal("isSafeSharedRoot basic cases")
	}
	if parent := filepath.Dir(home); parent != "/" && isSafeSharedRoot(parent, home) {
		t.Fatalf("ancestor %q of home should be rejected", parent)
	}
	paths := SharedWritablePaths(wd, true)
	foundCache := false
	wantCache := filepath.Join(home, ".cache")
	if real, err := filepath.EvalSymlinks(wantCache); err == nil {
		wantCache = real
	}
	for _, p := range paths {
		if p == wantCache {
			foundCache = true
		}
		if p == wd {
			t.Fatalf("SharedWritablePaths must not include workdir %q: %v", wd, paths)
		}
	}
	if !foundCache {
		t.Fatalf("expected cache in SharedWritablePaths: %v", paths)
	}
	// Caches omitted when includeCaches=false.
	temps := SharedWritablePaths(wd, false)
	for _, p := range temps {
		if p == wantCache {
			t.Fatalf("temp-only list has cache %q: %v", p, temps)
		}
	}
}

func TestExplainAndProfileText(t *testing.T) {
	wd := t.TempDir()
	text := Explain(Policy{
		Mode:           ModeWorkspaceWrite,
		WorkDir:        wd,
		NoNetwork:      true,
		NetworkAllow:   []string{"api.github.com", "10.0.0.0/8"},
		DenyWriteGlobs: []string{"**/*.env"},
	})
	for _, want := range []string{
		"sandbox mode: workspace-write",
		"network: false",
		"network allowlist: api.github.com, 10.0.0.0/8",
		"egress enforcement: preflight",
		"OS network: off",
		"deny-write globs: **/*.env",
		"profile:",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("Explain missing %q:\n%s", want, text)
		}
	}
	unrestricted := Explain(Policy{Mode: ModeReadOnly, WorkDir: wd})
	if !strings.Contains(unrestricted, "network allowlist: (none — unrestricted public)") {
		t.Errorf("Explain unrestricted allowlist:\n%s", unrestricted)
	}
	if !strings.Contains(unrestricted, "egress enforcement: none") {
		t.Errorf("Explain egress none:\n%s", unrestricted)
	}
	if !strings.Contains(unrestricted, "OS host filter: none") {
		t.Errorf("Explain OS host filter gap:\n%s", unrestricted)
	}
	if !strings.Contains(unrestricted, "network: true") {
		t.Errorf("Explain default network on:\n%s", unrestricted)
	}
	off := Explain(Policy{Mode: ModeOff})
	if !strings.Contains(off, "disabled") {
		t.Fatalf("off explain = %q", off)
	}
	prof := ProfileText(Policy{Mode: ModeReadOnly, WorkDir: wd})
	if prof == "" {
		t.Fatal("empty ProfileText")
	}
}

func TestWrapDarwinArgvShape(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin seatbelt argv shape")
	}
	ResetWarnForTest()
	forceSetAvailabilityForTest(availInfo{ok: true, name: "sandbox-exec"})
	wd := t.TempDir()
	res := WrapResult([]string{"bash", "-c", "echo hi"}, Policy{
		Mode:    ModeWorkspaceWrite,
		WorkDir: wd,
	})
	if !res.Applied || res.Backend != "sandbox-exec" {
		t.Fatalf("result = %+v", res)
	}
	argv := res.Argv
	if len(argv) < 5 {
		t.Fatalf("argv too short: %#v", argv)
	}
	if !strings.HasSuffix(argv[0], "sandbox-exec") {
		t.Fatalf("launcher = %q", argv[0])
	}
	if argv[1] != "-f" {
		t.Fatalf("expected -f, got %#v", argv)
	}
	profile := argv[2]
	body, err := os.ReadFile(profile)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "(version 1)") || !strings.Contains(text, "(deny default)") {
		t.Fatalf("profile missing base rules:\n%s", text)
	}
	if !strings.Contains(text, "file-write*") || !strings.Contains(text, "subpath") {
		t.Fatalf("profile missing workspace write:\n%s", text)
	}
	for _, rule := range []string{
		`(global-name "com.apple.SecurityServer")`,
		`(global-name "com.apple.securityd.xpc")`,
		`(global-name "com.apple.trustd.agent")`,
		`(global-name "com.apple.TrustEvaluationAgent")`,
		`(global-name "com.apple.ocspd")`,
		`(ipc-posix-name "com.apple.AppleDatabaseChanged")`,
		`(preference-domain "com.apple.security")`,
		`(preference-domain "com.apple.security_common")`,
	} {
		if !strings.Contains(text, rule) {
			t.Errorf("profile missing Keychain rule %q:\n%s", rule, text)
		}
	}
	// Workdir (or its real path) must appear.
	realWD, _ := filepath.EvalSymlinks(wd)
	if realWD == "" {
		realWD = wd
	}
	if !strings.Contains(text, realWD) && !strings.Contains(text, wd) {
		t.Fatalf("profile missing workdir %q:\n%s", wd, text)
	}
	if argv[3] != "bash" || argv[4] != "-c" {
		t.Fatalf("command suffix = %#v", argv[3:])
	}

	// Read-only profile must not grant workspace write subpath for wd.
	ro := Wrap([]string{"true"}, Policy{Mode: ModeReadOnly, WorkDir: wd})
	roBody, err := os.ReadFile(ro[2])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(roBody), realWD) || strings.Contains(string(roBody), filepath.Clean(wd)) {
		// Allow if only in a comment — require no subpath allow for it.
		if strings.Contains(string(roBody), `subpath "`+realWD) || strings.Contains(string(roBody), `subpath "`+filepath.Clean(wd)) {
			t.Fatalf("read-only profile must not allow write to workdir:\n%s", roBody)
		}
	}
}

func TestDarwinSandboxHTTPSHelper(t *testing.T) {
	if os.Getenv("STRIKE_SANDBOX_HTTPS_HELPER") == "" {
		t.Skip("helper process")
	}
	// Prove TLS + host networking work under seatbelt. Any HTTP response
	// (including 403/429 from api.github.com rate limits on CI runners) means
	// certificate trust and dial succeeded; only transport errors fail the check.
	resp, err := http.Get("https://api.github.com/zen")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode < 100 || resp.StatusCode > 599 {
		t.Fatalf("status = %s", resp.Status)
	}
}

func TestDarwinIntegrationAllowsKeychainRead(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin seatbelt integration")
	}
	ResetWarnForTest()
	resetAvailabilityForTest()
	if !Available() {
		if os.Getenv("CI") != "" {
			t.Fatal("sandbox-exec unavailable or blocked in macOS CI")
		}
		t.Skip("sandbox-exec unavailable or blocked in this environment")
	}

	wd := t.TempDir()
	keychain := filepath.Join(wd, "test.keychain-db")
	const password = "strike-sandbox-test"
	for _, args := range [][]string{
		{"create-keychain", "-p", password, keychain},
		{"unlock-keychain", "-p", password, keychain},
		{"add-generic-password", "-s", "strike-sandbox-test", "-a", "test", "-w", password, keychain},
	} {
		if out, err := exec.Command("/usr/bin/security", args...).CombinedOutput(); err != nil {
			t.Fatalf("security %s: %v: %s", args[0], err, out)
		}
	}

	listArgv := Wrap([]string{"/usr/bin/security", "list-keychains"}, Policy{
		Mode: ModeWorkspaceWrite, WorkDir: wd,
	})
	if out, err := exec.Command(listArgv[0], listArgv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("sandboxed Keychain search list: %v: %s", err, out)
	}

	argv := Wrap([]string{
		"/usr/bin/security", "find-generic-password",
		"-s", "strike-sandbox-test", "-a", "test", "-w", keychain,
	}, Policy{Mode: ModeWorkspaceWrite, WorkDir: wd})
	out, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
	if err != nil {
		t.Fatalf("sandboxed Keychain read: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != password {
		t.Fatalf("sandboxed Keychain read = %q", out)
	}

	httpsArgv := Wrap([]string{
		os.Args[0], "-test.run=^TestDarwinSandboxHTTPSHelper$", "-test.count=1",
	}, Policy{Mode: ModeWorkspaceWrite, WorkDir: wd})
	httpsCmd := exec.Command(httpsArgv[0], httpsArgv[1:]...)
	httpsCmd.Env = append(os.Environ(), "STRIKE_SANDBOX_HTTPS_HELPER=1")
	if out, err := httpsCmd.CombinedOutput(); err != nil {
		t.Fatalf("sandboxed Go HTTPS trust evaluation: %v: %s", err, out)
	}
}

func TestLinuxIntegrationEnforcesFS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux bwrap integration")
	}
	ResetWarnForTest()
	// Use real probe — skip when bwrap cannot run in this environment.
	resetAvailabilityForTest()
	if !Available() {
		t.Skipf("bwrap unavailable or blocked in this environment")
	}
	wd := t.TempDir()
	// Outside must not sit under shared writable roots (/tmp, ~/.cache, …).
	home, err := os.UserHomeDir()
	if err != nil || home == "" || IsSharedWritablePath(home) {
		t.Skip("need a non-shared-writable home for outside fixture")
	}
	outside, err := os.MkdirTemp(home, ".strike-sandbox-outside-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(outside) })
	if IsSharedWritablePath(outside) {
		t.Skipf("outside fixture %q is shared-writable", outside)
	}

	argv := Wrap([]string{"bash", "-c",
		"echo in > '" + filepath.Join(wd, "ok.txt") + "' && " +
			"echo out > '" + filepath.Join(outside, "nope.txt") + "'",
	}, Policy{Mode: ModeWorkspaceWrite, WorkDir: wd})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	// Outside write should fail; overall command non-zero.
	if err == nil {
		t.Fatalf("expected failure writing outside workspace; out=%s", out)
	}
	if _, err := os.Stat(filepath.Join(wd, "ok.txt")); err != nil {
		// Some environments fail entirely; still require outside absent.
		t.Logf("inside write missing (out=%s err=%v)", out, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "nope.txt")); err == nil {
		t.Fatal("outside write must be blocked by bwrap")
	}
}

func TestLinuxIntegrationAllowsSharedTemp(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux bwrap integration")
	}
	ResetWarnForTest()
	resetAvailabilityForTest()
	if !Available() {
		t.Skipf("bwrap unavailable or blocked in this environment")
	}
	wd := t.TempDir()
	marker := filepath.Join("/tmp", "strike-sandbox-shared-"+filepath.Base(wd))
	t.Cleanup(func() { os.Remove(marker) })
	argv := Wrap([]string{"bash", "-c", "echo shared > '" + marker + "'"}, Policy{
		Mode:    ModeWorkspaceWrite,
		WorkDir: wd,
	})
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = wd
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shared /tmp write should succeed: err=%v out=%s", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker missing after shared write: %v", err)
	}
}

// TestLinuxIntegrationDefaultNetworkHTTPS is the #750 regression: default
// workspace-write policy (and bare Policy{Mode,WorkDir}) must reach the public
// internet. NoNetwork must air-gap. Skips when bwrap or outbound HTTPS is
// unavailable in the environment.
func TestLinuxIntegrationDefaultNetworkHTTPS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux bwrap integration")
	}
	ResetWarnForTest()
	resetAvailabilityForTest()
	if !Available() {
		t.Skipf("bwrap unavailable or blocked in this environment")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not available")
	}
	// Host baseline: skip when this environment has no outbound HTTPS.
	base := exec.Command("curl", "-sI", "--max-time", "5", "https://example.com")
	if out, err := base.CombinedOutput(); err != nil {
		t.Skipf("host HTTPS unavailable (skip integration): err=%v out=%s", err, out)
	}

	wd := t.TempDir()
	curlCmd := "curl -sI --max-time 5 https://example.com | head -n 1"

	run := func(name string, p Policy) (string, error) {
		t.Helper()
		argv := Wrap([]string{"bash", "-c", curlCmd}, p)
		joined := "\x00" + strings.Join(argv, "\x00") + "\x00"
		hasUnshare := strings.Contains(joined, "\x00--unshare-net\x00")
		if p.NoNetwork && !hasUnshare {
			t.Fatalf("%s: expected --unshare-net in %#v", name, argv)
		}
		if !p.NoNetwork && hasUnshare {
			t.Fatalf("%s: unexpected --unshare-net in %#v", name, argv)
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = wd
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// Bare policy (zero NoNetwork) — the footgun PR #751 left open.
	out, err := run("bare", Policy{Mode: ModeWorkspaceWrite, WorkDir: wd})
	if err != nil {
		t.Fatalf("default network bare Policy HTTPS failed: err=%v out=%s", err, out)
	}
	if !strings.Contains(out, "HTTP") {
		t.Fatalf("default network bare Policy expected HTTP response, got %q", out)
	}

	// Explicit air-gap must fail DNS/connect (gh reports "token invalid" for
	// the same failure mode — see issue #750 screenshot).
	outOff, errOff := run("NoNetwork", Policy{Mode: ModeWorkspaceWrite, WorkDir: wd, NoNetwork: true})
	if errOff == nil && strings.Contains(outOff, "HTTP/2 200") {
		t.Fatalf("NoNetwork must not reach HTTPS; out=%s", outOff)
	}
	// Prefer a clear network failure signal when curl runs.
	if errOff == nil && strings.TrimSpace(outOff) != "" &&
		!strings.Contains(strings.ToLower(outOff), "fail") &&
		!strings.Contains(strings.ToLower(outOff), "resolv") &&
		!strings.Contains(strings.ToLower(outOff), "network") &&
		!strings.Contains(outOff, "Could not") {
		t.Logf("NoNetwork out (non-HTTP failure shape ok): %q", outOff)
	}
}

func TestNetworkEnabledZeroValue(t *testing.T) {
	if !(Policy{}).NetworkEnabled() {
		t.Fatal("zero Policy must enable network")
	}
	if (Policy{NoNetwork: true}).NetworkEnabled() {
		t.Fatal("NoNetwork must disable network")
	}
}
