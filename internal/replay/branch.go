package replay

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/session"
)

// BranchSelector identifies where to fork a session event log.
// Construct via BranchAtEvent, BranchKeep, BranchAtTurn, or BranchRef.
type BranchSelector struct {
	kind  branchKind
	index int    // event index or keep count depending on kind
	turn  string // turn id when kindTurn
	ref   string // raw ref when kindRef (parsed later)
}

type branchKind int

const (
	branchUnset branchKind = iota
	branchEvent            // inclusive 0-based event index → keep = index+1
	branchKeep             // exclusive keep count (session.ForkAt)
	branchTurn             // through turn id
	branchRef              // parse EventRef string
)

// BranchAtEvent keeps events [0..index] inclusive (index is 0-based).
func BranchAtEvent(index int) BranchSelector {
	return BranchSelector{kind: branchEvent, index: index}
}

// BranchKeep keeps the first n events (exclusive end; same as session.ForkAt).
func BranchKeep(n int) BranchSelector {
	return BranchSelector{kind: branchKeep, index: n}
}

// BranchAtTurn keeps through the TurnCompleted (or last event) for turnID.
func BranchAtTurn(turnID string) BranchSelector {
	return BranchSelector{kind: branchTurn, turn: strings.TrimSpace(turnID)}
}

// BranchRef parses "event:N", "keep:N", "turn:<id>", or a bare integer (event index).
func BranchRef(ref string) BranchSelector {
	return BranchSelector{kind: branchRef, ref: strings.TrimSpace(ref)}
}

// BranchResult is the outcome of BranchFromEvent.
type BranchResult struct {
	// Info is the new forked root session.
	Info session.Info
	// SourceID is the session that was forked.
	SourceID string
	// KeepEvents is how many leading events were copied.
	KeepEvents int
	// AtEventIndex is the last included event index (KeepEvents-1), or -1 when empty.
	AtEventIndex int
	// SideEffectsReplayed is always false: only JSONL prefix is copied;
	// tools are not re-executed.
	SideEffectsReplayed bool
	// Recording is a derived recording of the forked prefix.
	Recording Recording
}

// BranchFromEvent forks sourceID at a timeline event without replaying live
// side effects. It wraps session.Manager.ForkAt: the parent log is unchanged,
// the fork is a new root with meta.ForkedFrom = sourceID, and only the event
// prefix is copied (no tool re-execution, no disk mutations).
func BranchFromEvent(m *session.Manager, sourceID string, sel BranchSelector) (BranchResult, error) {
	if m == nil {
		return BranchResult{}, fmt.Errorf("replay: BranchFromEvent: nil session manager")
	}
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return BranchResult{}, fmt.Errorf("replay: BranchFromEvent: empty source session id")
	}

	// Flush open source so keep resolution matches ForkAt's view of the log.
	_ = m.Sync(sourceID)

	events, err := m.Replay(sourceID)
	if err != nil {
		return BranchResult{}, fmt.Errorf("replay: BranchFromEvent: replay source: %w", err)
	}

	keep, err := resolveBranchKeep(events, sel)
	if err != nil {
		return BranchResult{}, err
	}

	info, err := m.ForkAt(sourceID, keep)
	if err != nil {
		return BranchResult{}, fmt.Errorf("replay: BranchFromEvent: fork: %w", err)
	}

	// Prefer the durable fork log (post-ForkAt) so the recording matches what
	// was actually copied (keep was resolved against the synced source above).
	prefix, err := m.Replay(info.ID)
	if err != nil {
		return BranchResult{}, fmt.Errorf("replay: BranchFromEvent: replay fork: %w", err)
	}
	at := keep - 1
	if keep == 0 {
		at = -1
	}
	rec := BuildRecording(prefix, RecordingOptions{SessionID: info.ID})
	rec.SideEffectsReplayed = false
	rec.Note = "Branch-from-event fork prefix. Side effects not replayed; parent session unchanged. " + rec.Note

	return BranchResult{
		Info:                info,
		SourceID:            sourceID,
		KeepEvents:          keep,
		AtEventIndex:        at,
		SideEffectsReplayed: false,
		Recording:           rec,
	}, nil
}

// ResolveBranchKeep exports keep-count resolution for tests and callers that
// only need the index without forking.
func ResolveBranchKeep(events []protocol.Event, sel BranchSelector) (int, error) {
	return resolveBranchKeep(events, sel)
}

func resolveBranchKeep(events []protocol.Event, sel BranchSelector) (int, error) {
	switch sel.kind {
	case branchUnset:
		return 0, fmt.Errorf("replay: BranchFromEvent: empty selector (use BranchAtEvent, BranchKeep, BranchAtTurn, or BranchRef)")
	case branchRef:
		return parseEventRef(events, sel.ref)
	case branchTurn:
		if sel.turn == "" {
			return 0, fmt.Errorf("replay: BranchFromEvent: empty turn id")
		}
		keep, ok := keepThroughTurn(events, sel.turn)
		if !ok {
			return 0, fmt.Errorf("replay: BranchFromEvent: turn %q not found", sel.turn)
		}
		return keep, nil
	case branchKeep:
		if sel.index < 0 {
			return 0, fmt.Errorf("replay: BranchFromEvent: keep %d invalid", sel.index)
		}
		if sel.index > len(events) {
			return 0, fmt.Errorf("replay: BranchFromEvent: keep %d exceeds log length %d", sel.index, len(events))
		}
		return sel.index, nil
	case branchEvent:
		if sel.index < 0 || sel.index >= len(events) {
			return 0, fmt.Errorf("replay: BranchFromEvent: event index %d out of range [0,%d)", sel.index, len(events))
		}
		return sel.index + 1, nil
	default:
		return 0, fmt.Errorf("replay: BranchFromEvent: unknown selector kind %d", sel.kind)
	}
}

func parseEventRef(events []protocol.Event, ref string) (int, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, fmt.Errorf("replay: BranchFromEvent: empty event ref")
	}
	lower := strings.ToLower(ref)
	switch {
	case strings.HasPrefix(lower, "event:"):
		n, err := strconv.Atoi(strings.TrimSpace(ref[len("event:"):]))
		if err != nil {
			return 0, fmt.Errorf("replay: BranchFromEvent: bad event ref %q: %w", ref, err)
		}
		return resolveBranchKeep(events, BranchAtEvent(n))
	case strings.HasPrefix(lower, "keep:"):
		n, err := strconv.Atoi(strings.TrimSpace(ref[len("keep:"):]))
		if err != nil {
			return 0, fmt.Errorf("replay: BranchFromEvent: bad keep ref %q: %w", ref, err)
		}
		return resolveBranchKeep(events, BranchKeep(n))
	case strings.HasPrefix(lower, "turn:"):
		return resolveBranchKeep(events, BranchAtTurn(strings.TrimSpace(ref[len("turn:"):])))
	default:
		n, err := strconv.Atoi(ref)
		if err != nil {
			return 0, fmt.Errorf("replay: BranchFromEvent: unrecognized event ref %q", ref)
		}
		return resolveBranchKeep(events, BranchAtEvent(n))
	}
}

// keepThroughTurn returns the exclusive end index after the turn completes
// (TurnCompleted for that turn id). If the turn started but never completed,
// keeps through the last event that carries that turn id.
func keepThroughTurn(events []protocol.Event, turnID string) (int, bool) {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return 0, false
	}
	last := -1
	completed := -1
	for i, ev := range events {
		tid := eventTurnID(ev)
		if tid != turnID {
			continue
		}
		last = i
		if _, ok := ev.(protocol.TurnCompleted); ok {
			completed = i
		}
	}
	if completed >= 0 {
		return completed + 1, true
	}
	if last >= 0 {
		return last + 1, true
	}
	return 0, false
}

func eventTurnID(ev protocol.Event) string {
	switch e := ev.(type) {
	case protocol.UserMessage:
		return e.TurnID
	case protocol.TurnStarted:
		return e.TurnID
	case protocol.TurnCompleted:
		return e.TurnID
	case protocol.TextDelta:
		return e.TurnID
	case protocol.ReasoningDelta:
		return e.TurnID
	case protocol.ToolCallBegin:
		return e.TurnID
	case protocol.ToolCallEnd:
		return e.TurnID
	case protocol.ToolCallOutput:
		return e.TurnID
	case protocol.UsageReported:
		return e.TurnID
	case protocol.EngineError:
		return e.TurnID
	default:
		return ""
	}
}
