package host

// Pane modes (docs/plugin-panes.md).
const (
	PaneModeStatic  = "static"
	PaneModeProcess = "process"
)

// PaneInfo is one enabled pane contribution ready for a frontend host.
// DefinitionJSON is the validated pane/1 definition document (no secrets).
// Process panes set Trusted from the plugin trust record; static panes are
// always loadable when the plugin is enabled.
type PaneInfo struct {
	ID            string
	PluginID      string
	PluginVersion string
	Scope         string // PluginScopeGlobal | PluginScopeProject
	Title         string
	Mode          string // PaneModeStatic | PaneModeProcess
	// Trusted is true when process mode may start (matching trust grant).
	// Static panes always report true.
	Trusted bool
	// PluginRoot is the absolute plugin install directory (process cwd).
	PluginRoot string
	// DefinitionJSON is the pane definition file bytes (JSON, comments stripped).
	DefinitionJSON []byte
	// LoadError is set when the contribution is registered but cannot mount
	// (e.g. untrusted process pane, network grant rejected). Empty when OK.
	LoadError string
	// Collision notes the other plugin id when this pane id lost uniqueness.
	Collision string
}

// Provenance returns a short display label for diagnostics / plugin manager.
func (p PaneInfo) Provenance() string {
	mode := p.Mode
	if mode == "" {
		mode = "?"
	}
	ver := p.PluginVersion
	if ver == "" {
		ver = "?"
	}
	return "plugin=" + p.PluginID + "@" + ver + " pane=" + p.ID + " mode=" + mode
}

// Panes lists enabled pane contributions for frontend hosts (TUI #731, web #732).
// Nil means the capability is absent; frontends must degrade gracefully.
// Implementations fail closed on id collisions and skip disabled/malformed
// plugins so bad contributions cannot take down the host.
type Panes interface {
	// List returns enabled pane contributions in stable order
	// (global then project, plugin id ascending, pane id ascending).
	// Colliding pane ids are omitted with LoadError set on neither winner —
	// both sides fail closed and appear only in diagnostics via LoadError
	// entries when the implementation surfaces them as zero-body infos.
	List() ([]PaneInfo, error)
}
