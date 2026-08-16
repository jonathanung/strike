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

// ExecTerminationError adds container state to an unexpected interactive exec
// exit while preserving the underlying *exec.ExitError for its exit status.
type ExecTerminationError struct {
	Err               error
	ExecExitCode      int
	ContainerRunning  bool
	ContainerStatus   string
	ContainerExitCode int
	OOMKilled         bool
	ContainerError    string
	InspectErr        error
}

func (e *ExecTerminationError) Error() string {
	if e == nil {
		return "container exec terminated"
	}
	if e.OOMKilled {
		return fmt.Sprintf("container exec terminated: container was OOM-killed (exec exit %d, container exit %d)", e.ExecExitCode, e.ContainerExitCode)
	}
	if e.InspectErr != nil {
		if e.ExecExitCode == 137 || e.ExecExitCode < 0 {
			return fmt.Sprintf("container exec was killed (exit %d); container inspection failed: %v", e.ExecExitCode, e.InspectErr)
		}
		return fmt.Sprintf("container exec exited with status %d; container inspection failed: %v", e.ExecExitCode, e.InspectErr)
	}
	if e.ContainerStatus != "" && !e.ContainerRunning {
		msg := fmt.Sprintf("container exec terminated because the container exited (status %s, exec exit %d, container exit %d)", e.ContainerStatus, e.ExecExitCode, e.ContainerExitCode)
		if e.ContainerError != "" {
			msg += ": " + e.ContainerError
		}
		return msg
	}
	if e.ExecExitCode == 137 || e.ExecExitCode < 0 {
		return fmt.Sprintf("container exec was killed (exit %d) while the container remained running; check container memory/PID limits and runtime events", e.ExecExitCode)
	}
	return fmt.Sprintf("container exec exited with status %d while the container remained running", e.ExecExitCode)
}

func (e *ExecTerminationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
