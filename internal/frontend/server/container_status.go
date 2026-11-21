package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jonathanung/strike/internal/integrate/container"
	"github.com/jonathanung/strike/internal/product/config"
	"github.com/jonathanung/strike/internal/product/project"
	"github.com/jonathanung/strike/internal/product/version"
	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/trust/workspacetrust"
)

// containerStatusTimeout bounds the engine probes behind one GET.
const containerStatusTimeout = 15 * time.Second

// containerSessionModel matches `strike container status`.
const containerSessionModel = "one container per repo (N sessions attach)"

// containerRung is one step of the E12.7 isolation ladder. Labels state what
// a posture is; host rungs are first-class modes, never errors (#1290).
type containerRung struct {
	Label    string `json:"label"`
	Describe string `json:"describe"`
	Active   bool   `json:"active"`
}

// containerRuntimeInfo is the engine probe (`docker info`).
type containerRuntimeInfo struct {
	// Engine is the configured or resolved CLI ("docker", "podman", path).
	Engine string `json:"engine"`
	// Found is true when the engine binary resolves on PATH.
	Found bool `json:"found"`
	// Available is true when the engine daemon answered.
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

// containerInfo mirrors `strike container status --json` for the workspace.
type containerInfo struct {
	Name         string `json:"name"`
	ID           string `json:"id,omitempty"`
	Running      bool   `json:"running"`
	Status       string `json:"status"`
	Image        string `json:"image,omitempty"`
	Compatible   *bool  `json:"compatible,omitempty"`
	Reason       string `json:"reason,omitempty"`
	HaveHash     string `json:"haveHash,omitempty"`
	WantHash     string `json:"wantHash,omitempty"`
	SessionModel string `json:"sessionModel"`
}

// containerDrift mirrors `strike container drift` for the default ejected
// Dockerfile: matches | drifted | not-ejected | error.
type containerDrift struct {
	State string `json:"state"`
	Path  string `json:"path"`
	Have  string `json:"have,omitempty"`
	Want  string `json:"want,omitempty"`
	Error string `json:"error,omitempty"`
}

// containerStatusResponse is GET /v1/container/status (#1584). Read-only.
type containerStatusResponse struct {
	// Posture is the ladder label for the agent process; PostureSource says
	// whether it came from STRIKE_ISOLATION ("env") or the host dials.
	Posture        string          `json:"posture"`
	PostureSource  string          `json:"postureSource"`
	Describe       string          `json:"describe"`
	Ladder         []containerRung `json:"ladder"`
	PermissionMode string          `json:"permissionMode,omitempty"`
	SandboxMode    string          `json:"sandboxMode,omitempty"`
	// Workspace is the directory the container status is computed for.
	Workspace string `json:"workspace"`
	// ConfigScope is "project" when the workspace's container config was
	// read, "global" when the workspace is untrusted (#1352) and only global
	// + builtin container config applies.
	ConfigScope string                 `json:"configScope"`
	Runtime     containerRuntimeInfo   `json:"runtime"`
	Container   containerInfo          `json:"container"`
	Drift       containerDrift         `json:"drift"`
	Detect      container.DetectedDeps `json:"detect"`
	// Errors are non-fatal problems (config parse, inspect failure).
	Errors []string `json:"errors,omitempty"`
}

// containerLadder is the ordered E12.7 ladder.
var containerLadder = []string{
	protocol.IsolationHostYolo,
	protocol.IsolationHostDefault,
	protocol.IsolationHostSandbox,
	protocol.IsolationContainer,
	protocol.IsolationContainerNoNet,
}

// containerCapable reports whether GET /v1/container/status can answer:
// it needs a workspace directory (a live root or the daemon WorkDir). The
// route is read-only, so attach-only and --read-only daemons serve it too.
func (s *Server) containerCapable() bool {
	return s.hasLive() || strings.TrimSpace(s.opts.WorkDir) != ""
}

// containerRuntime returns the engine CLI for binary (test seam).
func (s *Server) containerRuntime(binary string) *container.CLI {
	if s.opts.ContainerRuntime != nil {
		return s.opts.ContainerRuntime(binary)
	}
	return container.NewCLI(binary)
}

// containerWorkspace picks the directory like the CLI's cwd: the request's
// live root CWD, else the daemon WorkDir.
func (s *Server) containerWorkspace(r *http.Request) string {
	if cwd := strings.TrimSpace(s.currentCWD(r)); cwd != "" {
		return cwd
	}
	return strings.TrimSpace(s.opts.WorkDir)
}

// containerConfigTrusted reports whether workDir's own container config may
// be read: only for a trusted workspace (#1352), because container config
// names the engine binary this route executes.
func (s *Server) containerConfigTrusted(ctx context.Context, workDir string) bool {
	path := workspacetrust.DefaultPath(config.GlobalRoot())
	if path == "" {
		return false
	}
	store, err := workspacetrust.Open(path)
	if err != nil {
		return false
	}
	id, err := project.Resolve(ctx, workDir)
	if err != nil {
		return false
	}
	return store.Trusted(id.Root)
}

// containerPosture computes the ladder label like the TUI /container:
// STRIKE_ISOLATION wins, else the live session's permission + sandbox dials.
func (s *Server) containerPosture(r *http.Request) (posture, source, perm, sandboxMode string) {
	var st *StatusSnapshot
	if s.opts.LiveHub != nil {
		if live := s.opts.LiveHub.LiveFor(rootParam(r)); live != nil {
			v := live.Status()
			st = &v
		}
	} else if s.opts.Live != nil {
		v := s.opts.Live.Status()
		st = &v
	}
	if st != nil {
		perm = strings.TrimSpace(st.PermissionMode)
		sandboxMode = strings.TrimSpace(st.Sandbox)
	}
	if sandboxMode == "" && s.opts.Sandbox != nil {
		sandboxMode = strings.TrimSpace(s.opts.Sandbox.Mode)
	}
	if p, ok := protocol.ParseIsolationEnv(os.Getenv(protocol.IsolationEnvKey)); ok {
		return p, "env", perm, sandboxMode
	}
	pm := protocol.PermissionModeDefault
	if parsed, ok := protocol.ParsePermissionMode(perm); ok {
		pm = parsed
	}
	return protocol.ComputeIsolation(false, false, pm, sandboxMode), "dials", perm, sandboxMode
}

func (s *Server) handleContainerStatus(w http.ResponseWriter, r *http.Request) {
	if !s.containerCapable() {
		capabilityUnavailable(w, "container")
		return
	}
	if id := rootParam(r); id != "" && s.opts.LiveHub != nil && s.opts.LiveHub.LiveFor(id) == nil {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "root not found"})
		return
	}
	workDir := s.containerWorkspace(r)
	if workDir == "" {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "no workspace directory is known for this root"})
		return
	}
	if info, err := os.Stat(workDir); err != nil || !info.IsDir() {
		writeJSON(w, http.StatusConflict, opErrorResponse{Error: "workspace directory is not available"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), containerStatusTimeout)
	defer cancel()

	out := containerStatusResponse{Workspace: workDir, ConfigScope: "global"}
	out.Posture, out.PostureSource, out.PermissionMode, out.SandboxMode = s.containerPosture(r)
	out.Describe = protocol.IsolationDescribe(out.Posture)
	for _, label := range containerLadder {
		out.Ladder = append(out.Ladder, containerRung{Label: label, Describe: protocol.IsolationDescribe(label), Active: label == out.Posture})
	}
	out.Detect = container.DetectProjectDeps(workDir)

	scope := ""
	if s.containerConfigTrusted(ctx, workDir) {
		scope = workDir
		out.ConfigScope = "project"
	}
	cfg, err := config.Load(scope)
	if err != nil {
		out.Errors = append(out.Errors, "config: "+err.Error())
	}
	rtCfg := cfg.Container.ToRuntime(version.Version)

	out.Drift = containerDriftState(rtCfg, workDir)

	cli := s.containerRuntime(cfg.Container.Engine)
	out.Runtime.Engine = cli.Engine()
	out.Container = containerInfo{Name: container.ContainerName(workDir), Status: "absent", SessionModel: containerSessionModel}
	if bin, err := cli.Resolve(); err != nil {
		out.Runtime.Error = err.Error()
	} else {
		out.Runtime.Found = true
		out.Runtime.Engine = bin
		if err := cli.Available(ctx); err != nil {
			out.Runtime.Error = err.Error()
		} else {
			out.Runtime.Available = true
		}
	}
	if out.Runtime.Available {
		mgr, err := container.NewManager(workDir, rtCfg, cli)
		if err != nil {
			out.Errors = append(out.Errors, "container: "+err.Error())
		} else {
			mgr.Stderr = nil
			// The container id cache lives inside the repo
			// (.strike/container), so an untrusted workspace could point it
			// at another container. Untrusted: inspect only the
			// deterministic per-repo name.
			var st *container.InspectState
			if out.ConfigScope == "project" {
				st, err = mgr.Status(ctx)
			} else {
				st, err = cli.InspectContainer(ctx, container.ContainerName(workDir))
			}
			switch {
			case errors.Is(err, container.ErrNoContainer):
				// absent
			case err != nil:
				out.Container.Status = "unknown"
				out.Errors = append(out.Errors, "inspect: "+err.Error())
			case st != nil:
				out.Container.ID = st.ID
				out.Container.Running = st.Running
				out.Container.Status = st.Status
				out.Container.Image = st.Image
				if st.Name != "" {
					out.Container.Name = st.Name
				}
				ok, reason, have, want := mgr.Compatibility(st)
				out.Container.Compatible = &ok
				out.Container.HaveHash, out.Container.WantHash = have, want
				if !ok {
					out.Container.Reason = reason
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// containerDriftState runs the same check as `strike container drift` on
// the default ejected Dockerfile.
func containerDriftState(cfg container.Config, workDir string) containerDrift {
	d := containerDrift{Path: container.DefaultEjectName}
	drifted, have, want, err := container.CheckDrift(cfg, workDir, container.DefaultEjectName, version.Version)
	switch {
	case errors.Is(err, os.ErrNotExist):
		d.State = "not-ejected"
	case err != nil:
		d.State = "error"
		d.Error = err.Error()
	case drifted:
		d.State, d.Have, d.Want = "drifted", have, want
	default:
		d.State, d.Have, d.Want = "matches", have, want
	}
	return d
}
