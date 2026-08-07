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
// removed and descriptions compacted for the wire. Used for the provider
// Tools array (every stream, including turn 1) so the model never sees tools
// it cannot call under the active agent/phase/permission profile. The additive
// tools prompt layer uses the same effective name set (see toolGuidanceLayer)
// without restating schemas.
//
// Always-on payload strategy (#436 + #437):
//  1. Subset by hard deny — agent permissions and plan-mode posture drop tools
//     the model must not call (Peek == Deny). Ask/Allow tools stay listed.
//  2. Compact descriptions — tool.CompactSchemaDescription replaces long usage
//     prose with short purposes (skill keeps its available-skills list). Full
//     InputSchema is unchanged; Registry.Schemas keeps full descriptions for
//     toolsearch.
//  3. System guidance is additive only — usage policy / when-to-use tips, not
//     a second name/purpose catalog (#437).
//
// Optional defer_loading (#438/#988): when registry.SetDeferLoading is on
// (config deferTools, default on), non-core tools are also omitted from
// SchemasForProvider until toolsearch, direct call, history re-promote, or
// deterministic workflow activation (#991) discovers them. Core coding tools
// remain always bound on the first stream.
func (e *Engine) effectiveToolSchemas() (schemas []provider.ToolSchema, omitted int) {
	if e == nil || e.opts.Registry == nil {
		return nil, 0
	}
	// Re-promote tools already used in history (resume / --continue).
	e.discoverToolsFromHistory()
	// Deterministic workflow-state activation (#991): plan/child/team families.
	e.lastToolActivation = e.applyWorkflowToolActivation()
	all := e.opts.Registry.SchemasForProvider()
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

// discoverToolsFromHistory promotes deferred tools that already appear as
// assistant tool calls in model history so resume keeps their schemas loaded.
// Progressive tools (e.g. task) restore advanced schema when history args
// required it; basic-only history stays on the compact schema.
func (e *Engine) discoverToolsFromHistory() {
	if e == nil || e.opts.Registry == nil {
		return
	}
	// Progressive schema restore runs even when defer loading is off.
	for _, m := range e.messages {
		for _, c := range m.ToolCalls {
			name := strings.TrimSpace(c.Name)
			if name == "" {
				continue
			}
			if e.opts.Registry.DeferLoading() {
				e.opts.Registry.Discover(name)
			}
			if tool.ArgsNeedAdvancedSchema(name, c.Args) {
				e.opts.Registry.PromoteSchema(name)
			}
		}
	}
}

// toolGuidanceLayer builds the additive Available tools prompt section from
// the live registry after hard permission denies (and defer filtering).
// Schemas carry names and descriptions; this layer is usage policy /
// when-to-use only. Empty when no tools remain.
func (e *Engine) toolGuidanceLayer() (text, source string) {
	schemas, omitted := e.effectiveToolSchemas()
	if len(schemas) == 0 {
		return "", ""
	}
	entries := make([]tool.GuidanceEntry, 0, len(schemas))
	for _, s := range schemas {
		entries = append(entries, tool.GuidanceEntry{Name: s.Name})
	}
	text = tool.BuildGuidance(entries)
	if pending := e.opts.Registry.DeferredPendingCount(); pending > 0 {
		text = strings.TrimRight(text, "\n") + "\n\n" +
			fmt.Sprintf("%d additional tool(s) are deferred — use `toolsearch` to discover them; matches load full schemas on the next model request.\n", pending)
	}
	if strings.TrimSpace(text) == "" {
		return "", ""
	}
	source = "registry:effective"
	if omitted > 0 {
		source = fmt.Sprintf("registry:effective+denied:%d", omitted)
	}
	if e.opts.Registry.DeferLoading() {
		if pending := e.opts.Registry.DeferredPendingCount(); pending > 0 {
			source = fmt.Sprintf("%s+deferred:%d", source, pending)
		} else {
			source = source + "+defer"
		}
	}
	if suf := activationSourceSuffix(e.lastToolActivation); suf != "" {
		source += suf
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
