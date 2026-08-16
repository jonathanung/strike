package engine

import (
	"encoding/json"
	"strings"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// applyPlanSectionDelegate settles plan section correlation after a child
// finishes. Only the correlated section is considered; content CAS inside the
// plan store rejects intervening user edits without overwrite.
func (e *Engine) applyPlanSectionDelegate(h *childHandle, completed protocol.ChildCompleted) {
	if e == nil || h == nil || e.opts.PlanStore == nil {
		return
	}
	planID := strings.TrimSpace(h.planID)
	sectionID := strings.TrimSpace(h.sectionID)
	if planID == "" || sectionID == "" {
		return
	}
	actor := e.rootSessionID()
	if actor == "" {
		return
	}
	outcome := sectionDelegateOutcome(completed)
	_, _ = e.opts.PlanStore.FinishSectionDelegate(planID, actor, sectionID, h.id, outcome)
}

// sectionDelegateOutcome maps child terminal status + handoff into a plan
// DelegateOutcome. Failed/canceled/malformed preserve section content.
func sectionDelegateOutcome(completed protocol.ChildCompleted) DelegateOutcome {
	status := completed.Status
	handoff := completed.Handoff
	title, body, hasSection := sectionFieldsFromHandoff(handoff)

	switch status {
	case protocol.ChildStatusCanceled:
		detail := "child canceled; section content preserved"
		if s := strings.TrimSpace(handoff.Summary); s != "" && s != defaultHandoffSummary(status) {
			detail = s
		}
		return DelegateOutcome{Status: DelegateCanceled, Detail: detail}
	case protocol.ChildStatusFailed:
		detail := "child failed; section content preserved"
		if s := strings.TrimSpace(handoff.Summary); s != "" {
			detail = s
		}
		return DelegateOutcome{Status: DelegateFailed, Detail: detail}
	case protocol.ChildStatusBlocked:
		detail := "child blocked (verification failed); section content preserved"
		if s := strings.TrimSpace(completed.Summary); s != "" {
			detail = s
		} else if s := strings.TrimSpace(handoff.Summary); s != "" {
			detail = s
		}
		return DelegateOutcome{Status: DelegateFailed, Detail: detail}
	default:
		// completed (or unknown treated as completed attempt)
		if handoff.Incomplete && !hasSection {
			return DelegateOutcome{
				Status: DelegateMalformed,
				Detail: "malformed child handoff (no structured section payload); section content preserved",
			}
		}
		if !hasSection || body == nil {
			return DelegateOutcome{
				Status: DelegateMalformed,
				Detail: "child completed without section_body; section content preserved",
			}
		}
		detail := strings.TrimSpace(handoff.Summary)
		if detail == "" {
			detail = "section updated from child handoff"
		}
		return DelegateOutcome{
			Status: DelegateApplied,
			Title:  title,
			Body:   body,
			Detail: detail,
		}
	}
}

// sectionFieldsFromHandoff reads typed section fields first, then falls back to
// re-parsing summary JSON (legacy / nested plan_section objects).
func sectionFieldsFromHandoff(h protocol.CompletionHandoff) (title, body *string, ok bool) {
	if h.SectionBodySet || strings.TrimSpace(h.SectionTitle) != "" {
		if t := strings.TrimSpace(h.SectionTitle); t != "" {
			title = &t
		}
		if h.SectionBodySet {
			b := h.SectionBody
			body = &b
		}
		return title, body, true
	}
	// Fallback: summary may still be the raw handoff JSON.
	for _, raw := range handoffJSONCandidates(h.Summary) {
		if t, b, found := decodeSectionFields(raw); found {
			return t, b, true
		}
	}
	return nil, nil, false
}

func decodeSectionFields(raw string) (title, body *string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' {
		return nil, nil, false
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, nil, false
	}
	// Nested plan_section object.
	if nest, has := m["plan_section"]; has {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(nest, &inner); err == nil {
			return sectionPtrs(inner)
		}
	}
	if nest, has := m["planSection"]; has {
		var inner map[string]json.RawMessage
		if err := json.Unmarshal(nest, &inner); err == nil {
			return sectionPtrs(inner)
		}
	}
	return sectionPtrs(m)
}

func sectionPtrs(m map[string]json.RawMessage) (title, body *string, ok bool) {
	t := firstString(m, "section_title", "sectionTitle")
	bRaw, hasBody := m["section_body"]
	if !hasBody {
		bRaw, hasBody = m["sectionBody"]
	}
	if t == "" && !hasBody {
		return nil, nil, false
	}
	if t != "" {
		title = &t
	}
	if hasBody {
		var b string
		if err := json.Unmarshal(bRaw, &b); err != nil {
			return nil, nil, false
		}
		// Allow empty body as explicit clear.
		body = &b
	}
	return title, body, true
}
