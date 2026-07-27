package engine

import (
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// effectiveToolSchemas returns registry tool schemas with hard-denied tools
// removed and descriptions compacted for the wire. Used for both the provider
// Tools array (every stream, including turn 1) and the Available tools prompt
// layer so the model never sees tools it cannot call under the active
// agent/phase/permission profile.
//
// Always-on payload strategy (#436):
//  1. Subset by hard deny — agent permissions and plan-mode posture drop tools
//     the model must not call (Peek == Deny). Ask/Allow tools stay listed.
//  2. Compact descriptions — tool.CompactSchemaDescription replaces long usage
//     prose with short purposes (skill keeps its available-skills list). Full
//     InputSchema is unchanged; Registry.Schemas keeps full descriptions for
//     toolsearch. This is not defer_loading (#438): every remaining tool is
//     still bound on the first stream.
func (e *Engine) effectiveToolSchemas() (schemas []provider.ToolSchema, omitted int) {
	if e == nil || e.opts.Registry == nil {
		return nil, 0
	}
	all := e.opts.Registry.Schemas()
	if len(all) == 0 {
		return nil, 0
	}
	out := make([]provider.ToolSchema, 0, len(all))
	for _, s := range all {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		perm := tool.PermissionName(name)
		if e.perms != nil && e.perms.Peek(perm) == permission.Deny {
			omitted++
			continue
		}
		s.Description = tool.CompactSchemaDescription(name, s.Description)
		out = append(out, s)
	}
	return out, omitted
}

// toolGuidanceLayer builds the effective Available tools section from the
// live registry after hard permission denies. Empty when no tools remain.
func (e *Engine) toolGuidanceLayer() (text, source string) {
	schemas, omitted := e.effectiveToolSchemas()
	if len(schemas) == 0 {
		return "", ""
	}
	entries := make([]tool.GuidanceEntry, 0, len(schemas))
	for _, s := range schemas {
		entries = append(entries, tool.GuidanceEntry{
			Name:    s.Name,
			Purpose: tool.ShortPurpose(s.Name, s.Description),
		})
	}
	text = tool.BuildGuidance(entries)
	if strings.TrimSpace(text) == "" {
		return "", ""
	}
	source = "registry:effective"
	if omitted > 0 {
		source = fmt.Sprintf("registry:effective+denied:%d", omitted)
	}
	return text, source
}

// appendToolGuidanceLayer inserts the tools layer after shared when present.
func appendToolGuidanceLayer(e *Engine, layers []promptLayer) []promptLayer {
	text, source := e.toolGuidanceLayer()
	if text == "" {
		return layers
	}
	return append(layers, promptLayer{
		Kind:   protocol.PromptLayerTools,
		Source: source,
		Mode:   protocol.PromptLayerAppend,
		Text:   text,
	})
}
