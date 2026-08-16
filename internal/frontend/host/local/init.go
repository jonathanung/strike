package local

import (
	"errors"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

// NewProjectInit returns a host.ProjectInit that writes AGENTS.md under workDir.
// Empty workDir yields a nil service (capability absent).
func NewProjectInit(workDir string) host.ProjectInit {
	if workDir == "" {
		return nil
	}
	return projectInit{workDir: workDir}
}

type projectInit struct {
	workDir string
}

func (p projectInit) Exists() (bool, string, error) {
	ok, err := config.AgentsMDExists(p.workDir)
	return ok, config.AgentsMDPath(p.workDir), err
}

func (p projectInit) Write(force bool) (string, bool, error) {
	path, created, err := config.WriteAgentsMD(p.workDir, force)
	if errors.Is(err, config.ErrAgentsExists) {
		return path, false, host.ErrInitExists
	}
	return path, created, err
}
