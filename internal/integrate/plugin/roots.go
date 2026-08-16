package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Roots resolves strike roots and plugin install directories for a scope.
type Roots struct {
	Scope       Scope
	StrikeRoot  string // ~/.strike or <work>/.strike
	PluginsDir  string // <StrikeRoot>/plugins
	LockPath    string
	WorkDir     string
	GlobalRoot  string
	ProjectRoot string
}

// ResolveRoots builds Roots for scope using opts (same overrides as Discover).
func ResolveRoots(scope Scope, opts Options) (Roots, error) {
	globalRoot := opts.GlobalRoot
	if globalRoot == "" {
		globalRoot = defaultGlobalRoot()
	}
	projectRoot := opts.ProjectRoot
	if projectRoot == "" && opts.WorkDir != "" {
		projectRoot = defaultProjectRoot(opts.WorkDir)
	}

	var strikeRoot string
	switch scope {
	case ScopeGlobal:
		if globalRoot == "" {
			return Roots{}, fmt.Errorf("cannot resolve global plugin root (home directory unavailable)")
		}
		strikeRoot = globalRoot
	case ScopeProject:
		if projectRoot == "" {
			return Roots{}, fmt.Errorf("project scope requires a work directory")
		}
		strikeRoot = projectRoot
	default:
		return Roots{}, fmt.Errorf("unknown scope %q", scope)
	}

	pluginsDir := filepath.Join(strikeRoot, "plugins")
	return Roots{
		Scope:       scope,
		StrikeRoot:  strikeRoot,
		PluginsDir:  pluginsDir,
		LockPath:    LockfilePath(strikeRoot),
		WorkDir:     opts.WorkDir,
		GlobalRoot:  globalRoot,
		ProjectRoot: projectRoot,
	}, nil
}

// InstallDir returns the destination directory for plugin id under this root.
// Directory name is the plugin id (safe per ValidatePluginID grammar).
func (r Roots) InstallDir(pluginID string) string {
	return filepath.Join(r.PluginsDir, pluginID)
}

// EnsurePluginsDir creates the plugins directory if needed.
func (r Roots) EnsurePluginsDir() error {
	return os.MkdirAll(r.PluginsDir, 0o755)
}

// ConfinePath ensures absPath resolves under the plugins directory (no escape).
func (r Roots) ConfinePath(absPath string) error {
	root, err := filepath.Abs(r.PluginsDir)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	} else if !os.IsNotExist(err) {
		// plugins dir may not exist yet — use cleaned abs
		root = filepath.Clean(root)
	}
	path, err := filepath.Abs(absPath)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if !isUnder(root, path) && root != path {
		return fmt.Errorf("path %q escapes configured plugins root %q", absPath, r.PluginsDir)
	}
	return nil
}

// stagingDir creates a unique staging directory under pluginsDir.
func (r Roots) stagingDir(pluginID string) (string, error) {
	if err := r.EnsurePluginsDir(); err != nil {
		return "", err
	}
	// Keep staging inside plugins root so rename is same-filesystem atomic.
	pattern := filepath.Join(r.PluginsDir, ".staging-"+sanitizeDirComponent(pluginID)+"-*")
	return os.MkdirTemp(r.PluginsDir, filepath.Base(pattern))
}

func sanitizeDirComponent(id string) string {
	// Plugin IDs are already restricted; replace any odd chars defensively.
	var b strings.Builder
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	s := b.String()
	if s == "" {
		return "plugin"
	}
	return s
}

// removeAllUnderPlugins removes path only if it is confined under pluginsDir.
func (r Roots) removeAllUnderPlugins(path string) error {
	if path == "" {
		return nil
	}
	if err := r.ConfinePath(path); err != nil {
		return err
	}
	// Never delete the plugins root itself.
	cleanPlugins := filepath.Clean(r.PluginsDir)
	cleanPath := filepath.Clean(path)
	if cleanPath == cleanPlugins {
		return fmt.Errorf("refusing to remove plugins root")
	}
	return os.RemoveAll(path)
}
