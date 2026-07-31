package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Board task lifecycle statuses.
const (
	BoardStatusPending   = "pending"
	BoardStatusClaimed   = "claimed"
	BoardStatusCompleted = "completed"
	BoardStatusCancelled = "cancelled"
)

// MaxBoardTaskContentRunes caps board item content size.
const MaxBoardTaskContentRunes = 4 * 1024

// MaxBoardTasks bounds items on one team board (lead-scoped).
const MaxBoardTasks = 256

// BoardTask is one shared work item on the team task board.
//
// Version is a compare-and-swap token: mutating ops that pass ExpectedVersion > 0
// fail when the stored version differs. Claim is owner-exclusive regardless.
type BoardTask struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	Status    string    `json:"status"`
	Owner     string    `json:"owner,omitempty"` // session id; empty when unclaimed
	Version   int       `json:"version"`
	CreatedBy string    `json:"created_by,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// BoardConflictError is returned when CAS version or claim ownership fails.
type BoardConflictError struct {
	Reason string
	Task   BoardTask
}

func (e *BoardConflictError) Error() string {
	if e == nil {
		return "board conflict"
	}
	if e.Reason == "" {
		return "board conflict"
	}
	return e.Reason
}

// Board returns a stable snapshot of team board tasks (id ascending).
func (t *Team) Board() []BoardTask {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.boardSnapshotLocked()
}

func (t *Team) boardSnapshotLocked() []BoardTask {
	if len(t.board) == 0 {
		return []BoardTask{}
	}
	out := make([]BoardTask, 0, len(t.board))
	for _, item := range t.board {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// CreateBoardTask appends a pending unclaimed item. createdBy is the actor session id.
func (t *Team) CreateBoardTask(content, createdBy string) (BoardTask, error) {
	if t == nil {
		return BoardTask{}, fmt.Errorf("no team")
	}
	content = strings.TrimSpace(content)
	createdBy = strings.TrimSpace(createdBy)
	if content == "" {
		return BoardTask{}, fmt.Errorf("content is required")
	}
	if n := utf8.RuneCountInString(content); n > MaxBoardTaskContentRunes {
		return BoardTask{}, fmt.Errorf("content exceeds %d runes (%d)", MaxBoardTaskContentRunes, n)
	}
	if createdBy == "" {
		return BoardTask{}, fmt.Errorf("created_by is required")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.members) == 0 {
		return BoardTask{}, fmt.Errorf("team is dissolved")
	}
	if _, ok := t.members[createdBy]; !ok {
		return BoardTask{}, fmt.Errorf("actor is not on this team")
	}
	if len(t.board) >= MaxBoardTasks {
		return BoardTask{}, fmt.Errorf("board is full (%d tasks)", MaxBoardTasks)
	}
	t.boardSeq++
	id := fmt.Sprintf("t%d", t.boardSeq)
	now := time.Now().UTC()
	item := BoardTask{
		ID:        id,
		Content:   content,
		Status:    BoardStatusPending,
		Version:   1,
		CreatedBy: createdBy,
		UpdatedAt: now,
	}
	if t.board == nil {
		t.board = make(map[string]BoardTask, 8)
	}
	t.board[id] = item
	return item, nil
}

// ClaimBoardTask assigns ownership to actor when the task is unclaimed or already
// owned by actor. Concurrent claims by different actors: exactly one wins.
// expectedVersion > 0 enables CAS against Version.
func (t *Team) ClaimBoardTask(id, actor string, expectedVersion int) (BoardTask, error) {
	if t == nil {
		return BoardTask{}, fmt.Errorf("no team")
	}
	id = strings.TrimSpace(id)
	actor = strings.TrimSpace(actor)
	if id == "" {
		return BoardTask{}, fmt.Errorf("id is required")
	}
	if actor == "" {
		return BoardTask{}, fmt.Errorf("actor is required")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.members) == 0 {
		return BoardTask{}, fmt.Errorf("team is dissolved")
	}
	if _, ok := t.members[actor]; !ok {
		return BoardTask{}, fmt.Errorf("actor is not on this team")
	}
	item, ok := t.board[id]
	if !ok {
		return BoardTask{}, fmt.Errorf("task %q not found", id)
	}
	if expectedVersion > 0 && item.Version != expectedVersion {
		return BoardTask{}, &BoardConflictError{
			Reason: fmt.Sprintf("version conflict: have %d, expected %d", item.Version, expectedVersion),
			Task:   item,
		}
	}
	switch item.Status {
	case BoardStatusCompleted, BoardStatusCancelled:
		return BoardTask{}, &BoardConflictError{
			Reason: fmt.Sprintf("task %q is %s and cannot be claimed", id, item.Status),
			Task:   item,
		}
	}
	owner := strings.TrimSpace(item.Owner)
	if owner != "" && owner != actor {
		return BoardTask{}, &BoardConflictError{
			Reason: fmt.Sprintf("task %q is claimed by %s", id, owner),
			Task:   item,
		}
	}
	item.Owner = actor
	item.Status = BoardStatusClaimed
	item.Version++
	item.UpdatedAt = time.Now().UTC()
	t.board[id] = item
	return item, nil
}

// UpdateBoardTask patches content and/or status with optional CAS.
// Status may be pending|claimed|completed|cancelled. Setting claimed without
// an owner is rejected; completing does not require ownership (use Complete
// for owner-gated finish). When status becomes pending, owner is cleared.
func (t *Team) UpdateBoardTask(id, actor string, content *string, status *string, expectedVersion int) (BoardTask, error) {
	if t == nil {
		return BoardTask{}, fmt.Errorf("no team")
	}
	id = strings.TrimSpace(id)
	actor = strings.TrimSpace(actor)
	if id == "" {
		return BoardTask{}, fmt.Errorf("id is required")
	}
	if actor == "" {
		return BoardTask{}, fmt.Errorf("actor is required")
	}
	if content == nil && status == nil {
		return BoardTask{}, fmt.Errorf("provide content and/or status to update")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.members) == 0 {
		return BoardTask{}, fmt.Errorf("team is dissolved")
	}
	if _, ok := t.members[actor]; !ok {
		return BoardTask{}, fmt.Errorf("actor is not on this team")
	}
	item, ok := t.board[id]
	if !ok {
		return BoardTask{}, fmt.Errorf("task %q not found", id)
	}
	if expectedVersion > 0 && item.Version != expectedVersion {
		return BoardTask{}, &BoardConflictError{
			Reason: fmt.Sprintf("version conflict: have %d, expected %d", item.Version, expectedVersion),
			Task:   item,
		}
	}
	if content != nil {
		c := strings.TrimSpace(*content)
		if c == "" {
			return BoardTask{}, fmt.Errorf("content must not be empty")
		}
		if n := utf8.RuneCountInString(c); n > MaxBoardTaskContentRunes {
			return BoardTask{}, fmt.Errorf("content exceeds %d runes (%d)", MaxBoardTaskContentRunes, n)
		}
		item.Content = c
	}
	if status != nil {
		s := strings.TrimSpace(*status)
		switch s {
		case BoardStatusPending:
			item.Status = BoardStatusPending
			item.Owner = ""
		case BoardStatusClaimed:
			if strings.TrimSpace(item.Owner) == "" {
				item.Owner = actor
			}
			item.Status = BoardStatusClaimed
		case BoardStatusCompleted, BoardStatusCancelled:
			item.Status = s
		default:
			return BoardTask{}, fmt.Errorf("status must be pending, claimed, completed, or cancelled")
		}
	}
	item.Version++
	item.UpdatedAt = time.Now().UTC()
	t.board[id] = item
	return item, nil
}

// CompleteBoardTask marks a task completed. If the task is claimed, only the
// owner may complete it (prevents double-finish races). Unclaimed tasks may be
// completed by any teammate. expectedVersion > 0 enables CAS.
func (t *Team) CompleteBoardTask(id, actor string, expectedVersion int) (BoardTask, error) {
	if t == nil {
		return BoardTask{}, fmt.Errorf("no team")
	}
	id = strings.TrimSpace(id)
	actor = strings.TrimSpace(actor)
	if id == "" {
		return BoardTask{}, fmt.Errorf("id is required")
	}
	if actor == "" {
		return BoardTask{}, fmt.Errorf("actor is required")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.members) == 0 {
		return BoardTask{}, fmt.Errorf("team is dissolved")
	}
	if _, ok := t.members[actor]; !ok {
		return BoardTask{}, fmt.Errorf("actor is not on this team")
	}
	item, ok := t.board[id]
	if !ok {
		return BoardTask{}, fmt.Errorf("task %q not found", id)
	}
	if expectedVersion > 0 && item.Version != expectedVersion {
		return BoardTask{}, &BoardConflictError{
			Reason: fmt.Sprintf("version conflict: have %d, expected %d", item.Version, expectedVersion),
			Task:   item,
		}
	}
	if item.Status == BoardStatusCompleted {
		return item, nil // idempotent
	}
	if item.Status == BoardStatusCancelled {
		return BoardTask{}, &BoardConflictError{
			Reason: fmt.Sprintf("task %q is cancelled", id),
			Task:   item,
		}
	}
	owner := strings.TrimSpace(item.Owner)
	if owner != "" && owner != actor {
		return BoardTask{}, &BoardConflictError{
			Reason: fmt.Sprintf("task %q is owned by %s", id, owner),
			Task:   item,
		}
	}
	if owner == "" {
		item.Owner = actor
	}
	item.Status = BoardStatusCompleted
	item.Version++
	item.UpdatedAt = time.Now().UTC()
	t.board[id] = item
	return item, nil
}

// clearBoardLocked drops all board items (team GC). Caller holds t.mu.
func (t *Team) clearBoardLocked() {
	t.board = make(map[string]BoardTask)
	t.boardSeq = 0
}
