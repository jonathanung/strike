package local

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/integrate/mcp"
	"github.com/jonathanung/strike/internal/integrate/powers"
	"github.com/jonathanung/strike/internal/product/config"
)

// NewPowers adapts Powers pack discovery and per-session activation to
// host.Powers. Packs are re-discovered on every call, so a pack added on
// disk shows up without a restart. workspaceRoot is "" for an untrusted
// workspace, so repo-supplied packs never load there. Mute state and
// last-used times stay in memory; NewScopedPowers persists them.
func NewPowers(workspaceRoot, globalRoot string, conn powers.Connector, deny powers.DenyFunc) host.Powers {
	p, _ := NewScopedPowers(workspaceRoot, globalRoot, "", conn, deny)
	return p
}

// NewScopedPowers is NewPowers plus the per-session scope engines read
// (#1943): which skills and servers each session's active packs supply.
// statePath, when set, persists mute state and last-used times there, so
// they survive a restart; a state file that fails to load is reported as a
// pack load error and Powers start empty.
func NewScopedPowers(workspaceRoot, globalRoot, statePath string, conn powers.Connector, deny powers.DenyFunc) (host.Powers, *PowersScope) {
	a := &powersAdapter{
		workspaceRoot: workspaceRoot,
		globalRoot:    globalRoot,
		statePath:     statePath,
		mgr:           powers.NewManager(conn, deny),
		now:           time.Now,
		muted:         map[string]bool{},
		lastUsed:      map[string]time.Time{},
	}
	if statePath != "" {
		st, err := powers.LoadState(statePath)
		if err != nil {
			a.stateErr = err.Error()
		}
		for _, name := range st.Muted {
			a.muted[name] = true
		}
		for name, t := range st.LastUsed {
			a.lastUsed[name] = t
		}
	}
	return a, &PowersScope{a: a}
}

type powersAdapter struct {
	workspaceRoot string
	globalRoot    string
	statePath     string // "" keeps state in memory only
	mgr           *powers.Manager
	now           func() time.Time

	mu       sync.Mutex // guards muted, lastUsed, stateErr and state writes
	muted    map[string]bool
	lastUsed map[string]time.Time
	stateErr string
}

// saveLocked persists mute state and last-used times. a.mu must be held.
func (a *powersAdapter) saveLocked() error {
	if a.statePath == "" {
		return nil
	}
	st := powers.State{LastUsed: make(map[string]time.Time, len(a.lastUsed))}
	for name := range a.muted {
		st.Muted = append(st.Muted, name)
	}
	for name, t := range a.lastUsed {
		st.LastUsed[name] = t
	}
	return powers.SaveState(a.statePath, st)
}

func (a *powersAdapter) discover() ([]powers.Power, []error) {
	return powers.Discover(a.workspaceRoot, a.globalRoot)
}

func (a *powersAdapter) find(name string) (powers.Power, error) {
	packs, _ := a.discover()
	for _, p := range packs {
		if p.Name == name {
			return p, nil
		}
	}
	return powers.Power{}, fmt.Errorf("powers: unknown pack %q", name)
}

func (a *powersAdapter) Packs() ([]host.PowerPack, []string) {
	packs, errs := a.discover()
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]host.PowerPack, 0, len(packs))
	for _, p := range packs {
		hp := host.PowerPack{
			Name:     p.Name,
			Scope:    string(p.Scope),
			Keywords: append([]string(nil), p.Keywords...),
			Servers:  append([]string(nil), p.Servers...),
			Skills:   append([]string(nil), p.Skills...),
			Muted:    a.muted[p.Name],
		}
		if t, ok := a.lastUsed[p.Name]; ok {
			hp.LastUsed = &t
		}
		out = append(out, hp)
	}
	msgs := make([]string, 0, len(errs)+1)
	if a.stateErr != "" {
		msgs = append(msgs, a.stateErr)
	}
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	return out, msgs
}

func (a *powersAdapter) Suggest(session, prompt string) []string {
	packs, _ := a.discover()
	active := map[string]bool{}
	for _, r := range a.mgr.Active(session) {
		active[r.Name] = true
	}
	a.mu.Lock()
	muted := make(map[string]bool, len(a.muted))
	for k, v := range a.muted {
		muted[k] = v
	}
	a.mu.Unlock()
	return powers.Suggest(prompt, packs, active, muted)
}

func (a *powersAdapter) Activate(session, name string) (host.PowerActivation, error) {
	p, err := a.find(name)
	if err != nil {
		return host.PowerActivation{}, err
	}
	res, err := a.mgr.Activate(session, p)
	if err != nil {
		return host.PowerActivation{}, err
	}
	a.mu.Lock()
	a.lastUsed[p.Name] = a.now()
	// Activation already succeeded; a failed write only loses the time.
	_ = a.saveLocked()
	a.mu.Unlock()
	return toHostActivation(res), nil
}

func (a *powersAdapter) Deactivate(session, name string) error {
	return a.mgr.Deactivate(session, name)
}

func (a *powersAdapter) Active(session string) []host.PowerActivation {
	raw := a.mgr.Active(session)
	out := make([]host.PowerActivation, 0, len(raw))
	for _, r := range raw {
		out = append(out, toHostActivation(r))
	}
	return out
}

func (a *powersAdapter) SetMuted(name string, muted bool) error {
	if _, err := a.find(name); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	was := a.muted[name]
	if muted {
		a.muted[name] = true
	} else {
		delete(a.muted, name)
	}
	if err := a.saveLocked(); err != nil {
		// Keep memory and disk in agreement: a mute that cannot be saved
		// would silently come back unmuted after a restart.
		if was {
			a.muted[name] = true
		} else {
			delete(a.muted, name)
		}
		return err
	}
	return nil
}

func (a *powersAdapter) EndSession(session string) {
	a.mgr.EndSession(session)
}

// PowersScope reports what each session's active packs supply, for the
// engine's per-session tool and skill catalogs (#1943).
type PowersScope struct {
	a *powersAdapter
}

// Servers returns the MCP servers session's active packs hold.
func (s *PowersScope) Servers(session string) []string {
	if s == nil || s.a == nil {
		return nil
	}
	return s.a.mgr.Servers(session)
}

// Skills returns the skills session's active packs supply, parsed from each
// pack's skills/<name>/SKILL.md. Packs are visited by name; a skill name an
// earlier pack already supplied is skipped. A skill file that no longer
// parses is left out.
func (s *PowersScope) Skills(session string) []config.Skill {
	if s == nil || s.a == nil {
		return nil
	}
	active := s.a.mgr.Active(session)
	if len(active) == 0 {
		return nil
	}
	packs, _ := s.a.discover()
	dirs := make(map[string]string, len(packs))
	for _, p := range packs {
		dirs[p.Name] = p.Dir
	}
	var out []config.Skill
	seen := map[string]bool{}
	for _, r := range active {
		dir, ok := dirs[r.Name]
		if !ok {
			continue // pack removed from disk while active
		}
		for _, name := range r.Skills {
			sk, err := config.ParseSkillFile(filepath.Join(dir, "skills", name, "SKILL.md"))
			if err != nil || sk == nil || seen[sk.Name] {
				continue
			}
			seen[sk.Name] = true
			out = append(out, *sk)
		}
	}
	return out
}

// PowersToolVisible scopes Powers tools to the sessions that activated them.
// scopedTool (mcp.PackConnector.ScopedTool) names the Powers-attached server
// a tool belongs to; such a tool is visible only to a session whose active
// packs hold that server. Every other tool, including those of servers
// configured outside Powers, stays visible to all sessions.
func PowersToolVisible(scope *PowersScope, scopedTool func(tool string) (string, bool)) func(session, tool string) bool {
	return func(session, tool string) bool {
		server, ok := scopedTool(tool)
		if !ok {
			return true
		}
		for _, held := range scope.Servers(session) {
			if held == server {
				return true
			}
		}
		return false
	}
}

func toHostActivation(r powers.Result) host.PowerActivation {
	out := host.PowerActivation{
		Name:     r.Name,
		Admitted: append([]string(nil), r.Admitted...),
		Skills:   append([]string(nil), r.Skills...),
	}
	if len(r.Failed) > 0 {
		out.Failed = make(map[string]string, len(r.Failed))
		for k, v := range r.Failed {
			out.Failed[k] = v
		}
	}
	return out
}

// PowerServerConfig resolves a pack server name to its MCP configuration
// from the packs' mcp.json files. When two packs define the same server
// name, the first pack by name wins. Stdio servers run in workDir unless
// their entry sets cwd.
func PowerServerConfig(workspaceRoot, globalRoot, workDir string) func(server string) (mcp.ServerConfig, bool) {
	return func(server string) (mcp.ServerConfig, bool) {
		packs, _ := powers.Discover(workspaceRoot, globalRoot)
		for _, p := range packs {
			raws, err := powers.ServerConfigs(p)
			if err != nil {
				continue
			}
			raw, ok := raws[server]
			if !ok {
				continue
			}
			var f mcp.ServerConfigFields
			if err := json.Unmarshal(raw, &f); err != nil {
				return mcp.ServerConfig{}, false
			}
			cfgs := mcp.ConfigsFromMap(map[string]mcp.ServerConfigFields{server: f}, workDir)
			if len(cfgs) != 1 {
				return mcp.ServerConfig{}, false
			}
			return cfgs[0], true
		}
		return mcp.ServerConfig{}, false
	}
}

// PowersDeny builds the Powers deny check from permission layers: a server
// is denied when the layers deny every tool on it (mcp "<server>/*").
// Activation never widens permissions; a narrower per-tool deny still
// applies at call time.
func PowersDeny(layers []permission.Ruleset) powers.DenyFunc {
	return func(_, server string) (bool, string) {
		if permission.Evaluate(mcp.Permission, server+"/*", layers...) == permission.Deny {
			return true, "permission rules deny " + mcp.Permission + ":" + server + "/*"
		}
		return false, ""
	}
}
