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
	case SessionTitled:
		return "session.titled"
	case TurnStarted:
		return "turn.started"
	case TextDelta:
		return "text.delta"
	case ToolCallBegin:
		return "tool.begin"
	case ToolCallEnd:
		return "tool.end"
	case ToolCallOutput:
		return "tool.output"
	case ProcessStarted:
		return "process.started"
	case ProcessOutput:
		return "process.output"
	case ProcessExited:
		return "process.exited"
	case PermissionAsked:
		return "permission.asked"
	case PermissionResolved:
		return "permission.resolved"
	case QuestionAsked:
		return "question.asked"
	case QuestionResolved:
		return "question.resolved"
	case TurnCompleted:
		return "turn.completed"
	case ModelSelected:
		return "model.selected"
	case AgentSelected:
		return "agent.selected"
	case EffortSelected:
		return "effort.selected"
	case AutonomySelected:
		return "autonomy.selected"
	case FastSelected:
		return "fast.selected"
	case FilesInvalidated:
		return "files.invalidated"
	case EngineError:
		return "engine.error"
	case ChildStarted:
		return "child.started"
	case ChildCompleted:
		return "child.completed"
	case UsageReported:
		return "usage.reported"
	case ProviderRetrying:
		return "provider.retrying"
	case SessionMeta:
		return "session.meta"
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
	case "session.titled":
		ev = &SessionTitled{}
	case "turn.started":
		ev = &TurnStarted{}
	case "text.delta":
		ev = &TextDelta{}
	case "tool.begin":
		ev = &ToolCallBegin{}
	case "tool.end":
		ev = &ToolCallEnd{}
	case "tool.output":
		ev = &ToolCallOutput{}
	case "process.started":
		ev = &ProcessStarted{}
	case "process.output":
		ev = &ProcessOutput{}
	case "process.exited":
		ev = &ProcessExited{}
	case "permission.asked":
		ev = &PermissionAsked{}
	case "permission.resolved":
		ev = &PermissionResolved{}
	case "question.asked":
		ev = &QuestionAsked{}
	case "question.resolved":
		ev = &QuestionResolved{}
	case "turn.completed":
		ev = &TurnCompleted{}
	case "model.selected":
		ev = &ModelSelected{}
	case "agent.selected":
		ev = &AgentSelected{}
	case "effort.selected":
		ev = &EffortSelected{}
	case "autonomy.selected":
		ev = &AutonomySelected{}
	case "fast.selected":
		ev = &FastSelected{}
	case "files.invalidated":
		ev = &FilesInvalidated{}
	case "engine.error":
		ev = &EngineError{}
	case "child.started":
		ev = &ChildStarted{}
	case "child.completed":
		ev = &ChildCompleted{}
	case "usage.reported":
		ev = &UsageReported{}
	case "provider.retrying":
		ev = &ProviderRetrying{}
	case "session.meta":
		ev = &SessionMeta{}
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
	case *SessionTitled:
		return *v
	case *TurnStarted:
		return *v
	case *TextDelta:
		return *v
	case *ToolCallBegin:
		return *v
	case *ToolCallEnd:
		return *v
	case *ToolCallOutput:
		return *v
	case *ProcessStarted:
		return *v
	case *ProcessOutput:
		return *v
	case *ProcessExited:
		return *v
	case *PermissionAsked:
		return *v
	case *PermissionResolved:
		return *v
	case *QuestionAsked:
		return *v
	case *QuestionResolved:
		return *v
	case *TurnCompleted:
		return *v
	case *ModelSelected:
		return *v
	case *AgentSelected:
		return *v
	case *EffortSelected:
		return *v
	case *AutonomySelected:
		return *v
	case *FastSelected:
		return *v
	case *FilesInvalidated:
		return *v
	case *EngineError:
		return *v
	case *ChildStarted:
		return *v
	case *ChildCompleted:
		return *v
	case *UsageReported:
		return *v
	case *ProviderRetrying:
		return *v
	case *SessionMeta:
		return *v
	default:
		return ev
	}
}
