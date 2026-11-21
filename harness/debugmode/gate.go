package debugmode

import (
	"encoding/json"
	"fmt"
	"strings"
)

// GateError is a phase-gate denial with a user-facing reason. Callers
// surface it like a permission denial so the model can correct course.
type GateError struct {
	Tool   string
	Reason string
}

func (e *GateError) Error() string {
	return fmt.Sprintf("debug phase gate denied %s: %s", e.Tool, e.Reason)
}

// CheckToolCall applies the phase gate to one tool call. In the hypothesis
// phase, edit and apply_patch are allowed only when every changed line is
// tagged with Marker (instrumentation); write, delete and move are denied. Other
// tools and other phases pass. Shell commands are not inspected here; the
// permission layer governs bash.
func (s *Session) CheckToolCall(name string, args json.RawMessage) error {
	if s.Phase() != PhaseHypothesis {
		return nil
	}
	return checkHypothesisCall(name, args)
}

func checkHypothesisCall(name string, args json.RawMessage) error {
	deny := func(reason string) error { return &GateError{Tool: name, Reason: reason} }
	switch name {
	case "edit":
		var a struct {
			OldString string `json:"oldString"`
			NewString string `json:"newString"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return deny("unreadable edit arguments")
		}
		if line, ok := untaggedChange(a.OldString, a.NewString); !ok {
			return deny(untaggedReason(line))
		}
		return nil
	case "write":
		// write replaces a whole file and could clobber code with an
		// all-tagged body; instrumentation goes through edit or apply_patch.
		return deny("whole-file writes are fix edits; add tagged lines with edit or apply_patch, or confirm a hypothesis first")
	case "apply_patch":
		var a struct {
			Patch string `json:"patch"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return deny("unreadable patch arguments")
		}
		return checkPatch(name, a.Patch)
	case "delete", "move":
		return deny("file deletes and moves are fix edits; confirm a hypothesis or override first")
	}
	return nil
}

// checkPatch requires every +/- body line of an apply_patch envelope to be
// tagged, and rejects delete/move operations.
func checkPatch(name, patch string) error {
	for _, ln := range splitLines(patch) {
		switch {
		case strings.HasPrefix(ln, "*** Delete File:"), strings.HasPrefix(ln, "*** Move to:"):
			return &GateError{Tool: name, Reason: "file deletes and moves are fix edits; confirm a hypothesis or override first"}
		case strings.HasPrefix(ln, "***"):
			continue
		case strings.HasPrefix(ln, "+"), strings.HasPrefix(ln, "-"):
			body := ln[1:]
			if strings.TrimSpace(body) != "" && !strings.Contains(body, Marker) {
				return &GateError{Tool: name, Reason: untaggedReason(body)}
			}
		}
	}
	return nil
}

// untaggedChange requires the untagged, non-blank lines of old and new to
// be the same sequence, so only tagged (and blank) lines may be added,
// removed or moved. It reports the first differing untagged line.
func untaggedChange(oldText, newText string) (string, bool) {
	a, b := untagged(splitLines(oldText)), untagged(splitLines(newText))
	for i := 0; i < len(a) || i < len(b); i++ {
		switch {
		case i >= len(a):
			return b[i], false
		case i >= len(b):
			return a[i], false
		case a[i] != b[i]:
			return b[i], false
		}
	}
	return "", true
}

func untagged(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" && !strings.Contains(l, Marker) {
			out = append(out, l)
		}
	}
	return out
}

func untaggedReason(line string) string {
	line = strings.TrimSpace(line)
	if len(line) > 80 {
		line = line[:80] + "…"
	}
	return fmt.Sprintf("hypothesis phase allows only instrumentation lines tagged %s; untagged change %q. Confirm a hypothesis with evidence (or have the user override) to unlock fix edits", Marker, line)
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
