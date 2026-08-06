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
		}},
		ModelSelected{Correlation: corr, Provider: "echo", Model: "echo"},
		AgentSelected{Correlation: corr, Name: "build"},
		PhaseChanged{Correlation: corr, Workflow: "plan-implement", Phase: "plan", Index: 0, Gate: "user"},
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
		CompactionStarted{Correlation: corr, Reason: CompactionReasonManual, Strategy: CompactionStrategySummarize},
		CompactionCompleted{Correlation: corr, Reason: CompactionReasonThreshold, Strategy: CompactionStrategyTrim, Removed: 4, Kept: 3, Summary: "prior work on foo"},
		SessionMeta{Correlation: corr, PRURL: "https://github.com/acme/repo/pull/7", PRNumber: 7, PRState: "open"},
		SessionRewound{Correlation: corr, Removed: 2, TurnID: "turn-9", RestoreFiles: true, FilesRestored: 3, FilesSkipped: 1},
		EffectivePrompt{
			Correlation: corr,
			Layers: []PromptLayerInfo{
				{Kind: PromptLayerShared, Source: "builtin:shared", Mode: PromptLayerAppend, Chars: 12, Preview: "You are strike"},
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

func TestWrapUnknownEvent(t *testing.T) {
	type unknown struct{}
	// unknown does not implement Event; use a typed nil Event via empty interface cast workaround
	// by wrapping a value that implements isEvent through a private type is not possible from tests.
	// Instead verify Decode rejects unknown envelope types.
	env := Envelope{Type: "not.a.real.type", Data: json.RawMessage(`{}`)}
	if _, err := env.Decode(); err == nil {
		t.Fatal("expected error for unknown envelope type")
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

func TestEventTypeCoverage(t *testing.T) {
	// Ensure every known event maps to a stable type string used by sessions.
	want := map[string]Event{
		"user.message":         UserMessage{},
		"turn.started":         TurnStarted{},
		"text.delta":           TextDelta{},
		"reasoning.delta":      ReasoningDelta{},
		"tool.begin":           ToolCallBegin{},
		"tool.end":             ToolCallEnd{},
		"tool.output":          ToolCallOutput{},
		"process.started":      ProcessStarted{},
		"process.output":       ProcessOutput{},
		"process.exited":       ProcessExited{},
		"permission.asked":     PermissionAsked{},
		"permission.resolved":  PermissionResolved{},
		"question.asked":       QuestionAsked{},
		"question.resolved":    QuestionResolved{},
		"turn.completed":       TurnCompleted{},
		"harness.progress":     HarnessProgress{Name: "test", Payload: json.RawMessage(`{}`)},
		"model.selected":       ModelSelected{},
		"agent.selected":       AgentSelected{},
		"phase.changed":        PhaseChanged{},
		"phase.grant_approved": PhaseGrantApproved{},
		"effort.selected":      EffortSelected{},
		"autonomy.selected":    AutonomySelected{},
		"permission.mode":      PermissionModeSelected{},
		"fast.selected":        FastSelected{},
		"files.invalidated":    FilesInvalidated{},
		"engine.error":         EngineError{},
		"child.started":        ChildStarted{},
		"child.completed":      ChildCompleted{},
		"agent.message":        AgentMessage{},
		"team.roster":          TeamRoster{},
		"usage.reported":       UsageReported{},
		"provider.retrying":    ProviderRetrying{},
		"scheduler.queued":     SchedulerQueued{},
		"scheduler.admitted":   SchedulerAdmitted{},
		"scheduler.canceled":   SchedulerCanceled{},
		"compaction.started":   CompactionStarted{},
		"compaction.completed": CompactionCompleted{},
		"session.meta":         SessionMeta{},
		"session.rewound":      SessionRewound{},
		"hook.matched":         HookMatched{},
		"prompt.effective":     EffectivePrompt{},
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
