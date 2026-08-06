package secret

import (
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// RedactEvent returns a copy of ev with credential-shaped spans scrubbed from
// string payloads that may reach session JSONL, timeline export, or bundles.
// Structural fields (IDs, names, stop reasons) are preserved.
//
// Callers that tee engine events should redact on the persist path; the engine
// also scrubs tool results before emit so the model never sees raw secrets.
func RedactEvent(ev protocol.Event) protocol.Event {
	if ev == nil {
		return nil
	}
	switch e := ev.(type) {
	case protocol.UserMessage:
		e.Text = redact.String(e.Text)
		return e
	case protocol.TextDelta:
		e.Text = redact.String(e.Text)
		return e
	case protocol.ReasoningDelta:
		e.Text = redact.String(e.Text)
		return e
	case protocol.ToolCallBegin:
		e.Args = redact.JSON(e.Args)
		return e
	case protocol.ToolCallEnd:
		e.Title = redact.String(e.Title)
		e.Output = redact.ScrubToolOutput(e.Output)
		e.Metadata = redact.JSON(e.Metadata)
		return e
	case protocol.ToolCallOutput:
		e.Data = redact.ScrubToolOutput(e.Data)
		return e
	case protocol.ProcessStarted:
		e.Argv = redactStrings(e.Argv)
		return e
	case protocol.ProcessOutput:
		e.Data = redact.ScrubToolOutput(e.Data)
		return e
	case protocol.EngineError:
		e.Message = redact.String(e.Message)
		return e
	case protocol.PermissionAsked:
		e.Patterns = redactStrings(e.Patterns)
		e.Always = redactStrings(e.Always)
		e.Metadata = redact.JSON(e.Metadata)
		return e
	case protocol.PermissionDecided:
		e.Patterns = redactStrings(e.Patterns)
		e.RulePattern = redact.String(e.RulePattern)
		e.Layer = redact.String(e.Layer)
		e.RulePermission = redact.String(e.RulePermission)
		e.RuleAction = redact.String(e.RuleAction)
		return e
	case protocol.AgentMessage:
		e.Body = redact.String(e.Body)
		e.Summary = redact.String(e.Summary)
		return e
	case protocol.ChildStarted:
		e.Prompt = redact.String(e.Prompt)
		if e.ContextBundle != nil {
			b := redactContextBundle(*e.ContextBundle)
			e.ContextBundle = &b
		}
		return e
	case protocol.ChildCompleted:
		e.Summary = redact.String(e.Summary)
		e.Handoff = redactHandoff(e.Handoff)
		if e.Verification != nil {
			rep := redactVerification(*e.Verification)
			e.Verification = &rep
		}
		return e
	case protocol.TurnCompleted:
		if e.Verification != nil {
			rep := redactVerification(*e.Verification)
			e.Verification = &rep
		}
		return e
	case protocol.VerificationCompleted:
		e.Report = redactVerification(e.Report)
		return e
	case protocol.ChildEscalated:
		e.Reason = redact.String(e.Reason)
		if e.Budget != nil && e.Budget.EscalateReason != "" {
			cp := *e.Budget
			cp.EscalateReason = redact.String(cp.EscalateReason)
			e.Budget = &cp
		}
		return e
	case protocol.ArtifactUpdated:
		e.Title = redact.String(e.Title)
		return e
	case protocol.CompactionCompleted:
		e.Summary = redact.String(e.Summary)
		return e
	case protocol.SessionTitled:
		e.Title = redact.String(e.Title)
		return e
	case protocol.EffectivePrompt:
		if len(e.Layers) > 0 {
			layers := make([]protocol.PromptLayerInfo, len(e.Layers))
			copy(layers, e.Layers)
			for i := range layers {
				layers[i].Source = redact.String(layers[i].Source)
				layers[i].Preview = redact.String(layers[i].Preview)
			}
			e.Layers = layers
		}
		return e
	case protocol.DiagnosticBundle:
		e.StrikeVersion = redact.String(e.StrikeVersion)
		e.Note = redact.String(e.Note)
		e.Session.SessionID = redact.String(e.Session.SessionID)
		e.Session.ParentSessionID = redact.String(e.Session.ParentSessionID)
		e.Session.RootSessionID = redact.String(e.Session.RootSessionID)
		if len(e.Prompt.Layers) > 0 {
			layers := make([]protocol.PromptLayerInfo, len(e.Prompt.Layers))
			copy(layers, e.Prompt.Layers)
			for i := range layers {
				layers[i].Source = redact.String(layers[i].Source)
				layers[i].Preview = redact.String(layers[i].Preview)
			}
			e.Prompt.Layers = layers
		}
		e.Config.Provider = redact.String(e.Config.Provider)
		e.Config.Model = redact.String(e.Config.Model)
		e.Config.Agent = redact.String(e.Config.Agent)
		e.Config.WorkDir = redact.String(e.Config.WorkDir)
		e.Config.ProjectRoot = redact.String(e.Config.ProjectRoot)
		e.Config.Compaction.Model = redact.String(e.Config.Compaction.Model)
		e.Warnings = redactStrings(e.Warnings)
		return e
	case protocol.HarnessProgress:
		e.Payload = redact.JSON(e.Payload)
		return e
	default:
		return ev
	}
}

func redactHandoff(h protocol.CompletionHandoff) protocol.CompletionHandoff {
	h.Summary = redact.String(h.Summary)
	h.Verification = redact.String(h.Verification)
	h.RecommendedNextAction = redact.String(h.RecommendedNextAction)
	h.Findings = redactStrings(h.Findings)
	h.Blockers = redactStrings(h.Blockers)
	h.FilesChanged = redactStrings(h.FilesChanged)
	h.Provenance = redactStrings(h.Provenance)
	if len(h.ArtifactRefs) > 0 {
		refs := make([]protocol.ArtifactRef, len(h.ArtifactRefs))
		copy(refs, h.ArtifactRefs)
		for i := range refs {
			refs[i].ID = redact.String(refs[i].ID)
			refs[i].Type = redact.String(refs[i].Type)
		}
		h.ArtifactRefs = refs
	}
	if len(h.MissingContext) > 0 {
		mc := make([]protocol.MissingContextEntry, len(h.MissingContext))
		copy(mc, h.MissingContext)
		for i := range mc {
			mc[i].Kind = redact.String(mc[i].Kind)
			mc[i].Path = redact.String(mc[i].Path)
			mc[i].Question = redact.String(mc[i].Question)
			mc[i].ArtifactID = redact.String(mc[i].ArtifactID)
			mc[i].ItemID = redact.String(mc[i].ItemID)
			mc[i].Detail = redact.String(mc[i].Detail)
		}
		h.MissingContext = mc
	}
	return h
}

func redactContextBundle(b protocol.ContextBundle) protocol.ContextBundle {
	b.Goal = redact.String(b.Goal)
	b.Acceptance = redactStrings(b.Acceptance)
	b.AllowedPaths = redactStrings(b.AllowedPaths)
	b.RequiredPaths = redactStrings(b.RequiredPaths)
	b.Constraints = redactStrings(b.Constraints)
	if len(b.Artifacts) > 0 {
		refs := make([]protocol.ArtifactRef, len(b.Artifacts))
		copy(refs, b.Artifacts)
		for i := range refs {
			refs[i].ID = redact.String(refs[i].ID)
			refs[i].Type = redact.String(refs[i].Type)
		}
		b.Artifacts = refs
	}
	if len(b.Items) > 0 {
		items := make([]protocol.ContextBundleItem, len(b.Items))
		copy(items, b.Items)
		for i := range items {
			items[i].ID = redact.String(items[i].ID)
			items[i].Kind = redact.String(items[i].Kind)
			items[i].Title = redact.String(items[i].Title)
			items[i].Text = redact.String(items[i].Text)
			items[i].Path = redact.String(items[i].Path)
			items[i].Hash = redact.String(items[i].Hash)
			if items[i].Artifact != nil {
				ref := *items[i].Artifact
				ref.ID = redact.String(ref.ID)
				ref.Type = redact.String(ref.Type)
				items[i].Artifact = &ref
			}
		}
		b.Items = items
	}
	if len(b.FilePins) > 0 {
		pins := make([]protocol.ContextFilePin, len(b.FilePins))
		copy(pins, b.FilePins)
		for i := range pins {
			pins[i].Path = redact.String(pins[i].Path)
			pins[i].Hash = redact.String(pins[i].Hash)
			pins[i].Text = redact.String(pins[i].Text)
		}
		b.FilePins = pins
	}
	return b
}

func redactVerification(r protocol.VerificationReport) protocol.VerificationReport {
	r.Summary = redact.String(r.Summary)
	if len(r.Checks) == 0 {
		return r
	}
	checks := make([]protocol.VerificationCheck, len(r.Checks))
	copy(checks, r.Checks)
	for i := range checks {
		checks[i].Output = redact.ScrubToolOutput(checks[i].Output)
		checks[i].Error = redact.String(checks[i].Error)
		checks[i].Name = redact.String(checks[i].Name)
		checks[i].Value = redact.String(checks[i].Value)
	}
	r.Checks = checks
	return r
}

func redactStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = redact.String(s)
	}
	return out
}
