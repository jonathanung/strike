package container

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Manager orchestrates per-repo container lifecycle on top of Runtime/CLI.
// Harness abstraction from Zone is stripped — strike is the only guest agent.
type Manager struct {
	RT      *CLI
	Cfg     Config
	Cache   *Cache
	RepoDir string
	// Stderr receives non-fatal warnings (env pattern misses, SSH skip).
	Stderr io.Writer
	// AttachFn runs interactive attach; tests replace it. Default: docker exec -it.
	AttachFn func(ctx context.Context, engine, containerID string, cmd []string, asRoot bool) error
}

// NewManager constructs a Manager for repoDir. rt may be nil (NewCLI("")).
func NewManager(repoDir string, cfg Config, rt *CLI) (*Manager, error) {
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, fmt.Errorf("container: repo path: %w", err)
	}
	if rt == nil {
		rt = NewCLI("")
	}
	m := &Manager{
		RT:      rt,
		Cfg:     cfg,
		Cache:   NewCache(abs),
		RepoDir: abs,
		Stderr:  os.Stderr,
	}
	m.AttachFn = defaultAttach
	return m, nil
}

func (m *Manager) warnf(format string, args ...any) {
	w := m.Stderr
	if w == nil {
		w = io.Discard
	}
	fmt.Fprintf(w, format, args...)
}

// dockerfileAndHash resolves Dockerfile body and cache hash.
// Prefers cfg.Dockerfile, then ejected Dockerfile.devcontainer, else template.
func (m *Manager) dockerfileAndHash() (body, hash string, err error) {
	ver := m.Cfg.TemplateVersion
	if ver == "" {
		ver = "dev"
	}
	cfg := m.Cfg
	if cfg.Dockerfile == "" {
		// Prefer committed eject artifact when present.
		cand := filepath.Join(m.RepoDir, DefaultEjectName)
		if _, statErr := os.Stat(cand); statErr == nil {
			cfg.Dockerfile = DefaultEjectName
		}
	}
	return RenderDockerfile(cfg, m.RepoDir, ver)
}

// NeedsBuild reports whether an image build is required.
func (m *Manager) NeedsBuild(ctx context.Context, force bool) bool {
	if force {
		return true
	}
	_, hash, err := m.dockerfileAndHash()
	if err != nil {
		return true
	}
	cached, err := m.Cache.ConfigHash()
	if err != nil || cached == "" || cached != hash {
		return true
	}
	img, err := m.Cache.ImageID()
	if err != nil || img == "" {
		return true
	}
	return !m.RT.ImageExists(ctx, img)
}

// Build builds (or rebuilds) the image and updates cache.
func (m *Manager) Build(ctx context.Context, opts BuildOpts) (string, error) {
	if err := m.RT.Available(ctx); err != nil {
		return "", err
	}
	if err := m.Cache.EnsureDir(); err != nil {
		return "", err
	}
	body, hash, err := m.dockerfileAndHash()
	if err != nil {
		return "", err
	}
	tag := opts.Tag
	if tag == "" {
		tag = fmt.Sprintf("strike-dev:%s", hash[:12])
	}
	opts.Tag = tag
	id, err := m.RT.BuildImage(ctx, body, opts)
	if err != nil {
		return "", err
	}
	if err := m.Cache.SetImageID(id); err != nil {
		return "", err
	}
	if err := m.Cache.SetConfigHash(hash); err != nil {
		return "", err
	}
	return id, nil
}

// LaunchMode reports whether Launch attached to an existing container or started a new one.
// Session model (E12.6): one managed container per repo path (ContainerName); N sessions join it.
type LaunchMode string

const (
	// LaunchModeAttached reused a compatible running container.
	LaunchModeAttached LaunchMode = "attached"
	// LaunchModeStarted created and started a new container.
	LaunchModeStarted LaunchMode = "started"
	// LaunchModeRestarted started an exited container that still matched config.
	LaunchModeRestarted LaunchMode = "restarted"
	// LaunchModeRebuilt replaced a drifted container after an explicit rebuild choice.
	LaunchModeRebuilt LaunchMode = "rebuilt"
)

// LaunchOpts configures Launch.
type LaunchOpts struct {
	// ForceRebuild rebuilds even when hash matches.
	ForceRebuild bool
	// NoCache passes --no-cache to build.
	NoCache bool
	// Cmd is the process to exec after start (default: keep sleep infinity / attach shell).
	Cmd []string
	// Attach opens an interactive TTY (docker exec -it) after start.
	Attach bool
	// AsRoot runs attach/exec as root.
	AsRoot bool
	// Headless skips attach and returns after start.
	Headless bool
	// Replace stops an existing running container before create (rebuild path).
	Replace bool
	// AttachStale attaches to a drifted running container without rebuilding
	// (user chose "attach anyway" after StaleContainerError).
	AttachStale bool
}

// LaunchResult is the outcome of Launch / LaunchWithResult (E12.6).
type LaunchResult struct {
	ID   string
	Name string
	Mode LaunchMode
}

// Launch implements build-if-needed → create → start → optional attach.
// Prefer LaunchWithResult when the caller needs attached-vs-started messaging.
func (m *Manager) Launch(ctx context.Context, opts LaunchOpts) (containerID string, err error) {
	res, err := m.LaunchWithResult(ctx, opts)
	return res.ID, err
}

// LaunchWithResult is Launch plus mode (attached | started | restarted | rebuilt).
func (m *Manager) LaunchWithResult(ctx context.Context, opts LaunchOpts) (LaunchResult, error) {
	var zero LaunchResult
	if err := m.RT.Available(ctx); err != nil {
		return zero, err
	}
	if err := ValidateRequiredEnv(m.Cfg.Auth.RequiredEnv, m.Cfg.Auth.EnvFile, m.RepoDir); err != nil {
		return zero, err
	}
	if err := m.Cache.EnsureDir(); err != nil {
		return zero, err
	}
	unlock, err := lockLifecycle(ctx, m.lockPath(lifecycleLockName))
	if err != nil {
		return zero, err
	}
	res, err := m.launchLocked(ctx, opts)
	if unlockErr := unlock(); err == nil && unlockErr != nil {
		return zero, unlockErr
	}
	if err != nil {
		return zero, err
	}
	return m.finishAttach(ctx, res.ID, res.Name, res.Mode, opts)
}

// launchLocked performs all inspect/build/create/replace decisions while the
// per-repository lifecycle lock is held. Interactive exec happens after this
// returns so multiple sessions can attach concurrently.
func (m *Manager) launchLocked(ctx context.Context, opts LaunchOpts) (LaunchResult, error) {
	var zero LaunchResult
	name := ContainerName(m.RepoDir)

	// Reuse running container when labels match (cache id or deterministic name).
	if st, err := m.findExisting(ctx); err == nil && st != nil {
		_ = m.Cache.SetContainerID(st.ID)
		if st.Running {
			if changed, reason := m.runningChanged(st); changed {
				if opts.AttachStale {
					return LaunchResult{ID: st.ID, Name: name, Mode: LaunchModeAttached}, nil
				}
				if !opts.Replace {
					return zero, m.staleContainerError(st, reason)
				}
				return m.rebuildPath(ctx, opts)
			}
			if opts.Replace && opts.ForceRebuild {
				return m.rebuildPath(ctx, opts)
			}
			return LaunchResult{ID: st.ID, Name: name, Mode: LaunchModeAttached}, nil
		}
		// exited container
		if changed, _ := m.runningChanged(st); !changed && !opts.ForceRebuild && !opts.Replace {
			if err := m.RT.Start(ctx, st.ID); err == nil {
				_ = m.Cache.SetContainerID(st.ID)
				return LaunchResult{ID: st.ID, Name: name, Mode: LaunchModeRestarted}, nil
			}
		}
		_ = m.RT.Remove(ctx, st.ID)
		_ = m.Cache.ClearRuntime()
	}

	return m.createPath(ctx, opts, false)
}

func (m *Manager) lockPath(name string) string {
	return filepath.Join(m.Cache.Dir(), name)
}

// rebuildPath holds the exclusive attach lease while replacing the live
// container. Active sessions retain shared leases, so rebuild waits for them
// instead of killing their execs.
func (m *Manager) rebuildPath(ctx context.Context, opts LaunchOpts) (LaunchResult, error) {
	var zero LaunchResult
	release, err := lockAttachExclusive(ctx, m.lockPath(attachLockName))
	if err != nil {
		return zero, err
	}
	defer release()

	st, err := m.RT.InspectContainer(ctx, ContainerName(m.RepoDir))
	if err == nil && st != nil {
		_ = m.Cache.SetContainerID(st.ID)
		if st.Running {
			changed, reason := m.runningChanged(st)
			if changed && !opts.Replace {
				return zero, m.staleContainerError(st, reason)
			}
			if !changed && !(opts.Replace && opts.ForceRebuild) {
				return LaunchResult{ID: st.ID, Name: ContainerName(m.RepoDir), Mode: LaunchModeAttached}, nil
			}
			_ = m.Stop(ctx)
		} else {
			_ = m.RT.Remove(ctx, st.ID)
			_ = m.Cache.ClearRuntime()
		}
	} else if err != nil && !errors.Is(err, ErrNoContainer) {
		return zero, err
	}

	res, err := m.createPath(ctx, opts, true)
	if err != nil {
		return zero, err
	}
	res.Mode = LaunchModeRebuilt
	return res, nil
}

func (m *Manager) staleContainerError(st *InspectState, reason string) *StaleContainerError {
	_, want, _ := m.dockerfileAndHash()
	have := ""
	if st != nil && st.Labels != nil {
		have = st.Labels[LabelConfigHash]
	}
	return &StaleContainerError{
		Reason:      reason,
		ContainerID: st.ID,
		Name:        ContainerName(m.RepoDir),
		WantHash:    want,
		HaveHash:    have,
	}
}

// findExisting locates the per-repo container via cache id, then ContainerName.
func (m *Manager) findExisting(ctx context.Context) (*InspectState, error) {
	if existing, _ := m.Cache.ContainerID(); existing != "" {
		st, err := m.RT.InspectContainer(ctx, existing)
		if err == nil {
			return st, nil
		}
		// stale cache entry
		_ = m.Cache.ClearRuntime()
	}
	// Folder → container mapping (E12.6): deterministic name from repo path.
	name := ContainerName(m.RepoDir)
	st, err := m.RT.InspectContainer(ctx, name)
	if err != nil {
		return nil, err
	}
	return st, nil
}

func (m *Manager) finishAttach(ctx context.Context, id, name string, mode LaunchMode, opts LaunchOpts) (LaunchResult, error) {
	res := LaunchResult{ID: id, Name: name, Mode: mode}
	if opts.Headless || !opts.Attach {
		return res, nil
	}
	res, release, err := m.acquireAttachLease(ctx, res, opts.AttachStale)
	if err != nil {
		return LaunchResult{}, err
	}
	cmd := opts.Cmd
	if len(cmd) == 0 {
		cmd = []string{m.Cfg.shell()}
	}
	attachErr := m.attach(ctx, res.ID, cmd, opts.AsRoot)
	if unlockErr := release(); attachErr == nil && unlockErr != nil {
		return res, unlockErr
	}
	return res, attachErr
}

func (m *Manager) acquireAttachLease(ctx context.Context, res LaunchResult, attachStale bool) (LaunchResult, func() error, error) {
	var zero LaunchResult
	release, err := lockAttachShared(ctx, m.lockPath(attachLockName))
	if err != nil {
		return zero, nil, err
	}
	st, err := m.RT.InspectContainer(ctx, ContainerName(m.RepoDir))
	if err != nil {
		_ = release()
		return zero, nil, err
	}
	if !st.Running {
		_ = release()
		return zero, nil, fmt.Errorf("%w: container not running (%s)", ErrNoContainer, st.Status)
	}
	if changed, reason := m.runningChanged(st); changed && !attachStale {
		_ = release()
		return zero, nil, m.staleContainerError(st, reason)
	}
	if res.ID != st.ID {
		res.Mode = LaunchModeAttached
	}
	res.ID = st.ID
	res.Name = ContainerName(m.RepoDir)
	return res, release, nil
}

func (m *Manager) createPath(ctx context.Context, opts LaunchOpts, attachExclusive bool) (LaunchResult, error) {
	var zero LaunchResult
	name := ContainerName(m.RepoDir)
	if m.NeedsBuild(ctx, opts.ForceRebuild) {
		if _, err := m.Build(ctx, BuildOpts{NoCache: opts.NoCache}); err != nil {
			return zero, err
		}
	}
	img, err := m.Cache.ImageID()
	if err != nil || img == "" {
		return zero, fmt.Errorf("container: no image id after build")
	}

	id, err := m.createAndStart(ctx, img)
	if err != nil {
		return m.recoverCreateConflict(ctx, opts, err, attachExclusive)
	}
	return LaunchResult{ID: id, Name: name, Mode: LaunchModeStarted}, nil
}

// recoverCreateConflict re-inspects the deterministic name after create
// fails. Another process may have won creation before adopting the lifecycle
// lock; a compatible live container is safe to join.
func (m *Manager) recoverCreateConflict(ctx context.Context, opts LaunchOpts, createErr error, attachExclusive bool) (LaunchResult, error) {
	var zero LaunchResult
	name := ContainerName(m.RepoDir)
	st, err := m.RT.InspectContainer(ctx, name)
	if err != nil || st == nil {
		return zero, createErr
	}
	_ = m.Cache.SetContainerID(st.ID)
	changed, reason := m.runningChanged(st)
	if st.Running {
		if !changed || opts.AttachStale {
			return LaunchResult{ID: st.ID, Name: name, Mode: LaunchModeAttached}, nil
		}
		if !opts.Replace {
			return zero, m.staleContainerError(st, reason)
		}
		if !attachExclusive {
			release, err := lockAttachExclusive(ctx, m.lockPath(attachLockName))
			if err != nil {
				return zero, err
			}
			defer release()
			return m.recoverCreateConflict(ctx, opts, createErr, true)
		}
		_ = m.RT.Stop(ctx, st.ID, 5)
		_ = m.RT.Remove(ctx, st.ID)
		_ = m.Cache.ClearRuntime()
	} else {
		if !changed && !opts.ForceRebuild && !opts.Replace {
			if err := m.RT.Start(ctx, st.ID); err == nil {
				_ = m.Cache.SetContainerID(st.ID)
				return LaunchResult{ID: st.ID, Name: name, Mode: LaunchModeRestarted}, nil
			}
		}
		_ = m.RT.Remove(ctx, st.ID)
		_ = m.Cache.ClearRuntime()
	}

	img, err := m.Cache.ImageID()
	if err != nil || img == "" {
		return zero, createErr
	}
	id, err := m.createAndStart(ctx, img)
	if err != nil {
		return zero, err
	}
	mode := LaunchModeStarted
	if opts.Replace {
		mode = LaunchModeRebuilt
	}
	return LaunchResult{ID: id, Name: name, Mode: mode}, nil
}

func (m *Manager) runningChanged(st *InspectState) (bool, string) {
	ok, reason, _, _ := m.compatibility(st)
	return !ok, reason
}

// Compatibility reports whether st matches the current recipe (config hash / image id).
// have/want are config-hash labels (want may be empty if hash compute failed).
func (m *Manager) Compatibility(st *InspectState) (ok bool, reason, have, want string) {
	return m.compatibility(st)
}

func (m *Manager) compatibility(st *InspectState) (ok bool, reason, have, want string) {
	_, hash, err := m.dockerfileAndHash()
	want = hash
	if err != nil {
		return false, "hash compute failed", "", ""
	}
	if st == nil {
		return false, "no container", "", want
	}
	if st.Labels != nil {
		have = st.Labels[LabelConfigHash]
	}
	if st.Labels == nil {
		return false, "missing labels", have, want
	}
	if got := st.Labels[LabelConfigHash]; got != "" && got != hash {
		return false, fmt.Sprintf("config hash %s != %s", shortHash(got), shortHash(hash)), have, want
	}
	if got := st.Labels[LabelConfigHash]; got == "" {
		return false, "missing config hash label", have, want
	}
	img, _ := m.Cache.ImageID()
	if img != "" && st.Labels[LabelImageID] != "" && st.Labels[LabelImageID] != img {
		return false, "image id mismatch", have, want
	}
	return true, "", have, want
}

func (m *Manager) createAndStart(ctx context.Context, imageID string) (string, error) {
	name := ContainerName(m.RepoDir)

	netMode := strings.ToLower(strings.TrimSpace(m.Cfg.Network.Mode))
	var network string
	if netMode != "none" {
		network = NetworkName(m.RepoDir)
		if err := m.RT.EnsureNetwork(ctx, network); err != nil {
			return "", err
		}
		_ = m.Cache.SetNetworkID(network)
	}

	_, hash, err := m.dockerfileAndHash()
	if err != nil {
		return "", err
	}
	labs := Labels(m.RepoDir, hash, imageID, "dev")

	env, err := m.collectEnv()
	if err != nil {
		return "", err
	}
	binds, err := m.collectBinds()
	if err != nil {
		return "", err
	}
	extra, err := m.createExtraArgs()
	if err != nil {
		return "", err
	}
	if netMode == "none" {
		extra = append(extra, "--network", "none")
	}

	workDir := m.Cfg.mountPath()
	createOpts := CreateOpts{
		Name:      name,
		WorkDir:   workDir,
		Env:       env,
		HostBinds: binds,
		Labels:    labs,
		Network:   network,
		ExtraArgs: extra,
		Cmd:       []string{"sleep", "infinity"},
	}
	id, err := m.RT.Create(ctx, imageID, createOpts)
	if err != nil {
		return "", err
	}
	if err := m.RT.Start(ctx, id); err != nil {
		_ = m.RT.Remove(ctx, id)
		return "", err
	}
	if err := m.Cache.SetContainerID(id); err != nil {
		return "", err
	}
	return id, nil
}

func (m *Manager) collectEnv() ([]string, error) {
	envVars, warnings := CollectForwardedEnv(m.Cfg.Auth.ForwardEnv)
	for _, w := range warnings {
		m.warnf("warning: %s\n", w)
	}
	if m.Cfg.Auth.EnvFile != "" {
		path := m.Cfg.Auth.EnvFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(m.RepoDir, path)
		}
		fileVars, err := ParseEnvFile(path)
		if err != nil {
			return nil, err
		}
		for k, v := range fileVars {
			envVars = append(envVars, k+"="+v)
		}
	}
	envVars = append(envVars, resolveProxyEnv(m.Cfg.Network)...)
	if m.Cfg.forwardSSH() && runtime.GOOS != "darwin" {
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			if fi, err := os.Stat(sock); err == nil && fi.Mode()&os.ModeSocket != 0 {
				envVars = append(envVars, "SSH_AUTH_SOCK=/tmp/ssh-agent.sock")
			}
		}
	}
	envVars = append(envVars, "STRIKE_ISOLATION="+IsolationEnvValue(m.Cfg))
	return envVars, nil
}

func (m *Manager) collectBinds() ([]string, error) {
	hostPath := m.Cfg.Workspace.HostPath
	if hostPath == "" {
		hostPath = m.RepoDir
	}
	mountPath := m.Cfg.mountPath()
	binds := []string{fmt.Sprintf("%s:%s", hostPath, mountPath)}
	if m.Cfg.persistHome() {
		binds = append(binds, fmt.Sprintf("%s:/home/strike", homeVolumeName(m.RepoDir)))
	}
	if m.Cfg.forwardSSH() {
		if runtime.GOOS == "darwin" {
			m.warnf("warning: SSH agent forwarding is not available on macOS (domain sockets cannot be bind-mounted)\n")
		} else {
			sock := os.Getenv("SSH_AUTH_SOCK")
			if sock == "" {
				m.warnf("warning: SSH_AUTH_SOCK unset; SSH agent forwarding skipped\n")
			} else if fi, err := os.Stat(sock); err == nil && fi.Mode()&os.ModeSocket != 0 {
				binds = append(binds, fmt.Sprintf("%s:/tmp/ssh-agent.sock:ro", sock))
			} else {
				m.warnf("warning: SSH_AUTH_SOCK not a socket; SSH agent forwarding skipped\n")
			}
		}
	}
	binds = append(binds, m.Cfg.Workspace.ExtraBinds...)
	return binds, nil
}

func homeVolumeName(repoPath string) string {
	abs, _ := filepath.Abs(repoPath)
	sum := sha256.Sum256([]byte(abs))
	return "strike-home-" + hex.EncodeToString(sum[:])[:16]
}

func (m *Manager) createExtraArgs() ([]string, error) {
	sec := DefaultSecurityFlags()
	pids := sec.PidsLimit
	if m.Cfg.Resources.PidsLimit > 0 {
		pids = m.Cfg.Resources.PidsLimit
	}
	args := []string{
		"--security-opt", "no-new-privileges",
		"--cap-drop", "ALL",
		"--pids-limit", fmt.Sprintf("%d", pids),
	}
	for _, c := range sec.CapAdd {
		args = append(args, "--cap-add", c)
	}
	if mem := strings.TrimSpace(m.Cfg.Resources.Memory); mem != "" && mem != "0" {
		if _, err := ParseMemoryBytes(mem); err != nil {
			return nil, err
		}
		args = append(args, "--memory", mem)
	}
	if cpus := strings.TrimSpace(m.Cfg.Resources.CPUs); cpus != "" && cpus != "0" {
		if _, err := ParseNanoCPUs(cpus); err != nil {
			return nil, err
		}
		args = append(args, "--cpus", cpus)
	}
	gpu, err := ParseGPURequest(m.Cfg.Resources.GPUs)
	if err != nil {
		return nil, err
	}
	if gpu != nil {
		if f := gpu.CLIFlag(); f != "" {
			args = append(args, "--gpus", f)
		}
	}
	ports, err := ParsePortBindings(m.Cfg.Workspace.Ports)
	if err != nil {
		return nil, err
	}
	for _, p := range ports {
		args = append(args, "-p", p.Host+":"+p.Container)
	}
	return args, nil
}

func (m *Manager) attach(ctx context.Context, id string, cmd []string, asRoot bool) error {
	fn := m.AttachFn
	if fn == nil {
		fn = defaultAttach
	}
	engine := m.RT.Engine()
	if resolved, err := m.RT.Resolve(); err == nil {
		engine = resolved
	}
	return fn(ctx, engine, id, cmd, asRoot)
}

func defaultAttach(ctx context.Context, engine, containerID string, cmd []string, asRoot bool) error {
	args := []string{"exec", "-it"}
	if asRoot {
		args = append(args, "-u", "root")
	}
	args = append(args, containerID)
	args = append(args, cmd...)
	c := exec.CommandContext(ctx, engine, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// Stop stops and removes the current container and network; keeps image + hash.
func (m *Manager) Stop(ctx context.Context) error {
	id, err := m.Cache.ContainerID()
	if err != nil {
		return err
	}
	if id != "" {
		_ = m.RT.Stop(ctx, id, 10)
		_ = m.RT.Remove(ctx, id)
	}
	// also try by deterministic name
	_ = m.RT.Remove(ctx, ContainerName(m.RepoDir))
	if net, _ := m.Cache.NetworkID(); net != "" {
		_ = m.RT.RemoveNetwork(ctx, net)
	}
	_ = m.RT.RemoveNetwork(ctx, NetworkName(m.RepoDir))
	return m.Cache.ClearRuntime()
}

// Restart stops then launches headless (no attach).
func (m *Manager) Restart(ctx context.Context) (string, error) {
	_ = m.Stop(ctx)
	return m.Launch(ctx, LaunchOpts{Headless: true})
}

// Destroy stops runtime and removes the cached image.
func (m *Manager) Destroy(ctx context.Context) error {
	_ = m.Stop(ctx)
	if img, _ := m.Cache.ImageID(); img != "" {
		_ = m.RT.RemoveImage(ctx, img)
	}
	_ = m.Cache.SetImageID("")
	_ = m.Cache.SetConfigHash("")
	return nil
}

// Clean destroys runtime state and deletes the on-disk cache directory.
func (m *Manager) Clean(ctx context.Context) error {
	_ = m.Destroy(ctx)
	return m.Cache.Clean()
}

// Exec runs a command in the managed container (non-interactive).
func (m *Manager) Exec(ctx context.Context, cmd []string, opts ExecOpts) (stdout, stderr string, code int, err error) {
	id, err := m.Cache.ContainerID()
	if err != nil {
		return "", "", -1, err
	}
	if id == "" {
		// try name
		id = ContainerName(m.RepoDir)
	}
	st, err := m.RT.InspectContainer(ctx, id)
	if err != nil {
		return "", "", -1, err
	}
	if !st.Running {
		return "", "", -1, fmt.Errorf("%w: container not running", ErrNoContainer)
	}
	return m.RT.Exec(ctx, st.ID, cmd, opts)
}

// Attach attaches an interactive session to the running container.
func (m *Manager) Attach(ctx context.Context, cmd []string, asRoot bool) error {
	id, err := m.Cache.ContainerID()
	if err != nil {
		return err
	}
	if id == "" {
		id = ContainerName(m.RepoDir)
	}
	st, err := m.RT.InspectContainer(ctx, id)
	if err != nil {
		return err
	}
	if !st.Running {
		return fmt.Errorf("%w: container not running", ErrNoContainer)
	}
	res, release, err := m.acquireAttachLease(ctx, LaunchResult{ID: st.ID, Name: ContainerName(m.RepoDir), Mode: LaunchModeAttached}, true)
	if err != nil {
		return err
	}
	if len(cmd) == 0 {
		cmd = []string{m.Cfg.shell()}
	}
	attachErr := m.attach(ctx, res.ID, cmd, asRoot)
	if unlockErr := release(); attachErr == nil && unlockErr != nil {
		return unlockErr
	}
	return attachErr
}

// Status returns inspect state for the managed container, or ErrNoContainer.
func (m *Manager) Status(ctx context.Context) (*InspectState, error) {
	id, err := m.Cache.ContainerID()
	if err != nil {
		return nil, err
	}
	if id == "" {
		id = ContainerName(m.RepoDir)
	}
	return m.RT.InspectContainer(ctx, id)
}

// ListManaged lists containers with com.strike.managed=true.
func (m *Manager) ListManaged(ctx context.Context) ([]InspectState, error) {
	stdout, stderr, code, err := m.RT.run(ctx, "ps", "-a",
		"--filter", "label="+LabelManaged+"=true",
		"--format", "{{.ID}}")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("container: ps: %s", strings.TrimSpace(stderr))
	}
	var out []InspectState
	for _, line := range strings.Split(stdout, "\n") {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		st, err := m.RT.InspectContainer(ctx, id)
		if err != nil {
			continue
		}
		out = append(out, *st)
	}
	return out, nil
}
