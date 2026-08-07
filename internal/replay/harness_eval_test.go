package replay_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/replay"
	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/tool"
	"github.com/jonathanung/strike-cli/pkg/redact"
	"github.com/jonathanung/strike-cli/pkg/timeline"
)

// harnessEvalScenario is one offline regression check for #807 themes.
type harnessEvalScenario struct {
	name  string
	theme string
	run   func(t *testing.T) string // returns detail on pass; fatals on fail
}

// TestHarnessEvalSuite is the harness regression pack (#807).
// Themes: correctness, safety, recovery, latency/cost, plus recording
// consumption from #791/#782. Offline (echo/fixtures); no network.
//
//	make harness-eval
//	HARNESS_EVAL_REPORT=path go test ./internal/replay/ -run TestHarnessEvalSuite -v
//
// Scenario failures are hard errors (regression gate under `make test`).
// The verbose report step in CI is non-blocking (continue-on-error); set
// HARNESS_EVAL_STRICT=1 only if a future job should fail the soft step on
// report write issues. Path to blocking: drop continue-on-error on the CI
// "Harness eval report" step once the pack is stable.
func TestHarnessEvalSuite(t *testing.T) {
	scenarios := []harnessEvalScenario{
		// --- correctness ---
		{
			name:  "tool-contract-codes",
			theme: replay.ThemeCorrectness,
			run:   scenarioToolContracts,
		},
		{
			name:  "precondition-fail-closed",
			theme: replay.ThemeCorrectness,
			run:   scenarioPreconditionFailClosed,
		},
		{
			name:  "golden-echo-replay",
			theme: replay.ThemeCorrectness,
			run:   scenarioGoldenEchoReplay,
		},
		// --- safety ---
		{
			name:  "secret-redaction",
			theme: replay.ThemeSafety,
			run:   scenarioSecretRedaction,
		},
		{
			name:  "permission-deny",
			theme: replay.ThemeSafety,
			run:   scenarioPermissionDeny,
		},
		{
			name:  "sandbox-capability-report",
			theme: replay.ThemeSafety,
			run:   scenarioSandboxCapabilityReport,
		},
		// --- recovery ---
		{
			name:  "cancel-error-code",
			theme: replay.ThemeRecovery,
			run:   scenarioCancelErrorCode,
		},
		{
			name:  "no-mutative-double-retry",
			theme: replay.ThemeRecovery,
			run:   scenarioNoMutativeDoubleRetry,
		},
		// --- latency / cost ---
		{
			name:  "timeline-cost-fields",
			theme: replay.ThemeLatencyCost,
			run:   scenarioTimelineCostFields,
		},
		{
			name:  "budget-wire-and-echo-tokens",
			theme: replay.ThemeLatencyCost,
			run:   scenarioBudgetAndEchoTokens,
		},
		// --- recording consumption (#791/#782) ---
		{
			name:  "recording-compare-self",
			theme: replay.ThemeRecording,
			run:   scenarioRecordingCompareSelf,
		},
		{
			name:  "run-snapshot-round-trip",
			theme: replay.ThemeRecording,
			run:   scenarioRunSnapshotRoundTrip,
		},
	}

	results := make([]replay.EvalResult, 0, len(scenarios))
	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.theme+"/"+sc.name, func(t *testing.T) {
			start := time.Now()
			var detail string
			defer func() {
				status := replay.EvalPass
				if t.Failed() {
					status = replay.EvalFail
					if detail == "" {
						detail = "scenario failed"
					}
				}
				results = append(results, replay.EvalResult{
					Name:     sc.name,
					Theme:    sc.theme,
					Status:   status,
					Detail:   detail,
					Duration: time.Since(start),
				})
			}()
			detail = sc.run(t)
		})
	}

	// Subtests finished; build report from collected results.
	// Note: parallel subtests are not used so results order is deterministic
	// enough; BuildEvalReport sorts anyway.
	rep := replay.BuildEvalReport(results, time.Now().UTC())
	reportText := replay.FormatEvalReport(rep)
	t.Logf("\n%s", reportText)

	// Required themes must appear (acceptance: ≥1 scenario per core theme).
	covered := map[string]bool{}
	for _, th := range replay.ThemesCovered(results) {
		covered[th] = true
	}
	for _, want := range []string{
		replay.ThemeCorrectness,
		replay.ThemeSafety,
		replay.ThemeRecovery,
		replay.ThemeLatencyCost,
	} {
		if !covered[want] {
			t.Errorf("missing required theme %q in suite", want)
		}
	}

	if rep.Failed > 0 {
		t.Errorf("harness eval: %d scenario(s) failed", rep.Failed)
	}

	// Optional artifact path for CI / local tracking.
	outPath := strings.TrimSpace(os.Getenv("HARNESS_EVAL_REPORT"))
	if outPath == "" {
		// Default under testdata when UPDATE_HARNESS_EVAL=1 (refresh checked-in sample).
		if os.Getenv("UPDATE_HARNESS_EVAL") == "1" {
			outPath = filepath.Join("testdata", "harness-eval-report.json")
		}
	}
	if outPath != "" {
		if err := replay.WriteEvalReport(outPath, rep); err != nil {
			if os.Getenv("HARNESS_EVAL_STRICT") == "1" {
				t.Fatalf("write report %s: %v", outPath, err)
			}
			t.Logf("write report %s: %v (non-blocking)", outPath, err)
		} else {
			t.Logf("wrote harness eval report %s", outPath)
		}
	}
}

func TestBuildEvalReportAndFormat(t *testing.T) {
	t.Parallel()
	results := []replay.EvalResult{
		{Name: "b", Theme: replay.ThemeSafety, Status: replay.EvalPass, Duration: time.Millisecond},
		{Name: "a", Theme: replay.ThemeCorrectness, Status: replay.EvalFail, Detail: "boom", Duration: 2 * time.Millisecond},
		{Name: "c", Theme: replay.ThemeRecovery, Status: replay.EvalSkip, Detail: "n/a"},
	}
	rep := replay.BuildEvalReport(results, time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC))
	if rep.SchemaVersion != replay.EvalReportSchemaVersion {
		t.Fatalf("schema = %q", rep.SchemaVersion)
	}
	if rep.Passed != 1 || rep.Failed != 1 || rep.Skipped != 1 {
		t.Fatalf("counts pass=%d fail=%d skip=%d", rep.Passed, rep.Failed, rep.Skipped)
	}
	if rep.Results[0].Theme != replay.ThemeCorrectness || rep.Results[0].Name != "a" {
		t.Fatalf("sort order: %+v", rep.Results)
	}
	text := replay.FormatEvalReport(rep)
	for _, want := range []string{"correctness", "fail", "pass", "summary:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := replay.WriteEvalReport(path, rep); err != nil {
		t.Fatal(err)
	}
	got, err := replay.LoadEvalReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Failed != 1 || len(got.Results) != 3 {
		t.Fatalf("round-trip = %+v", got)
	}
	themes := replay.ThemesCovered(results)
	if len(themes) != 3 {
		t.Fatalf("themes = %v", themes)
	}
}

// --- scenarios ---

func scenarioToolContracts(t *testing.T) string {
	t.Helper()
	cases := []struct {
		tool tool.Tool
		se   tool.SideEffect
		id   tool.Idempotency
	}{
		{tool.NewBash(), tool.SideEffectProcess, tool.IdempotencyUnsafe},
		{tool.NewEdit(), tool.SideEffectWorkspaceMutative, tool.IdempotencyConditional},
		{tool.NewWrite(), tool.SideEffectWorkspaceMutative, tool.IdempotencyConditional},
		{tool.NewMove(), tool.SideEffectWorkspaceMutative, tool.IdempotencyConditional},
		{tool.NewDelete(), tool.SideEffectWorkspaceMutative, tool.IdempotencyConditional},
		{tool.NewRead(), tool.SideEffectRead, tool.IdempotencySafeRetry},
	}
	for _, tc := range cases {
		c := tool.LookupContract(tc.tool)
		if err := c.Validate(); err != nil {
			t.Fatalf("%s: %v", tc.tool.Name(), err)
		}
		if c.SideEffect != tc.se || c.Idempotency != tc.id {
			t.Fatalf("%s contract = %+v, want se=%s id=%s", tc.tool.Name(), c, tc.se, tc.id)
		}
	}
	for _, code := range []tool.ErrorCode{
		tool.CodePermissionDenied, tool.CodeInvalidArgs, tool.CodePreconditionFailed,
		tool.CodeCanceled, tool.CodeTimeout, tool.CodeTransient, tool.CodeInternal, tool.CodeBlocked,
		tool.CodeSandboxDenied, tool.CodeContentGuardDenied, tool.CodeNetworkDenied,
	} {
		if !tool.ValidErrorCode(code) {
			t.Fatalf("ValidErrorCode(%q) = false", code)
		}
	}
	return "bash/edit/write/read contracts + error code vocabulary ok"
}

func scenarioPreconditionFailClosed(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "h.txt")
	content := []byte("alpha beta\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	tc := &tool.Context{
		WorkDir: dir,
		Ask:     func(context.Context, tool.AskRequest) error { return nil },
		Files:   &tool.FileState{},
	}
	_, err := tool.NewEdit().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath":  "h.txt",
		"oldString": "alpha",
		"newString": "ALPHA",
		"baseHash":  strings.Repeat("0", 64),
	}), tc)
	if err == nil || tool.CodeOf(err) != string(tool.CodePreconditionFailed) {
		t.Fatalf("want precondition_failed, got %v (code=%q)", err, tool.CodeOf(err))
	}
	// File must be unchanged (fail closed).
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(content) {
		t.Fatalf("file mutated on failed precondition: %q", got)
	}
	return "baseHash mismatch → precondition_failed; file unchanged"
}

func scenarioGoldenEchoReplay(t *testing.T) string {
	t.Helper()
	goldenPath := filepath.Join("testdata", "plain-echo.jsonl")
	golden, err := replay.LoadJSONL(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	inputs := replay.ExtractUserInputs(golden)
	if len(inputs) == 0 {
		t.Fatal("golden has no user inputs")
	}
	res, err := replay.Run(context.Background(), inputs, replay.Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	want := replay.ExtractToolCalls(golden)
	if err := replay.DiffToolCalls(want, res.ToolCalls); err != nil {
		t.Fatalf("tool-call sequence: %v", err)
	}
	if res.Turns < 1 {
		t.Fatalf("turns = %d", res.Turns)
	}
	return "plain-echo.jsonl tool sequence matches echo replay"
}

func scenarioSecretRedaction(t *testing.T) string {
	t.Helper()
	secret := "sk-ant-api03-SECRETVALUE1234567890abcd"
	corr := protocol.Correlation{SessionID: "s", TurnID: "t"}
	events := []protocol.Event{
		protocol.UserMessage{Correlation: corr, Text: "token " + secret},
		protocol.ToolCallBegin{
			Correlation: corr, CallID: "c1", Name: "bash",
			Args: json.RawMessage(`{"command":"echo ` + secret + `"}`),
		},
		protocol.ToolCallEnd{Correlation: corr, CallID: "c1", Title: "bash", Output: secret + "\n"},
	}
	rec := replay.BuildRecording(events, replay.RecordingOptions{SessionID: "s"})
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SECRETVALUE") {
		t.Fatalf("secret leaked into recording: %s", raw)
	}
	// pkg/redact also scrubs free text used by timeline/export.
	if scrubbed := redact.String("key=" + secret); strings.Contains(scrubbed, "SECRETVALUE") {
		t.Fatalf("redact.String leaked: %q", scrubbed)
	}
	return "recording + pkg/redact scrub credential-shaped secrets"
}

func scenarioPermissionDeny(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	tc := &tool.Context{
		WorkDir: dir,
		Ask: func(context.Context, tool.AskRequest) error {
			return tool.ErrPermissionDenied("write denied by harness eval")
		},
		Files: &tool.FileState{},
	}
	_, err := tool.NewWrite().Execute(context.Background(), mustJSON(t, map[string]any{
		"filePath": "secret.txt",
		"content":  "nope",
	}), tc)
	if err == nil {
		t.Fatal("expected permission deny")
	}
	if tool.CodeOf(err) != string(tool.CodePermissionDenied) {
		t.Fatalf("code = %q, want %s (err=%v)", tool.CodeOf(err), tool.CodePermissionDenied, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "secret.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("denied write must not create file: %v", statErr)
	}
	return "write deny → permission_denied; no file created"
}

func scenarioSandboxCapabilityReport(t *testing.T) string {
	t.Helper()
	p := sandbox.Policy{
		Mode:           sandbox.ModeWorkspaceWrite,
		WorkDir:        t.TempDir(),
		NoNetwork:      false,
		DenyWriteGlobs: []string{"**/.env"},
	}
	report := sandbox.Explain(p)
	for _, want := range []string{"sandbox mode:", "network", "backend:", "workspace-write:"} {
		if !strings.Contains(report, want) {
			t.Fatalf("Explain missing %q:\n%s", want, report)
		}
	}
	// BackendName is platform-specific but must be a non-panic string.
	_ = sandbox.BackendName()
	_ = sandbox.Available()
	return "sandbox.Explain reports mode/network/backend/workspace-write"
}

func scenarioCancelErrorCode(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	tc := &tool.Context{
		WorkDir:     t.TempDir(),
		SandboxMode: "off", // CI may lack OS sandbox backend (#1030 fail-closed)
		Ask:         func(context.Context, tool.AskRequest) error { return nil },
	}
	// Print then sleep so cancel is observed after the process starts
	// (matches internal/tool TestBashCancelPreservesPartialOutput).
	done := make(chan tool.Result, 1)
	errc := make(chan error, 1)
	go func() {
		res, err := tool.NewBash().Execute(ctx, mustJSON(t, map[string]any{
			"command": "printf 'harness-eval-cancel\\n'; sleep 30",
		}), tc)
		done <- res
		errc <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case res := <-done:
		if err := <-errc; err != nil {
			t.Fatalf("err = %v", err)
		}
		if res.ErrorCode != tool.ErrorCodeCanceled {
			t.Fatalf("ErrorCode = %q, want %q", res.ErrorCode, tool.ErrorCodeCanceled)
		}
		return "bash cancel → ErrorCode canceled"
	case <-time.After(5 * time.Second):
		t.Fatal("bash did not return after cancel")
		return ""
	}
}

func scenarioNoMutativeDoubleRetry(t *testing.T) string {
	t.Helper()
	mutative := []tool.Tool{
		tool.NewEdit(), tool.NewWrite(), tool.NewApplyPatch(), tool.NewMove(), tool.NewDelete(), tool.NewBash(),
	}
	for _, tl := range mutative {
		c := tool.LookupContract(tl)
		for _, code := range []tool.ErrorCode{
			tool.CodeTransient, tool.CodeTimeout, tool.CodeInternal, "",
		} {
			if d := tool.DecideRetry(code, c.Idempotency); d == tool.DecisionRetry {
				t.Fatalf("%s DecideRetry(%q,%q)=retry; mutative must not auto-retry",
					tl.Name(), code, c.Idempotency)
			}
		}
		// Unsafe never retries anything.
		if c.Idempotency == tool.IdempotencyUnsafe {
			if d := tool.DecideRetry(tool.CodeTransient, c.Idempotency); d != tool.DecisionFail {
				t.Fatalf("unsafe %s: want fail, got %s", tl.Name(), d)
			}
		}
	}
	// Safe-retry tools may still retry transient (contrast check).
	if d := tool.DecideRetry(tool.CodeTransient, tool.IdempotencySafeRetry); d != tool.DecisionRetry {
		t.Fatalf("safe-retry transient = %s, want retry", d)
	}
	return "mutative/process tools never auto-retry transient/timeout"
}

func scenarioTimelineCostFields(t *testing.T) string {
	t.Helper()
	base := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	corr := protocol.Correlation{SessionID: "sess-eval", TurnID: "turn-1"}
	events := []timeline.TimedEvent{
		{Time: base, Event: protocol.TurnStarted{Correlation: corr}},
		{Time: base.Add(10 * time.Millisecond), Event: protocol.UsageReported{
			Correlation: protocol.Correlation{
				SessionID: "sess-eval", TurnID: "turn-1",
				ProviderRequestID: "preq-1", Attempt: 1,
			},
			Input:  protocol.KnownTokens(42),
			Output: protocol.KnownTokens(7),
			Source: protocol.UsageSourceActual,
		}},
		{Time: base.Add(20 * time.Millisecond), Event: protocol.ToolCallBegin{
			Correlation: protocol.Correlation{
				SessionID: "sess-eval", TurnID: "turn-1",
				ProviderRequestID: "preq-1", Attempt: 1,
			},
			CallID: "call-1", Name: "bash",
			Args: json.RawMessage(`{"command":"echo hi"}`),
		}},
		{Time: base.Add(80 * time.Millisecond), Event: protocol.ToolCallEnd{
			Correlation: protocol.Correlation{
				SessionID: "sess-eval", TurnID: "turn-1",
				ProviderRequestID: "preq-1",
			},
			CallID: "call-1", Title: "echo hi", Output: "hi\n",
		}},
		{Time: base.Add(100 * time.Millisecond), Event: protocol.TurnCompleted{
			Correlation: corr, StopReason: "end_turn",
		}},
	}
	tr := timeline.Build(events, timeline.Options{
		SessionID: "sess-eval",
		Clock:     func() time.Time { return base.Add(time.Second) },
	})
	if tr.Summary.Turns != 1 || tr.Summary.Tools != 1 {
		t.Fatalf("summary = %+v", tr.Summary)
	}
	if tr.Summary.InputTok != 42 || tr.Summary.OutputTok != 7 {
		t.Fatalf("token fields missing: in=%d out=%d", tr.Summary.InputTok, tr.Summary.OutputTok)
	}
	if tr.Summary.DurationMs <= 0 {
		t.Fatalf("DurationMs = %d, want > 0", tr.Summary.DurationMs)
	}
	var sawToolDuration bool
	for _, e := range tr.Entries {
		if e.Kind == timeline.KindTool && e.DurationMs != nil && *e.DurationMs > 0 {
			sawToolDuration = true
		}
	}
	if !sawToolDuration {
		t.Fatal("tool entry missing durationMs")
	}
	if !tr.Redacted {
		t.Fatal("timeline export must mark redacted=true")
	}
	return "timeline summary has durationMs + input/output token fields"
}

func scenarioBudgetAndEchoTokens(t *testing.T) string {
	t.Helper()
	// Budget wire shape carries cost fields used by roster/escalation.
	rem := 1.25
	view := protocol.AgentBudgetView{
		MaxCostUSD:       5,
		CostUSDUsed:      0.5,
		CostUSDRemaining: &rem,
		MaxTokens:        1000,
		TokensUsed:       100,
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"maxCostUsd", "costUsdUsed", "costUsdRemaining"} {
		if !strings.Contains(s, want) {
			t.Fatalf("budget JSON missing %q: %s", want, s)
		}
	}
	// Echo replay produces usage metrics (latency/cost signal path offline).
	res, err := replay.Run(context.Background(), []string{"hello harness-eval"}, replay.Options{
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	m := replay.CollectMetrics("budget-echo", res)
	if m.Turns != 1 {
		t.Fatalf("turns = %d", m.Turns)
	}
	// Echo estimates tokens; require a positive usage signal for cost tracking.
	if m.UsedTokens <= 0 && m.InputTokens <= 0 && m.OutputTokens <= 0 {
		t.Fatalf("token metrics missing/zero: %+v", m)
	}
	return "AgentBudgetView cost fields + echo CollectMetrics tokens present"
}

func scenarioRecordingCompareSelf(t *testing.T) string {
	t.Helper()
	goldenPath := filepath.Join("testdata", "bash-run.jsonl")
	events, err := replay.LoadJSONL(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	rec := replay.BuildRecording(events, replay.RecordingOptions{SessionID: "eval"})
	// Self-compare must match (deterministic recording projection).
	rep := replay.CompareRecordings(rec, rec, replay.CompareOptions{})
	if !rep.Equal() {
		t.Fatalf("self-compare diverged: %+v", rep.Divergences)
	}
	// Live echo run → recording should share tool sequence with golden extract.
	inputs := replay.ExtractUserInputs(events)
	res, err := replay.Run(context.Background(), inputs, replay.Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	live := replay.BuildRecordingFromResult(res, replay.RecordingOptions{SessionID: "eval-live"})
	// Tool sequences from golden vs live echo should match for bash-run fixture.
	if err := replay.DiffToolCalls(rec.ToolCalls, live.ToolCalls); err != nil {
		t.Fatalf("live vs golden tools: %v", err)
	}
	return "BuildRecording self-compare + bash-run live tool sequence"
}

func scenarioRunSnapshotRoundTrip(t *testing.T) string {
	t.Helper()
	fixed := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	snap := replay.BuildStartSnapshot(replay.SnapshotOptions{
		SnapshotID:      "eval-snap-1",
		DelegationID:    "del-eval",
		ParentSessionID: "parent-sess",
		ChildSessionID:  "child-sess",
		Prompt:          "run harness eval offline",
		Agent:           "build",
		Settings: replay.SettingsDigest{
			Provider: "echo",
			Model:    "echo",
			Agent:    "build",
		},
		ToolAllowList: []string{"bash", "read"},
		Clock:         func() time.Time { return fixed },
	})
	if snap.SnapshotID == "" || snap.Settings.Provider != "echo" {
		t.Fatalf("snapshot = %+v", snap)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	if err := replay.WriteRunSnapshot(path, snap); err != nil {
		t.Fatal(err)
	}
	got, err := replay.LoadRunSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SnapshotID != snap.SnapshotID || got.DelegationID != snap.DelegationID {
		t.Fatalf("round-trip ids: got %+v want %+v", got, snap)
	}
	cmp := replay.CompareRunSnapshots(snap, got, replay.CompareOptions{})
	if !cmp.Equal() {
		t.Fatalf("snapshot compare: %+v", cmp.Divergences)
	}
	return "RunSnapshot write/load/compare (#782) ok"
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
