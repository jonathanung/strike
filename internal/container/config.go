package container

// Config is the in-process container configuration used by Manager.
// E12.2 (#584) will load this from layered JSON; until then callers construct it.
type Config struct {
	// BaseImage is the FROM image (default ubuntu:24.04).
	BaseImage string
	// AptPackages are extra apt packages installed at build time.
	AptPackages []string
	// Shell is the login shell inside the container (default /bin/bash).
	Shell string
	// Resources are cgroup-style limits applied at create.
	Resources Resources
	// Workspace controls bind mounts and ports.
	Workspace Workspace
	// Auth controls credential forwarding (env patterns, env file, SSH agent).
	Auth Auth
	// Network is a coarse mode string: "default" | "none" (iptables filters later).
	Network Network
	// Dockerfile is an optional host path to a hand-written Dockerfile.
	// When set, Build/Eject uses it instead of the embedded template.
	Dockerfile string
	// TemplateVersion is included in the build cache hash (strike version).
	TemplateVersion string

	// Optional project dependency branches (set by /devcontainer skill or config).
	NeedsNode     bool
	NodeVersion   string
	NeedsPython   bool
	PythonVersion string
	NpmPackages   []string
	PipPackages   []string
}

// Resources holds optional create-time limits (CLI flags).
type Resources struct {
	// Memory e.g. "512m", "2g"; empty = unlimited.
	Memory string
	// CPUs e.g. "0.5", "2"; empty = unlimited.
	CPUs string
	// PidsLimit; 0 uses default (512).
	PidsLimit int64
	// GPUs: "", "none", "all", "2", "device=0,1".
	GPUs string
}

// Workspace controls mounts and published ports.
type Workspace struct {
	// MountPath inside the container (default /workspace).
	MountPath string
	// HostPath overrides the host side of the workspace bind (default repo dir).
	HostPath string
	// Ports are "host:container" TCP mappings.
	Ports []string
	// PersistHome mounts a named volume at /home/strike when true (default true).
	PersistHome *bool
	// ExtraBinds are additional "host:container[:opts]" mounts.
	ExtraBinds []string
}

// Auth controls how credentials reach the container (never baked into images).
type Auth struct {
	// ForwardEnv are filepath.Match patterns against host env keys.
	ForwardEnv []string
	// EnvFile is a Docker-style KEY=VALUE file (relative to repo or absolute).
	EnvFile string
	// RequiredEnv must be present on host or in EnvFile before launch.
	RequiredEnv []string
	// ForwardSSHAgent bind-mounts SSH_AUTH_SOCK when true (Linux only).
	ForwardSSHAgent *bool
}

// Network is coarse container networking posture.
type Network struct {
	// Mode: "default" (bridge), "none" (no network), or empty (=default).
	Mode string
	// HTTPProxy / HTTPSProxy / NoProxy override host proxy env when set.
	HTTPProxy  string
	HTTPSProxy string
	NoProxy    string
}

// DefaultConfig returns sensible defaults for a strike dev container.
func DefaultConfig() Config {
	persist := true
	ssh := false
	return Config{
		BaseImage: "ubuntu:24.04",
		Shell:     "/bin/bash",
		Resources: Resources{PidsLimit: 512},
		Workspace: Workspace{
			MountPath:   "/workspace",
			PersistHome: &persist,
		},
		Auth: Auth{
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
		Network: Network{Mode: "default"},
	}
}

func (c Config) mountPath() string {
	if c.Workspace.MountPath != "" {
		return c.Workspace.MountPath
	}
	return "/workspace"
}

func (c Config) baseImage() string {
	if c.BaseImage != "" {
		return c.BaseImage
	}
	return "ubuntu:24.04"
}

func (c Config) shell() string {
	if c.Shell != "" {
		return c.Shell
	}
	return "/bin/bash"
}

func (c Config) persistHome() bool {
	if c.Workspace.PersistHome == nil {
		return true
	}
	return *c.Workspace.PersistHome
}

func (c Config) forwardSSH() bool {
	return c.Auth.ForwardSSHAgent != nil && *c.Auth.ForwardSSHAgent
}
