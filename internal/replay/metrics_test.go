package replay_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/replay"
)

func TestCollectMetricsFromEvents(t *testing.T) {
	res := replay.Result{
		Turns:     2,
		ToolCalls: []replay.ToolCall{{Name: "bash"}, {Name: "read"}},
		Events: []protocol.Event{
			protocol.UsageReported{
				Input:  protocol.KnownTokens(10),
				Output: protocol.KnownTokens(4),
				Used:   protocol.KnownTokens(14),
			},
			// Child usage must not count.
			protocol.UsageReported{
				Correlation: protocol.Correlation{ParentSessionID: "p", Depth: 1},
				Input:       protocol.KnownTokens(99),
				Output:      protocol.KnownTokens(99),
				Used:        protocol.KnownTokens(198),
			},
			protocol.UsageReported{
				Input:  protocol.KnownTokens(20),
				Output: protocol.KnownTokens(5),
				Used:   protocol.KnownTokens(25),
			},
		},
		Effective: &protocol.EffectivePrompt{
			SystemChars: 500,
			Layers: []protocol.PromptLayerInfo{
				{Kind: protocol.PromptLayerShared, Chars: 100},
				{Kind: protocol.PromptLayerTools, Chars: 50},
				{Kind: protocol.PromptLayerEnvironment, Chars: 200},
			},
			Attribution: protocol.RequestTokenAttribution{
				System: protocol.KnownTokens(40),
				Tools:  protocol.KnownTokens(12),
				Total:  protocol.KnownTokens(80),
			},
		},
	}
	m := replay.CollectMetrics("sample", res)
	if m.Scenario != "sample" {
		t.Errorf("scenario = %q", m.Scenario)
	}
	if m.ToolCallCount != 2 || m.Turns != 2 {
		t.Errorf("tools/turns = %d/%d", m.ToolCallCount, m.Turns)
	}
	if m.InputTokens != 30 || m.OutputTokens != 9 || m.UsedTokens != 39 {
		t.Errorf("tokens in/out/used = %d/%d/%d", m.InputTokens, m.OutputTokens, m.UsedTokens)
	}
	if m.SystemChars != 500 {
		t.Errorf("SystemChars = %d", m.SystemChars)
	}
	if m.PromptChars != 150 { // excludes environment 200
		t.Errorf("PromptChars = %d, want 150", m.PromptChars)
	}
	if m.SystemTokens != 40 || m.ToolsTokens != 12 || m.TotalTokens != 80 {
		t.Errorf("attr = sys %d tools %d total %d", m.SystemTokens, m.ToolsTokens, m.TotalTokens)
	}
}

func TestDiffMetricsAndReport(t *testing.T) {
	want := replay.Metrics{Scenario: "a", ToolCallCount: 1, Turns: 1, UsedTokens: 10, PromptChars: 100, SystemTokens: 20}
	got := replay.Metrics{Scenario: "a", ToolCallCount: 2, Turns: 1, UsedTokens: 15, PromptChars: 110, SystemTokens: 22}
	d := replay.DiffMetrics(want, got)
	if d.ToolCallCount != 1 || d.Turns != 0 || d.UsedTokens != 5 || d.PromptChars != 10 || d.SystemTokens != 2 {
		t.Fatalf("delta = %+v", d)
	}
	if d.Zero() {
		t.Fatal("expected non-zero delta")
	}
	if !replay.DiffMetrics(want, want).Zero() {
		t.Fatal("identical metrics should have zero delta")
	}
	// Path/date noise on system totals must not trip stable comparison.
	noisy := want
	noisy.SystemChars += 50
	noisy.SystemTokens += 10
	noisy.TotalTokens += 10
	if !replay.DiffMetrics(want, noisy).StableZero() {
		t.Fatal("StableZero should ignore system char/token drift")
	}
	if replay.DiffMetrics(want, got).StableZero() {
		t.Fatal("StableZero should detect tool/token regressions")
	}
	report := replay.FormatMetricsReport([]replay.Metrics{got}, map[string]replay.Metrics{"a": want})
	if !strings.Contains(report, "a") || !strings.Contains(report, "+1") {
		t.Fatalf("report missing expected cells:\n%s", report)
	}
	// Missing baseline → n/a deltas.
	na := replay.FormatMetricsReport([]replay.Metrics{got}, nil)
	if !strings.Contains(na, "n/a") {
		t.Fatalf("want n/a deltas:\n%s", na)
	}
}

func TestMetricsBaselineRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.json")
	rows := []replay.Metrics{
		{Scenario: "z-last", Turns: 1, UsedTokens: 3},
		{Scenario: "a-first", Turns: 2, ToolCallCount: 1, PromptChars: 9},
	}
	if err := replay.WriteMetricsBaseline(path, rows); err != nil {
		t.Fatal(err)
	}
	got, err := replay.LoadMetricsBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got["a-first"].Turns != 2 || got["z-last"].UsedTokens != 3 {
		t.Fatalf("got = %#v", got)
	}
	// Keys should be sorted in file for stable diffs.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	aIdx := strings.Index(string(raw), `"a-first"`)
	zIdx := strings.Index(string(raw), `"z-last"`)
	if aIdx < 0 || zIdx < 0 || aIdx > zIdx {
		t.Fatalf("expected sorted keys in\n%s", raw)
	}
}

func TestRunInspectPrompt(t *testing.T) {
	res, err := replay.Run(context.Background(), []string{"hello metrics"}, replay.Options{
		WorkDir:       t.TempDir(),
		InspectPrompt: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Effective == nil {
		t.Fatal("Effective is nil")
	}
	if res.Effective.SystemChars <= 0 {
		t.Fatalf("SystemChars = %d", res.Effective.SystemChars)
	}
	m := replay.CollectMetrics("inspect", res)
	if m.PromptChars <= 0 {
		t.Fatalf("PromptChars = %d", m.PromptChars)
	}
	if m.UsedTokens <= 0 {
		t.Fatalf("UsedTokens = %d (echo should emit estimated usage)", m.UsedTokens)
	}
	if m.ToolCallCount != 0 || m.Turns != 1 {
		t.Fatalf("tools=%d turns=%d", m.ToolCallCount, m.Turns)
	}
}

func TestRunInspectPromptOnlyNoInputs(t *testing.T) {
	res, err := replay.Run(context.Background(), nil, replay.Options{
		WorkDir:       t.TempDir(),
		InspectPrompt: true,
		Agents: []engine.Agent{
			{Name: "build", Description: "default"},
			{Name: "plan", Description: "plan"},
		},
		InitialAgent: "plan",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Turns != 0 {
		t.Fatalf("turns = %d", res.Turns)
	}
	if res.Effective == nil || res.Effective.SystemChars == 0 {
		t.Fatalf("effective = %#v", res.Effective)
	}
	// Plan agent gets plan overlay and/or active plan phase context.
	var hasPlanish bool
	for _, layer := range res.Effective.Layers {
		switch layer.Kind {
		case protocol.PromptLayerPlan, protocol.PromptLayerPhase:
			hasPlanish = true
		}
	}
	if !hasPlanish {
		t.Fatalf("plan agent missing plan/phase layer: %+v", res.Effective.Layers)
	}
	// Plan composition should be larger than bare build (plan copy is long).
	build, err := replay.Run(context.Background(), nil, replay.Options{
		WorkDir:       t.TempDir(),
		InspectPrompt: true,
		Agents:        []engine.Agent{{Name: "build"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	planM := replay.CollectMetrics("plan", res)
	buildM := replay.CollectMetrics("build", build)
	if planM.PromptChars <= buildM.PromptChars {
		t.Fatalf("plan PromptChars=%d should exceed build=%d", planM.PromptChars, buildM.PromptChars)
	}
}

func TestRunLeanCodeAffectsPromptChars(t *testing.T) {
	agents := []engine.Agent{{Name: "build", Description: "default"}}
	off, err := replay.Run(context.Background(), nil, replay.Options{
		WorkDir:       t.TempDir(),
		InspectPrompt: true,
		Agents:        agents,
		LeanCode:      "off",
	})
	if err != nil {
		t.Fatal(err)
	}
	full, err := replay.Run(context.Background(), nil, replay.Options{
		WorkDir:       t.TempDir(),
		InspectPrompt: true,
		Agents:        agents,
		LeanCode:      "full",
	})
	if err != nil {
		t.Fatal(err)
	}
	offM := replay.CollectMetrics("off", off)
	fullM := replay.CollectMetrics("full", full)
	if fullM.PromptChars <= offM.PromptChars {
		t.Fatalf("lean full promptChars=%d should exceed off=%d", fullM.PromptChars, offM.PromptChars)
	}
}

// promptRegScenarios are the E3.2 corpus. Re-run (and UPDATE_METRICS=1) after
// changes to internal/engine/prompt.go, prompt_tools.go (tool guidance),
// prompt/*.txt, or internal/config/agents definitions.
var promptRegScenarios = []struct {
	name   string
	inputs []string
	opts   func(workDir string) replay.Options
}{
	{
		name:   "plain-echo",
		inputs: []string{"hello strike"},
		opts: func(workDir string) replay.Options {
			return replay.Options{WorkDir: workDir, InspectPrompt: true}
		},
	},
	{
		name:   "bash-run",
		inputs: []string{"run echo hello-strike"},
		opts: func(workDir string) replay.Options {
			return replay.Options{WorkDir: workDir, InspectPrompt: true}
		},
	},
	{
		name:   "multi-turn",
		inputs: []string{"ping", "run echo second-pass"},
		opts: func(workDir string) replay.Options {
			return replay.Options{WorkDir: workDir, InspectPrompt: true}
		},
	},
	{
		name:   "prompt-build-lite",
		inputs: nil, // composition-only
		opts: func(workDir string) replay.Options {
			return replay.Options{
				WorkDir:       workDir,
				InspectPrompt: true,
				Agents:        []engine.Agent{{Name: "build"}},
				LeanCode:      "lite",
			}
		},
	},
	{
		name:   "prompt-build-full",
		inputs: nil,
		opts: func(workDir string) replay.Options {
			return replay.Options{
				WorkDir:       workDir,
				InspectPrompt: true,
				Agents:        []engine.Agent{{Name: "build"}},
				LeanCode:      "full",
			}
		},
	},
	{
		name:   "prompt-plan",
		inputs: nil,
		opts: func(workDir string) replay.Options {
			return replay.Options{
				WorkDir:       workDir,
				InspectPrompt: true,
				Agents: []engine.Agent{
					{Name: "build"},
					{Name: "plan"},
				},
				InitialAgent: "plan",
				LeanCode:     "lite",
			}
		},
	},
	{
		name:   "prompt-agent-persona",
		inputs: nil,
		opts: func(workDir string) replay.Options {
			return replay.Options{
				WorkDir:       workDir,
				InspectPrompt: true,
				Agents: []engine.Agent{
					{Name: "build"},
					{Name: "reviewer", Prompt: "You are a meticulous code reviewer for prompt regression."},
				},
				InitialAgent: "reviewer",
				LeanCode:     "off",
			}
		},
	},
}

// TestPromptRegressionReport runs the E3.2 corpus and prints tool/turn/token
// deltas vs testdata/metrics.json. Default mode is non-blocking (report only).
//
//	UPDATE_METRICS=1           rewrite baseline metrics
//	PROMPT_REGRESSION_STRICT=1  fail the test on any metric delta
//
// Re-run after changes to internal/engine/prompt.go, prompt_tools.go,
// prompt/*.txt, or agent definitions (internal/config/agents).
func TestPromptRegressionReport(t *testing.T) {
	baselinePath := filepath.Join("testdata", "metrics.json")

	rows := make([]replay.Metrics, 0, len(promptRegScenarios))
	for _, sc := range promptRegScenarios {
		res, err := replay.Run(context.Background(), sc.inputs, sc.opts(t.TempDir()))
		if err != nil {
			t.Fatalf("%s: %v", sc.name, err)
		}
		m := replay.CollectMetrics(sc.name, res)
		switch sc.name {
		case "bash-run":
			if m.ToolCallCount != 1 || m.Turns != 1 {
				t.Fatalf("bash-run tools=%d turns=%d", m.ToolCallCount, m.Turns)
			}
		case "multi-turn":
			if m.ToolCallCount != 1 || m.Turns != 2 {
				t.Fatalf("multi-turn tools=%d turns=%d", m.ToolCallCount, m.Turns)
			}
		case "plain-echo":
			if m.ToolCallCount != 0 || m.Turns != 1 {
				t.Fatalf("plain-echo tools=%d turns=%d", m.ToolCallCount, m.Turns)
			}
		}
		if m.PromptChars <= 0 {
			t.Fatalf("%s: PromptChars = %d, want composed system prompt", sc.name, m.PromptChars)
		}
		rows = append(rows, m)
	}

	if os.Getenv("UPDATE_METRICS") == "1" {
		if err := replay.WriteMetricsBaseline(baselinePath, rows); err != nil {
			t.Fatalf("write baseline: %v", err)
		}
		t.Logf("updated %s (%d scenarios)", baselinePath, len(rows))
	}

	baseline, err := replay.LoadMetricsBaseline(baselinePath)
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("missing %s — run UPDATE_METRICS=1 go test ./internal/replay/ -run TestPromptRegressionReport", baselinePath)
		}
		t.Fatal(err)
	}

	report := replay.FormatMetricsReport(rows, baseline)
	t.Logf("prompt regression report (E3.2):\n%s", report)

	var diverged []string
	for _, row := range rows {
		want, ok := baseline[row.Scenario]
		if !ok {
			diverged = append(diverged, row.Scenario+": missing from baseline")
			continue
		}
		want.Scenario = row.Scenario
		d := replay.DiffMetrics(want, row)
		// StableZero ignores cwd/date-sensitive system totals.
		if !d.StableZero() {
			raw, _ := json.Marshal(d)
			diverged = append(diverged, row.Scenario+": "+string(raw))
		}
	}
	if len(diverged) == 0 {
		t.Log("all scenarios match metrics baseline")
		return
	}
	msg := "metric deltas vs baseline:\n  " + strings.Join(diverged, "\n  ")
	if os.Getenv("PROMPT_REGRESSION_STRICT") == "1" {
		t.Fatal(msg + "\n(re-baseline with UPDATE_METRICS=1 after intentional prompt changes)")
	}
	// Non-blocking report (default): surface deltas without failing make test / CI.
	t.Log(msg + "\n(non-blocking; set PROMPT_REGRESSION_STRICT=1 to fail)")
}
