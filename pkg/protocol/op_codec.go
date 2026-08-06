package protocol

import (
	"encoding/json"
	"fmt"
)

// OpEnvelope wraps an Op with a type tag for WebSocket/HTTP JSON transport.
//
// Version is the wire schema version ([Version]) written by [WrapOp]. Empty on
// decode means a legacy record; treat it as [LegacyVersion].
type OpEnvelope struct {
	Type    string          `json:"type"`
	Version string          `json:"v,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func opType(op Op) string {
	switch op.(type) {
	case UserInput:
		return "user.input"
	case PermissionReply:
		return "permission.reply"
	case QuestionReply:
		return "question.reply"
	case Interrupt:
		return "interrupt"
	case SelectModel:
		return "select.model"
	case SelectAgent:
		return "select.agent"
	case SetEffort:
		return "set.effort"
	case SetAutonomy:
		return "set.autonomy"
	case SetPermissionMode:
		return "set.permission_mode"
	case SetFast:
		return "set.fast"
	case FilesChanged:
		return "files.changed"
	case Compact:
		return "compact"
	case InspectEffectivePrompt:
		return "inspect.prompt"
	case Rewind:
		return "rewind"
	default:
		return ""
	}
}

// WrapOp encodes an op into a transport envelope.
func WrapOp(op Op) (OpEnvelope, error) {
	t := opType(op)
	if t == "" {
		return OpEnvelope{}, fmt.Errorf("protocol: unknown op type %T", op)
	}
	switch op.(type) {
	case Interrupt, InspectEffectivePrompt:
		return OpEnvelope{Type: t, Version: Version}, nil
	}
	data, err := json.Marshal(op)
	if err != nil {
		return OpEnvelope{}, err
	}
	return OpEnvelope{Type: t, Version: Version, Data: data}, nil
}

// SchemaVersion returns the op envelope's wire schema version, defaulting
// empty (legacy) records to [LegacyVersion].
func (e OpEnvelope) SchemaVersion() string {
	if e.Version == "" {
		return LegacyVersion
	}
	return e.Version
}

// Decode reverses WrapOp.
func (e OpEnvelope) Decode() (Op, error) {
	var op Op
	switch e.Type {
	case "user.input":
		op = &UserInput{}
	case "permission.reply":
		op = &PermissionReply{}
	case "question.reply":
		op = &QuestionReply{}
	case "interrupt":
		return Interrupt{}, nil
	case "select.model":
		op = &SelectModel{}
	case "select.agent":
		op = &SelectAgent{}
	case "set.effort":
		op = &SetEffort{}
	case "set.autonomy":
		op = &SetAutonomy{}
	case "set.permission_mode":
		op = &SetPermissionMode{}
	case "set.fast":
		op = &SetFast{}
	case "files.changed":
		op = &FilesChanged{}
	case "compact":
		op = &Compact{}
	case "inspect.prompt":
		return InspectEffectivePrompt{}, nil
	case "rewind":
		op = &Rewind{}
	default:
		return nil, fmt.Errorf("protocol: unknown op envelope type %q", e.Type)
	}
	if len(e.Data) == 0 {
		e.Data = []byte("{}")
	}
	if err := json.Unmarshal(e.Data, op); err != nil {
		return nil, err
	}
	return derefOp(op), nil
}

func derefOp(op Op) Op {
	switch v := op.(type) {
	case *UserInput:
		return *v
	case *PermissionReply:
		return *v
	case *QuestionReply:
		return *v
	case *SelectModel:
		return *v
	case *SelectAgent:
		return *v
	case *SetEffort:
		return *v
	case *SetAutonomy:
		return *v
	case *SetPermissionMode:
		return *v
	case *SetFast:
		return *v
	case *FilesChanged:
		return *v
	case *Compact:
		return *v
	case *Rewind:
		return *v
	default:
		return op
	}
}
