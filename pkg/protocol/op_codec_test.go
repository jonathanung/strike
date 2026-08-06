package protocol

import (
	"encoding/json"
	"testing"
)

func TestWrapOpDecodeRoundTrip(t *testing.T) {
	cases := []Op{
		UserInput{Text: "hi"},
		PermissionReply{RequestID: "p1", Decision: DecisionOnce},
		QuestionReply{RequestID: "q1", Answers: []string{"a"}},
		Interrupt{},
		SelectModel{Provider: "echo", Model: "echo"},
		SelectAgent{Name: "build"},
		SetEffort{Level: EffortLow},
		SetAutonomy{Mode: AutonomyAgent},
		SetPermissionMode{Mode: PermissionModeYolo},
		SetFast{Enabled: true},
		StartWorkflow{Name: "review-fix"},
		StopWorkflow{},
		Compact{Strategy: "trim"},
		InspectEffectivePrompt{},
		SetContextControls{
			ExcludeKinds: []string{PromptLayerMemory},
			SetExclude:   true,
			PinKinds:     []string{PromptLayerPersona},
			SetPin:       true,
		},
		Rewind{RestoreFiles: true},
	}
	for _, op := range cases {
		env, err := WrapOp(op)
		if err != nil {
			t.Fatalf("WrapOp(%T): %v", op, err)
		}
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		var back OpEnvelope
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		got, err := back.Decode()
		if err != nil {
			t.Fatalf("Decode %q: %v", env.Type, err)
		}
		if opType(got) != opType(op) {
			t.Fatalf("type %T -> %T", op, got)
		}
	}
}

func TestDecodeOpUnknown(t *testing.T) {
	if _, err := (OpEnvelope{Type: "nope"}).Decode(); err == nil {
		t.Fatal("want error")
	}
}
