package engine

import (
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/version"
	"github.com/jonathanung/strike-cli/pkg/diag"
	pub "github.com/jonathanung/strike-cli/pkg/protocol"
)

// buildDiagnosticBundleEvent assembles a redacted DiagnosticBundle event from
// the last Stream layer map (or current composition) plus effective dials.
func (e *Engine) buildDiagnosticBundleEvent() protocol.DiagnosticBundle {
	snap := e.lastOrCurrentEffective()
	cfg := e.diagnosticConfig()
	b := diag.Build(diag.Input{
		Session: diag.Session{
			SessionID:       e.opts.SessionID,
			ParentSessionID: e.opts.ParentSessionID,
			RootSessionID:   e.rootSessionID(),
			Depth:           e.opts.Depth,
		},
		Layers:          snap.Layers,
		SystemChars:     snap.SystemChars,
		MessageCount:    snap.MessageCount,
		FromLastStream:  snap.FromLastStream,
		Attribution:     snap.Attribution,
		Config:          cfg,
		ProtocolVersion: pub.Version,
		StrikeVersion:   version.Version,
	})
	ev := diag.ToProtocol(b)
	ev.Correlation = e.sessionCorr()
	// Ensure session block carries correlation ids even if Build scrubbed empty.
	if strings.TrimSpace(ev.Session.SessionID) == "" {
		ev.Session.SessionID = e.opts.SessionID
	}
	if strings.TrimSpace(ev.Session.ParentSessionID) == "" {
		ev.Session.ParentSessionID = e.opts.ParentSessionID
	}
	if strings.TrimSpace(ev.Session.RootSessionID) == "" {
		ev.Session.RootSessionID = e.rootSessionID()
	}
	ev.Session.Depth = e.opts.Depth
	if e.opts.ParentSessionID != "" || e.opts.Depth > 0 {
		ev.Session.IsChild = true
	}
	return ev
}

func (e *Engine) diagnosticConfig() diag.Config {
	sandboxMode := strings.TrimSpace(e.opts.SandboxMode)
	if sandboxMode == "" {
		sandboxMode = "workspace-write"
	}
	lean := strings.TrimSpace(e.opts.LeanCode)
	if lean == "" {
		lean = "lite"
	}
	// Resolve zero-means-default dials so the bundle matches runtime behavior
	// (raw 0 would look like "disabled" for threshold/buffer).
	threshold := e.opts.CompactionThreshold
	if threshold <= 0 {
		threshold = defaultCompactionThreshold
	}
	buffer := e.opts.CompactionBuffer
	if buffer <= 0 {
		buffer = defaultCompactionBuffer
	}
	keep := e.opts.KeepUserTurns
	if keep < 1 {
		keep = defaultKeepUserTurns
	}
	// Turn timeout: report effective seconds for inspect. Engine zero means
	// disabled (-1 on the wire); positive duration → whole seconds.
	turnTimeoutS := -1
	if e.opts.TurnTimeout > 0 {
		turnTimeoutS = int(e.opts.TurnTimeout / time.Second)
		if turnTimeoutS < 1 {
			turnTimeoutS = 1
		}
	}
	cfg := diag.Config{
		Provider:       e.provName,
		Model:          e.model,
		Agent:          e.agent.Name,
		Effort:         string(e.effort),
		Autonomy:       string(e.autonomy),
		PermissionMode: string(e.permMode),
		Sandbox:        sandboxMode,
		LeanCode:       lean,
		Fast:           e.priority,
		MaxTokens:      e.opts.MaxTokens,
		MaxChildDepth:  e.opts.MaxChildDepth,
		ContextWindow:  e.contextWindow(),
		TurnTimeoutS:   turnTimeoutS,
		WorkDir:        e.opts.WorkDir,
		ProjectRoot:    e.opts.ProjectRoot,
		Compaction: diag.Compaction{
			Strategy:           resolveCompactionStrategy(e.opts.CompactionStrategy),
			Model:              strings.TrimSpace(e.opts.CompactionModel),
			Threshold:          threshold,
			Buffer:             buffer,
			KeepUserTurns:      keep,
			PruneProtectTokens: e.opts.PruneProtectTokens,
			PruneMinimumTokens: e.opts.PruneMinimumTokens,
			PruneKeepUserTurns: e.opts.PruneKeepUserTurns,
			PruneProtectTools:  append([]string(nil), e.opts.PruneProtectTools...),
		},
	}
	if e.opts.SchedulerPolicy != nil && len(e.opts.SchedulerPolicy.Limits) > 0 {
		lim := make(map[string]int, len(e.opts.SchedulerPolicy.Limits))
		for k, v := range e.opts.SchedulerPolicy.Limits {
			lim[k] = v
		}
		cfg.Scheduler.Limits = lim
	}
	return cfg
}
