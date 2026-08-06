package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestWrapDecodeRoundTrip(t *testing.T) {
	corr := Correlation{SessionID: "session-1", TurnID: "turn-1", ProviderRequestID: "provider-1"}
	childCorr := Correlation{SessionID: "child-1", ParentSessionID: "session-1", Depth: 1}
	events := []Event{
		UserMessage{Correlation: corr, Text: "hi"},
		SessionTitled{Correlation: Correlation{SessionID: "session-1"}, Title: "hi"},
		TurnStarted{Correlation: corr},
		TextDelta{Correlation: corr, Text: "chunk"},
		ReasoningDelta{Correlation: corr, Text: "let me think"},
		ToolCallBegin{Correlation: corr, CallID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"echo"}`)},
		ToolCallOutput{Correlation: corr, CallID: "c1", Data: "ok\n"},
		ToolCallEnd{Correlation: corr, CallID: "c1", Title: "echo", Output: "ok", IsError: false, Metadata: json.RawMessage(`{"exitCode":0}`)},
		ToolCallEnd{Correlation: corr, CallID: "c2", Title: "sleep", Output: "partial\n(incomplete: tool call canceled because the turn was interrupted.)", IsError: true, ErrorCode: ErrorCodeCanceled, Metadata: json.RawMessage(`{"incomplete":true}`)},
		ToolCallEnd{Correlation: corr, CallID: "c3", Title: "sleep", Output: "(command timed out after 1s)", IsError: true, ErrorCode: ErrorCodeTimeout},
		ToolCallEnd{Correlation: corr, CallID: "c4", Title: "edit", Output: "Permission denied.", IsError: true, ErrorCode: ErrorCodePermissionDenied},
		EngineError{Correlation: corr, Message: "input queue full; wait for the current turn to finish", Code: ErrorCodeQueueFull},
		ProcessStarted{Correlation: corr, ProcessID: "p1", CallID: "c1", Argv: []string{"bash", "-c", "echo"}, Cwd: "/tmp"},
		ProcessOutput{Correlation: corr, ProcessID: "p1", Stream: ProcessStreamStdout, Data: "ok\n"},
		ProcessExited{Correlation: corr, ProcessID: "p1", ExitCode: 0, Status: ProcessStatusExited},
		PermissionAsked{Correlation: corr, RequestID: "p1", Permission: "bash", Patterns: []string{"echo hi"}, Always: []string{"echo *"}},
		PermissionResolved{Correlation: corr, RequestID: "p1", Decision: DecisionOnce},
		QuestionAsked{Correlation: corr, RequestID: "q1", Questions: []QuestionPrompt{
			{ID: "pref", Header: "Style", Question: "Which style?", Options: []QuestionOption{
				{Label: "A", Description: "option a"},
				{Label: "B", Description: "option b"},
			}},
		}},
		QuestionResolved{Correlation: corr, RequestID: "q1"},
		TurnCompleted{Correlation: corr, StopReason: "end_turn", Files: []TurnFileChange{
			{Path: "a.go", Kind: "create"},
			{Path: "b.go", Kind: "update"},
		}, CheckpointSkipped: 1, Uncovered: []string{"bash"}},
		ModelSelected{Correlation: corr, Provider: "echo", Model: "echo"},
		AgentSelected{Correlation: corr, Name: "build"},
		PhaseChanged{Correlation: corr, Workflow: "plan-implement", Phase: "plan", Index: 0, Gate: "user", Source: "builtin", Fingerprint: "abc123"},
		PhaseChanged{Correlation: corr, Workflow: "gone", Phase: "step", Index: 0, Fingerprint: "old", Status: PhaseStatusMissing},
		PlanHandoff{
			Correlation:    corr,
			PlanID:         "abcd1234",
			PlanVersion:    3,
			ApprovalSource: PlanApprovalUser,
			Title:          "Ship feature",
			Agent:          "build",
		},
		PlanHandoff{
			Correlation:    corr,
			ApprovalSource: PlanApprovalSkipAll,
			Title:          "legacy plan",
			Agent:          "orchestrator",
			LegacyText:     "1. do the thing",
		},
		PhaseGrantApproved{
			Correlation: corr,
			Workflow:    "plan-implement",
			Phase:       "implement",
			Index:       1,
			Fingerprint: "abc123",
			Grants:      []PhaseGrantRule{{Permission: "bash", Pattern: "*", Action: "allow"}},
			Auto:        true,
		},
		EffortSelected{Correlation: corr, Level: EffortXHigh},
		AutonomySelected{Correlation: corr, Mode: AutonomyAgent},
		PermissionModeSelected{Correlation: corr, Mode: PermissionModeYolo},
		FastSelected{Correlation: corr, Enabled: true},
		FilesInvalidated{Correlation: corr, Paths: []string{"a.go", "b.go"}, Reason: "external_editor"},
		PathOverlap{
			Correlation: childCorr,
			Path:        "internal/foo.go",
			Policy:      "warn",
			Holders: []PathOverlapHolder{
				{SessionID: "session-1", Name: "lead", Source: "touch"},
			},
			Warning: "path overlap on internal/foo.go",
		},
		EngineError{Correlation: corr, Message: "boom"},
		ChildStarted{Correlation: childCorr, Agent: "build", Prompt: "do the subtask"},
		ChildCompleted{Correlation: childCorr, Status: ChildStatusCompleted, Summary: "done"},
		AgentMessage{
			Correlation: childCorr,
			From:        "session-1",
			To:          "child-1",
			Body:        "handoff package path",
			Summary:     "handoff",
			TeamID:      "session-1",
			MessageID:   "msg-1",
			TaskID:      "d1",
			Urgency:     AgentUrgencyHigh,
			Kind:        AgentMessageKindRequest,
			RequireAck:  true,
			EscalateTo:  "session-1",
			AckStatus:   "pending",
		},
		AgentContractTimeout{
			Correlation: childCorr,
			MessageID:   "msg-1",
			From:        "session-1",
			To:          "child-1",
			TaskID:      "d1",
			TeamID:      "session-1",
			Urgency:     AgentUrgencyHigh,
			EscalateTo:  "session-1",
			Detail:      "ack timed out",
		},
		TeamRoster{
			Correlation: Correlation{SessionID: "session-1"},
			LeadID:      "session-1",
			Members: []TeamRosterMember{
				{SessionID: "session-1", Agent: "build", State: "working", Role: "lead"},
				{SessionID: "child-1", Agent: "explore", State: "completed", ParentSessionID: "session-1", Depth: 1, Role: "member", TerminalSummary: "done"},
			},
		},
		UsageReported{
			Correlation: corr,
			Input:       KnownTokens(100),
			Output:      KnownTokens(50),
			Used:        KnownTokens(150),
			Source:      UsageSourceActual,
		},
		ProviderRetrying{
			Correlation: Correlation{SessionID: "session-1", TurnID: "turn-1", ProviderRequestID: "provider-1", Attempt: 1},
			NextAttempt: 2,
			DelayMs:     200,
			Message:     "rate limited",
		},
		ToolRetrying{
			Correlation: corr,
			CallID:      "c-retry",
			Name:        "webfetch",
			NextAttempt: 2,
			DelayMs:     100,
			ErrorCode:   ErrorCodeTransient,
			Message:     "connection reset",
		},
		ToolLoopDetected{
			Correlation: corr,
			Reason:      "identical_calls",
			ToolName:    "read",
			Count:       3,
			Message:     "repeated identical failing tool calls",
		},
		CompactionStarted{Correlation: corr, Reason: CompactionReasonManual, Strategy: CompactionStrategySummarize},
		CompactionCompleted{
			Correlation: corr,
			Reason:      CompactionReasonThreshold,
			Strategy:    CompactionStrategyTrim,
			Removed:     4,
			Kept:        3,
			Summary:     "prior work on foo",
			Residue: &CompactionResidue{
				SchemaVersion: CompactionResidueSchemaVersion,
				Strategy:      CompactionStrategyTrim,
				Reason:        CompactionReasonThreshold,
				Removed:       4,
				Decisions: []ResidueItem{{
					ID:         "decision-use-api-hist-0",
					Kind:       ResidueKindDecision,
					Text:       "use API X",
					Confidence: "high",
					Freshness:  "fresh",
					SourceIDs:  []string{"hist:0", "ledger:abc"},
					LedgerID:   "abc",
				}},
				Facts: []ResidueItem{{
					ID:        "fact-path-hist-1",
					Kind:      ResidueKindFact,
					Text:      "main entry is cmd/strike/main.go",
					SourceIDs: []string{"hist:1"},
					FileRefs:  []string{"cmd/strike/main.go"},
				}},
				OpenQuestions: []ResidueItem{{
					ID:        "open_question-flaky-hist-2",
					Kind:      ResidueKindOpenQuestion,
					Text:      "are tests flaky on CI?",
					SourceIDs: []string{"hist:2"},
				}},
				PinnedKinds: []string{"project_memory"},
			},
		},
		SessionMeta{Correlation: corr, PRURL: "https://github.com/acme/repo/pull/7", PRNumber: 7, PRState: "open"},
		SessionRewound{Correlation: corr, Removed: 2, TurnID: "turn-9", RestoreFiles: true, FilesRestored: 3, FilesSkipped: 1, Files: []string{"a.go", "b.go"}, Uncovered: []string{"bash"}},
		EffectivePrompt{
			Correlation: corr,
			Layers: []PromptLayerInfo{
				{Kind: PromptLayerShared, Source: "builtin:shared", Mode: PromptLayerAppend, Chars: 12, EstTokens: 3, Preview: "You are strike"},
			},
			SystemChars:    100,
			MessageCount:   2,
			FromLastStream: true,
			Attribution: RequestTokenAttribution{
				System:      KnownTokens(25),
				Tools:       KnownTokens(40),
				Messages:    KnownTokens(10),
				ToolResults: KnownTokens(5),
				Total:       KnownTokens(80),
				Source:      UsageSourceEstimated,
			},
			ExcludedKinds: []string{PromptLayerMemory},
			PinnedKinds:   []string{PromptLayerPersona},
			ShedKinds:     []string{PromptLayerLean},
		},
		ContextFitWarning{
			Correlation:     corr,
			EstimatedTokens: 180_000,
			ContextLimit:    200_000,
			Level:           ContextFitWarn,
			Message:         "projected prompt ~180k tok is ≥80% of the 200k context window",
			Source:          UsageSourceEstimated,
		},
		ContextControlsSelected{
			Correlation:   corr,
			ExcludedKinds: []string{PromptLayerMemory},
			PinnedKinds:   []string{PromptLayerPersona},
		},
		DiagnosticBundle{
			Correlation:     corr,
			SchemaVersion:   "1.0.0",
			ProtocolVersion: Version,
			ExportedAt:      time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
			Redacted:        true,
			Session: DiagnosticSession{
				SessionID: "session-1", RootSessionID: "session-1", Depth: 0,
			},
			Prompt: DiagnosticPrompt{
				Precedence:  []string{PromptLayerShared},
				Layers:      []PromptLayerInfo{{Kind: PromptLayerShared, Source: "builtin:shared", Mode: PromptLayerAppend, Chars: 12}},
				LayerCount:  1,
				SystemChars: 12,
			},
			Config: DiagnosticConfig{
				Provider: "echo", Model: "echo", Agent: "build",
				Digests: map[string]string{"effective": "abc"},
			},
		},
	}
	for _, want := range events {
		env, err := Wrap(want)
		if err != nil {
			t.Fatalf("Wrap(%T): %v", want, err)
		}
		if env.Type == "" {
			t.Fatalf("empty type for %T", want)
		}
		if env.Time.IsZero() || env.Time.Location() != time.UTC {
			t.Errorf("time = %v, want non-zero UTC", env.Time)
		}
		got, err := env.Decode()
		if err != nil {
			t.Fatalf("Decode %s: %v", env.Type, err)
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("%s: got %s, want %s", env.Type, gotJSON, wantJSON)
		}
	}
}

func TestFastSelectedJSONIsFlatAndOptional(t *testing.T) {
	b, err := json.Marshal(FastSelected{
		Correlation: Correlation{SessionID: "session-1", TurnID: "turn-1", ProviderRequestID: "provider-1"},
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"sessionId":         `"session-1"`,
		"turnId":            `"turn-1"`,
		"providerRequestId": `"provider-1"`,
		"enabled":           `true`,
	} {
		if string(got[key]) != want {
			t.Errorf("%s = %s, want %s; JSON: %s", key, got[key], want, b)
		}
	}
	if _, ok := got["correlation"]; ok {
		t.Errorf("correlation must not be nested: %s", b)
	}
}

func TestCorrelationJSONIsFlatAndOptional(t *testing.T) {
	ev := PermissionAsked{
		Correlation: Correlation{
			SessionID:         "session-1",
			TurnID:            "turn-1",
			ProviderRequestID: "provider-1",
		},
		RequestID:  "permission-1",
		Permission: "bash",
		Patterns:   []string{"echo hi"},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"sessionId":         `"session-1"`,
		"turnId":            `"turn-1"`,
		"providerRequestId": `"provider-1"`,
		"requestId":         `"permission-1"`,
	}
	for key, value := range want {
		if string(got[key]) != value {
			t.Errorf("%s = %s, want %s; JSON: %s", key, got[key], value, b)
		}
	}
	if _, ok := got["correlation"]; ok {
		t.Errorf("correlation must not be nested: %s", b)
	}

	empty, err := json.Marshal(TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	if string(empty) != `{}` {
		t.Errorf("empty correlation JSON = %s, want {}", empty)
	}
}

func TestCorrelationParentSessionIDAndDepthJSON(t *testing.T) {
	withLineage := ChildStarted{
		Correlation: Correlation{
			SessionID:       "child-1",
			ParentSessionID: "parent-1",
			Depth:           1,
		},
		Agent:  "build",
		Prompt: "subtask",
	}
	b, err := json.Marshal(withLineage)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"sessionId":       `"child-1"`,
		"parentSessionId": `"parent-1"`,
		"depth":           `1`,
		"agent":           `"build"`,
		"prompt":          `"subtask"`,
	} {
		if string(got[key]) != want {
			t.Errorf("%s = %s, want %s; JSON: %s", key, got[key], want, b)
		}
	}
	if _, ok := got["correlation"]; ok {
		t.Errorf("correlation must not be nested: %s", b)
	}

	// omitempty: zero ParentSessionID and Depth must not appear.
	root := TurnStarted{Correlation: Correlation{SessionID: "root-1"}}
	rootJSON, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	var rootMap map[string]json.RawMessage
	if err := json.Unmarshal(rootJSON, &rootMap); err != nil {
		t.Fatal(err)
	}
	if _, ok := rootMap["parentSessionId"]; ok {
		t.Errorf("parentSessionId present when empty: %s", rootJSON)
	}
	if _, ok := rootMap["depth"]; ok {
		t.Errorf("depth present when zero: %s", rootJSON)
	}
	if string(rootMap["sessionId"]) != `"root-1"` {
		t.Errorf("sessionId = %s, want root-1", rootMap["sessionId"])
	}
}

func TestChildCompletedStatusesRoundTrip(t *testing.T) {
	corr := Correlation{SessionID: "child-1", ParentSessionID: "parent-1", Depth: 1}
	for _, status := range []ChildStatus{ChildStatusCompleted, ChildStatusFailed, ChildStatusCanceled, ChildStatusBlocked} {
		want := ChildCompleted{
			Correlation: corr,
			Status:      status,
			Summary:     string(status),
			Handoff: CompletionHandoff{
				Summary:      string(status),
				FilesChanged: []string{"a.go"},
				Findings:     []string{},
				Blockers:     []string{},
			},
		}
		env, err := Wrap(want)
		if err != nil {
			t.Fatalf("Wrap(%s): %v", status, err)
		}
		if env.Type != "child.completed" {
			t.Errorf("type = %q, want child.completed", env.Type)
		}
		got, err := env.Decode()
		if err != nil {
			t.Fatalf("Decode(%s): %v", status, err)
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("%s: got %s, want %s", status, gotJSON, wantJSON)
		}
	}
}

func TestChildCompletedHandoffRoundTrip(t *testing.T) {
	want := ChildCompleted{
		Correlation: Correlation{SessionID: "c1", ParentSessionID: "p1", Depth: 1},
		Status:      ChildStatusCompleted,
		Summary:     "done",
		Name:        "builder",
		Handoff: CompletionHandoff{
			Summary:               "done",
			FilesChanged:          []string{"internal/x.go"},
			Verification:          "make test",
			Findings:              []string{"note"},
			Blockers:              []string{},
			RecommendedNextAction: "review PR",
		},
		Verification: &VerificationReport{
			Passed:   true,
			Claimed:  true,
			Verified: true,
			Checks: []VerificationCheck{{
				Name:   "unit",
				Kind:   "cmd",
				Value:  "make test",
				Passed: true,
			}},
			Env: VerificationEnv{
				WorkDir:   "/tmp/ws",
				SessionID: "c1",
				ModelID:   "m1",
			},
			Summary: "verified: 1/1 gates passed",
		},
	}
	env, err := Wrap(want)
	if err != nil {
		t.Fatal(err)
	}
	gotEv, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := gotEv.(ChildCompleted)
	if !ok {
		t.Fatalf("type %T", gotEv)
	}
	if got.Handoff.Summary != "done" || len(got.Handoff.FilesChanged) != 1 {
		t.Fatalf("handoff = %#v", got.Handoff)
	}
	if got.Handoff.RecommendedNextAction != "review PR" {
		t.Fatalf("next = %q", got.Handoff.RecommendedNextAction)
	}
	if got.Verification == nil || !got.Verification.Passed || len(got.Verification.Checks) != 1 {
		t.Fatalf("verification = %#v", got.Verification)
	}
	if got.Verification.Env.SessionID != "c1" {
		t.Fatalf("env = %#v", got.Verification.Env)
	}
	// Wire uses camelCase.
	raw, _ := json.Marshal(got.Handoff)
	if !strings.Contains(string(raw), `"filesChanged"`) {
		t.Fatalf("wire JSON missing filesChanged: %s", raw)
	}
}

func TestChildEscalatedRoundTrip(t *testing.T) {
	rem := 3
	want := ChildEscalated{
		Correlation:    Correlation{SessionID: "c9", ParentSessionID: "p1", Depth: 1},
		Name:           "worker",
		Kind:           "tool_calls",
		Reason:         "tool-call budget exhausted (3/3)",
		Action:         "interrupted",
		TerminalStatus: ChildStatusFailed,
		Budget: &AgentBudgetView{
			MaxToolCalls:       3,
			ToolCalls:          3,
			ToolCallsRemaining: &rem,
			Escalated:          true,
			EscalateKind:       "tool_calls",
		},
	}
	env, err := Wrap(want)
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != "child.escalated" {
		t.Fatalf("type=%q", env.Type)
	}
	gotEv, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := gotEv.(ChildEscalated)
	if !ok {
		t.Fatalf("type %T", gotEv)
	}
	if got.Kind != "tool_calls" || got.Action != "interrupted" || got.Name != "worker" {
		t.Fatalf("got %#v", got)
	}
	if got.Budget == nil || got.Budget.MaxToolCalls != 3 || !got.Budget.Escalated {
		t.Fatalf("budget %#v", got.Budget)
	}
}

func TestVerificationEventsAndTurnCompletedRoundTrip(t *testing.T) {
	corr := Correlation{SessionID: "s1", TurnID: "t1"}
	rep := VerificationReport{
		Passed:   false,
		Claimed:  true,
		Verified: false,
		Checks: []VerificationCheck{{
			Name:     "unit",
			Kind:     "cmd",
			Value:    "false",
			Passed:   false,
			ExitCode: 1,
			Error:    "exit 1",
		}},
		Env:     VerificationEnv{WorkDir: "/ws", SessionID: "s1", ModelID: "m"},
		Summary: "verification failed: unit: exit 1",
	}
	cases := []Event{
		VerificationStarted{Correlation: corr, Scope: VerificationScopeTurn, GateCount: 1},
		VerificationCompleted{Correlation: corr, Scope: VerificationScopeTurn, Report: rep},
		TurnCompleted{Correlation: corr, StopReason: "end_turn", Verification: &rep},
	}
	for _, want := range cases {
		env, err := Wrap(want)
		if err != nil {
			t.Fatalf("Wrap %T: %v", want, err)
		}
		gotEv, err := env.Decode()
		if err != nil {
			t.Fatalf("Decode %T: %v", want, err)
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(gotEv)
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("%T: got %s, want %s", want, gotJSON, wantJSON)
		}
	}
	// Type strings.
	if env, _ := Wrap(VerificationStarted{}); env.Type != "verification.started" {
		t.Fatalf("started type = %q", env.Type)
	}
	if env, _ := Wrap(VerificationCompleted{}); env.Type != "verification.completed" {
		t.Fatalf("completed type = %q", env.Type)
	}
}

func TestDecodeLiteralLegacyEnvelopeHasEmptyCorrelation(t *testing.T) {
	literal := `{"type":"permission.asked","time":"2020-01-01T00:00:00Z","data":{"requestId":"perm_7","permission":"bash","patterns":["echo hi"]}}`
	var env Envelope
	if err := json.Unmarshal([]byte(literal), &env); err != nil {
		t.Fatal(err)
	}
	decoded, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decoded.(PermissionAsked)
	if !ok {
		t.Fatalf("decoded event = %T, want PermissionAsked", decoded)
	}
	if got.Correlation != (Correlation{}) {
		t.Errorf("legacy correlation = %#v, want empty", got.Correlation)
	}
	if got.RequestID != "perm_7" {
		t.Errorf("requestId = %q, want perm_7", got.RequestID)
	}
}

func TestDecodeLiteralLegacyFastEnvelopeHasEmptyCorrelation(t *testing.T) {
	literal := `{"type":"fast.selected","time":"2020-01-01T00:00:00Z","data":{"enabled":true}}`
	var env Envelope
	if err := json.Unmarshal([]byte(literal), &env); err != nil {
		t.Fatal(err)
	}
	decoded, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decoded.(FastSelected)
	if !ok {
		t.Fatalf("decoded event = %T, want FastSelected", decoded)
	}
	if got.Correlation != (Correlation{}) {
		t.Errorf("legacy correlation = %#v, want empty", got.Correlation)
	}
	if !got.Enabled {
		t.Error("enabled = false, want true")
	}
}

func TestDecodeUnknownEventForwardCompat(t *testing.T) {
	payload := json.RawMessage(`{"span":"gate","ok":true,"extra":1}`)
	env := Envelope{Type: "harness.future_gate", Time: time.Unix(0, 0).UTC(), Data: payload}
	got, err := env.Decode()
	if err != nil {
		t.Fatalf("Decode unknown type: %v", err)
	}
	u, ok := got.(UnknownEvent)
	if !ok {
		t.Fatalf("got %T, want UnknownEvent", got)
	}
	if !IsUnknown(got) {
		t.Fatal("IsUnknown = false")
	}
	if u.Type != "harness.future_gate" {
		t.Fatalf("Type = %q", u.Type)
	}
	if string(u.Data) != string(payload) {
		t.Fatalf("Data = %s, want %s", u.Data, payload)
	}
	// Round-trip preserves wire type + data for session rewrite / fork.
	back, err := Wrap(u)
	if err != nil {
		t.Fatal(err)
	}
	if back.Type != "harness.future_gate" {
		t.Fatalf("re-Wrap type = %q", back.Type)
	}
	if string(back.Data) != string(payload) {
		t.Fatalf("re-Wrap data = %s", back.Data)
	}
	if back.Version != Version {
		t.Fatalf("re-Wrap version = %q, want %q", back.Version, Version)
	}
}

func TestDecodeEmptyEnvelopeType(t *testing.T) {
	if _, err := (Envelope{Data: json.RawMessage(`{}`)}).Decode(); err == nil {
		t.Fatal("expected error for empty envelope type")
	}
}

func TestWrapUnknownEventEmptyType(t *testing.T) {
	if _, err := Wrap(UnknownEvent{Data: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("expected error for UnknownEvent with empty type")
	}
}

func TestDecodeUnknownEventInvalidJSON(t *testing.T) {
	env := Envelope{Type: "future.event", Data: json.RawMessage(`{`)}
	if _, err := env.Decode(); err == nil {
		t.Fatal("expected error for invalid JSON in unknown envelope")
	}
}

func TestDecodeMalformedData(t *testing.T) {
	env := Envelope{Type: "user.message", Data: json.RawMessage(`{`)}
	if _, err := env.Decode(); err == nil {
		t.Fatal("expected error for malformed data")
	}
}

func TestTokenCountKnownVsUnknown(t *testing.T) {
	known := KnownTokens(0)
	if !known.Known || known.N != 0 {
		t.Errorf("KnownTokens(0) = %+v, want Known=true N=0", known)
	}
	known = KnownTokens(42)
	if !known.Known || known.N != 42 {
		t.Errorf("KnownTokens(42) = %+v, want Known=true N=42", known)
	}
	unknown := UnknownTokens()
	if unknown.Known || unknown.N != 0 {
		t.Errorf("UnknownTokens() = %+v, want Known=false N=0", unknown)
	}

	// JSON: known zero keeps "known":true; unknown omits n and known is false.
	b, err := json.Marshal(UsageReported{
		Input:         KnownTokens(0),
		Output:        UnknownTokens(),
		CacheRead:     KnownTokens(3),
		CacheCreation: UnknownTokens(),
		Used:          KnownTokens(10),
		Source:        UsageSourceEstimated,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got UsageReported
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Input.Known || got.Input.N != 0 {
		t.Errorf("input after round-trip = %+v, want known zero", got.Input)
	}
	if got.Output.Known {
		t.Errorf("output after round-trip = %+v, want unknown", got.Output)
	}
	if !got.CacheRead.Known || got.CacheRead.N != 3 {
		t.Errorf("cacheRead after round-trip = %+v, want known 3", got.CacheRead)
	}
	if got.CacheCreation.Known {
		t.Errorf("cacheCreation after round-trip = %+v, want unknown", got.CacheCreation)
	}
	if !got.Used.Known || got.Used.N != 10 {
		t.Errorf("used after round-trip = %+v, want known 10", got.Used)
	}
	if got.Source != UsageSourceEstimated {
		t.Errorf("source = %q, want %q", got.Source, UsageSourceEstimated)
	}
}

func TestLedgerUpdatedRoundTrip(t *testing.T) {
	want := LedgerUpdated{
		Correlation:   Correlation{SessionID: "s1", TurnID: "t1"},
		ID:            "deadbeef",
		Kind:          "assumption",
		Status:        "invalidated",
		Op:            "invalidate",
		Statement:     "API X is dead",
		Reason:        "still referenced",
		AuthorSession: "s1",
		SessionID:     "s1",
	}
	env, err := Wrap(want)
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != "ledger.updated" {
		t.Fatalf("type = %q", env.Type)
	}
	gotEv, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := gotEv.(LedgerUpdated)
	if !ok {
		t.Fatalf("got %T", gotEv)
	}
	if got.ID != want.ID || got.Op != "invalidate" || got.Status != "invalidated" || got.Reason != want.Reason {
		t.Fatalf("got = %#v", got)
	}
}

func TestArtifactUpdatedAndHandoffRefsRoundTrip(t *testing.T) {
	want := ArtifactUpdated{
		Correlation: Correlation{SessionID: "s1"},
		ID:          "ab12cd34",
		Type:        "findings",
		Version:     2,
		Scope:       "project",
		Title:       "Review",
		Op:          "update",
		SessionID:   "s1",
	}
	env, err := Wrap(want)
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != "artifact.updated" {
		t.Fatalf("type = %q", env.Type)
	}
	gotEv, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := gotEv.(ArtifactUpdated)
	if !ok {
		t.Fatalf("type %T", gotEv)
	}
	if got.ID != want.ID || got.Version != 2 || got.Op != "update" {
		t.Fatalf("got = %#v", got)
	}

	// CompletionHandoff.artifactRefs on the wire.
	cc := ChildCompleted{
		Correlation: Correlation{SessionID: "c1"},
		Status:      ChildStatusCompleted,
		Handoff: CompletionHandoff{
			Summary: "done",
			ArtifactRefs: []ArtifactRef{
				{ID: "ab12cd34", Version: 2, Type: "findings"},
			},
		},
	}
	env2, err := Wrap(cc)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := env2.Decode()
	if err != nil {
		t.Fatal(err)
	}
	cc2 := got2.(ChildCompleted)
	if len(cc2.Handoff.ArtifactRefs) != 1 || cc2.Handoff.ArtifactRefs[0].ID != "ab12cd34" {
		t.Fatalf("refs = %#v", cc2.Handoff.ArtifactRefs)
	}
	raw, _ := json.Marshal(cc2.Handoff)
	if !strings.Contains(string(raw), `"artifactRefs"`) {
		t.Fatalf("wire missing artifactRefs: %s", raw)
	}

	// Context bundle on ChildStarted + missingContext/provenance on handoff.
	cs := ChildStarted{
		Correlation: Correlation{SessionID: "c2", ParentSessionID: "p1", Depth: 1},
		Agent:       "build",
		Prompt:      "go",
		ContextBundle: &ContextBundle{
			Goal:         "ship",
			AllowedPaths: []string{"internal/x"},
			Items:        []ContextBundleItem{{ID: "goal", Kind: "goal", Text: "ship"}},
		},
	}
	env3, err := Wrap(cs)
	if err != nil {
		t.Fatal(err)
	}
	got3, err := env3.Decode()
	if err != nil {
		t.Fatal(err)
	}
	cs2 := got3.(ChildStarted)
	if cs2.ContextBundle == nil || cs2.ContextBundle.Goal != "ship" {
		t.Fatalf("bundle = %#v", cs2.ContextBundle)
	}
	cc3 := ChildCompleted{
		Correlation: Correlation{SessionID: "c2"},
		Status:      ChildStatusBlocked,
		Handoff: CompletionHandoff{
			Summary: "blocked: missing context",
			MissingContext: []MissingContextEntry{
				{Kind: "path", Path: "docs/a.md"},
			},
			Provenance: []string{"goal"},
		},
	}
	env4, err := Wrap(cc3)
	if err != nil {
		t.Fatal(err)
	}
	got4, err := env4.Decode()
	if err != nil {
		t.Fatal(err)
	}
	cc4 := got4.(ChildCompleted)
	if len(cc4.Handoff.MissingContext) != 1 || cc4.Handoff.MissingContext[0].Path != "docs/a.md" {
		t.Fatalf("missing = %#v", cc4.Handoff.MissingContext)
	}
	if len(cc4.Handoff.Provenance) != 1 || cc4.Handoff.Provenance[0] != "goal" {
		t.Fatalf("provenance = %#v", cc4.Handoff.Provenance)
	}
	rawH, _ := json.Marshal(cc4.Handoff)
	if !strings.Contains(string(rawH), `"missingContext"`) || !strings.Contains(string(rawH), `"provenance"`) {
		t.Fatalf("wire missing fields: %s", rawH)
	}
}

func TestEventTypeCoverage(t *testing.T) {
	// Ensure every known event maps to a stable type string used by sessions.
	want := map[string]Event{
		"user.message":           UserMessage{},
		"turn.started":           TurnStarted{},
		"text.delta":             TextDelta{},
		"reasoning.delta":        ReasoningDelta{},
		"tool.begin":             ToolCallBegin{},
		"tool.end":               ToolCallEnd{},
		"tool.output":            ToolCallOutput{},
		"process.started":        ProcessStarted{},
		"process.output":         ProcessOutput{},
		"process.exited":         ProcessExited{},
		"permission.asked":       PermissionAsked{},
		"permission.resolved":    PermissionResolved{},
		"permission.decided":     PermissionDecided{},
		"question.asked":         QuestionAsked{},
		"question.resolved":      QuestionResolved{},
		"turn.completed":         TurnCompleted{},
		"verification.started":   VerificationStarted{},
		"verification.completed": VerificationCompleted{},
		"harness.progress":       HarnessProgress{Name: "test", Payload: json.RawMessage(`{}`)},
		"model.selected":         ModelSelected{},
		"agent.selected":         AgentSelected{},
		"phase.changed":          PhaseChanged{},
		"plan.handoff":           PlanHandoff{},
		"artifact.updated":       ArtifactUpdated{},
		"ledger.updated":         LedgerUpdated{},
		"phase.grant_approved":   PhaseGrantApproved{},
		"effort.selected":        EffortSelected{},
		"autonomy.selected":      AutonomySelected{},
		"permission.mode":        PermissionModeSelected{},
		"fast.selected":          FastSelected{},
		"files.invalidated":      FilesInvalidated{},
		"engine.error":           EngineError{},
		"child.started":          ChildStarted{},
		"child.completed":        ChildCompleted{},
		"child.escalated":        ChildEscalated{},
		"delegation.changed":     DelegationChanged{},
		"wait.started":           WaitStarted{},
		"wait.resolved":          WaitResolved{},
		"agent.message":          AgentMessage{},
		"agent.contract.timeout": AgentContractTimeout{},
		"team.roster":            TeamRoster{},
		"usage.reported":         UsageReported{},
		"provider.retrying":      ProviderRetrying{},
		"tool.retrying":          ToolRetrying{},
		"tool.loop_detected":     ToolLoopDetected{},
		"scheduler.queued":       SchedulerQueued{},
		"scheduler.admitted":     SchedulerAdmitted{},
		"scheduler.canceled":     SchedulerCanceled{},
		"compaction.started":     CompactionStarted{},
		"compaction.completed":   CompactionCompleted{},
		"session.meta":           SessionMeta{},
		"session.rewound":        SessionRewound{},
		"hook.matched":           HookMatched{},
		"prompt.effective":       EffectivePrompt{},
		"diagnostic.bundle":      DiagnosticBundle{},
		"context.fit_warning":    ContextFitWarning{},
		"context.controls":       ContextControlsSelected{},
	}
	for typ, ev := range want {
		env, err := Wrap(ev)
		if err != nil {
			t.Fatalf("Wrap %s: %v", typ, err)
		}
		if env.Type != typ {
			t.Errorf("type = %q, want %q", env.Type, typ)
		}
	}
}

func TestSchedulerQueueEventsRoundTrip(t *testing.T) {
	corr := Correlation{SessionID: "s1", TurnID: "t1", ParentSessionID: "p", Depth: 1}
	cases := []Event{
		SchedulerQueued{Correlation: corr, RequestID: "r1", Pools: []string{"model"}, Label: "model"},
		SchedulerAdmitted{Correlation: corr, RequestID: "r1", Pools: []string{"model"}, Label: "model", WaitMs: 42},
		SchedulerCanceled{Correlation: corr, RequestID: "r2", Pools: []string{"process", "build"}, Label: "bash:build", WaitMs: 7, Reason: SchedulerReasonCanceled},
	}
	for _, ev := range cases {
		env, err := Wrap(ev)
		if err != nil {
			t.Fatalf("Wrap %T: %v", ev, err)
		}
		got, err := env.Decode()
		if err != nil {
			t.Fatalf("Decode %s: %v", env.Type, err)
		}
		switch want := ev.(type) {
		case SchedulerQueued:
			g, ok := got.(SchedulerQueued)
			if !ok || g.RequestID != want.RequestID || g.Label != want.Label || len(g.Pools) != 1 || g.Pools[0] != "model" {
				t.Fatalf("queued got %#v want %#v", got, want)
			}
			if g.SessionID != "s1" || g.ParentSessionID != "p" || g.Depth != 1 {
				t.Fatalf("queued correlation = %+v", g.Correlation)
			}
		case SchedulerAdmitted:
			g, ok := got.(SchedulerAdmitted)
			if !ok || g.RequestID != want.RequestID || g.WaitMs != 42 {
				t.Fatalf("admitted got %#v want %#v", got, want)
			}
		case SchedulerCanceled:
			g, ok := got.(SchedulerCanceled)
			if !ok || g.Reason != SchedulerReasonCanceled || g.WaitMs != 7 || len(g.Pools) != 2 {
				t.Fatalf("canceled got %#v want %#v", got, want)
			}
		}
	}
}

func TestUserMessageImagesRoundTrip(t *testing.T) {
	ev := UserMessage{
		Text:   "hi",
		Images: []ImageAttachment{{MIME: "image/png", Data: "abc"}},
	}
	env, err := Wrap(ev)
	if err != nil {
		t.Fatal(err)
	}
	got, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	um, ok := got.(UserMessage)
	if !ok {
		t.Fatalf("type %T", got)
	}
	if um.Text != "hi" || len(um.Images) != 1 || um.Images[0].MIME != "image/png" || um.Images[0].Data != "abc" {
		t.Fatalf("got %#v", um)
	}
}

func TestToolRetryEventsRoundTrip(t *testing.T) {
	corr := Correlation{SessionID: "s1", TurnID: "t1", ProviderRequestID: "p1", Attempt: 1}
	cases := []Event{
		ToolRetrying{Correlation: corr, CallID: "c1", Name: "webfetch", NextAttempt: 2, DelayMs: 50, ErrorCode: ErrorCodeTransient, Message: "connection reset"},
		ToolLoopDetected{Correlation: corr, Reason: "identical_calls", ToolName: "read", Count: 3, Message: "repeated identical failing tool calls"},
	}
	for _, ev := range cases {
		env, err := Wrap(ev)
		if err != nil {
			t.Fatalf("Wrap: %v", err)
		}
		got, err := env.Decode()
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		rawWant, _ := json.Marshal(ev)
		rawGot, _ := json.Marshal(got)
		if string(rawWant) != string(rawGot) {
			t.Fatalf("round-trip mismatch\nwant %s\ngot  %s", rawWant, rawGot)
		}
	}
}

// Golden forward-compat: additive unknown JSON fields on known harness events
// must decode without error and preserve known fields (#811).
func TestGoldenAdditiveFieldsHarnessEvents(t *testing.T) {
	fixed := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		raw   string
		check func(t *testing.T, ev Event)
	}{
		{
			name: "verification.started additive",
			raw: `{"type":"verification.started","time":"2026-08-06T12:00:00Z","v":"1.10.0","data":{` +
				`"sessionId":"s1","turnId":"t1","scope":"turn","gateCount":2,` +
				`"futureField":"ok","nested":{"x":1}}}`,
			check: func(t *testing.T, ev Event) {
				t.Helper()
				got, ok := ev.(VerificationStarted)
				if !ok {
					t.Fatalf("%T", ev)
				}
				if got.SessionID != "s1" || got.TurnID != "t1" || got.Scope != VerificationScopeTurn || got.GateCount != 2 {
					t.Fatalf("%+v", got)
				}
			},
		},
		{
			name: "verification.completed additive",
			raw: `{"type":"verification.completed","time":"2026-08-06T12:00:00Z","v":"1.10.0","data":{` +
				`"sessionId":"s1","scope":"child","report":{"passed":true,"claimed":true,"verified":true,` +
				`"checks":[{"name":"unit","passed":true}],"env":{"sessionId":"s1"},"futureScore":0.9}}}`,
			check: func(t *testing.T, ev Event) {
				t.Helper()
				got, ok := ev.(VerificationCompleted)
				if !ok {
					t.Fatalf("%T", ev)
				}
				if got.Scope != VerificationScopeChild || !got.Report.Passed || !got.Report.Verified || len(got.Report.Checks) != 1 {
					t.Fatalf("%+v", got)
				}
			},
		},
		{
			name: "harness.progress additive",
			raw: `{"type":"harness.progress","time":"2026-08-06T12:00:00Z","v":"1.10.0","data":{` +
				`"sessionId":"s1","name":"choose_best","payload":{"kind":"iter","n":3},` +
				`"traceSpanId":"span-9"}}`,
			check: func(t *testing.T, ev Event) {
				t.Helper()
				got, ok := ev.(HarnessProgress)
				if !ok {
					t.Fatalf("%T", ev)
				}
				if got.Name != "choose_best" || !json.Valid(got.Payload) {
					t.Fatalf("%+v", got)
				}
			},
		},
		{
			name: "permission.decided additive",
			raw: `{"type":"permission.decided","time":"2026-08-06T12:00:00Z","v":"1.10.0","data":{` +
				`"permission":"bash","action":"allow","patterns":["echo *"],` +
				`"layer":"project","auditId":"a1"}}`,
			check: func(t *testing.T, ev Event) {
				t.Helper()
				got, ok := ev.(PermissionDecided)
				if !ok {
					t.Fatalf("%T", ev)
				}
				if got.Permission != "bash" || got.Action != "allow" || got.Layer != "project" {
					t.Fatalf("%+v", got)
				}
			},
		},
		{
			name: "turn.completed verification additive",
			raw: `{"type":"turn.completed","time":"2026-08-06T12:00:00Z","v":"1.10.0","data":{` +
				`"sessionId":"s1","stopReason":"end_turn","verification":{"passed":false,"claimed":true,` +
				`"verified":false,"checks":[],"env":{},"reasonCode":"gate_failed"}}}`,
			check: func(t *testing.T, ev Event) {
				t.Helper()
				got, ok := ev.(TurnCompleted)
				if !ok {
					t.Fatalf("%T", ev)
				}
				if got.StopReason != "end_turn" || got.Verification == nil || got.Verification.Passed || !got.Verification.Claimed {
					t.Fatalf("%+v", got)
				}
			},
		},
		{
			name: "diagnostic.bundle additive",
			raw: `{"type":"diagnostic.bundle","time":"2026-08-06T12:00:00Z","v":"1.10.0","data":{` +
				`"schemaVersion":"1.0.0","redacted":true,"session":{},"prompt":{},"config":{},` +
				`"pluginNote":"future"}}`,
			check: func(t *testing.T, ev Event) {
				t.Helper()
				got, ok := ev.(DiagnosticBundle)
				if !ok {
					t.Fatalf("%T", ev)
				}
				if got.SchemaVersion != "1.0.0" || !got.Redacted {
					t.Fatalf("%+v", got)
				}
			},
		},
		{
			name: "hook.matched additive",
			raw: `{"type":"hook.matched","time":"2026-08-06T12:00:00Z","v":"1.10.0","data":{` +
				`"event":"PreToolUse","action":"allow","tool":"bash","contribId":"plug.1"}}`,
			check: func(t *testing.T, ev Event) {
				t.Helper()
				got, ok := ev.(HookMatched)
				if !ok {
					t.Fatalf("%T", ev)
				}
				if got.Event != "PreToolUse" || got.Action != "allow" || got.Tool != "bash" {
					t.Fatalf("%+v", got)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var env Envelope
			if err := json.Unmarshal([]byte(tc.raw), &env); err != nil {
				t.Fatal(err)
			}
			if !env.Time.Equal(fixed) {
				t.Fatalf("time = %v", env.Time)
			}
			ev, err := env.Decode()
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			tc.check(t, ev)
		})
	}
}

func TestGoldenUnknownHarnessExtensionTypes(t *testing.T) {
	// Future harness/timeline extension type strings must not fail Decode.
	lines := []string{
		`{"type":"timeline.span","time":"2026-08-06T12:00:00Z","v":"9.0.0","data":{"id":"s1","kind":"verify"}}`,
		`{"type":"harness.gate","time":"2026-08-06T12:00:00Z","data":{"name":"lint","passed":true}}`,
		`{"type":"permission.audit","time":"2026-08-06T12:00:00Z","data":{"decision":"deny"}}`,
		`{"type":"context.snapshot","time":"2026-08-06T12:00:00Z","data":{}}`,
	}
	for _, line := range lines {
		var env Envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("unmarshal %s: %v", line, err)
		}
		ev, err := env.Decode()
		if err != nil {
			t.Fatalf("Decode %q: %v", env.Type, err)
		}
		if !IsUnknown(ev) {
			t.Fatalf("%q: got %T, want UnknownEvent", env.Type, ev)
		}
	}
}
