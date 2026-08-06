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
	case protocol.AgentMessage:
		e.Body = redact.String(e.Body)
		e.Summary = redact.String(e.Summary)
		return e
	case protocol.ChildCompleted:
		e.Summary = redact.String(e.Summary)
		e.Handoff = redactHandoff(e.Handoff)
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
	if len(h.ArtifactRefs) > 0 {
		refs := make([]protocol.ArtifactRef, len(h.ArtifactRefs))
		copy(refs, h.ArtifactRefs)
		for i := range refs {
			refs[i].ID = redact.String(refs[i].ID)
			refs[i].Type = redact.String(refs[i].Type)
		}
		h.ArtifactRefs = refs
	}
	return h
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
