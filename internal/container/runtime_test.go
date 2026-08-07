package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestContainerNameDeterministic(t *testing.T) {
	a := ContainerName("/home/u/proj")
	b := ContainerName("/home/u/proj")
	if a != b {
		t.Fatalf("not deterministic: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, "strike-proj-") {
		t.Fatalf("prefix: got %q", a)
	}
	// sha256 hex suffix length 16
	parts := strings.Split(a, "-")
	suf := parts[len(parts)-1]
	if len(suf) != 16 {
		t.Fatalf("hash len: got %d in %q", len(suf), a)
	}
	// different path → different name
	c := ContainerName("/home/u/other")
	if a == c {
		t.Fatalf("expected different names for different paths")
	}
}

func TestContainerNameSanitizes(t *testing.T) {
	name := ContainerName("/tmp/my repo!")
	if strings.Contains(name, " ") || strings.Contains(name, "!") {
		t.Fatalf("unsanitized: %q", name)
	}
	if !strings.HasPrefix(name, "strike-my-repo--") && !strings.HasPrefix(name, "strike-my-repo-") {
		// "my repo!" → "my-repo-"
		if !strings.Contains(name, "strike-") {
			t.Fatalf("got %q", name)
		}
	}
}

func TestNetworkName(t *testing.T) {
	n := NetworkName("/repo/x")
	if !strings.HasSuffix(n, "-net") {
		t.Fatalf("got %q", n)
	}
	if !strings.HasPrefix(n, ContainerName("/repo/x")) {
		t.Fatalf("network should extend container name: %q", n)
	}
}

func TestLabelsAndArgs(t *testing.T) {
	labs := Labels("/repo", "cfgabc", "img123", "dev")
	if labs[LabelManaged] != "true" {
		t.Fatalf("managed: %v", labs)
	}
	if labs[LabelRepoPath] != "/repo" || labs[LabelConfigHash] != "cfgabc" {
		t.Fatalf("labels: %v", labs)
	}
	if labs[LabelPurpose] != "dev" {
		t.Fatalf("purpose: %v", labs)
	}
	args := LabelArgs(labs)
	if len(args) != 5 {
		t.Fatalf("args len %d: %v", len(args), args)
	}
	// sorted keys
	if args[0] != LabelConfigHash+"=cfgabc" {
		t.Fatalf("first arg (sorted): %q", args[0])
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, LabelManaged+"=true") {
		t.Fatalf("missing managed: %v", args)
	}
}

func TestCLIResolveExplicitAndAuto(t *testing.T) {
	c := NewCLI("podman")
	c.LookPath = func(file string) (string, error) {
		if file == "podman" {
			return "/usr/bin/podman", nil
		}
		return "", errors.New("not found")
	}
	got, err := c.Resolve()
	if err != nil || got != "/usr/bin/podman" {
		t.Fatalf("resolve podman: %q %v", got, err)
	}
	if c.Engine() != "/usr/bin/podman" {
		t.Fatalf("engine: %q", c.Engine())
	}

	auto := NewCLI("")
	auto.LookPath = func(file string) (string, error) {
		if file == "docker" {
			return "", errors.New("no")
		}
		if file == "podman" {
			return "/bin/podman", nil
		}
		return "", errors.New("no")
	}
	got, err = auto.Resolve()
	if err != nil || got != "/bin/podman" {
		t.Fatalf("auto podman: %q %v", got, err)
	}

	none := NewCLI("")
	none.LookPath = func(string) (string, error) { return "", errors.New("no") }
	if _, err := none.Resolve(); !errors.Is(err, ErrEngineNotFound) {
		t.Fatalf("want ErrEngineNotFound, got %v", err)
	}
}

func TestCLIAvailablePullCreateLifecycle(t *testing.T) {
	var calls [][]string
	c := NewCLI("docker")
	c.LookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	c.ExecFn = func(_ context.Context, name string, args ...string) (string, string, int, error) {
		if name != "/usr/bin/docker" {
			t.Fatalf("bin: %q", name)
		}
		calls = append(calls, append([]string{}, args...))
		switch {
		case len(args) >= 1 && args[0] == "info":
			return "24.0.0", "", 0, nil
		case len(args) >= 1 && args[0] == "pull":
			return "", "", 0, nil
		case len(args) >= 1 && args[0] == "create":
			return "cid123\n", "", 0, nil
		case len(args) >= 1 && args[0] == "start":
			return "", "", 0, nil
		case len(args) >= 1 && args[0] == "exec":
			return "ok", "", 0, nil
		case len(args) >= 1 && args[0] == "stop":
			return "", "", 0, nil
		case len(args) >= 1 && args[0] == "rm":
			return "", "", 0, nil
		case len(args) >= 1 && args[0] == "inspect":
			return "cid123", "", 0, nil
		default:
			return "", "unknown", 1, nil
		}
	}
	ctx := context.Background()
	if err := c.Available(ctx); err != nil {
		t.Fatalf("available: %v", err)
	}
	if err := c.Pull(ctx, "alpine:latest"); err != nil {
		t.Fatalf("pull: %v", err)
	}
	id, err := c.Create(ctx, "alpine:latest", CreateOpts{
		Name:   "strike-test",
		Env:    []string{"A=1"},
		Labels: Labels("/repo", "h", "i", "eval"),
	})
	if err != nil || id != "cid123" {
		t.Fatalf("create: %q %v", id, err)
	}
	// create argv includes labels and sleep infinity default cmd
	var createArgs []string
	for _, call := range calls {
		if len(call) > 0 && call[0] == "create" {
			createArgs = call
			break
		}
	}
	joined := strings.Join(createArgs, " ")
	if !strings.Contains(joined, "--name strike-test") {
		t.Fatalf("create args missing name: %v", createArgs)
	}
	if !strings.Contains(joined, "--label "+LabelManaged+"=true") {
		t.Fatalf("create args missing label: %v", createArgs)
	}
	if !strings.HasSuffix(joined, "sleep infinity") {
		t.Fatalf("expected default sleep infinity: %v", createArgs)
	}
	if err := c.Start(ctx, id); err != nil {
		t.Fatalf("start: %v", err)
	}
	out, _, code, err := c.Exec(ctx, id, []string{"echo", "hi"}, ExecOpts{Timeout: time.Second})
	if err != nil || code != 0 || out != "ok" {
		t.Fatalf("exec: out=%q code=%d err=%v", out, code, err)
	}
	gotID, err := c.InspectID(ctx, "strike-test")
	if err != nil || gotID != "cid123" {
		t.Fatalf("inspect: %q %v", gotID, err)
	}
	if err := c.Stop(ctx, id, 5); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Remove(ctx, id); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

func TestCLIAvailableDaemonDown(t *testing.T) {
	c := NewCLI("docker")
	c.LookPath = func(string) (string, error) { return "docker", nil }
	c.ExecFn = func(context.Context, string, ...string) (string, string, int, error) {
		return "", "Cannot connect to the Docker daemon", 1, nil
	}
	err := c.Available(context.Background())
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("want ErrEngineUnavailable, got %v", err)
	}
}

func TestCLIInspectMissing(t *testing.T) {
	c := NewCLI("docker")
	c.LookPath = func(string) (string, error) { return "docker", nil }
	c.ExecFn = func(context.Context, string, ...string) (string, string, int, error) {
		return "", "Error: No such object: missing", 1, nil
	}
	_, err := c.InspectID(context.Background(), "missing")
	if !errors.Is(err, ErrNoContainer) {
		t.Fatalf("want ErrNoContainer, got %v", err)
	}
}

func TestCLICopy(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out")
	var saw []string
	c := NewCLI("docker")
	c.LookPath = func(string) (string, error) { return "docker", nil }
	c.ExecFn = func(_ context.Context, _ string, args ...string) (string, string, int, error) {
		saw = args
		return "", "", 0, nil
	}
	if err := c.CopyFrom(context.Background(), "cid", "/app", dst); err != nil {
		t.Fatalf("copyfrom: %v", err)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("dst mkdir: %v", err)
	}
	if len(saw) < 3 || saw[0] != "cp" || saw[1] != "cid:/app" {
		t.Fatalf("copyfrom args: %v", saw)
	}
	if err := c.CopyTo(context.Background(), "cid", "/host/f", "/c/f"); err != nil {
		t.Fatalf("copyto: %v", err)
	}
	if len(saw) < 3 || saw[0] != "cp" || saw[2] != "cid:/c/f" {
		t.Fatalf("copyto args: %v", saw)
	}
}

func TestCLICreateEmptyImageAndEmptyID(t *testing.T) {
	c := NewCLI("docker")
	c.LookPath = func(string) (string, error) { return "docker", nil }
	c.ExecFn = func(context.Context, string, ...string) (string, string, int, error) {
		return "  \n", "", 0, nil
	}
	if _, err := c.Create(context.Background(), "", CreateOpts{}); err == nil {
		t.Fatal("expected empty image error")
	}
	if _, err := c.Create(context.Background(), "img", CreateOpts{}); !errors.Is(err, ErrEmptyID) {
		t.Fatalf("want ErrEmptyID, got %v", err)
	}
}

func TestDefaultExecFuncSmoke(t *testing.T) {
	// Use a portable builtin via the shell? Prefer `true` if present.
	path, err := execLookPath("true")
	if err != nil {
		t.Skip("true not on PATH")
	}
	stdout, stderr, code, err := DefaultExecFunc(context.Background(), path)
	if err != nil || code != 0 {
		t.Fatalf("true: code=%d err=%v stderr=%q", code, err, stderr)
	}
	_ = stdout
}

// avoid importing os/exec in every test file path for LookPath of true
func execLookPath(file string) (string, error) {
	c := NewCLI("")
	return c.lookPath(file)
}
