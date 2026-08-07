package container

import (
	"errors"
	"fmt"
)

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

	// ErrConfigDrift is returned when a running container's labels do not match
	// the current config/image hash (refuse silent attach into stale env).
	ErrConfigDrift = errors.New("running container does not match current config")

	// ErrLaunchCancelled is returned when the user cancels a stale-container prompt.
	ErrLaunchCancelled = errors.New("container launch cancelled")
)

// StaleContainerError is returned when a live container exists for the repo but
// its config/image labels do not match the current recipe (E12.6).
// Callers should offer: attach anyway / rebuild / cancel (CLI prompt or question tool).
type StaleContainerError struct {
	// Reason is a human-readable compatibility failure (hash/image mismatch).
	Reason string
	// ContainerID is the running container id.
	ContainerID string
	// Name is the deterministic container name (strike-<repo>-<hash>).
	Name string
	// WantHash is the current config hash (may be empty if compute failed).
	WantHash string
	// HaveHash is the label on the running container (may be empty).
	HaveHash string
}

func (e *StaleContainerError) Error() string {
	if e == nil {
		return ErrConfigDrift.Error()
	}
	base := fmt.Sprintf("%s: %s", ErrConfigDrift.Error(), e.Reason)
	if e.Name != "" {
		base += fmt.Sprintf(" (container %s)", e.Name)
	}
	return base + " — choose attach anyway, rebuild, or cancel"
}

func (e *StaleContainerError) Unwrap() error { return ErrConfigDrift }

// QuestionOptions returns the three stable choices for a question-tool / CLI prompt.
func (e *StaleContainerError) QuestionOptions() []string {
	return []string{"attach", "rebuild", "cancel"}
}
