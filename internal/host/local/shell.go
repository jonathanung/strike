package local

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// shellService runs user-initiated bash (composer !) via the bash tool with
// allow-all Ask so interactive permission prompts are skipped. The tool still
// enforces the workspace destructive-path guard and the OS sandbox policy
// (dial + permission-compiled denials/network).
type shellService struct {
	mu      sync.Mutex
	workDir string
	policy  sandbox.Policy
}

// NewShell returns a host.Shell bound to workDir and the given OS sandbox policy.
// policy.WorkDir is filled from workDir when empty. Callers should pass a
// policy from permission.CompileSandbox (or at least Mode + WorkDir).
func NewShell(workDir string, policy sandbox.Policy) host.Shell {
	if strings.TrimSpace(policy.WorkDir) == "" {
		policy.WorkDir = workDir
	}
	return &shellService{workDir: workDir, policy: policy}
}

// SetShellWorkDir updates the CWD for subsequent Run calls (multi-root switch).
func SetShellWorkDir(s host.Shell, workDir string) {
	sh, ok := s.(*shellService)
	if !ok || sh == nil {
		return
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.workDir = workDir
	sh.policy.WorkDir = workDir
}

func (s *shellService) workRoot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workDir
}

func (s *shellService) sandboxPolicy() sandbox.Policy {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.policy
	p.WorkDir = s.workDir
	return p
}

func (s *shellService) Run(ctx context.Context, command string) (host.ShellResult, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return host.ShellResult{}, fmt.Errorf("command is empty")
	}
	workDir := s.workRoot()
	args, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		return host.ShellResult{Command: command}, err
	}
	pol := s.sandboxPolicy()
	tc := &tool.Context{
		WorkDir:     workDir,
		SandboxMode: pol.Mode.String(),
		Sandbox:     pol,
		Ask:         func(context.Context, tool.AskRequest) error { return nil },
	}
	res, err := tool.NewBash().Execute(ctx, args, tc)
	out := host.ShellResult{Command: command, Output: res.Output}
	if res.Metadata != nil {
		var meta struct {
			ExitCode int `json:"exitCode"`
		}
		if json.Unmarshal(res.Metadata, &meta) == nil {
			out.ExitCode = meta.ExitCode
		}
	}
	if err != nil {
		if out.Output == "" {
			out.Output = err.Error()
		}
		return out, err
	}
	return out, nil
}
