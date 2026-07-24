package permission

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestEvaluateLastMatchWins(t *testing.T) {
	base := Defaults()
	project := Ruleset{
		{Permission: "bash", Pattern: "git *", Action: Allow},
		{Permission: "bash", Pattern: "git push *", Action: Deny},
	}
	cases := []struct {
		perm, pattern string
		want          Action
	}{
		{"read", "any.go", Allow},
		{"bash", "ls", Ask},
		{"bash", "git status", Allow},
		{"bash", "git push origin main", Deny},
		{"edit", "x.go", Ask},
		{"unknown", "*", Ask},
	}
	for _, tc := range cases {
		if got := Evaluate(tc.perm, tc.pattern, base, project); got != tc.want {
			t.Errorf("Evaluate(%q,%q) = %q, want %q", tc.perm, tc.pattern, got, tc.want)
		}
	}
}

func TestEvaluateEmptyPatternMatchesStar(t *testing.T) {
	set := Ruleset{{Permission: "edit", Pattern: "", Action: Deny}}
	if got := Evaluate("edit", "foo.go", set); got != Deny {
		t.Errorf("got %q, want deny", got)
	}
}

func TestAskAllowAndDeny(t *testing.T) {
	var events []protocol.Event
	svc := New(func(ev protocol.Event) { events = append(events, ev) }, Ruleset{
		{Permission: "read", Pattern: "*", Action: Allow},
		{Permission: "bash", Pattern: "*", Action: Deny},
	})
	if err := svc.Ask(context.Background(), tool.AskRequest{Permission: "read", Patterns: []string{"a"}}); err != nil {
		t.Fatalf("allow: %v", err)
	}
	err := svc.Ask(context.Background(), tool.AskRequest{Permission: "bash", Patterns: []string{"rm -rf /"}})
	var rej *RejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("deny err = %v, want RejectedError", err)
	}
	if len(events) != 0 {
		t.Errorf("expected no ask events for allow/deny, got %#v", events)
	}
}

func TestAskOnceReply(t *testing.T) {
	var mu sync.Mutex
	var asked protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = a
			mu.Unlock()
		}
	}, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash",
			Patterns:   []string{"echo hi"},
		})
	}()

	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		id := asked.RequestID
		mu.Unlock()
		if id != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for ask")
		case <-time.After(5 * time.Millisecond):
		}
	}

	mu.Lock()
	id := asked.RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: id, Decision: protocol.DecisionOnce})
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("ask: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reply")
	}
}

func TestAskRejectWithMessage(t *testing.T) {
	var asked protocol.PermissionAsked
	var mu sync.Mutex
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = a
			mu.Unlock()
		}
	}, Ruleset{{Permission: "*", Pattern: "*", Action: Ask}})

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(context.Background(), tool.AskRequest{Permission: "edit", Patterns: []string{"f.go"}})
	}()
	waitAsked(t, &mu, &asked)
	mu.Lock()
	id := asked.RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: id, Decision: protocol.DecisionReject, Message: "nope"})
	err := <-errCh
	var rej *RejectedError
	if !errors.As(err, &rej) || rej.Message != "nope" {
		t.Fatalf("err = %v", err)
	}
	if rej.Error() == "" || !strings.Contains(rej.Error(), "nope") {
		t.Errorf("Error() = %q", rej.Error())
	}
}

func TestAlwaysGrantResolvesSiblings(t *testing.T) {
	var mu sync.Mutex
	var asked []protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = append(asked, a)
			mu.Unlock()
		}
	}, Ruleset{{Permission: "bash", Pattern: "*", Action: Ask}})

	err1 := make(chan error, 1)
	err2 := make(chan error, 1)
	go func() {
		err1 <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash", Patterns: []string{"git status"}, Always: []string{"git *"},
		})
	}()
	// Wait for first ask before starting second so IDs are ordered.
	waitAskedN(t, &mu, &asked, 1)
	go func() {
		err2 <- svc.Ask(context.Background(), tool.AskRequest{
			Permission: "bash", Patterns: []string{"git log"}, Always: []string{"git *"},
		})
	}()
	waitAskedN(t, &mu, &asked, 2)

	mu.Lock()
	first := asked[0].RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: first, Decision: protocol.DecisionAlways})

	for i, ch := range []chan error{err1, err2} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("ask %d: %v", i+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out on ask %d", i+1)
		}
	}

	// Subsequent matching ask should auto-allow without emitting another ask.
	before := len(asked)
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "bash", Patterns: []string{"git diff"},
	}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	after := len(asked)
	mu.Unlock()
	if after != before {
		t.Errorf("always grant still prompted; asks %d -> %d", before, after)
	}
}

func TestRejectCascadesToSiblings(t *testing.T) {
	var mu sync.Mutex
	var asked []protocol.PermissionAsked
	svc := New(func(ev protocol.Event) {
		if a, ok := ev.(protocol.PermissionAsked); ok {
			mu.Lock()
			asked = append(asked, a)
			mu.Unlock()
		}
	}, Ruleset{{Permission: "*", Pattern: "*", Action: Ask}})

	err1 := make(chan error, 1)
	err2 := make(chan error, 1)
	go func() {
		err1 <- svc.Ask(context.Background(), tool.AskRequest{Permission: "edit", Patterns: []string{"a.go"}})
	}()
	waitAskedN(t, &mu, &asked, 1)
	go func() {
		err2 <- svc.Ask(context.Background(), tool.AskRequest{Permission: "edit", Patterns: []string{"b.go"}})
	}()
	waitAskedN(t, &mu, &asked, 2)

	mu.Lock()
	id := asked[0].RequestID
	mu.Unlock()
	svc.Reply(protocol.PermissionReply{RequestID: id, Decision: protocol.DecisionReject})

	for i, ch := range []chan error{err1, err2} {
		select {
		case err := <-ch:
			var rej *RejectedError
			if !errors.As(err, &rej) {
				t.Fatalf("ask %d: %v", i+1, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out on ask %d", i+1)
		}
	}
}

func TestAskContextCancel(t *testing.T) {
	svc := New(func(protocol.Event) {}, Ruleset{{Permission: "*", Pattern: "*", Action: Ask}})
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Ask(ctx, tool.AskRequest{Permission: "bash", Patterns: []string{"sleep 99"}})
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out")
	}
}

func TestWorstCaseAcrossPatterns(t *testing.T) {
	svc := New(func(protocol.Event) {}, Ruleset{
		{Permission: "edit", Pattern: "safe.go", Action: Allow},
		{Permission: "edit", Pattern: "secret.go", Action: Deny},
	})
	err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "edit",
		Patterns:   []string{"safe.go", "secret.go"},
	})
	var rej *RejectedError
	if !errors.As(err, &rej) {
		t.Fatalf("err = %v, want deny", err)
	}
}

func waitAsked(t *testing.T, mu *sync.Mutex, asked *protocol.PermissionAsked) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		ok := asked.RequestID != ""
		mu.Unlock()
		if ok {
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for ask")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func waitAskedN(t *testing.T, mu *sync.Mutex, asked *[]protocol.PermissionAsked, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		got := len(*asked)
		mu.Unlock()
		if got >= n {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d asks (have %d)", n, got)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
