package container

import "errors"

// Sentinel errors returned by Runtime operations.
var (
	// ErrEngineNotFound is returned when neither docker nor podman is on PATH
	// (or the configured binary cannot be resolved).
	ErrEngineNotFound = errors.New("container engine not found on PATH (install docker or podman)")

	// ErrEngineUnavailable is returned when the CLI is present but the daemon
	// (or podman machine) is not reachable.
	ErrEngineUnavailable = errors.New("container engine daemon is not available")

	// ErrNoContainer is returned when an operation requires a container that
	// does not exist for the given name or id.
	ErrNoContainer = errors.New("no container found")

	// ErrEmptyID is returned when the engine creates a container but prints no id.
	ErrEmptyID = errors.New("container engine returned an empty container id")
)
