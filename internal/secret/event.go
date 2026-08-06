package secret

import (
	"encoding/json"

	"github.com/jonathanung/strike-cli/internal/protocol"
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
		e.Text = Redact(e.Text)
		return e
	case protocol.TextDelta:
		e.Text = Redact(e.Text)
		return e
	case protocol.ReasoningDelta:
		e.Text = Redact(e.Text)
		return e
	case protocol.ToolCallBegin:
		e.Args = redactJSON(e.Args)
		return e
	case protocol.ToolCallEnd:
		e.Title = Redact(e.Title)
		e.Output = ScrubToolOutput(e.Output)
		e.Metadata = redactJSON(e.Metadata)
		return e
	case protocol.ToolCallOutput:
		e.Data = ScrubToolOutput(e.Data)
		return e
	case protocol.ProcessStarted:
		e.Argv = redactStrings(e.Argv)
		return e
	case protocol.ProcessOutput:
		e.Data = ScrubToolOutput(e.Data)
		return e
	case protocol.EngineError:
		e.Message = Redact(e.Message)
		return e
	case protocol.PermissionAsked:
		e.Patterns = redactStrings(e.Patterns)
		e.Always = redactStrings(e.Always)
		e.Metadata = redactJSON(e.Metadata)
		return e
	case protocol.AgentMessage:
		e.Body = Redact(e.Body)
		e.Summary = Redact(e.Summary)
		return e
	case protocol.ChildCompleted:
		e.Summary = Redact(e.Summary)
		e.Handoff = redactHandoff(e.Handoff)
		return e
	case protocol.CompactionCompleted:
		e.Summary = Redact(e.Summary)
		return e
	case protocol.SessionTitled:
		e.Title = Redact(e.Title)
		return e
	case protocol.EffectivePrompt:
		if len(e.Layers) > 0 {
			layers := make([]protocol.PromptLayerInfo, len(e.Layers))
			copy(layers, e.Layers)
			for i := range layers {
				layers[i].Source = Redact(layers[i].Source)
				layers[i].Preview = Redact(layers[i].Preview)
			}
			e.Layers = layers
		}
		return e
	case protocol.HarnessProgress:
		e.Payload = redactJSON(e.Payload)
		return e
	default:
		return ev
	}
}

func redactHandoff(h protocol.CompletionHandoff) protocol.CompletionHandoff {
	h.Summary = Redact(h.Summary)
	h.Verification = Redact(h.Verification)
	h.RecommendedNextAction = Redact(h.RecommendedNextAction)
	h.Findings = redactStrings(h.Findings)
	h.Blockers = redactStrings(h.Blockers)
	// FilesChanged are paths — redact only if a path somehow embeds a token.
	h.FilesChanged = redactStrings(h.FilesChanged)
	return h
}

func redactStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = Redact(s)
	}
	return out
}

// RedactJSON walks a JSON value and redacts string leaves (and known
// credential object fields). Invalid JSON is redacted as a text blob.
// Safe for tool-call args previews and metadata on egress paths.
func RedactJSON(raw json.RawMessage) json.RawMessage {
	return redactJSON(raw)
}

// redactJSON walks a JSON value and redacts string leaves. Invalid JSON is
// redacted as a raw string blob.
func redactJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// Not JSON — treat as text.
		return json.RawMessage(jsonQuote(Redact(string(raw))))
	}
	v = redactJSONValue(v)
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`"[REDACTED]"`)
	}
	return b
}

func redactJSONValue(v any) any {
	switch t := v.(type) {
	case string:
		return Redact(t)
	case map[string]any:
		for k, child := range t {
			// Known credential field names: always placeholder the value.
			if isCredentialField(k) {
				if s, ok := child.(string); ok && s != "" {
					t[k] = Placeholder
					continue
				}
			}
			t[k] = redactJSONValue(child)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = redactJSONValue(child)
		}
		return t
	default:
		return v
	}
}

func isCredentialField(k string) bool {
	switch k {
	case "apiKey", "access", "refresh", "idToken", "clientSecret", "token",
		"password", "secret", "authorization", "Authorization":
		return true
	default:
		return false
	}
}

func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `"[REDACTED]"`
	}
	return string(b)
}
