package container

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInsideContainerEnv(t *testing.T) {
	t.Setenv("STRIKE_ISOLATION", "")
	if InsideContainer() {
		t.Fatal("expected false")
	}
	t.Setenv("STRIKE_ISOLATION", "container")
	if !InsideContainer() {
		t.Fatal("expected true")
	}
	t.Setenv("STRIKE_ISOLATION", "container+no-network")
	if !InsideContainer() {
		t.Fatal("expected true for prefix")
	}
}

func TestPreflightTable(t *testing.T) {
	repo := t.TempDir()
	ctx := context.Background()

	t.Run("already_inside", func(t *testing.T) {
		t.Setenv("STRIKE_ISOLATION", "container")
		err := Preflight(ctx, NewCLI("docker"), DefaultConfig(), repo, PreflightOpts{})
		var pe *PreflightError
		if !errors.As(err, &pe) || pe.Code != CodeAlreadyInside {
			t.Fatalf("%v", err)
		}
	})

	t.Run("engine_missing", func(t *testing.T) {
		t.Setenv("STRIKE_ISOLATION", "")
		cli := NewCLI("docker")
		cli.LookPath = func(string) (string, error) { return "", errors.New("no") }
		err := Preflight(ctx, cli, DefaultConfig(), repo, PreflightOpts{AllowInside: true})
		var pe *PreflightError
		if !errors.As(err, &pe) || pe.Code != CodeEngineMissing {
			t.Fatalf("%v", err)
		}
	})

	t.Run("engine_down", func(t *testing.T) {
		cli := NewCLI("docker")
		cli.LookPath = func(string) (string, error) { return "docker", nil }
		cli.ExecFn = func(context.Context, string, ...string) (string, string, int, error) {
			return "", "cannot connect", 1, nil
		}
		err := Preflight(ctx, cli, DefaultConfig(), repo, PreflightOpts{AllowInside: true})
		var pe *PreflightError
		if !errors.As(err, &pe) || pe.Code != CodeEngineDown {
			t.Fatalf("%v", err)
		}
	})

	t.Run("no_dockerfile", func(t *testing.T) {
		cli := NewCLI("docker")
		cli.LookPath = func(string) (string, error) { return "docker", nil }
		cli.ExecFn = func(_ context.Context, _ string, args ...string) (string, string, int, error) {
			if len(args) > 0 && args[0] == "info" {
				return "1", "", 0, nil
			}
			return "", "", 1, nil
		}
		err := Preflight(ctx, cli, DefaultConfig(), repo, PreflightOpts{
			AllowInside:       true,
			RequireDockerfile: true,
		})
		var pe *PreflightError
		if !errors.As(err, &pe) || pe.Code != CodeNoDockerfile {
			t.Fatalf("%v", err)
		}
	})

	t.Run("dockerfile_ok", func(t *testing.T) {
		cli := NewCLI("docker")
		cli.LookPath = func(string) (string, error) { return "docker", nil }
		cli.ExecFn = func(_ context.Context, _ string, args ...string) (string, string, int, error) {
			if len(args) > 0 && args[0] == "info" {
				return "1", "", 0, nil
			}
			return "", "", 0, nil
		}
		path := filepath.Join(repo, DefaultEjectName)
		if err := os.WriteFile(path, []byte("# syntax=docker/dockerfile:1\nFROM x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// eject properly for drift check
		cfg := DefaultConfig()
		if _, err := Eject(cfg, repo, EjectOpts{Version: "v1", Force: true}); err != nil {
			t.Fatal(err)
		}
		err := Preflight(ctx, cli, cfg, repo, PreflightOpts{
			AllowInside:       true,
			RequireDockerfile: true,
			CheckDrift:        true,
			Version:           "v1",
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
