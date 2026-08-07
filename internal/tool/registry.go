package tool

import (
	"encoding/json"
	"strings"
	"sync"

	"github.com/jonathanung/strike-cli/internal/provider"
)

// Registry holds the tools visible to the model. It is the primary
// extension surface: MCP servers (internal/mcp) and plugin tools register here.
//
// When defer loading is enabled (SetDeferLoading), SchemasForProvider omits
// non-core tools until Discover promotes them (toolsearch or direct call).
// Schemas() always returns the full registered set (used by toolsearch).
//
// Progressive tools (Progressive interface) start with a compact basic schema
// in SchemasForProvider until PromoteSchema elevates them to the full advanced
// form. Schemas() always exposes the advanced/full contract for discovery.
type Registry struct {
	order []string
	tools map[string]Tool

	mu         sync.RWMutex
	deferLoad  bool
	discovered map[string]struct{}
	// advanced tracks progressive tools promoted to full schema.
	advanced map[string]struct{}
}

func NewRegistry(tools ...Tool) *Registry {
	r := &Registry{tools: map[string]Tool{}}
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

func (r *Registry) Register(t Tool) {
	if _, exists := r.tools[t.Name()]; !exists {
		r.order = append(r.order, t.Name())
	}
	r.tools[t.Name()] = t
}

// Unregister removes tools by name. Missing names are ignored.
func (r *Registry) Unregister(names ...string) {
	if r == nil || len(names) == 0 {
		return
	}
	for _, name := range names {
		delete(r.tools, name)
	}
	order := make([]string, 0, len(r.order))
	for _, name := range r.order {
		if _, ok := r.tools[name]; ok {
			order = append(order, name)
		}
	}
	r.order = order
	r.mu.Lock()
	if r.discovered != nil {
		for _, name := range names {
			delete(r.discovered, name)
		}
	}
	if r.advanced != nil {
		for _, name := range names {
			delete(r.advanced, name)
		}
	}
	r.mu.Unlock()
}

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names returns registered tool names in registration order.
func (r *Registry) Names() []string {
	if r == nil || len(r.order) == 0 {
		return nil
	}
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// SetDeferLoading enables or disables toolsearch-backed schema deferral.
// When on, SchemasForProvider returns only core tools plus those Discover
// has promoted (session-scoped on this registry).
func (r *Registry) SetDeferLoading(on bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deferLoad = on
	if on && r.discovered == nil {
		r.discovered = map[string]struct{}{}
	}
}

// DeferLoading reports whether non-core schemas are omitted until discovered.
func (r *Registry) DeferLoading() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.deferLoad
}

// Discover promotes deferred tools into the provider Tools set for subsequent
// streams. Core tools and unknown names are ignored. No-op when defer is off.
func (r *Registry) Discover(names ...string) {
	if r == nil || len(names) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.deferLoad {
		return
	}
	if r.discovered == nil {
		r.discovered = map[string]struct{}{}
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := r.tools[name]; !ok {
			continue
		}
		if IsCoreTool(name) {
			continue
		}
		r.discovered[name] = struct{}{}
	}
}

// PromoteSchema elevates progressive tools to their advanced/full provider
// schema for subsequent streams. Non-progressive and unknown names are ignored.
func (r *Registry) PromoteSchema(names ...string) {
	if r == nil || len(names) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.advanced == nil {
		r.advanced = map[string]struct{}{}
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		t, ok := r.tools[name]
		if !ok {
			continue
		}
		if _, prog := progressiveTool(t); !prog {
			continue
		}
		r.advanced[name] = struct{}{}
	}
}

// NoteToolCall records a model tool call: discovers deferred tools and
// promotes progressive schemas when args require the advanced surface (or
// when the tool is progressive and was invoked — basic calls stay basic
// unless args need advanced). Always safe; no-op for unknown names.
func (r *Registry) NoteToolCall(name string, args json.RawMessage) {
	if r == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	r.Discover(name)
	if ArgsNeedAdvancedSchema(name, args) {
		r.PromoteSchema(name)
	}
}

// SchemaLevel returns the current provider schema level for name.
// Non-progressive tools always report SchemaAdvanced.
func (r *Registry) SchemaLevel(name string) SchemaLevel {
	if r == nil {
		return SchemaAdvanced
	}
	t, ok := r.tools[name]
	if !ok {
		return SchemaAdvanced
	}
	if _, prog := progressiveTool(t); !prog {
		return SchemaAdvanced
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.advanced[name]; ok {
		return SchemaAdvanced
	}
	return SchemaBasic
}

// SchemaAdvanced reports whether name is on the advanced progressive schema
// (true for non-progressive tools when registered).
func (r *Registry) SchemaAdvanced(name string) bool {
	return r.SchemaLevel(name) == SchemaAdvanced
}

// Discovered reports whether name has been promoted (always true for core
// tools when registered; false when defer is off for non-core).
func (r *Registry) Discovered(name string) bool {
	if r == nil {
		return false
	}
	if IsCoreTool(name) {
		_, ok := r.tools[name]
		return ok
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.deferLoad {
		_, ok := r.tools[name]
		return ok
	}
	_, ok := r.discovered[name]
	return ok
}

// DeferredPendingCount is how many registered non-core tools are not yet
// discovered. Zero when defer loading is off.
func (r *Registry) DeferredPendingCount() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.deferLoad {
		return 0
	}
	n := 0
	for _, name := range r.order {
		if IsCoreTool(name) {
			continue
		}
		if _, ok := r.discovered[name]; !ok {
			n++
		}
	}
	return n
}

// Schemas returns the full model-facing declarations in registration order.
// Used by toolsearch to scan the whole registry. Prefer SchemasForProvider
// for the provider Tools array when defer loading or progressive schemas
// may apply. Progressive tools always contribute their advanced Schema().
func (r *Registry) Schemas() []provider.ToolSchema {
	if r == nil {
		return nil
	}
	out := make([]provider.ToolSchema, 0, len(r.order))
	for _, name := range r.order {
		t := r.tools[name]
		out = append(out, provider.ToolSchema{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.Schema(),
		})
	}
	return out
}

// Contract returns the static contract for a registered tool.
func (r *Registry) Contract(name string) (Contract, bool) {
	if r == nil {
		return Contract{}, false
	}
	t, ok := r.tools[name]
	if !ok {
		return Contract{}, false
	}
	return LookupContract(t), true
}

// Contracts returns name → contract for every registered tool in registration
// order as a map. Used by tests and diagnostics; not sent to the model.
func (r *Registry) Contracts() map[string]Contract {
	if r == nil {
		return nil
	}
	out := make(map[string]Contract, len(r.order))
	for _, name := range r.order {
		out[name] = LookupContract(r.tools[name])
	}
	return out
}

// SchemasForProvider returns schemas for provider Request.Tools: full set
// when defer is off; core + discovered when on. Progressive tools use the
// basic schema until PromoteSchema elevates them.
func (r *Registry) SchemasForProvider() []provider.ToolSchema {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	deferLoad := r.deferLoad
	r.mu.RUnlock()
	if !deferLoad {
		return r.providerSchemas(false)
	}
	return r.providerSchemas(true)
}

// providerSchemas builds provider tool schemas. When filterDefer is true,
// omit non-core tools that are not yet discovered.
func (r *Registry) providerSchemas(filterDefer bool) []provider.ToolSchema {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]provider.ToolSchema, 0, len(r.order))
	for _, name := range r.order {
		if filterDefer && !IsCoreTool(name) {
			if _, ok := r.discovered[name]; !ok {
				continue
			}
		}
		t := r.tools[name]
		out = append(out, r.schemaForLocked(t))
	}
	return out
}

// schemaForLocked builds one provider schema. Caller must hold r.mu (RLock ok).
func (r *Registry) schemaForLocked(t Tool) provider.ToolSchema {
	name := t.Name()
	desc := t.Description()
	schema := t.Schema()
	if p, ok := progressiveTool(t); ok {
		if _, adv := r.advanced[name]; !adv {
			schema = p.BasicSchema()
			if bd := p.BasicDescription(); bd != "" {
				desc = bd
			}
		}
	}
	return provider.ToolSchema{
		Name:        name,
		Description: desc,
		InputSchema: schema,
	}
}

// CloneWithout returns a new registry with the same tools in registration
// order, omitting any whose name is listed. Used to build child-session
// registries without the task tool. Copies defer-loading mode, the
// discovered set, and progressive advanced promotions (minus omitted names).
func (r *Registry) CloneWithout(names ...string) *Registry {
	if r == nil {
		return NewRegistry()
	}
	skip := make(map[string]struct{}, len(names))
	for _, name := range names {
		skip[name] = struct{}{}
	}
	tools := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		if _, omit := skip[name]; omit {
			continue
		}
		tools = append(tools, r.tools[name])
	}
	out := NewRegistry(tools...)
	r.mu.RLock()
	deferLoad := r.deferLoad
	var disc map[string]struct{}
	if r.discovered != nil {
		disc = make(map[string]struct{}, len(r.discovered))
		for name := range r.discovered {
			if _, omit := skip[name]; omit {
				continue
			}
			if _, ok := out.tools[name]; ok {
				disc[name] = struct{}{}
			}
		}
	}
	var adv map[string]struct{}
	if r.advanced != nil {
		adv = make(map[string]struct{}, len(r.advanced))
		for name := range r.advanced {
			if _, omit := skip[name]; omit {
				continue
			}
			if _, ok := out.tools[name]; ok {
				adv[name] = struct{}{}
			}
		}
	}
	r.mu.RUnlock()
	out.mu.Lock()
	if deferLoad {
		out.deferLoad = true
		out.discovered = disc
		if out.discovered == nil {
			out.discovered = map[string]struct{}{}
		}
	}
	if len(adv) > 0 {
		out.advanced = adv
	}
	out.mu.Unlock()
	return out
}
