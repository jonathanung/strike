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
	case Steer:
		return "steer"
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
	case StartWorkflow:
		return "workflow.start"
	case StopWorkflow:
		return "workflow.stop"
	case FilesChanged:
		return "files.changed"
	case Compact:
		return "compact"
	case InspectEffectivePrompt:
		return "inspect.prompt"
	case SetContextControls:
		return "context.controls"
	case InspectDiagnosticBundle:
		return "inspect.diagnostic"
	case Rewind:
		return "rewind"
	case TeamSpawn:
		return OpTeamSpawn
	case TeamMessage:
		return OpTeamMessage
	case TeamBroadcast:
		return OpTeamBroadcast
	case TeamChildInterrupt:
		return OpTeamChildInterrupt
	case TeamTaskTransition:
		return OpTeamTaskTransition
	case TeamBoardCreate:
		return OpTeamBoardCreate
	case TeamBoardClaim:
		return OpTeamBoardClaim
	case TeamBoardComplete:
		return OpTeamBoardComplete
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
	case Interrupt, InspectEffectivePrompt, InspectDiagnosticBundle, StopWorkflow:
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
	case "steer":
		op = &Steer{}
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
	case "workflow.start":
		op = &StartWorkflow{}
	case "workflow.stop":
		return StopWorkflow{}, nil
	case "files.changed":
		op = &FilesChanged{}
	case "compact":
		op = &Compact{}
	case "inspect.prompt":
		return InspectEffectivePrompt{}, nil
	case "context.controls":
		op = &SetContextControls{}
	case "inspect.diagnostic":
		return InspectDiagnosticBundle{}, nil
	case "rewind":
		op = &Rewind{}
	case OpTeamSpawn:
		op = &TeamSpawn{}
	case OpTeamMessage:
		op = &TeamMessage{}
	case OpTeamBroadcast:
		op = &TeamBroadcast{}
	case OpTeamChildInterrupt:
		op = &TeamChildInterrupt{}
	case OpTeamTaskTransition:
		op = &TeamTaskTransition{}
	case OpTeamBoardCreate:
		op = &TeamBoardCreate{}
	case OpTeamBoardClaim:
		op = &TeamBoardClaim{}
	case OpTeamBoardComplete:
		op = &TeamBoardComplete{}
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
	case *Steer:
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
	case *StartWorkflow:
		return *v
	case *FilesChanged:
		return *v
	case *Compact:
		return *v
	case *SetContextControls:
		return *v
	case *Rewind:
		return *v
	case *TeamSpawn:
		return *v
	case *TeamMessage:
		return *v
	case *TeamBroadcast:
		return *v
	case *TeamChildInterrupt:
		return *v
	case *TeamTaskTransition:
		return *v
	case *TeamBoardCreate:
		return *v
	case *TeamBoardClaim:
		return *v
	case *TeamBoardComplete:
		return *v
	default:
		return op
	}
}
