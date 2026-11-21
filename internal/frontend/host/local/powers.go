package local

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/integrate/mcp"
	"github.com/jonathanung/strike/internal/integrate/powers"
)

// NewPowers adapts Powers pack discovery and per-session activation to
// host.Powers. Packs are re-discovered on every call, so a pack added on
// disk shows up without a restart. workspaceRoot is "" for an untrusted
// workspace, so repo-supplied packs never load there.
func NewPowers(workspaceRoot, globalRoot string, conn powers.Connector, deny powers.DenyFunc) host.Powers {
	return &powersAdapter{
		workspaceRoot: workspaceRoot,
		globalRoot:    globalRoot,
		mgr:           powers.NewManager(conn, deny),
		muted:         map[string]bool{},
	}
}

type powersAdapter struct {
	workspaceRoot string
	globalRoot    string
	mgr           *powers.Manager

	mu    sync.Mutex
	muted map[string]bool // in memory for now; resets on restart
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
		if t, ok := a.mgr.LastUsed(p.Name); ok {
			hp.LastUsed = &t
		}
		out = append(out, hp)
	}
	msgs := make([]string, 0, len(errs))
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
	if muted {
		a.muted[name] = true
	} else {
		delete(a.muted, name)
	}
	return nil
}

func (a *powersAdapter) EndSession(session string) {
	a.mgr.EndSession(session)
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
