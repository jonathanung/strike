package replay

import (
	"fmt"
	"strings"
)

// CompareRunSnapshots diffs two multi-agent run snapshots (#782).
// want is the baseline; got is the candidate.
//
// Compares spawn identity (agent, settings, bundle digest, config, tools,
// gates specs) and completion outcome (exit, handoff, verification). When both
// sides embed a Recording, also runs CompareRecordings on those (tool sequence).
func CompareRunSnapshots(want, got RunSnapshot, opts CompareOptions) CompareReport {
	rep := CompareReport{Match: true}

	addDiv := func(path, msg, w, g string) {
		rep.Match = false
		rep.Divergences = append(rep.Divergences, Divergence{
			Path:    path,
			Message: msg,
			Want:    w,
			Got:     g,
		})
	}

	// Exit status
	if want.ExitStatus != got.ExitStatus {
		d := FieldDelta{Path: "exitStatus", Want: want.ExitStatus, Got: got.ExitStatus}
		rep.ExitStatus = &d
		addDiv("exitStatus", "exit status mismatch", want.ExitStatus, got.ExitStatus)
	}

	// Spawn identity
	if want.Agent != got.Agent {
		addDiv("agent", "agent mismatch", want.Agent, got.Agent)
	}
	if want.PromptDigest != "" && got.PromptDigest != "" && want.PromptDigest != got.PromptDigest {
		addDiv("promptDigest", "prompt digest mismatch", want.PromptDigest, got.PromptDigest)
	}

	// Settings (shared with Recording compare)
	setDeltas := diffSettings(want.Settings, got.Settings, opts, nil)
	rep.SettingsDeltas = setDeltas
	for _, d := range setDeltas {
		if opts.FlagNondeterministic && (d.Path == "settings.promptDigest" || d.Path == "settings.systemChars") {
			rep.Flags = append(rep.Flags, fmt.Sprintf("%s: %q → %q", d.Path, d.Want, d.Got))
			continue
		}
		addDiv(d.Path, "settings mismatch", d.Want, d.Got)
	}

	// Tool allow-list
	if !stringSlicesEqual(want.ToolAllowList, got.ToolAllowList) {
		addDiv("toolAllowList", "tool allow-list mismatch",
			strings.Join(want.ToolAllowList, ","), strings.Join(got.ToolAllowList, ","))
	}

	// Context bundle digest
	wb, gb := "", ""
	if want.ContextBundle != nil {
		wb = want.ContextBundle.Digest
	}
	if got.ContextBundle != nil {
		gb = got.ContextBundle.Digest
	}
	if wb != gb {
		addDiv("contextBundle.digest", "context bundle digest mismatch", wb, gb)
	}

	// Config digest
	if want.Config.Digest != got.Config.Digest {
		addDiv("config.digest", "config digest mismatch", want.Config.Digest, got.Config.Digest)
	}

	// Configured gate specs (spawn)
	if !gateSpecsEqual(want.VerifyGates, got.VerifyGates) {
		addDiv("verifyGates", "verify gate specs mismatch",
			formatGateSpecs(want.VerifyGates), formatGateSpecs(got.VerifyGates))
	}

	// Handoff (single)
	var wantH, gotH []HandoffSnapshot
	if want.Handoff != nil {
		wantH = []HandoffSnapshot{*want.Handoff}
	}
	if got.Handoff != nil {
		gotH = []HandoffSnapshot{*got.Handoff}
	}
	rep.HandoffDeltas = diffHandoffs(wantH, gotH)
	for _, d := range rep.HandoffDeltas {
		// Rewrite path prefix handoffs[0] → handoff for snapshot clarity.
		path := strings.Replace(d.Path, "handoffs[0]", "handoff", 1)
		path = strings.Replace(path, "handoffs.length", "handoff", 1)
		addDiv(path, "handoff mismatch", d.Want, d.Got)
	}

	// Verification (single)
	var wantV, gotV []VerificationSnapshot
	if want.Verification != nil {
		wantV = []VerificationSnapshot{*want.Verification}
	}
	if got.Verification != nil {
		gotV = []VerificationSnapshot{*got.Verification}
	}
	rep.GateDeltas = diffVerifications(wantV, gotV)
	for _, d := range rep.GateDeltas {
		path := strings.Replace(d.Path, "verifications[0]", "verification", 1)
		path = strings.Replace(path, "verifications.length", "verification", 1)
		addDiv(path, "verification/gate mismatch", d.Want, d.Got)
	}

	// Embedded recordings (tool sequence etc.). Only when both sides captured a
	// child Recording — synthetic completion-only sides would false-diverge on tools.
	if want.Recording != nil && got.Recording != nil {
		wr := *want.Recording
		gr := *got.Recording
		sub := CompareRecordings(wr, gr, opts)
		// Merge tool sequence / recording-level divergences; skip fields already
		// covered at snapshot level (exit, handoff, gates, settings).
		if sub.ToolSequence != "" {
			rep.ToolSequence = sub.ToolSequence
			rep.WantTools = sub.WantTools
			rep.GotTools = sub.GotTools
		}
		for _, d := range sub.Divergences {
			if d.Path == "exitStatus" ||
				strings.HasPrefix(d.Path, "settings.") ||
				strings.HasPrefix(d.Path, "handoffs") ||
				strings.HasPrefix(d.Path, "verifications") ||
				d.Path == "filesChanged" {
				continue
			}
			addDiv("recording."+d.Path, d.Message, d.Want, d.Got)
		}
		rep.Flags = append(rep.Flags, sub.Flags...)
	}

	if len(rep.Divergences) > 0 {
		rep.Match = false
	}
	return rep
}

func gateSpecsEqual(a, b []GateSpecSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Kind != b[i].Kind || a[i].Value != b[i].Value {
			return false
		}
	}
	return true
}

func formatGateSpecs(gs []GateSpecSnapshot) string {
	if len(gs) == 0 {
		return ""
	}
	parts := make([]string, len(gs))
	for i, g := range gs {
		parts[i] = g.Kind + ":" + g.Name + ":" + g.Value
	}
	return strings.Join(parts, ";")
}
