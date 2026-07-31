package engine

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBoardCreateListClaimComplete(t *testing.T) {
	tm := NewTeam("L", "build")
	if !tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll A")
	}
	if !tm.Enroll(TeamMember{SessionID: "B", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll B")
	}

	item, err := tm.CreateBoardTask("fix auth", "L")
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "t1" || item.Status != BoardStatusPending || item.Owner != "" || item.Version != 1 {
		t.Fatalf("create = %+v", item)
	}
	list := tm.Board()
	if len(list) != 1 || list[0].ID != "t1" {
		t.Fatalf("board = %+v", list)
	}

	// Both children see the same board.
	if got := len(tm.Board()); got != 1 {
		t.Fatalf("shared board len = %d", got)
	}

	claimed, err := tm.ClaimBoardTask("t1", "A", 1)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.Owner != "A" || claimed.Status != BoardStatusClaimed || claimed.Version != 2 {
		t.Fatalf("claim = %+v", claimed)
	}

	// B cannot steal claim.
	_, err = tm.ClaimBoardTask("t1", "B", 0)
	var conf *BoardConflictError
	if !errors.As(err, &conf) {
		t.Fatalf("second claim err = %v, want conflict", err)
	}
	if conf.Task.Owner != "A" {
		t.Fatalf("conflict task owner = %q", conf.Task.Owner)
	}

	// B cannot complete A's claim.
	_, err = tm.CompleteBoardTask("t1", "B", 0)
	if !errors.As(err, &conf) {
		t.Fatalf("foreign complete err = %v", err)
	}

	done, err := tm.CompleteBoardTask("t1", "A", 2)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != BoardStatusCompleted || done.Version != 3 {
		t.Fatalf("complete = %+v", done)
	}
	// Idempotent complete.
	again, err := tm.CompleteBoardTask("t1", "A", 0)
	if err != nil || again.Status != BoardStatusCompleted {
		t.Fatalf("idempotent complete = %+v err=%v", again, err)
	}
}

func TestBoardCASVersionConflict(t *testing.T) {
	tm := NewTeam("L", "build")
	item, err := tm.CreateBoardTask("work", "L")
	if err != nil {
		t.Fatal(err)
	}
	content := "updated"
	_, err = tm.UpdateBoardTask(item.ID, "L", &content, nil, item.Version)
	if err != nil {
		t.Fatal(err)
	}
	// Stale version.
	content2 := "stale"
	_, err = tm.UpdateBoardTask(item.ID, "L", &content2, nil, item.Version)
	var conf *BoardConflictError
	if !errors.As(err, &conf) {
		t.Fatalf("stale update err = %v", err)
	}
	if conf.Task.Content != "updated" {
		t.Fatalf("conflict content = %q", conf.Task.Content)
	}
}

func TestBoardConcurrentClaimOneWinner(t *testing.T) {
	tm := NewTeam("L", "build")
	const n = 32
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("c%d", i)
		if !tm.Enroll(TeamMember{SessionID: id, ParentSessionID: "L", Depth: 1}) {
			t.Fatalf("enroll %s", id)
		}
	}
	item, err := tm.CreateBoardTask("hot item", "L")
	if err != nil {
		t.Fatal(err)
	}

	var wins atomic.Int32
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			actor := fmt.Sprintf("c%d", i)
			_, err := tm.ClaimBoardTask(item.ID, actor, 0)
			if err == nil {
				wins.Add(1)
				return
			}
			var conf *BoardConflictError
			if !errors.As(err, &conf) {
				t.Errorf("claim %s: unexpected err %v", actor, err)
			}
		}(i)
	}
	wg.Wait()
	if got := wins.Load(); got != 1 {
		t.Fatalf("concurrent claim winners = %d, want 1", got)
	}
	board := tm.Board()
	if len(board) != 1 || board[0].Status != BoardStatusClaimed || board[0].Owner == "" {
		t.Fatalf("after race board = %+v", board)
	}
}

func TestBoardClearedOnDissolve(t *testing.T) {
	tm := NewTeam("L", "build")
	if _, err := tm.CreateBoardTask("a", "L"); err != nil {
		t.Fatal(err)
	}
	if _, err := tm.CreateBoardTask("b", "L"); err != nil {
		t.Fatal(err)
	}
	if len(tm.Board()) != 2 {
		t.Fatalf("pre-dissolve board = %d", len(tm.Board()))
	}
	tm.Dissolve()
	if got := tm.Board(); len(got) != 0 {
		t.Fatalf("board after dissolve = %+v", got)
	}
	if _, err := tm.CreateBoardTask("c", "L"); err == nil {
		t.Fatal("create after dissolve should fail")
	}
}

func TestBoardRejectsOffTeamActor(t *testing.T) {
	tm := NewTeam("L", "build")
	item, err := tm.CreateBoardTask("x", "L")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tm.ClaimBoardTask(item.ID, "stranger", 0); err == nil {
		t.Fatal("off-team claim should fail")
	}
}

func TestBoardUpdateAndCancel(t *testing.T) {
	tm := NewTeam("L", "build")
	if !tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll A")
	}
	item, err := tm.CreateBoardTask("old", "L")
	if err != nil {
		t.Fatal(err)
	}
	status := BoardStatusCancelled
	got, err := tm.UpdateBoardTask(item.ID, "L", nil, &status, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != BoardStatusCancelled || got.Version != 2 {
		t.Fatalf("cancel update = %+v", got)
	}
	if _, err := tm.ClaimBoardTask(item.ID, "L", 0); err == nil {
		t.Fatal("claim cancelled should fail")
	}
}

func TestBoardUpdateCannotBypassForeignOwner(t *testing.T) {
	tm := NewTeam("L", "build")
	if !tm.Enroll(TeamMember{SessionID: "A", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll A")
	}
	if !tm.Enroll(TeamMember{SessionID: "B", ParentSessionID: "L", Depth: 1}) {
		t.Fatal("enroll B")
	}
	item, err := tm.CreateBoardTask("owned", "L")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tm.ClaimBoardTask(item.ID, "A", 0); err != nil {
		t.Fatal(err)
	}
	// B must not complete/cancel/reclaim via update.
	for _, st := range []string{BoardStatusCompleted, BoardStatusCancelled, BoardStatusClaimed} {
		s := st
		_, err := tm.UpdateBoardTask(item.ID, "B", nil, &s, 0)
		var conf *BoardConflictError
		if !errors.As(err, &conf) {
			t.Fatalf("update status=%s err = %v, want conflict", st, err)
		}
	}
}
