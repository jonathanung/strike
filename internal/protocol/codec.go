package protocol

import (
	"encoding/json"
	"fmt"
	"time"
)

// Envelope wraps an Event with a type tag and timestamp for JSONL
// persistence and replay.
type Envelope struct {
	Type string          `json:"type"`
	Time time.Time       `json:"time"`
	Data json.RawMessage `json:"data"`
}

func eventType(ev Event) string {
	switch ev.(type) {
	case UserMessage:
		return "user.message"
	case TurnStarted:
		return "turn.started"
	case TextDelta:
		return "text.delta"
	case ToolCallBegin:
		return "tool.begin"
	case ToolCallEnd:
		return "tool.end"
	case PermissionAsked:
		return "permission.asked"
	case PermissionResolved:
		return "permission.resolved"
	case TurnCompleted:
		return "turn.completed"
	case ModelSelected:
		return "model.selected"
	case AgentSelected:
		return "agent.selected"
	case EngineError:
		return "engine.error"
	default:
		return ""
	}
}

// Wrap encodes an event into a persistable envelope.
func Wrap(ev Event) (Envelope, error) {
	t := eventType(ev)
	if t == "" {
		return Envelope{}, fmt.Errorf("protocol: unknown event type %T", ev)
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Type: t, Time: time.Now().UTC(), Data: data}, nil
}

// Decode reverses Wrap.
func (e Envelope) Decode() (Event, error) {
	var ev Event
	switch e.Type {
	case "user.message":
		ev = &UserMessage{}
	case "turn.started":
		ev = &TurnStarted{}
	case "text.delta":
		ev = &TextDelta{}
	case "tool.begin":
		ev = &ToolCallBegin{}
	case "tool.end":
		ev = &ToolCallEnd{}
	case "permission.asked":
		ev = &PermissionAsked{}
	case "permission.resolved":
		ev = &PermissionResolved{}
	case "turn.completed":
		ev = &TurnCompleted{}
	case "model.selected":
		ev = &ModelSelected{}
	case "agent.selected":
		ev = &AgentSelected{}
	case "engine.error":
		ev = &EngineError{}
	default:
		return nil, fmt.Errorf("protocol: unknown envelope type %q", e.Type)
	}
	if err := json.Unmarshal(e.Data, ev); err != nil {
		return nil, err
	}
	return deref(ev), nil
}

func deref(ev Event) Event {
	switch v := ev.(type) {
	case *UserMessage:
		return *v
	case *TurnStarted:
		return *v
	case *TextDelta:
		return *v
	case *ToolCallBegin:
		return *v
	case *ToolCallEnd:
		return *v
	case *PermissionAsked:
		return *v
	case *PermissionResolved:
		return *v
	case *TurnCompleted:
		return *v
	case *ModelSelected:
		return *v
	case *AgentSelected:
		return *v
	case *EngineError:
		return *v
	default:
		return ev
	}
}
