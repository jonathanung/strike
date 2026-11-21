package host

import "time"

// PowerPack is one discovered Powers capability pack (#1347): a named group
// of MCP servers and skills that a session activates on demand.
type PowerPack struct {
	Name     string     `json:"name"`
	Scope    string     `json:"scope"` // workspace | global
	Keywords []string   `json:"keywords,omitempty"`
	Servers  []string   `json:"servers,omitempty"`
	Skills   []string   `json:"skills,omitempty"`
	Muted    bool       `json:"muted"`              // keyword suggestion is off
	LastUsed *time.Time `json:"lastUsed,omitempty"` // last activation, any session
}

// PowerActivation is one pack's state in one session. Admitted servers are
// connected and their tools bound; Failed servers are not (server -> error).
// The cockpit chip and the session's tool set both read this.
type PowerActivation struct {
	Name     string            `json:"name"`
	Admitted []string          `json:"admitted,omitempty"`
	Failed   map[string]string `json:"failed,omitempty"`
	Skills   []string          `json:"skills,omitempty"`
}

// Powers lists, suggests and activates capability packs per session.
// Nil means the capability is absent; frontends must degrade gracefully.
type Powers interface {
	// Packs lists discovered packs sorted by name, plus non-fatal load
	// errors for packs that were skipped.
	Packs() (packs []PowerPack, loadErrors []string)
	// Suggest names packs whose keywords match prompt, skipping muted
	// packs and packs already active in session. It never activates.
	Suggest(session, prompt string) []string
	// Activate connects the pack's servers for session. A server that fails
	// to connect is reported in Failed and left out; a deny-listed server
	// keeps the whole pack inactive and returns an error.
	Activate(session, name string) (PowerActivation, error)
	// Deactivate unloads one pack from session.
	Deactivate(session, name string) error
	// Active lists session's active packs sorted by name.
	Active(session string) []PowerActivation
	// SetMuted turns keyword suggestion off (or back on) for a pack.
	SetMuted(name string, muted bool) error
	// EndSession unloads every pack from session.
	EndSession(session string)
}
