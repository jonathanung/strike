package sandbox

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
	// bwrap --ro-bind / / --bind WD WD --dev /dev --proc /proc --unshare-net --die-with-parent -- bash -c echo hi
	wantPrefix := []string{
		"bwrap",
		"--ro-bind", "/", "/",
		"--bind", realWD, realWD,
		"--dev", "/dev",
		"--proc", "/proc",
		"--unshare-net",
		"--die-with-parent",
		"--",
		"bash", "-c", "echo hi",
	}
	if len(argv) != len(wantPrefix) {
		t.Fatalf("argv len=%d\ngot  %#v\nwant %#v", len(argv), argv, wantPrefix)
	}
	for i := range wantPrefix {
		if argv[i] != wantPrefix[i] {
			t.Fatalf("argv[%d]=%q want %q\nfull=%#v", i, argv[i], wantPrefix[i], argv)
		}
	}

	// Read-only: no workdir bind.
	ro := Wrap([]string{"true"}, Policy{Mode: ModeReadOnly, WorkDir: wd})
	joined := strings.Join(ro, "\x00")
	if strings.Contains(joined, "\x00--bind\x00") {
		t.Fatalf("read-only must not --bind workdir: %#v", ro)
	}
	if ro[0] != "bwrap" || ro[len(ro)-1] != "true" {
		t.Fatalf("ro argv = %#v", ro)
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
	outside, err := os.MkdirTemp("", "strike-sandbox-outside-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(outside) })

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
