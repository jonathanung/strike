package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// E12.8 containerization test suite: golden Dockerfiles, preflight table,
// naming/hash, skill contract, and offline round-trip via fake CLI.

func TestGoldenDockerfiles(t *testing.T) {
	cases := []struct {
		name string
		cfg  func() Config
		file string
	}{
		{
			name: "default",
			cfg:  DefaultConfig,
			file: "default.Dockerfile",
		},
		{
			name: "node-python",
			cfg: func() Config {
				c := DefaultConfig()
				c.NeedsNode = true
				c.NodeVersion = "20"
				c.NeedsPython = true
				c.AptPackages = []string{"make"}
				return c
			},
			file: "node-python.Dockerfile",
		},
		{
			name: "go-rust-none-net",
			cfg: func() Config {
				c := DefaultConfig()
				c.NeedsGo = true
				c.GoVersion = "1.22"
				c.NeedsRust = true
				c.Network.Mode = "none"
				c.Resources.Memory = "1g"
				return c
			},
			file: "go-rust.Dockerfile",
		},
	}
	dir := filepath.Join("testdata", "dockerfiles")
	update := os.Getenv("UPDATE_GOLDEN") == "1"
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := MinimalDockerfile(tc.cfg(), 1000)
			// Normalize HOST_UID line for stability
			body = strings.ReplaceAll(body, "ARG HOST_UID=1000", "ARG HOST_UID=1000")
			path := filepath.Join(dir, tc.file)
			if update {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run UPDATE_GOLDEN=1): %v", err)
			}
			if string(want) != body {
				t.Fatalf("golden mismatch %s\n--- got ---\n%s\n--- want ---\n%s", tc.file, body, want)
			}
		})
	}
}

func TestPreflightFailuresTable(t *testing.T) {
	repo := t.TempDir()
	// Write a dockerfile for drift cases
	df := filepath.Join(repo, DefaultEjectName)
	if err := os.WriteFile(df, []byte("# strike-config-hash: deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef\nFROM ubuntu:24.04\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cliOK := NewCLI("docker")
	cliOK.LookPath = func(string) (string, error) { return "/bin/docker", nil }
	cliOK.ExecFn = func(_ context.Context, _ string, args ...string) (string, string, int, error) {
		if len(args) > 0 && args[0] == "info" {
			return "1", "", 0, nil
		}
		return "", "", 0, nil
	}
	cliMissing := NewCLI("docker")
	cliMissing.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	cliDown := NewCLI("docker")
	cliDown.LookPath = func(string) (string, error) { return "/bin/docker", nil }
	cliDown.ExecFn = func(context.Context, string, ...string) (string, string, int, error) {
		return "", "Cannot connect to the Docker daemon", 1, nil
	}

	tests := []struct {
		name string
		env  map[string]string
		rt   *CLI
		cfg  Config
		opts PreflightOpts
		code string
	}{
		{
			name: "already_inside",
			env:  map[string]string{"STRIKE_ISOLATION": "container"},
			rt:   cliOK,
			cfg:  DefaultConfig(),
			opts: PreflightOpts{},
			code: CodeAlreadyInside,
		},
		{
			name: "engine_not_found",
			env:  map[string]string{"STRIKE_ISOLATION": ""},
			rt:   cliMissing,
			cfg:  DefaultConfig(),
			opts: PreflightOpts{},
			code: CodeEngineMissing,
		},
		{
			name: "engine_unavailable",
			env:  map[string]string{"STRIKE_ISOLATION": ""},
			rt:   cliDown,
			cfg:  DefaultConfig(),
			opts: PreflightOpts{},
			code: CodeEngineDown,
		},
		{
			name: "no_dockerfile",
			env:  map[string]string{"STRIKE_ISOLATION": ""},
			rt:   cliOK,
			cfg:  DefaultConfig(),
			opts: PreflightOpts{RequireDockerfile: true},
			code: CodeNoDockerfile,
		},
		{
			name: "dockerfile_drift",
			env:  map[string]string{"STRIKE_ISOLATION": ""},
			rt:   cliOK,
			cfg:  DefaultConfig(),
			opts: PreflightOpts{RequireDockerfile: true, CheckDrift: true, Version: "test"},
			code: CodeDockerfileDrift,
		},
		{
			name: "required_env",
			env:  map[string]string{"STRIKE_ISOLATION": ""},
			rt:   cliOK,
			cfg: func() Config {
				c := DefaultConfig()
				c.Auth.RequiredEnv = []string{"MUST_EXIST_XYZ"}
				return c
			}(),
			opts: PreflightOpts{},
			code: CodeRequiredEnv,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			// no_dockerfile uses empty temp without file
			dir := repo
			if tc.code == CodeNoDockerfile {
				dir = t.TempDir()
			}
			err := Preflight(context.Background(), tc.rt, tc.cfg, dir, tc.opts)
			if err == nil {
				t.Fatal("expected error")
			}
			var pe *PreflightError
			if !errors.As(err, &pe) {
				t.Fatalf("want PreflightError, got %T %v", err, err)
			}
			if pe.Code != tc.code {
				t.Fatalf("code=%q want %q (%v)", pe.Code, tc.code, pe)
			}
		})
	}
}

func TestNamingHashMatcherNetwork(t *testing.T) {
	// Ported naming/hash/matcher coverage (Zone → strike labels).
	a := ContainerName("/tmp/repo-a")
	b := ContainerName("/tmp/repo-a")
	c := ContainerName("/tmp/repo-b")
	if a != b || a == c {
		t.Fatalf("%s %s %s", a, b, c)
	}
	if !strings.HasPrefix(a, "strike-") || len(a) < 20 {
		t.Fatalf("name shape: %s", a)
	}
	if NetworkName("/tmp/repo-a") != a+"-net" {
		t.Fatalf("network: %s", NetworkName("/tmp/repo-a"))
	}
	labs := Labels("/tmp/repo-a", "hash", "img", "dev")
	if labs[LabelManaged] != "true" || labs[LabelRepoPath] != "/tmp/repo-a" {
		t.Fatalf("%v", labs)
	}
	args := LabelArgs(labs)
	if len(args) != len(labs) {
		t.Fatalf("%v", args)
	}
	// hash stability
	cfg := DefaultConfig()
	body := MinimalDockerfile(cfg, 1000)
	h1, err := ComputeConfigHash(cfg, body, "v1")
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := ComputeConfigHash(cfg, body, "v1")
	if h1 != h2 || len(h1) != 64 {
		t.Fatalf("%s %s", h1, h2)
	}
	h3, _ := ComputeConfigHash(cfg, body, "v2")
	if h1 == h3 {
		t.Fatal("version should change hash")
	}
}

func TestDevcontainerSkillContract(t *testing.T) {
	// Skill is embedded via config package; assert file content contract here
	// so E12.8 does not depend on config package import cycles.
	data, err := os.ReadFile(filepath.Join("..", "..", "product", "config", "skills", "devcontainer.md"))
	if err != nil {
		// module-root relative when cwd is not the package dir
		data, err = os.ReadFile(filepath.Join("internal", "product", "config", "skills", "devcontainer.md"))
	}
	if err != nil {
		t.Skipf("devcontainer skill not found from %s: %v", mustWD(t), err)
	}
	s := string(data)
	for _, needle := range []string{
		"question",
		"strike container detect",
		"strike container eject",
		"Never",
		"$ARGUMENTS",
		"base image",
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("skill missing %q", needle)
		}
	}
}

func mustWD(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		return "?"
	}
	return wd
}

func TestOfflineRoundTripEjectLaunch(t *testing.T) {
	// eject → fake build/launch → exec pwd (no real docker).
	repo := t.TempDir()
	cfg := DefaultConfig()
	cfg.TemplateVersion = "suite"
	cfg.AptPackages = []string{"make"}
	res, err := Eject(cfg, repo, EjectOpts{Version: "suite"})
	if err != nil || !res.Wrote {
		t.Fatalf("eject: %+v %v", res, err)
	}
	body, err := os.ReadFile(res.Path)
	if err != nil || !strings.Contains(string(body), "strike-config-hash:") {
		t.Fatalf("dockerfile: %s %v", body, err)
	}

	// Fake engine lifecycle
	type state struct {
		images     map[string]string
		containers map[string]*InspectState
		networks   map[string]bool
	}
	st := &state{images: map[string]string{}, containers: map[string]*InspectState{}, networks: map[string]bool{}}
	cli := NewCLI("docker")
	cli.LookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	cli.ExecFn = func(_ context.Context, _ string, args ...string) (string, string, int, error) {
		if len(args) == 0 {
			return "", "no", 1, nil
		}
		switch args[0] {
		case "info":
			return "24", "", 0, nil
		case "build":
			tag := "strike-dev:x"
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-t" {
					tag = args[i+1]
				}
			}
			id := "sha256:img1"
			st.images[tag] = id
			st.images[id] = id
			return "ok\n", "", 0, nil
		case "image":
			if len(args) >= 2 && args[1] == "inspect" {
				ref := args[len(args)-1]
				if id, ok := st.images[ref]; ok {
					return id, "", 0, nil
				}
				return "", "missing", 1, nil
			}
		case "network":
			if args[1] == "create" {
				st.networks[args[len(args)-1]] = true
				return args[len(args)-1], "", 0, nil
			}
			if args[1] == "inspect" {
				if st.networks[args[len(args)-1]] {
					return "ok", "", 0, nil
				}
				return "", "no", 1, nil
			}
			if args[1] == "rm" {
				delete(st.networks, args[len(args)-1])
				return "", "", 0, nil
			}
		case "create":
			id := "cid-rt"
			name := ""
			labs := map[string]string{}
			for i, a := range args {
				if a == "--name" && i+1 < len(args) {
					name = args[i+1]
				}
				if a == "--label" && i+1 < len(args) {
					k, v, _ := strings.Cut(args[i+1], "=")
					labs[k] = v
				}
			}
			st.containers[id] = &InspectState{ID: id, Name: name, Labels: labs, Status: "created"}
			if name != "" {
				st.containers[name] = st.containers[id]
			}
			return id + "\n", "", 0, nil
		case "start":
			id := args[len(args)-1]
			if c := st.containers[id]; c != nil {
				c.Running = true
				c.Status = "running"
			}
			return "", "", 0, nil
		case "inspect":
			ref := args[len(args)-1]
			c := st.containers[ref]
			if c == nil {
				return "", "No such object", 1, nil
			}
			if strings.Contains(args[2], "json") {
				parts := make([]string, 0, len(c.Labels))
				for k, v := range c.Labels {
					parts = append(parts, `"`+k+`":"`+v+`"`)
				}
				return "{" + strings.Join(parts, ",") + "}", "", 0, nil
			}
			run := "false"
			if c.Running {
				run = "true"
			}
			return c.ID + "|" + c.Name + "|" + run + "|" + c.Status + "|img", "", 0, nil
		case "exec":
			// strike exec "pwd" simulation
			return "/workspace\n", "", 0, nil
		case "rm", "stop", "rmi", "ps":
			return "", "", 0, nil
		}
		return "", "unhandled " + args[0], 1, nil
	}

	m, err := NewManager(repo, cfg, cli)
	if err != nil {
		t.Fatal(err)
	}
	m.AttachFn = func(context.Context, string, string, []string, bool) error { return nil }
	ctx := context.Background()
	id, err := m.Launch(ctx, LaunchOpts{Headless: true})
	if err != nil || id == "" {
		t.Fatalf("launch: %q %v", id, err)
	}
	out, _, code, err := m.Exec(ctx, []string{"pwd"}, ExecOpts{})
	if err != nil || code != 0 || !strings.Contains(out, "/workspace") {
		t.Fatalf("exec pwd: out=%q code=%d err=%v", out, code, err)
	}
	// second launch attaches
	lr, err := m.LaunchWithResult(ctx, LaunchOpts{Headless: true})
	if err != nil || lr.Mode != LaunchModeAttached {
		t.Fatalf("attach: %+v %v", lr, err)
	}
}
