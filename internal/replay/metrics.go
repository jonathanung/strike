package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// Metrics is the per-scenario prompt-regression report row (E3.2).
// Token fields sum root-session UsageReported events from an echo replay.
// System/prompt fields come from EffectivePrompt (InspectPrompt).
type Metrics struct {
	Scenario string `json:"scenario"`

	// Behavioral counters (also covered by golden tool-sequence diffs).
	ToolCallCount int `json:"toolCallCount"`
	Turns         int `json:"turns"`

	// Sum of root UsageReported token parts (echo estimates; deterministic).
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	UsedTokens   int `json:"usedTokens"`

	// System prompt size from EffectivePrompt. PromptChars excludes the
	// environment layer so baselines stay stable across calendar dates.
	SystemChars  int `json:"systemChars"`
	PromptChars  int `json:"promptChars"`
	SystemTokens int `json:"systemTokens"` // attribution.system (est.)
	ToolsTokens  int `json:"toolsTokens"`  // attribution.tools (est.)
	TotalTokens  int `json:"totalTokens"`  // attribution.total (est.)
}

// MetricsDelta is got − want for each numeric field.
type MetricsDelta struct {
	ToolCallCount int `json:"toolCallCount"`
	Turns         int `json:"turns"`
	InputTokens   int `json:"inputTokens"`
	OutputTokens  int `json:"outputTokens"`
	UsedTokens    int `json:"usedTokens"`
	SystemChars   int `json:"systemChars"`
	PromptChars   int `json:"promptChars"`
	SystemTokens  int `json:"systemTokens"`
	ToolsTokens   int `json:"toolsTokens"`
	TotalTokens   int `json:"totalTokens"`
}

// CollectMetrics derives a Metrics row from a replay Result.
// scenario is a stable name for the report/baseline key.
func CollectMetrics(scenario string, res Result) Metrics {
	m := Metrics{
		Scenario:      scenario,
		ToolCallCount: len(res.ToolCalls),
		Turns:         res.Turns,
	}
	for _, ev := range res.Events {
		u, ok := ev.(protocol.UsageReported)
		if !ok || u.ParentSessionID != "" || u.Depth > 0 {
			continue
		}
		if u.Input.Known {
			m.InputTokens += u.Input.N
		}
		if u.Output.Known {
			m.OutputTokens += u.Output.N
		}
		if u.Used.Known {
			m.UsedTokens += u.Used.N
		}
	}
	if res.Effective != nil {
		m.SystemChars = res.Effective.SystemChars
		m.PromptChars = promptCharsExcludingEnv(res.Effective.Layers)
		if res.Effective.Attribution.System.Known {
			m.SystemTokens = res.Effective.Attribution.System.N
		}
		if res.Effective.Attribution.Tools.Known {
			m.ToolsTokens = res.Effective.Attribution.Tools.N
		}
		if res.Effective.Attribution.Total.Known {
			m.TotalTokens = res.Effective.Attribution.Total.N
		}
	}
	return m
}

// promptCharsExcludingEnv sums layer char counts except environment (date/cwd)
// so prompt-regression baselines do not drift day-to-day.
func promptCharsExcludingEnv(layers []protocol.PromptLayerInfo) int {
	var n int
	for _, layer := range layers {
		if layer.Kind == protocol.PromptLayerEnvironment {
			continue
		}
		n += layer.Chars
	}
	return n
}

// DiffMetrics returns got − want for each field.
func DiffMetrics(want, got Metrics) MetricsDelta {
	return MetricsDelta{
		ToolCallCount: got.ToolCallCount - want.ToolCallCount,
		Turns:         got.Turns - want.Turns,
		InputTokens:   got.InputTokens - want.InputTokens,
		OutputTokens:  got.OutputTokens - want.OutputTokens,
		UsedTokens:    got.UsedTokens - want.UsedTokens,
		SystemChars:   got.SystemChars - want.SystemChars,
		PromptChars:   got.PromptChars - want.PromptChars,
		SystemTokens:  got.SystemTokens - want.SystemTokens,
		ToolsTokens:   got.ToolsTokens - want.ToolsTokens,
		TotalTokens:   got.TotalTokens - want.TotalTokens,
	}
}

// Zero reports whether every delta field is zero.
func (d MetricsDelta) Zero() bool {
	return d == (MetricsDelta{})
}

// StableZero reports whether regression-relevant fields match.
// SystemChars/SystemTokens/TotalTokens are excluded: they embed the
// environment layer (cwd path + calendar date) and are not portable across
// machines or days. PromptChars strips that layer for stable baselines.
func (d MetricsDelta) StableZero() bool {
	return d.ToolCallCount == 0 &&
		d.Turns == 0 &&
		d.InputTokens == 0 &&
		d.OutputTokens == 0 &&
		d.UsedTokens == 0 &&
		d.PromptChars == 0 &&
		d.ToolsTokens == 0
}

// LoadMetricsBaseline reads a JSON object map[scenario]Metrics.
func LoadMetricsBaseline(path string) (map[string]Metrics, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]Metrics
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("replay: metrics baseline %s: %w", path, err)
	}
	if m == nil {
		m = map[string]Metrics{}
	}
	return m, nil
}

// WriteMetricsBaseline persists metrics keyed by scenario name (stable JSON).
func WriteMetricsBaseline(path string, rows []Metrics) error {
	byName := make(map[string]Metrics, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Scenario)
		if name == "" {
			return fmt.Errorf("replay: metrics row missing scenario name")
		}
		byName[name] = row
	}
	// Encode with sorted keys via intermediate ordered marshal.
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
		key, err := json.Marshal(name)
		if err != nil {
			return err
		}
		buf.Write(key)
		buf.WriteString(": ")
		// Compact single-line value.
		val, err := json.Marshal(byName[name])
		if err != nil {
			return err
		}
		buf.Write(val)
	}
	if len(names) > 0 {
		buf.WriteByte('\n')
	}
	buf.WriteString("}\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// FormatMetricsReport renders a human-readable table of metrics and deltas.
// Delta columns cover StableZero fields only (cwd/date-sensitive system totals
// are shown as absolute values, not deltas, so CI noise is not mistaken for
// regressions). When baseline is nil or missing a scenario, deltas show "n/a".
func FormatMetricsReport(rows []Metrics, baseline map[string]Metrics) string {
	var buf strings.Builder
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "scenario\ttools\tturns\tin\tout\tused\tpromptCh\ttoolsTok\tsysTok\tΔtools\tΔturns\tΔused\tΔpromptCh\tΔtoolsTok")
	for _, row := range rows {
		dTools, dTurns, dUsed, dPrompt, dToolsTok := "n/a", "n/a", "n/a", "n/a", "n/a"
		if baseline != nil {
			if want, ok := baseline[row.Scenario]; ok {
				d := DiffMetrics(want, row)
				dTools = fmt.Sprintf("%+d", d.ToolCallCount)
				dTurns = fmt.Sprintf("%+d", d.Turns)
				dUsed = fmt.Sprintf("%+d", d.UsedTokens)
				dPrompt = fmt.Sprintf("%+d", d.PromptChars)
				dToolsTok = fmt.Sprintf("%+d", d.ToolsTokens)
			}
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\n",
			row.Scenario,
			row.ToolCallCount, row.Turns,
			row.InputTokens, row.OutputTokens, row.UsedTokens,
			row.PromptChars, row.ToolsTokens, row.SystemTokens,
			dTools, dTurns, dUsed, dPrompt, dToolsTok,
		)
	}
	_ = w.Flush()
	return buf.String()
}
