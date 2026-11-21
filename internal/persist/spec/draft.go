package spec

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaxDraftRequirements bounds how many requirement rows one model draft
// may produce; extra rows are dropped.
const MaxDraftRequirements = 30

// ParseRequirementsDraft reads a model's requirements draft: a JSON array
// of {"story", "criteria"} objects, or an object holding one under
// "requirements", optionally wrapped in a code fence or prose. Rows get
// no id (ids are assigned when the user saves) and blank stories are
// dropped. The result is an unsaved draft; nothing here approves anything.
func ParseRequirementsDraft(text string) ([]Requirement, error) {
	raw := strings.TrimSpace(text)
	type row struct {
		Story    string   `json:"story"`
		Criteria []string `json:"criteria"`
	}
	var rows []row
	if err := json.Unmarshal([]byte(jsonSpan(raw, '[', ']')), &rows); err != nil {
		var wrapped struct {
			Requirements []row `json:"requirements"`
		}
		if err2 := json.Unmarshal([]byte(jsonSpan(raw, '{', '}')), &wrapped); err2 != nil {
			return nil, fmt.Errorf("%w: model draft is not a JSON list of requirements", ErrInvalid)
		}
		rows = wrapped.Requirements
	}
	out := []Requirement{}
	for _, r := range rows {
		story := strings.TrimSpace(r.Story)
		if story == "" {
			continue
		}
		crit := []string{}
		for _, c := range r.Criteria {
			if c = strings.TrimSpace(c); c != "" {
				crit = append(crit, c)
			}
		}
		out = append(out, Requirement{Story: story, Criteria: crit})
		if len(out) == MaxDraftRequirements {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: model draft has no requirements", ErrInvalid)
	}
	return out, nil
}

// jsonSpan returns s from the first open to the last close byte, which
// strips a code fence or a sentence of prose around the JSON. It returns
// s unchanged when either byte is missing.
func jsonSpan(s string, open, close byte) string {
	i := strings.IndexByte(s, open)
	j := strings.LastIndexByte(s, close)
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}
