package tool

import (
	"github.com/jonathanung/strike-cli/internal/provider"
)

// Registry holds the tools visible to the model. It is the primary
// extension surface: MCP servers (internal/mcp) and plugin tools register here.
type Registry struct {
	order []string
	tools map[string]Tool
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

// Schemas returns the model-facing declarations in registration order.
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

// CloneWithout returns a new registry with the same tools in registration
// order, omitting any whose name is listed. Used to build child-session
// registries without the task tool.
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
	return NewRegistry(tools...)
}
