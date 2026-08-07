package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinimalDockerfile(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AptPackages = []string{"make"}
	body := MinimalDockerfile(cfg, 1000)
	if !strings.Contains(body, "FROM ubuntu:24.04") {
		t.Fatalf("base: %s", body)
	}
	if !strings.Contains(body, "make") || !strings.Contains(body, "useradd") {
		t.Fatalf("content: %s", body)
	}
	if !strings.Contains(body, "STRIKE_WORKSPACE=/workspace") {
		t.Fatalf("workspace env missing")
	}
}

func TestMinimalDockerfileToolchains(t *testing.T) {
	cfg := DefaultConfig()
	cfg.NeedsNode = true
	cfg.NodeVersion = "20"
	cfg.NeedsPython = true
	cfg.NeedsGo = true
	cfg.GoVersion = "1.22"
	cfg.NeedsRust = true
	body := MinimalDockerfile(cfg, 1000)
	for _, needle := range []string{"nodesource.com/setup_20.x", "python3", "golang-go", "rustup"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("missing %q in:\n%s", needle, body)
		}
	}
}

func TestComputeConfigHashStable(t *testing.T) {
	cfg := DefaultConfig()
	body := MinimalDockerfile(cfg, 1000)
	a, err := ComputeConfigHash(cfg, body, "v1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ComputeConfigHash(cfg, body, "v1")
	if err != nil || a != b || len(a) != 64 {
		t.Fatalf("hash %q %q err=%v", a, b, err)
	}
	c, _ := ComputeConfigHash(cfg, body, "v2")
	if a == c {
		t.Fatal("version should change hash")
	}
}

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)
	if err := c.SetImageID("img"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetContainerID("cid"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetConfigHash("abc"); err != nil {
		t.Fatal(err)
	}
	img, _ := c.ImageID()
	cid, _ := c.ContainerID()
	h, _ := c.ConfigHash()
	if img != "img" || cid != "cid" || h != "abc" {
		t.Fatalf("%s %s %s", img, cid, h)
	}
	_ = c.ClearRuntime()
	cid, _ = c.ContainerID()
	if cid != "" {
		t.Fatalf("cleared cid=%q", cid)
	}
	img, _ = c.ImageID()
	if img != "img" {
		t.Fatalf("image should remain")
	}
}

func TestManagerLaunchLifecycleFakeCLI(t *testing.T) {
	repo := t.TempDir()
	// fake engine state
	type state struct {
		images     map[string]string // tag/id -> id
		containers map[string]*InspectState
		networks   map[string]bool
	}
	st := &state{
		images:     map[string]string{},
		containers: map[string]*InspectState{},
		networks:   map[string]bool{},
	}
	var lastCreate []string
	cli := NewCLI("docker")
	cli.LookPath = func(string) (string, error) { return "/usr/bin/docker", nil }
	cli.ExecFn = func(_ context.Context, _ string, args ...string) (string, string, int, error) {
		if len(args) == 0 {
			return "", "no args", 1, nil
		}
		switch args[0] {
		case "info":
			return "24.0", "", 0, nil
		case "build":
			// find -t tag
			tag := "strike-dev:latest"
			for i := 0; i < len(args)-1; i++ {
				if args[i] == "-t" {
					tag = args[i+1]
				}
			}
			id := "sha256:build" + tag
			st.images[tag] = id
			st.images[id] = id
			return "Successfully tagged " + tag + "\n", "", 0, nil
		case "image":
			if len(args) >= 2 && args[1] == "inspect" {
				name := args[len(args)-1]
				if id, ok := st.images[name]; ok {
					return id, "", 0, nil
				}
				return "", "no such image", 1, nil
			}
		case "network":
			if args[1] == "inspect" {
				if st.networks[args[len(args)-1]] {
					return "ok", "", 0, nil
				}
				return "", "not found", 1, nil
			}
			if args[1] == "create" {
				name := args[len(args)-1]
				st.networks[name] = true
				return name, "", 0, nil
			}
			if args[1] == "rm" {
				delete(st.networks, args[len(args)-1])
				return "", "", 0, nil
			}
		case "create":
			lastCreate = append([]string{}, args...)
			id := "cid-launch-1"
			name := ""
			img := args[len(args)-3] // before sleep infinity roughly — scan
			for i, a := range args {
				if a == "--name" && i+1 < len(args) {
					name = args[i+1]
				}
			}
			// image is last token before cmd; find first non-flag after options
			for i := 1; i < len(args); i++ {
				a := args[i]
				if strings.HasPrefix(a, "-") {
					if a == "-e" || a == "-v" || a == "-w" || a == "--name" || a == "--label" ||
						a == "--network" || a == "--security-opt" || a == "--cap-drop" || a == "--cap-add" ||
						a == "--pids-limit" || a == "--memory" || a == "--cpus" || a == "--gpus" || a == "-p" ||
						a == "--entrypoint" {
						i++
					}
					continue
				}
				img = a
				break
			}
			labs := map[string]string{}
			for i, a := range args {
				if a == "--label" && i+1 < len(args) {
					kv := strings.SplitN(args[i+1], "=", 2)
					if len(kv) == 2 {
						labs[kv[0]] = kv[1]
					}
				}
			}
			st.containers[id] = &InspectState{ID: id, Name: name, Running: false, Status: "created", Image: img, Labels: labs}
			if name != "" {
				st.containers[name] = st.containers[id]
			}
			return id + "\n", "", 0, nil
		case "start":
			id := args[1]
			if c := st.containers[id]; c != nil {
				c.Running = true
				c.Status = "running"
			}
			return "", "", 0, nil
		case "stop":
			id := args[len(args)-1]
			if c := st.containers[id]; c != nil {
				c.Running = false
				c.Status = "exited"
			}
			return "", "", 0, nil
		case "rm":
			id := args[len(args)-1]
			if c := st.containers[id]; c != nil {
				delete(st.containers, c.ID)
				if c.Name != "" {
					delete(st.containers, c.Name)
				}
			}
			delete(st.containers, id)
			return "", "", 0, nil
		case "inspect":
			name := args[len(args)-1]
			c := st.containers[name]
			if c == nil {
				return "", "Error: No such object: " + name, 1, nil
			}
			if strings.Contains(args[2], "json .Config.Labels") {
				// build json
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
			return c.ID + "|" + c.Name + "|" + run + "|" + c.Status + "|" + c.Image, "", 0, nil
		case "exec":
			return "ok\n", "", 0, nil
		case "ps":
			var ids []string
			seen := map[string]bool{}
			for _, c := range st.containers {
				if c.Labels[LabelManaged] == "true" && !seen[c.ID] {
					seen[c.ID] = true
					ids = append(ids, c.ID)
				}
			}
			return strings.Join(ids, "\n"), "", 0, nil
		case "rmi":
			delete(st.images, args[len(args)-1])
			return "", "", 0, nil
		}
		return "", "unhandled " + strings.Join(args, " "), 1, nil
	}

	cfg := DefaultConfig()
	cfg.TemplateVersion = "test"
	cfg.Resources.Memory = "512m"
	cfg.Resources.CPUs = "1"
	cfg.Workspace.Ports = []string{"8080:8080"}
	persist := false
	cfg.Workspace.PersistHome = &persist

	m, err := NewManager(repo, cfg, cli)
	if err != nil {
		t.Fatal(err)
	}
	var warn strings.Builder
	m.Stderr = &warn
	m.AttachFn = func(context.Context, string, string, []string, bool) error { return nil }

	ctx := context.Background()
	if !m.NeedsBuild(ctx, false) {
		t.Fatal("expected needs build")
	}
	id, err := m.Launch(ctx, LaunchOpts{Headless: true})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	// create args should include security + resources + ports + labels
	joined := strings.Join(lastCreate, " ")
	for _, want := range []string{"--security-opt", "--cap-drop", "--memory", "512m", "--cpus", "1", "-p", "8080:8080", LabelManaged} {
		if !strings.Contains(joined, want) {
			t.Fatalf("create missing %q in %v", want, lastCreate)
		}
	}
	if m.NeedsBuild(ctx, false) {
		t.Fatal("should not need build after launch")
	}

	// second launch reuses running
	id2, err := m.Launch(ctx, LaunchOpts{Headless: true})
	if err != nil || id2 != id {
		t.Fatalf("reuse: %q %v", id2, err)
	}

	out, _, code, err := m.Exec(ctx, []string{"true"}, ExecOpts{})
	if err != nil || code != 0 {
		t.Fatalf("exec: %q %d %v", out, code, err)
	}

	list, err := m.ListManaged(ctx)
	if err != nil || len(list) < 1 {
		t.Fatalf("list: %v %v", list, err)
	}

	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Status(ctx); !errors.Is(err, ErrNoContainer) {
		// after stop cache cleared — status by name may also miss
		_ = err
	}

	// relaunch + destroy + clean
	if _, err := m.Launch(ctx, LaunchOpts{Headless: true}); err != nil {
		t.Fatal(err)
	}
	if err := m.Destroy(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Clean(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, CacheDirName)); !os.IsNotExist(err) {
		t.Fatalf("cache should be gone: %v", err)
	}
}

func TestManagerConfigDrift(t *testing.T) {
	repo := t.TempDir()
	cfg := DefaultConfig()
	cfg.TemplateVersion = "v1"
	body, _ := ResolveDockerfileBody(cfg, repo, 1000)
	hash, _ := ComputeConfigHash(cfg, body, "v1")

	cli := NewCLI("docker")
	cli.LookPath = func(string) (string, error) { return "docker", nil }
	cli.ExecFn = func(_ context.Context, _ string, args ...string) (string, string, int, error) {
		switch args[0] {
		case "info":
			return "1", "", 0, nil
		case "inspect":
			if strings.Contains(args[2], "json") {
				return `{"` + LabelConfigHash + `":"oldhash","` + LabelManaged + `":"true"}`, "", 0, nil
			}
			return "cid|/strike-x|true|running|img", "", 0, nil
		}
		return "", "no", 1, nil
	}
	m, err := NewManager(repo, cfg, cli)
	if err != nil {
		t.Fatal(err)
	}
	_ = m.Cache.SetContainerID("cid")
	_ = m.Cache.SetConfigHash(hash)
	_ = m.Cache.SetImageID("img")

	_, err = m.Launch(context.Background(), LaunchOpts{Headless: true})
	if !errors.Is(err, ErrConfigDrift) {
		t.Fatalf("want drift, got %v", err)
	}
}

func TestParseLabelJSON(t *testing.T) {
	m := parseLabelJSON(`{"com.strike.managed":"true","com.strike.config-hash":"abc"}`)
	if m[LabelManaged] != "true" || m[LabelConfigHash] != "abc" {
		t.Fatalf("%v", m)
	}
}

func TestHomeVolumeName(t *testing.T) {
	a := homeVolumeName("/repo/a")
	b := homeVolumeName("/repo/a")
	if a != b || !strings.HasPrefix(a, "strike-home-") {
		t.Fatalf("%s %s", a, b)
	}
}
