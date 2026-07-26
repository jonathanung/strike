package engine

import (
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// toolGuidanceLayer builds the effective Available tools section from the
// live registry after hard permission denies. Empty when no tools remain.
func (e *Engine) toolGuidanceLayer() (text, source string) {
	if e == nil || e.opts.Registry == nil {
		return "", ""
	}
	schemas := e.opts.Registry.Schemas()
	if len(schemas) == 0 {
		return "", ""
	}
	entries := make([]tool.GuidanceEntry, 0, len(schemas))
	omitted := 0
	for _, s := range schemas {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		perm := tool.PermissionName(name)
		if e.perms != nil && e.perms.Peek(perm) == permission.Deny {
			omitted++
			continue
		}
		entries = append(entries, tool.GuidanceEntry{
			Name:    name,
			Purpose: tool.ShortPurpose(name, s.Description),
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
