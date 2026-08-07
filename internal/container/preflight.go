package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PreflightError is a user-actionable launch failure.
type PreflightError struct {
	Code    string // machine-stable short code
	Message string
}

func (e *PreflightError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Common preflight codes.
const (
	CodeAlreadyInside   = "already_inside_container"
	CodeEngineMissing   = "engine_not_found"
	CodeEngineDown      = "engine_unavailable"
	CodeNoDockerfile    = "no_dockerfile"
	CodeDockerfileDrift = "dockerfile_drift"
	CodeRequiredEnv     = "required_env"
)

// InsideContainer reports whether this process is already running in a strike
// managed container. Prefer STRIKE_ISOLATION (set at launch) over /.dockerenv.
func InsideContainer() bool {
	if v := strings.TrimSpace(os.Getenv("STRIKE_ISOLATION")); v != "" {
		return strings.HasPrefix(strings.ToLower(v), "container")
	}
	return false
}

// PreflightOpts configures launch preflight checks.
type PreflightOpts struct {
	// RequireDockerfile when true requires Dockerfile.devcontainer or cfg.Dockerfile.
	RequireDockerfile bool
	// CheckDrift when true fails if ejected file hash mismatches (unless force rebuild path).
	CheckDrift bool
	// Version for drift hash.
	Version string
	// AllowInside skips already-inside check (tests).
	AllowInside bool
}

// Preflight validates host readiness before launching inside a container.
func Preflight(ctx context.Context, rt *CLI, cfg Config, repoDir string, opts PreflightOpts) error {
	if !opts.AllowInside && InsideContainer() {
		return &PreflightError{
			Code:    CodeAlreadyInside,
			Message: "already running inside a container (STRIKE_ISOLATION is set). Nesting is not supported",
		}
	}
	if rt == nil {
		rt = NewCLI("")
	}
	if err := rt.Available(ctx); err != nil {
		if errors.Is(err, ErrEngineNotFound) {
			return &PreflightError{
				Code:    CodeEngineMissing,
				Message: "docker/podman not found on PATH. Install Docker or Podman, or set container.engine",
			}
		}
		return &PreflightError{
			Code:    CodeEngineDown,
			Message: fmt.Sprintf("container engine daemon not reachable (%v). Start Docker Desktop / podman machine", err),
		}
	}
	if err := ValidateRequiredEnv(cfg.Auth.RequiredEnv, cfg.Auth.EnvFile, repoDir); err != nil {
		return &PreflightError{Code: CodeRequiredEnv, Message: err.Error()}
	}
	if opts.RequireDockerfile {
		path := cfg.Dockerfile
		if path == "" {
			path = DefaultEjectName
		}
		full := path
		if !filepath.IsAbs(full) {
			full = filepath.Join(repoDir, path)
		}
		if _, err := os.Stat(full); err != nil {
			return &PreflightError{
				Code:    CodeNoDockerfile,
				Message: fmt.Sprintf("%s not found. Run: strike container eject", path),
			}
		}
		if opts.CheckDrift {
			drifted, have, want, err := CheckDrift(cfg, repoDir, path, opts.Version)
			if err == nil && drifted {
				return &PreflightError{
					Code: CodeDockerfileDrift,
					Message: fmt.Sprintf("%s is stale (have %s want %s). Run: strike container eject --force",
						path, shortHash(have), shortHash(want)),
				}
			}
		}
	}
	return nil
}
