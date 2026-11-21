package permission

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// recorder collects emitted events under a lock so -race stays clean.
type recorder struct {
	mu     sync.Mutex
	events []protocol.Event
}

func (r *recorder) emit(ev protocol.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) snapshot() []protocol.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]protocol.Event(nil), r.events...)
}

// waitAsk blocks until the n-th PermissionAsked (1-based) and returns it.
func (r *recorder) waitAsk(t *testing.T, n int) protocol.PermissionAsked {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		seen := 0
		for _, ev := range r.snapshot() {
			if a, ok := ev.(protocol.PermissionAsked); ok {
				seen++
				if seen == n {
					return a
				}
			}
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for ask %d (have %d)", n, seen)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (r *recorder) count(match func(protocol.Event) bool) int {
	n := 0
	for _, ev := range r.snapshot() {
		if match(ev) {
			n++
		}
	}
	return n
}

func askRules() Ruleset {
	return Ruleset{{Permission: "*", Pattern: "*", Action: Ask}}
}

func TestAskCanceledEmitsOneExpiredOutcome(t *testing.T) {
	rec := &recorder{}
	svc := New(rec.emit, askRules())
	corr := protocol.Correlation{SessionID: "s1", TurnID: "t1"}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.AskWithCorrelation(ctx, tool.AskRequest{Permission: "bash", Patterns: []string{"rm -rf build"}}, corr)
	}()
	ask := rec.waitAsk(t, 1)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask did not return after cancel")
	}

	var resolved []protocol.PermissionResolved
	var terminal []protocol.PermissionDecided
	for _, ev := range rec.snapshot() {
		switch e := ev.(type) {
		case protocol.PermissionResolved:
			resolved = append(resolved, e)
		case protocol.PermissionDecided:
			if e.Decision != "" {
				terminal = append(terminal, e)
			}
		}
	}
	if len(resolved) != 1 || resolved[0].Decision != protocol.DecisionExpired || resolved[0].RequestID != ask.RequestID {
		t.Fatalf("resolved = %#v, want one expired for %s", resolved, ask.RequestID)
	}
	if resolved[0].Correlation != corr {
		t.Fatalf("resolved correlation = %#v, want %#v", resolved[0].Correlation, corr)
	}
	if len(terminal) != 1 {
		t.Fatalf("terminal decided = %#v, want exactly one", terminal)
	}
	got := terminal[0]
	if got.Decision != protocol.DecisionExpired || got.Action != "ask" || got.RequestID != ask.RequestID {
		t.Fatalf("terminal decided = %#v", got)
	}
	// Same permission + patterns the ask itself carried, nothing more.
	if got.Permission != "bash" || len(got.Patterns) != 1 || got.Patterns[0] != "rm -rf build" {
		t.Fatalf("terminal summary = %q %v", got.Permission, got.Patterns)
	}

	// A later reply for the dead ask is a no-op: no second outcome.
	svc.Reply(protocol.PermissionReply{RequestID: ask.RequestID, Decision: protocol.DecisionOnce})
	if n := rec.count(func(ev protocol.Event) bool { _, ok := ev.(protocol.PermissionResolved); return ok }); n != 1 {
		t.Fatalf("resolved count after late reply = %d, want 1", n)
	}
}

func TestAskRepliedThenCanceledDoesNotExpire(t *testing.T) {
	rec := &recorder{}
	svc := New(rec.emit, askRules())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(ctx, tool.AskRequest{Permission: "bash", Patterns: []string{"ls"}})
	}()
	ask := rec.waitAsk(t, 1)
	svc.Reply(protocol.PermissionReply{RequestID: ask.RequestID, Decision: protocol.DecisionOnce})
	if err := <-errCh; err != nil {
		t.Fatalf("err = %v", err)
	}
	cancel()
	if n := rec.count(func(ev protocol.Event) bool {
		r, ok := ev.(protocol.PermissionResolved)
		return ok && r.Decision == protocol.DecisionExpired
	}); n != 0 {
		t.Fatalf("expired outcomes = %d, want 0 after a reply", n)
	}
}

func TestRejectLatchesSameSignatureUntilCleared(t *testing.T) {
	rec := &recorder{}
	svc := New(rec.emit, askRules())
	req := tool.AskRequest{Permission: "bash", Patterns: []string{"git push --force"}}

	errCh := make(chan error, 1)
	go func() { errCh <- svc.Ask(context.Background(), req) }()
	ask := rec.waitAsk(t, 1)
	svc.Reply(protocol.PermissionReply{RequestID: ask.RequestID, Decision: protocol.DecisionReject})
	var rej *RejectedError
	if err := <-errCh; !errors.As(err, &rej) {
		t.Fatalf("first ask err = %v, want RejectedError", err)
	}

	// Same action again: refused at once, no new card.
	err := svc.Ask(context.Background(), req)
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("latched ask err = %v, want DeniedError", err)
	}
	if !strings.Contains(err.Error(), "next message") {
		t.Fatalf("latched reason = %q, want it to name the user's next message", err.Error())
	}
	asks := rec.count(func(ev protocol.Event) bool { _, ok := ev.(protocol.PermissionAsked); return ok })
	if asks != 1 {
		t.Fatalf("PermissionAsked count = %d, want 1 (no second card)", asks)
	}
	latched := rec.count(func(ev protocol.Event) bool {
		d, ok := ev.(protocol.PermissionDecided)
		return ok && d.Layer == LayerUserDenied && d.Action == "deny" && d.RequestID == ""
	})
	if latched != 1 {
		t.Fatalf("user-denied audit count = %d, want 1", latched)
	}

	// A different action still asks.
	otherCh := make(chan error, 1)
	go func() {
		otherCh <- svc.Ask(context.Background(), tool.AskRequest{Permission: "bash", Patterns: []string{"git status"}})
	}()
	other := rec.waitAsk(t, 2)
	svc.Reply(protocol.PermissionReply{RequestID: other.RequestID, Decision: protocol.DecisionOnce})
	if err := <-otherCh; err != nil {
		t.Fatalf("other action err = %v", err)
	}

	// The user speaks again: the same action may be asked once more.
	svc.ClearDenyLatch()
	againCh := make(chan error, 1)
	go func() { againCh <- svc.Ask(context.Background(), req) }()
	again := rec.waitAsk(t, 3)
	if again.Permission != "bash" || len(again.Patterns) != 1 || again.Patterns[0] != "git push --force" {
		t.Fatalf("re-ask = %#v", again)
	}
	svc.Reply(protocol.PermissionReply{RequestID: again.RequestID, Decision: protocol.DecisionOnce})
	if err := <-againCh; err != nil {
		t.Fatalf("re-ask after clear err = %v", err)
	}
}

func TestLatchDoesNotBlockAllowOrWidenAccess(t *testing.T) {
	rec := &recorder{}
	svc := New(rec.emit, Ruleset{
		{Permission: "bash", Pattern: "*", Action: Ask},
		{Permission: "bash", Pattern: "rm *", Action: Deny},
	})
	req := tool.AskRequest{Permission: "bash", Patterns: []string{"make"}}
	errCh := make(chan error, 1)
	go func() { errCh <- svc.Ask(context.Background(), req) }()
	ask := rec.waitAsk(t, 1)
	svc.Reply(protocol.PermissionReply{RequestID: ask.RequestID, Decision: protocol.DecisionReject})
	<-errCh

	// Hard rule denies keep their own reason; the latch never turns a deny
	// into anything else.
	err := svc.Ask(context.Background(), tool.AskRequest{Permission: "bash", Patterns: []string{"rm x"}})
	var denied *DeniedError
	if !errors.As(err, &denied) || strings.Contains(err.Error(), "next message") {
		t.Fatalf("rule deny err = %v", err)
	}

	// The latch only refuses asks: when a rule change allows the action,
	// the allow is not overridden.
	svc.SetAgentRules(Ruleset{{Permission: "bash", Pattern: "make", Action: Allow}})
	if err := svc.Ask(context.Background(), req); err != nil {
		t.Fatalf("allowed action err = %v, want nil", err)
	}
}

func TestCascadedSiblingsAreNotLatched(t *testing.T) {
	rec := &recorder{}
	svc := New(rec.emit, askRules())
	first := tool.AskRequest{Permission: "edit", Patterns: []string{"a.go"}}
	second := tool.AskRequest{Permission: "edit", Patterns: []string{"b.go"}}
	err1 := make(chan error, 1)
	err2 := make(chan error, 1)
	go func() { err1 <- svc.Ask(context.Background(), first) }()
	a := rec.waitAsk(t, 1)
	go func() { err2 <- svc.Ask(context.Background(), second) }()
	rec.waitAsk(t, 2)
	svc.Reply(protocol.PermissionReply{RequestID: a.RequestID, Decision: protocol.DecisionReject})
	<-err1
	<-err2

	// The sibling was rejected by cascade, not by the user: it may ask again.
	againCh := make(chan error, 1)
	go func() { againCh <- svc.Ask(context.Background(), second) }()
	again := rec.waitAsk(t, 3)
	svc.Reply(protocol.PermissionReply{RequestID: again.RequestID, Decision: protocol.DecisionOnce})
	if err := <-againCh; err != nil {
		t.Fatalf("sibling re-ask err = %v", err)
	}
}

func TestClearDenyLatchNilSafe(t *testing.T) {
	var svc *Service
	svc.ClearDenyLatch()
}
