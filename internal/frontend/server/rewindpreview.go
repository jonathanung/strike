package server

// Rewind preview (#1336): GET /v1/sessions/{id}/rewind_preview?keep=K
// describes what "rewind to before this turn" would undo, from the session
// record alone, so the cockpit can show it before anything changes.
//
// `keep` is the turn's fork keep-count (protocol.RewindPoint.KeepEvents,
// the same number the cockpit stores per message as keep_events). The
// engine's Rewind op only drops the LAST completed turn, so rewinding to
// before turn N means N..last are undone one by one; this preview sums
// what those turns recorded on turn.completed: files the harness changed,
// checkpoint-skipped paths, and uncovered mutation reasons (e.g. "bash").
// Nothing here mutates state; the cockpit applies the rewind through the
// existing "rewind" protocol op.

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jonathanung/strike/pkg/protocol"
)

// rewindCheckpointTurns mirrors harness/tool.DefaultCheckpointMaxTurns:
// the engine keeps file checkpoints for at most this many recent turns.
const rewindCheckpointTurns = 50

type rewindPreviewFile struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type rewindPreview struct {
	// Turn is the 1-based completed turn the user picked; Turns is how many
	// turns (Turn..TotalTurns) a rewind to before it undoes.
	Turn       int `json:"turn"`
	Turns      int `json:"turns"`
	TotalTurns int `json:"totalTurns"`
	// Prompt is the picked turn's original user text, returned to the
	// composer after a conversation restore (never auto-sent).
	Prompt            string              `json:"prompt"`
	Files             []rewindPreviewFile `json:"files"`
	CheckpointSkipped int                 `json:"checkpointSkipped"`
	Uncovered         []string            `json:"uncovered"`
	// BeyondCheckpoints is true when more turns are undone than the engine
	// keeps file checkpoints for: older turns' files cannot be restored.
	BeyondCheckpoints bool `json:"beyondCheckpoints"`
	// Disabled carries a stated reason when rewind cannot run here.
	Disabled string `json:"disabled,omitempty"`
}

type rewindTurn struct {
	keep      int
	prompt    string
	files     []protocol.TurnFileChange
	skipped   int
	uncovered []string
}

func rootLineage(c protocol.Correlation) bool {
	return c.ParentSessionID == "" && c.Depth == 0
}

// rewindTurnStack rebuilds the completed-turn stack the engine would rewind,
// with the same boundaries as protocol.RewindPoints (SessionRewound pops).
func rewindTurnStack(events []protocol.Event) []rewindTurn {
	var (
		stack   []rewindTurn
		pending *rewindTurn
	)
	for i, ev := range events {
		switch e := ev.(type) {
		case protocol.UserMessage:
			if !rootLineage(e.Correlation) {
				continue
			}
			pending = &rewindTurn{prompt: e.Text}
		case protocol.TurnCompleted:
			if !rootLineage(e.Correlation) || pending == nil {
				continue
			}
			pending.keep = i + 1
			pending.files = append([]protocol.TurnFileChange(nil), e.Files...)
			pending.skipped = e.CheckpointSkipped
			pending.uncovered = append([]string(nil), e.Uncovered...)
			stack = append(stack, *pending)
			pending = nil
		case protocol.SessionRewound:
			if !rootLineage(e.Correlation) {
				continue
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			pending = nil
		}
	}
	return stack
}

// buildRewindPreview finds the turn ending at keep and sums what rewinding
// it and every later turn would undo. ok is false when keep is not a
// current turn boundary (already rewound, or not a completed turn).
func buildRewindPreview(events []protocol.Event, keep int) (rewindPreview, bool) {
	stack := rewindTurnStack(events)
	idx := -1
	for i, t := range stack {
		if t.keep == keep {
			idx = i
			break
		}
	}
	if idx < 0 {
		return rewindPreview{TotalTurns: len(stack)}, false
	}
	p := rewindPreview{
		Turn:       idx + 1,
		Turns:      len(stack) - idx,
		TotalTurns: len(stack),
		Prompt:     stack[idx].prompt,
		Files:      []rewindPreviewFile{},
		Uncovered:  []string{},
	}
	p.BeyondCheckpoints = p.Turns > rewindCheckpointTurns
	// Newest turn first: a path's kind is the one recorded latest.
	seenFile := map[string]bool{}
	seenReason := map[string]bool{}
	for i := len(stack) - 1; i >= idx; i-- {
		t := stack[i]
		for _, f := range t.files {
			if f.Path == "" || seenFile[f.Path] {
				continue
			}
			seenFile[f.Path] = true
			p.Files = append(p.Files, rewindPreviewFile{Path: f.Path, Kind: f.Kind})
		}
		p.CheckpointSkipped += t.skipped
		for _, r := range t.uncovered {
			r = strings.TrimSpace(r)
			if r != "" && !seenReason[r] {
				seenReason[r] = true
				p.Uncovered = append(p.Uncovered, r)
			}
		}
	}
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	sort.Strings(p.Uncovered)
	return p, true
}

func (s *Server) handleSessionRewindPreview(w http.ResponseWriter, r *http.Request) {
	if s.opts.Services == nil || s.opts.Services.Sessions == nil {
		capabilityUnavailable(w, "sessions")
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	keep, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("keep")))
	if id == "" || err != nil || keep <= 0 {
		writeJSON(w, http.StatusBadRequest, opErrorResponse{Error: "keep must be a positive turn keep-count", Code: protocol.ErrorCodeInvalidArgs})
		return
	}
	sess, found, err := s.opts.Services.Sessions.Get(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, lifecycleHTTPError(err))
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, opErrorResponse{Error: "session not found", Code: protocol.ErrorCodeSessionNotFound})
		return
	}
	raw, err := s.opts.Services.Sessions.ReplayJSONL(id)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, lifecycleHTTPError(err))
		return
	}
	evs, err := decodeSessionJSONL(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, lifecycleErrorBody{Error: err.Error(), Code: protocol.ErrorCodeSessionCorrupt})
		return
	}
	p, ok := buildRewindPreview(evs, keep)
	if !ok {
		writeJSON(w, http.StatusConflict, opErrorResponse{
			Error: "that turn is no longer in the session history (already rewound, or not completed)",
			Code:  protocol.ErrorCodePreconditionFailed,
		})
		return
	}
	switch {
	case s.opts.ReadOnly:
		p.Disabled = "This cockpit is read-only: rewind changes the session and is not allowed."
	case sess.ParentID != "":
		p.Disabled = "This is a delegated child session; rewind it from its parent session."
	}
	writeJSON(w, http.StatusOK, p)
}
