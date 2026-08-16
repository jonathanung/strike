package engine

import (
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// DiagnosticBuilder assembles a redacted DiagnosticBundle from kernel-collected
// input. Product wires this to pkg/diag via enginebind.Diagnostic.
type DiagnosticBuilder func(in DiagnosticBuildInput) protocol.DiagnosticBundle

// DiagnosticBuildInput is the kernel-collected snapshot for /diag.
type DiagnosticBuildInput struct {
	SessionID       string
	ParentSessionID string
	RootSessionID   string
	Depth           int
	Layers          []protocol.PromptLayerInfo
	SystemChars     int
	MessageCount    int
	FromLastStream  bool
	Attribution     protocol.RequestTokenAttribution
	Config          DiagnosticConfig
	ProtocolVersion string
	StrikeVersion   string
}

// DiagnosticConfig is the kernel projection of effective runtime dials.
type DiagnosticConfig struct {
	Provider        string
	Model           string
	Agent           string
	Effort          string
	Autonomy        string
	PermissionMode  string
	Sandbox         string
	LeanCode        string
	Fast            bool
	MaxTokens       int
	MaxChildDepth   int
	ContextWindow   int
	TurnTimeoutS    int
	WorkDir         string
	ProjectRoot     string
	Compaction      DiagnosticCompaction
	SchedulerLimits map[string]int
}

// DiagnosticCompaction is the kernel projection of compaction/prune dials.
type DiagnosticCompaction struct {
	Strategy           string
	Model              string
	Threshold          float64
	Buffer             int
	KeepUserTurns      int
	PruneProtectTokens int
	PruneMinimumTokens int
	PruneKeepUserTurns int
	PruneProtectTools  []string
}

// buildDiagnosticBundleEvent assembles a DiagnosticBundle event from the last
// Stream layer map (or current composition) plus effective dials.
func (e *Engine) buildDiagnosticBundleEvent() protocol.DiagnosticBundle {
	in := e.diagnosticBuildInput()
	var ev protocol.DiagnosticBundle
	if e.opts.BuildDiagnostic != nil {
		ev = e.opts.BuildDiagnostic(in)
	} else {
		ev = protocol.DiagnosticBundle{
			ProtocolVersion: in.ProtocolVersion,
			StrikeVersion:   in.StrikeVersion,
			Session: protocol.DiagnosticSession{
				SessionID:       in.SessionID,
				ParentSessionID: in.ParentSessionID,
				RootSessionID:   in.RootSessionID,
				Depth:           in.Depth,
			},
			Prompt: protocol.DiagnosticPrompt{
				Layers:         append([]protocol.PromptLayerInfo(nil), in.Layers...),
				LayerCount:     len(in.Layers),
				SystemChars:    in.SystemChars,
				MessageCount:   in.MessageCount,
				FromLastStream: in.FromLastStream,
				Attribution:    in.Attribution,
			},
		}
	}
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

func (e *Engine) diagnosticBuildInput() DiagnosticBuildInput {
	snap := e.lastOrCurrentEffective()
	return DiagnosticBuildInput{
		SessionID:       e.opts.SessionID,
		ParentSessionID: e.opts.ParentSessionID,
		RootSessionID:   e.rootSessionID(),
		Depth:           e.opts.Depth,
		Layers:          snap.Layers,
		SystemChars:     snap.SystemChars,
		MessageCount:    snap.MessageCount,
		FromLastStream:  snap.FromLastStream,
		Attribution:     snap.Attribution,
		Config:          e.diagnosticConfig(),
		ProtocolVersion: protocol.Version,
		StrikeVersion:   e.strikeVersion(),
	}
}

func (e *Engine) diagnosticConfig() DiagnosticConfig {
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
	cfg := DiagnosticConfig{
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
		Compaction: DiagnosticCompaction{
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
		cfg.SchedulerLimits = lim
	}
	return cfg
}

func (e *Engine) strikeVersion() string {
	if e == nil {
		return "dev"
	}
	if v := strings.TrimSpace(e.opts.Version); v != "" {
		return v
	}
	return "dev"
}
