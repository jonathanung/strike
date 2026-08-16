package enginebind

import (
	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/pkg/diag"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// Diagnostic returns the product diagnostic-bundle builder (pkg/diag).
// The engine cannot import pkg/diag without a module cycle through the root.
func Diagnostic() engine.DiagnosticBuilder {
	return func(in engine.DiagnosticBuildInput) protocol.DiagnosticBundle {
		b := diag.Build(diag.Input{
			Session: diag.Session{
				SessionID:       in.SessionID,
				ParentSessionID: in.ParentSessionID,
				RootSessionID:   in.RootSessionID,
				Depth:           in.Depth,
			},
			Layers:          in.Layers,
			SystemChars:     in.SystemChars,
			MessageCount:    in.MessageCount,
			FromLastStream:  in.FromLastStream,
			Attribution:     in.Attribution,
			Config:          diagnosticConfig(in.Config),
			ProtocolVersion: in.ProtocolVersion,
			StrikeVersion:   in.StrikeVersion,
		})
		return diag.ToProtocol(b)
	}
}

func diagnosticConfig(c engine.DiagnosticConfig) diag.Config {
	out := diag.Config{
		Provider:       c.Provider,
		Model:          c.Model,
		Agent:          c.Agent,
		Effort:         c.Effort,
		Autonomy:       c.Autonomy,
		PermissionMode: c.PermissionMode,
		Sandbox:        c.Sandbox,
		LeanCode:       c.LeanCode,
		Fast:           c.Fast,
		MaxTokens:      c.MaxTokens,
		MaxChildDepth:  c.MaxChildDepth,
		ContextWindow:  c.ContextWindow,
		TurnTimeoutS:   c.TurnTimeoutS,
		WorkDir:        c.WorkDir,
		ProjectRoot:    c.ProjectRoot,
		Compaction: diag.Compaction{
			Strategy:           c.Compaction.Strategy,
			Model:              c.Compaction.Model,
			Threshold:          c.Compaction.Threshold,
			Buffer:             c.Compaction.Buffer,
			KeepUserTurns:      c.Compaction.KeepUserTurns,
			PruneProtectTokens: c.Compaction.PruneProtectTokens,
			PruneMinimumTokens: c.Compaction.PruneMinimumTokens,
			PruneKeepUserTurns: c.Compaction.PruneKeepUserTurns,
			PruneProtectTools:  append([]string(nil), c.Compaction.PruneProtectTools...),
		},
	}
	if len(c.SchedulerLimits) > 0 {
		lim := make(map[string]int, len(c.SchedulerLimits))
		for k, v := range c.SchedulerLimits {
			lim[k] = v
		}
		out.Scheduler.Limits = lim
	}
	return out
}
