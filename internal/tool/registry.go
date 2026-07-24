package tool

import (
	"github.com/jonathanung/strike-cli/internal/provider"
)

// Registry holds the tools visible to the model. It is the primary
// extension surface: MCP servers and plugin tools register here later.
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

func (r *Registry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Schemas returns the model-facing declarations in registration order.
func (r *Registry) Schemas() []provider.ToolSchema {
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
