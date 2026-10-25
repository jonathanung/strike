package engine

import (
	"fmt"
	"strings"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/harness/verify"
)

// childHandoffRequired resolves whether a delegated child must pass
// schema:handoff (#1201). Per-spawn override wins, then the spawned agent's
// frontmatter, then the session/engine option. Nil at every layer means required.
func childHandoffRequired(session, agent, spawn *bool) bool {
	if spawn != nil {
		return *spawn
	}
	if agent != nil {
		return *agent
	}
	if session != nil {
		return *session
	}
	return true
}

func hasSchemaHandoff(gates []tool.VerifyGate) bool {
	for _, g := range gates {
		if strings.EqualFold(strings.TrimSpace(g.Kind), verify.KindSchema) &&
			strings.EqualFold(strings.TrimSpace(g.Value), "handoff") {
			return true
		}
	}
	return false
}

// withDefaultHandoffGate appends schema:handoff when required and not already
// present. Explicit caller gates are kept (union). The default counts toward
// verify.MaxGates.
func withDefaultHandoffGate(gates []tool.VerifyGate, required bool) ([]tool.VerifyGate, error) {
	if !required || hasSchemaHandoff(gates) {
		return gates, nil
	}
	if len(gates) >= verify.MaxGates {
		return nil, fmt.Errorf("verify: default schema:handoff would exceed %d gates; drop a gate or set handoff_required to false", verify.MaxGates)
	}
	out := make([]tool.VerifyGate, len(gates), len(gates)+1)
	copy(out, gates)
	out = append(out, tool.VerifyGate{Kind: verify.KindSchema, Value: "handoff"})
	return out, nil
}
