package replay

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// CompareOptions control how nondeterministic markers affect equality.
type CompareOptions struct {
	// IgnoreNondeterministic drops tool calls (and related fields) marked
	// nondeterministic from both sides before comparing.
	IgnoreNondeterministic bool
	// FlagNondeterministic reports differences that only touch marked
	// steps as Flags instead of Divergences (Equal may still be true when
	// deterministic parts match).
	FlagNondeterministic bool
}

// FieldDelta is one named field difference.
type FieldDelta struct {
	Path string `json:"path"`
	Want string `json:"want,omitempty"`
	Got  string `json:"got,omitempty"`
}

// Divergence is a deterministic mismatch that fails the compare.
type Divergence struct {
	Path    string `json:"path"`
	Message string `json:"message"`
	Want    string `json:"want,omitempty"`
	Got     string `json:"got,omitempty"`
}

// CompareReport is the structured delta between two recordings/runs.
// Match is true when Divergences is empty (deterministic equality).
type CompareReport struct {
	Match bool `json:"match"`

	// ToolSequence is set when tool sequences differ (after filters).
	ToolSequence string `json:"toolSequence,omitempty"`

	ExitStatus *FieldDelta `json:"exitStatus,omitempty"`

	SettingsDeltas []FieldDelta `json:"settingsDeltas,omitempty"`
	HandoffDeltas  []FieldDelta `json:"handoffDeltas,omitempty"`
	GateDeltas     []FieldDelta `json:"gateDeltas,omitempty"`
	FilesDelta     *FieldDelta  `json:"filesDelta,omitempty"`

	// Divergences are hard failures (deterministic mismatches).
	Divergences []Divergence `json:"divergences,omitempty"`
	// Flags are nondeterministic differences when FlagNondeterministic is set.
	Flags []string `json:"flags,omitempty"`

	// WantTools / GotTools are the sequences actually compared (post-filter).
	WantTools []ToolCall `json:"wantTools,omitempty"`
	GotTools  []ToolCall `json:"gotTools,omitempty"`
}

// Equal reports whether the compare found no deterministic divergences.
func (r CompareReport) Equal() bool {
	return r.Match && len(r.Divergences) == 0
}

// CompareRecordings diffs two Recordings. want is the baseline (golden);
// got is the candidate run.
func CompareRecordings(want, got Recording, opts CompareOptions) CompareReport {
	rep := CompareReport{Match: true}

	wantTools, wantDrop := filterTools(want.ToolCalls, want.Markers, opts)
	gotTools, gotDrop := filterTools(got.ToolCalls, got.Markers, opts)
	rep.WantTools = wantTools
	rep.GotTools = gotTools

	if err := DiffToolCalls(wantTools, gotTools); err != nil {
		msg := err.Error()
		if opts.FlagNondeterministic && onlyNondeterministicToolDiff(want, got, wantDrop, gotDrop) {
			rep.Flags = append(rep.Flags, "tool-sequence (nondeterministic): "+msg)
		} else {
			rep.Match = false
			rep.ToolSequence = msg
			rep.Divergences = append(rep.Divergences, Divergence{
				Path:    "toolCalls",
				Message: msg,
			})
		}
	}

	// Exit status
	if want.ExitStatus != got.ExitStatus {
		d := FieldDelta{Path: "exitStatus", Want: want.ExitStatus, Got: got.ExitStatus}
		rep.ExitStatus = &d
		rep.Match = false
		rep.Divergences = append(rep.Divergences, Divergence{
			Path:    "exitStatus",
			Message: "exit status mismatch",
			Want:    want.ExitStatus,
			Got:     got.ExitStatus,
		})
	}

	// Settings (behavioral identity) — skip prompt digest when env-marked unless strict.
	rep.SettingsDeltas = diffSettings(want.Settings, got.Settings, opts, want.Markers)
	for _, d := range rep.SettingsDeltas {
		if opts.FlagNondeterministic && (d.Path == "settings.promptDigest" || d.Path == "settings.systemChars") {
			rep.Flags = append(rep.Flags, fmt.Sprintf("%s: %q → %q", d.Path, d.Want, d.Got))
			continue
		}
		rep.Match = false
		rep.Divergences = append(rep.Divergences, Divergence{
			Path:    d.Path,
			Message: "settings mismatch",
			Want:    d.Want,
			Got:     d.Got,
		})
	}

	// Handoffs (#771)
	rep.HandoffDeltas = diffHandoffs(want.Handoffs, got.Handoffs)
	for _, d := range rep.HandoffDeltas {
		rep.Match = false
		rep.Divergences = append(rep.Divergences, Divergence{
			Path:    d.Path,
			Message: "handoff mismatch",
			Want:    d.Want,
			Got:     d.Got,
		})
	}

	// Verification gates (#780)
	rep.GateDeltas = diffVerifications(want.Verifications, got.Verifications)
	for _, d := range rep.GateDeltas {
		rep.Match = false
		rep.Divergences = append(rep.Divergences, Divergence{
			Path:    d.Path,
			Message: "verification/gate mismatch",
			Want:    d.Want,
			Got:     d.Got,
		})
	}

	// Files changed refs
	if !stringSlicesEqual(want.FilesChanged, got.FilesChanged) {
		d := FieldDelta{
			Path: "filesChanged",
			Want: strings.Join(want.FilesChanged, ","),
			Got:  strings.Join(got.FilesChanged, ","),
		}
		rep.FilesDelta = &d
		rep.Match = false
		rep.Divergences = append(rep.Divergences, Divergence{
			Path:    "filesChanged",
			Message: "filesChanged mismatch",
			Want:    d.Want,
			Got:     d.Got,
		})
	}

	// Tool result digests for deterministic tools (optional soft check).
	if !opts.IgnoreNondeterministic {
		for i := 0; i < len(want.ToolResults) && i < len(got.ToolResults) && i < len(want.ToolCalls) && i < len(got.ToolCalls); i++ {
			if toolMarked(want.Markers, i) || toolMarked(got.Markers, i) {
				if want.ToolResults[i].OutputDigest != got.ToolResults[i].OutputDigest {
					if opts.FlagNondeterministic {
						rep.Flags = append(rep.Flags, fmt.Sprintf("toolResults[%d].outputDigest (nondeterministic)", i))
					}
				}
				continue
			}
			// Name/error bit should match for aligned deterministic tools.
			if want.ToolCalls[i].Name != got.ToolCalls[i].Name {
				continue // already reported via tool sequence
			}
			if want.ToolResults[i].IsError != got.ToolResults[i].IsError {
				rep.Match = false
				rep.Divergences = append(rep.Divergences, Divergence{
					Path:    fmt.Sprintf("toolResults[%d].isError", i),
					Message: "tool error bit mismatch",
					Want:    fmt.Sprintf("%v", want.ToolResults[i].IsError),
					Got:     fmt.Sprintf("%v", got.ToolResults[i].IsError),
				})
			}
		}
	}

	if len(rep.Divergences) > 0 {
		rep.Match = false
	}
	return rep
}

// FormatCompareReport renders a human-readable summary.
func FormatCompareReport(rep CompareReport) string {
	var b strings.Builder
	if rep.Equal() {
		b.WriteString("equal: true\n")
	} else {
		b.WriteString("equal: false\n")
	}
	for _, d := range rep.Divergences {
		fmt.Fprintf(&b, "DIVERGE %s: %s", d.Path, d.Message)
		if d.Want != "" || d.Got != "" {
			fmt.Fprintf(&b, " want=%q got=%q", d.Want, d.Got)
		}
		b.WriteByte('\n')
	}
	for _, f := range rep.Flags {
		fmt.Fprintf(&b, "FLAG %s\n", f)
	}
	if len(rep.Divergences) == 0 && len(rep.Flags) == 0 && rep.Equal() {
		b.WriteString("(no deltas)\n")
	}
	return b.String()
}

func filterTools(calls []ToolCall, markers []Marker, opts CompareOptions) (filtered []ToolCall, dropped map[int]bool) {
	dropped = map[int]bool{}
	if !opts.IgnoreNondeterministic {
		return append([]ToolCall(nil), calls...), dropped
	}
	out := make([]ToolCall, 0, len(calls))
	for i, c := range calls {
		if toolMarked(markers, i) {
			dropped[i] = true
			continue
		}
		out = append(out, c)
	}
	return out, dropped
}

func toolMarked(markers []Marker, toolIndex int) bool {
	for _, m := range markers {
		if m.ToolIndex == toolIndex && m.ToolIndex >= 0 {
			return true
		}
	}
	return false
}

func onlyNondeterministicToolDiff(want, got Recording, _, _ map[int]bool) bool {
	// Heuristic: if filtering nondeterministic tools makes sequences match, the
	// diff is only nondeterministic.
	w, _ := filterTools(want.ToolCalls, want.Markers, CompareOptions{IgnoreNondeterministic: true})
	g, _ := filterTools(got.ToolCalls, got.Markers, CompareOptions{IgnoreNondeterministic: true})
	return DiffToolCalls(w, g) == nil && (hasToolMarkers(want.Markers) || hasToolMarkers(got.Markers))
}

func hasToolMarkers(markers []Marker) bool {
	for _, m := range markers {
		if m.ToolIndex >= 0 {
			return true
		}
	}
	return false
}

func diffSettings(want, got SettingsDigest, opts CompareOptions, markers []Marker) []FieldDelta {
	var out []FieldDelta
	add := func(path, w, g string) {
		if w == g {
			return
		}
		out = append(out, FieldDelta{Path: path, Want: w, Got: g})
	}
	add("settings.provider", want.Provider, got.Provider)
	add("settings.model", want.Model, got.Model)
	add("settings.agent", want.Agent, got.Agent)
	add("settings.effort", want.Effort, got.Effort)
	add("settings.autonomy", want.Autonomy, got.Autonomy)
	add("settings.permissionMode", want.PermissionMode, got.PermissionMode)
	// toolsDigest includes nondeterministic tool names (webfetch/sleep). When
	// ignoring those steps, skip digest compare so sequence-only equality works.
	if !opts.IgnoreNondeterministic {
		add("settings.toolsDigest", want.ToolsDigest, got.ToolsDigest)
	}

	// Prompt digest drifts with environment; ignore when requested or env-marked.
	hasEnv := false
	for _, m := range markers {
		if m.Kind == MarkerEnv {
			hasEnv = true
			break
		}
	}
	if opts.IgnoreNondeterministic || hasEnv {
		// skip promptDigest / systemChars
	} else {
		add("settings.promptDigest", want.PromptDigest, got.PromptDigest)
		if want.SystemChars != got.SystemChars {
			out = append(out, FieldDelta{
				Path: "settings.systemChars",
				Want: fmt.Sprintf("%d", want.SystemChars),
				Got:  fmt.Sprintf("%d", got.SystemChars),
			})
		}
	}

	wf, gf := "", ""
	if want.Fast != nil {
		wf = fmt.Sprintf("%v", *want.Fast)
	}
	if got.Fast != nil {
		gf = fmt.Sprintf("%v", *got.Fast)
	}
	add("settings.fast", wf, gf)
	return out
}

func diffHandoffs(want, got []HandoffSnapshot) []FieldDelta {
	var out []FieldDelta
	n := len(want)
	if len(got) > n {
		n = len(got)
	}
	if len(want) != len(got) {
		out = append(out, FieldDelta{
			Path: "handoffs.length",
			Want: fmt.Sprintf("%d", len(want)),
			Got:  fmt.Sprintf("%d", len(got)),
		})
	}
	for i := 0; i < n; i++ {
		var w, g HandoffSnapshot
		if i < len(want) {
			w = want[i]
		}
		if i < len(got) {
			g = got[i]
		}
		prefix := fmt.Sprintf("handoffs[%d]", i)
		// Compare structural fields; child session ids are nondeterministic.
		addH := func(field, wv, gv string) {
			if wv != gv {
				out = append(out, FieldDelta{Path: prefix + "." + field, Want: wv, Got: gv})
			}
		}
		addH("status", w.Status, g.Status)
		addH("summary", w.Summary, g.Summary)
		addH("verification", w.Verification, g.Verification)
		addH("recommendedNextAction", w.RecommendedNextAction, g.RecommendedNextAction)
		addH("incomplete", fmt.Sprintf("%v", w.Incomplete), fmt.Sprintf("%v", g.Incomplete))
		if !stringSlicesEqual(w.FilesChanged, g.FilesChanged) {
			addH("filesChanged", strings.Join(w.FilesChanged, ","), strings.Join(g.FilesChanged, ","))
		}
		if !stringSlicesEqual(w.Findings, g.Findings) {
			addH("findings", strings.Join(w.Findings, ","), strings.Join(g.Findings, ","))
		}
		if !stringSlicesEqual(w.Blockers, g.Blockers) {
			addH("blockers", strings.Join(w.Blockers, ","), strings.Join(g.Blockers, ","))
		}
	}
	return out
}

func diffVerifications(want, got []VerificationSnapshot) []FieldDelta {
	var out []FieldDelta
	if len(want) != len(got) {
		out = append(out, FieldDelta{
			Path: "verifications.length",
			Want: fmt.Sprintf("%d", len(want)),
			Got:  fmt.Sprintf("%d", len(got)),
		})
	}
	n := len(want)
	if len(got) > n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		var w, g VerificationSnapshot
		if i < len(want) {
			w = want[i]
		}
		if i < len(got) {
			g = got[i]
		}
		prefix := fmt.Sprintf("verifications[%d]", i)
		add := func(field, wv, gv string) {
			if wv != gv {
				out = append(out, FieldDelta{Path: prefix + "." + field, Want: wv, Got: gv})
			}
		}
		add("passed", fmt.Sprintf("%v", w.Passed), fmt.Sprintf("%v", g.Passed))
		add("claimed", fmt.Sprintf("%v", w.Claimed), fmt.Sprintf("%v", g.Claimed))
		add("verified", fmt.Sprintf("%v", w.Verified), fmt.Sprintf("%v", g.Verified))
		add("summary", w.Summary, g.Summary)
		if !stringSlicesEqual(w.CheckNames, g.CheckNames) {
			add("checkNames", strings.Join(w.CheckNames, ","), strings.Join(g.CheckNames, ","))
		}
		if !boolSlicesEqual(w.CheckPassed, g.CheckPassed) {
			add("checkPassed", fmt.Sprintf("%v", w.CheckPassed), fmt.Sprintf("%v", g.CheckPassed))
		}
	}
	return out
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	// Compare as sorted copies so order of discovery does not fail compare.
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	return reflect.DeepEqual(as, bs)
}

func boolSlicesEqual(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// DetectReplayDivergence re-runs user inputs from golden events via echo and
// compares the normalized tool sequence (and exit/toolsDigest). Nondeterministic
// markers and session-local settings are ignored so goldens stay portable.
// workDir is required (engine workspace).
func DetectReplayDivergence(ctx context.Context, goldenEvents []protocol.Event, workDir string) (CompareReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	want := BuildRecording(goldenEvents, RecordingOptions{})
	inputs := ExtractUserInputs(goldenEvents)
	res, err := Run(ctx, inputs, Options{WorkDir: workDir})
	if err != nil {
		return CompareReport{}, err
	}
	got := BuildRecordingFromResult(res, RecordingOptions{})
	rep := CompareRecordings(want, got, CompareOptions{
		IgnoreNondeterministic: true,
	})
	// Echo re-runs mint new session ids, agent defaults, and model text.
	// Keep tool sequence + toolsDigest; drop other settings and empty-exit noise.
	filtered := rep.Divergences[:0]
	for _, d := range rep.Divergences {
		if d.Path == "exitStatus" && want.ExitStatus == "" {
			continue
		}
		if strings.HasPrefix(d.Path, "settings.") && d.Path != "settings.toolsDigest" {
			continue
		}
		// File paths from live turns may be empty on goldens that omit Files.
		if d.Path == "filesChanged" && len(want.FilesChanged) == 0 {
			continue
		}
		// Handoffs/gates only when golden recorded them.
		if strings.HasPrefix(d.Path, "handoffs") && len(want.Handoffs) == 0 {
			continue
		}
		if strings.HasPrefix(d.Path, "verifications") && len(want.Verifications) == 0 {
			continue
		}
		filtered = append(filtered, d)
	}
	rep.Divergences = filtered
	if want.ExitStatus == "" {
		rep.ExitStatus = nil
	}
	rep.SettingsDeltas = nil
	rep.HandoffDeltas = nil
	rep.GateDeltas = nil
	rep.FilesDelta = nil
	rep.Match = len(rep.Divergences) == 0
	return rep, nil
}
