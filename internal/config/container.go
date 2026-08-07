package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/container"
)

// ContainerConfig is the JSON "container" object (and container.jsonc body).
// Maps onto internal/container.Config for Manager (E12.1+).
//
// Merge order: defaults → global config/container.jsonc → project → managed.
// Nested objects overlay field-by-field; slices replace when the layer sets them
// (including explicit empty arrays).
type ContainerConfig struct {
	// BaseImage is the Dockerfile FROM image (default ubuntu:24.04).
	BaseImage string `json:"baseImage,omitempty"`
	// Packages are extra apt packages at build time.
	Packages []string `json:"packages,omitempty"`
	// Shell is the login shell inside the container (default /bin/bash).
	Shell string `json:"shell,omitempty"`
	// Resources are cgroup-style create limits.
	Resources ContainerResources `json:"resources,omitempty"`
	// Workspace controls mounts and published ports.
	Workspace ContainerWorkspace `json:"workspace,omitempty"`
	// Auth controls credential forwarding (never baked into images).
	Auth ContainerAuth `json:"auth,omitempty"`
	// Network is container network posture (default|none) plus optional proxy.
	// Host allowlists for app-layer tools stay on top-level network.allow (#527).
	Network ContainerNetwork `json:"network,omitempty"`
	// Dockerfile is an optional path to a hand-written Dockerfile (relative to repo or absolute).
	Dockerfile string `json:"dockerfile,omitempty"`
	// Execution selects where the agent runs: "local" (default) or "container".
	// Wired by E12.4 (--launch-inside-container); stored here for layered config.
	Execution string `json:"execution,omitempty"`
	// Engine overrides the CLI binary ("docker", "podman", or absolute path).
	Engine string `json:"engine,omitempty"`

	// Language toolchain branches for the embedded Dockerfile template (/devcontainer).
	NeedsNode     *bool  `json:"needsNode,omitempty"`
	NodeVersion   string `json:"nodeVersion,omitempty"`
	NeedsPython   *bool  `json:"needsPython,omitempty"`
	PythonVersion string `json:"pythonVersion,omitempty"`
	NeedsGo       *bool  `json:"needsGo,omitempty"`
	GoVersion     string `json:"goVersion,omitempty"`
	NeedsRust     *bool  `json:"needsRust,omitempty"`
}

// ContainerResources is JSON "container.resources".
type ContainerResources struct {
	Memory    string `json:"memory,omitempty"`
	CPUs      string `json:"cpus,omitempty"`
	PidsLimit int64  `json:"pidsLimit,omitempty"`
	GPUs      string `json:"gpus,omitempty"`
}

// ContainerWorkspace is JSON "container.workspace".
type ContainerWorkspace struct {
	MountPath   string   `json:"mountPath,omitempty"`
	HostPath    string   `json:"hostPath,omitempty"`
	Ports       []string `json:"ports,omitempty"`
	PersistHome *bool    `json:"persistHome,omitempty"`
	ExtraBinds  []string `json:"extraBinds,omitempty"`
}

// ContainerAuth is JSON "container.auth".
type ContainerAuth struct {
	ForwardEnv      []string `json:"forwardEnv,omitempty"`
	EnvFile         string   `json:"envFile,omitempty"`
	RequiredEnv     []string `json:"requiredEnv,omitempty"`
	ForwardSSHAgent *bool    `json:"forwardSSHAgent,omitempty"`
}

// ContainerNetwork is JSON "container.network".
// Mode "default" uses a dedicated bridge; "none" disables networking.
// Allow is reserved for future container egress filters (same shape as network.allow).
type ContainerNetwork struct {
	Mode       string   `json:"mode,omitempty"`
	Allow      []string `json:"allow,omitempty"`
	HTTPProxy  string   `json:"httpProxy,omitempty"`
	HTTPSProxy string   `json:"httpsProxy,omitempty"`
	NoProxy    string   `json:"noProxy,omitempty"`
}

// DefaultContainer returns product defaults for the container block.
func DefaultContainer() ContainerConfig {
	persist := true
	ssh := false
	return ContainerConfig{
		BaseImage: "ubuntu:24.04",
		Shell:     "/bin/bash",
		Resources: ContainerResources{PidsLimit: 512},
		Workspace: ContainerWorkspace{
			MountPath:   "/workspace",
			PersistHome: &persist,
		},
		Auth: ContainerAuth{
			ForwardSSHAgent: &ssh,
			ForwardEnv: []string{
				"ANTHROPIC_API_KEY",
				"OPENAI_API_KEY",
				"OPENAI_API_KEY_*",
				"XAI_API_KEY",
				"GOOGLE_API_KEY",
				"STRIKE_*",
			},
		},
		Network:   ContainerNetwork{Mode: "default"},
		Execution: "local",
	}
}

// mergeContainer overlays layer onto base (field-wise; slices replace when set).
func mergeContainer(base, layer ContainerConfig) ContainerConfig {
	if layer.BaseImage != "" {
		base.BaseImage = layer.BaseImage
	}
	if layer.Packages != nil {
		base.Packages = append([]string(nil), layer.Packages...)
	}
	if layer.Shell != "" {
		base.Shell = layer.Shell
	}
	if layer.Dockerfile != "" {
		base.Dockerfile = layer.Dockerfile
	}
	if layer.Execution != "" {
		base.Execution = layer.Execution
	}
	if layer.Engine != "" {
		base.Engine = layer.Engine
	}
	if layer.NeedsNode != nil {
		v := *layer.NeedsNode
		base.NeedsNode = &v
	}
	if layer.NodeVersion != "" {
		base.NodeVersion = layer.NodeVersion
	}
	if layer.NeedsPython != nil {
		v := *layer.NeedsPython
		base.NeedsPython = &v
	}
	if layer.PythonVersion != "" {
		base.PythonVersion = layer.PythonVersion
	}
	if layer.NeedsGo != nil {
		v := *layer.NeedsGo
		base.NeedsGo = &v
	}
	if layer.GoVersion != "" {
		base.GoVersion = layer.GoVersion
	}
	if layer.NeedsRust != nil {
		v := *layer.NeedsRust
		base.NeedsRust = &v
	}
	base.Resources = mergeContainerResources(base.Resources, layer.Resources)
	base.Workspace = mergeContainerWorkspace(base.Workspace, layer.Workspace)
	base.Auth = mergeContainerAuth(base.Auth, layer.Auth)
	base.Network = mergeContainerNetwork(base.Network, layer.Network)
	return base
}

func mergeContainerResources(base, layer ContainerResources) ContainerResources {
	if layer.Memory != "" {
		base.Memory = layer.Memory
	}
	if layer.CPUs != "" {
		base.CPUs = layer.CPUs
	}
	if layer.PidsLimit != 0 {
		base.PidsLimit = layer.PidsLimit
	}
	if layer.GPUs != "" {
		base.GPUs = layer.GPUs
	}
	return base
}

func mergeContainerWorkspace(base, layer ContainerWorkspace) ContainerWorkspace {
	if layer.MountPath != "" {
		base.MountPath = layer.MountPath
	}
	if layer.HostPath != "" {
		base.HostPath = layer.HostPath
	}
	if layer.Ports != nil {
		base.Ports = append([]string(nil), layer.Ports...)
	}
	if layer.PersistHome != nil {
		v := *layer.PersistHome
		base.PersistHome = &v
	}
	if layer.ExtraBinds != nil {
		base.ExtraBinds = append([]string(nil), layer.ExtraBinds...)
	}
	return base
}

func mergeContainerAuth(base, layer ContainerAuth) ContainerAuth {
	if layer.ForwardEnv != nil {
		base.ForwardEnv = append([]string(nil), layer.ForwardEnv...)
	}
	if layer.EnvFile != "" {
		base.EnvFile = layer.EnvFile
	}
	if layer.RequiredEnv != nil {
		base.RequiredEnv = append([]string(nil), layer.RequiredEnv...)
	}
	if layer.ForwardSSHAgent != nil {
		v := *layer.ForwardSSHAgent
		base.ForwardSSHAgent = &v
	}
	return base
}

func mergeContainerNetwork(base, layer ContainerNetwork) ContainerNetwork {
	if layer.Mode != "" {
		base.Mode = layer.Mode
	}
	if layer.Allow != nil {
		base.Allow = append([]string(nil), layer.Allow...)
	}
	if layer.HTTPProxy != "" {
		base.HTTPProxy = layer.HTTPProxy
	}
	if layer.HTTPSProxy != "" {
		base.HTTPSProxy = layer.HTTPSProxy
	}
	if layer.NoProxy != "" {
		base.NoProxy = layer.NoProxy
	}
	return base
}

// ToRuntime maps layered JSON config onto internal/container.Config.
func (c ContainerConfig) ToRuntime(version string) container.Config {
	out := container.DefaultConfig()
	if c.BaseImage != "" {
		out.BaseImage = c.BaseImage
	}
	if c.Packages != nil {
		out.AptPackages = append([]string(nil), c.Packages...)
	}
	if c.Shell != "" {
		out.Shell = c.Shell
	}
	out.Resources = container.Resources{
		Memory:    c.Resources.Memory,
		CPUs:      c.Resources.CPUs,
		PidsLimit: c.Resources.PidsLimit,
		GPUs:      c.Resources.GPUs,
	}
	out.Workspace = container.Workspace{
		MountPath:   c.Workspace.MountPath,
		HostPath:    c.Workspace.HostPath,
		Ports:       append([]string(nil), c.Workspace.Ports...),
		PersistHome: c.Workspace.PersistHome,
		ExtraBinds:  append([]string(nil), c.Workspace.ExtraBinds...),
	}
	out.Auth = container.Auth{
		ForwardEnv:      append([]string(nil), c.Auth.ForwardEnv...),
		EnvFile:         c.Auth.EnvFile,
		RequiredEnv:     append([]string(nil), c.Auth.RequiredEnv...),
		ForwardSSHAgent: c.Auth.ForwardSSHAgent,
	}
	out.Network = container.Network{
		Mode:       c.Network.Mode,
		HTTPProxy:  c.Network.HTTPProxy,
		HTTPSProxy: c.Network.HTTPSProxy,
		NoProxy:    c.Network.NoProxy,
	}
	out.Dockerfile = c.Dockerfile
	out.TemplateVersion = version
	if c.NeedsNode != nil {
		out.NeedsNode = *c.NeedsNode
	}
	out.NodeVersion = c.NodeVersion
	if c.NeedsPython != nil {
		out.NeedsPython = *c.NeedsPython
	}
	out.PythonVersion = c.PythonVersion
	if c.NeedsGo != nil {
		out.NeedsGo = *c.NeedsGo
	}
	out.GoVersion = c.GoVersion
	if c.NeedsRust != nil {
		out.NeedsRust = *c.NeedsRust
	}
	return out
}

// NormalizeContainer validates and canonicalizes execution/network mode tokens.
func NormalizeContainer(c ContainerConfig) (ContainerConfig, error) {
	if c.Execution != "" {
		switch strings.ToLower(strings.TrimSpace(c.Execution)) {
		case "local", "host":
			c.Execution = "local"
		case "container", "docker", "podman":
			c.Execution = "container"
		default:
			return c, fmt.Errorf("container.execution %q: want local|container", c.Execution)
		}
	}
	if c.Network.Mode != "" {
		switch strings.ToLower(strings.TrimSpace(c.Network.Mode)) {
		case "default", "bridge":
			c.Network.Mode = "default"
		case "none", "off":
			c.Network.Mode = "none"
		default:
			return c, fmt.Errorf("container.network.mode %q: want default|none", c.Network.Mode)
		}
	}
	return c, nil
}

// --- dedicated container.jsonc/json (like mcp.jsonc) ---

// GlobalContainerFilePath prefers container.jsonc then container.json under ~/.strike.
func GlobalContainerFilePath() string {
	root := GlobalRoot()
	if root == "" {
		return ""
	}
	return firstExisting(
		filepath.Join(root, "container.jsonc"),
		filepath.Join(root, "container.json"),
	)
}

// ProjectContainerFilePath prefers container.jsonc then container.json under workDir/.strike.
func ProjectContainerFilePath(workDir string) string {
	if workDir == "" {
		return ""
	}
	root := projectRoot(workDir)
	return firstExisting(
		filepath.Join(root, "container.jsonc"),
		filepath.Join(root, "container.json"),
	)
}

func containerFileCandidates(dir string) []string {
	if dir == "" {
		return nil
	}
	return []string{
		filepath.Join(dir, "container.jsonc"),
		filepath.Join(dir, "container.json"),
	}
}

func loadContainerFileLayer(dir string) (ContainerConfig, bool, error) {
	for _, path := range containerFileCandidates(dir) {
		cc, err := ReadContainerFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return ContainerConfig{}, false, fmt.Errorf("%s: %w", path, err)
		}
		return cc, true, nil
	}
	return ContainerConfig{}, false, nil
}

// ReadContainerFile parses container.jsonc/json from path.
func ReadContainerFile(path string) (ContainerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ContainerConfig{}, err
	}
	return ParseContainerFile(data)
}

// ParseContainerFile decodes a container config object (same shape as "container" in main config).
func ParseContainerFile(data []byte) (ContainerConfig, error) {
	stripped, err := stripJSONC(data)
	if err != nil {
		return ContainerConfig{}, err
	}
	stripped = bytesTrimSpace(stripped)
	if len(stripped) == 0 {
		return ContainerConfig{}, nil
	}
	if stripped[0] != '{' {
		return ContainerConfig{}, fmt.Errorf("container file must be a JSON object")
	}
	var cc ContainerConfig
	if err := json.Unmarshal(stripped, &cc); err != nil {
		return ContainerConfig{}, err
	}
	return NormalizeContainer(cc)
}
