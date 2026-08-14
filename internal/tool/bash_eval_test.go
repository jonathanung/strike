package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvalContainerArgvRequiresBothEnv(t *testing.T) {
	t.Setenv("STRIKE_EVAL_CONTAINER", "")
	t.Setenv("STRIKE_EVAL_WORKDIR", "")
	if _, ok := evalContainerArgv("ls"); ok {
		t.Fatal("empty env should not route")
	}

	t.Setenv("STRIKE_EVAL_CONTAINER", "abc123def")
	t.Setenv("STRIKE_EVAL_WORKDIR", "")
	if _, ok := evalContainerArgv("ls"); ok {
		t.Fatal("SWE-style container-only env must not route host bash")
	}

	t.Setenv("STRIKE_EVAL_CONTAINER", "")
	t.Setenv("STRIKE_EVAL_WORKDIR", "/app")
	if _, ok := evalContainerArgv("ls"); ok {
		t.Fatal("workdir-only env should not route")
	}
}

func TestEvalContainerArgvRoutesTB(t *testing.T) {
	t.Setenv("STRIKE_EVAL_CONTAINER", "cafebabedeadbeef")
	t.Setenv("STRIKE_EVAL_WORKDIR", "/app")
	argv, ok := evalContainerArgv("python3 -c 'print(1)'")
	if !ok {
		t.Fatal("expected route")
	}
	want := []string{"docker", "exec", "-w", "/app", "cafebabedeadbeef", "bash", "-lc", "python3 -c 'print(1)'"}
	if len(argv) != len(want) {
		t.Fatalf("argv=%v", argv)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d]=%q want %q (%v)", i, argv[i], want[i], argv)
		}
	}
}

func TestEvalContainerArgvRejectsUnsafeValues(t *testing.T) {
	t.Setenv("STRIKE_EVAL_WORKDIR", "/app")
	for _, cid := range []string{
		"id; rm -rf /",
		"id$(reboot)",
		"id and more",
		"-sneaky",
		"",
	} {
		t.Setenv("STRIKE_EVAL_CONTAINER", cid)
		if _, ok := evalContainerArgv("true"); ok {
			t.Fatalf("cid %q should not route", cid)
		}
	}

	t.Setenv("STRIKE_EVAL_CONTAINER", "abc123")
	for _, work := range []string{
		"app",
		"/",
		"/app;reboot",
		"/app/../../etc",
		"/app with space",
	} {
		t.Setenv("STRIKE_EVAL_WORKDIR", work)
		if _, ok := evalContainerArgv("true"); ok {
			t.Fatalf("workdir %q should not route", work)
		}
	}
}

func TestMapEvalMountPath(t *testing.T) {
	t.Setenv("STRIKE_EVAL_WORKDIR", "/app")
	work := "/host/ws"
	if got := mapEvalMountPath("/app/ssl/server.key", work); got != filepath.Join(work, "ssl/server.key") {
		t.Fatalf("file: %s", got)
	}
	if got := mapEvalMountPath("/app", work); got != work {
		t.Fatalf("root: %s", got)
	}
	if got := mapEvalMountPath("/app/../etc/passwd", work); strings.HasPrefix(filepath.Clean(got), work) {
		t.Fatalf("escaped onto workspace: %s", got)
	}
	if got := mapEvalMountPath("ssl/server.key", work); got != "ssl/server.key" {
		t.Fatalf("relative unchanged: %s", got)
	}
	t.Setenv("STRIKE_EVAL_WORKDIR", "")
	if got := mapEvalMountPath("/app/x", work); got != "/app/x" {
		t.Fatalf("no env: %s", got)
	}
}

func TestResolveAllowedPathMapsEvalMount(t *testing.T) {
	t.Setenv("STRIKE_EVAL_WORKDIR", "/app")
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "ssl"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(work, "ssl", "server.key")
	if err := os.WriteFile(want, []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, rel, err := resolveAllowedPath(work, "", "/app/ssl/server.key")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("resolved %s want %s", got, want)
	}
	if rel != "ssl/server.key" && rel != "ssl\\server.key" {
		t.Fatalf("rel %s", rel)
	}
}

func TestEvalContainerArgvAcceptsDockerName(t *testing.T) {
	t.Setenv("STRIKE_EVAL_CONTAINER", "strike-eval-tb-1.2")
	t.Setenv("STRIKE_EVAL_WORKDIR", "/app")
	argv, ok := evalContainerArgv("openssl version")
	if !ok {
		t.Fatal("expected route for dotted container name")
	}
	if !strings.Contains(strings.Join(argv, " "), "openssl version") {
		t.Fatalf("%v", argv)
	}
}
