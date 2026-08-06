package permission

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestScopedGrantSessionTTL(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	svc := New(nil, Defaults())
	svc.SetClock(func() time.Time { return now })

	if err := svc.Grant(ScopedGrant{
		Permission: "bash",
		Pattern:    "go test *",
		Scope:      ScopeSession,
		TTL:        time.Minute,
	}, now); err != nil {
		t.Fatal(err)
	}
	if got := svc.Peek("bash", "go test foo"); got != Allow {
		t.Fatalf("during TTL = %s, want allow (grants=%+v)", got, svc.ActiveGrants(now))
	}
	// Advance past expiry.
	later := now.Add(2 * time.Minute)
	svc.SetClock(func() time.Time { return later })
	if got := svc.Peek("bash", "go test foo"); got != Ask {
		t.Fatalf("after TTL = %s, want ask", got)
	}
}

func TestScopedGrantDoesNotWidenDeny(t *testing.T) {
	base := Defaults()
	deny := Ruleset{{Permission: "bash", Pattern: "*", Action: Deny}}
	svc := New(nil, base, deny)
	err := svc.Grant(ScopedGrant{
		Permission: "bash",
		Pattern:    "*",
		Scope:      ScopeSession,
	}, time.Now())
	if !errors.Is(err, ErrGrantWidens) {
		t.Fatalf("err = %v, want ErrGrantWidens", err)
	}
	if got := svc.Peek("bash", "ls"); got != Deny {
		t.Fatalf("still deny = %s", got)
	}
}

func TestScopedGrantPathPrefix(t *testing.T) {
	svc := New(nil, Defaults())
	if err := svc.Grant(ScopedGrant{
		Permission: "edit",
		Pattern:    "internal/permission",
		Scope:      ScopePathPrefix,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := svc.Peek("edit", "internal/permission/grant.go"); got != Allow {
		t.Fatalf("under prefix = %s, want allow", got)
	}
	if got := svc.Peek("edit", "cmd/strike/main.go"); got != Ask {
		t.Fatalf("outside prefix = %s, want ask", got)
	}
}

func TestScopedGrantTool(t *testing.T) {
	svc := New(nil, Defaults())
	if err := svc.Grant(ScopedGrant{
		Permission: "webfetch",
		Scope:      ScopeTool,
		TTL:        time.Hour,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := svc.Peek("webfetch", "https://example.com"); got != Allow {
		t.Fatalf("tool grant = %s, want allow", got)
	}
	if got := svc.Peek("bash", "curl example.com"); got != Ask {
		t.Fatalf("other tool = %s, want ask", got)
	}
}

func TestScopedGrantCommandClass(t *testing.T) {
	svc := New(nil, Defaults())
	if err := svc.Grant(ScopedGrant{
		Pattern: "git",
		Scope:   ScopeCommandClass,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := svc.Peek("bash", "git status"); got != Allow {
		t.Fatalf("git class = %s, want allow", got)
	}
	if got := svc.Peek("bash", "rm -rf /"); got != Ask {
		t.Fatalf("non-git = %s, want ask", got)
	}
}

func TestScopedGrantAskUsesGrant(t *testing.T) {
	var events []protocol.Event
	svc := New(func(ev protocol.Event) { events = append(events, ev) }, Defaults())
	if err := svc.Grant(ScopedGrant{
		Permission: "bash",
		Pattern:    "echo *",
		Scope:      ScopeSession,
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := svc.Ask(context.Background(), tool.AskRequest{
		Permission: "bash",
		Patterns:   []string{"echo hi"},
	}); err != nil {
		t.Fatalf("Ask = %v", err)
	}
	// Synchronous allow from a scoped grant does not emit (avoid log flood);
	// deny/ask/reply still audit.
	if len(events) != 0 {
		t.Fatalf("events = %#v, want none on grant allow", events)
	}
}

func TestDenyEmitsPermissionDecided(t *testing.T) {
	var events []protocol.Event
	svc := New(func(ev protocol.Event) { events = append(events, ev) }, Defaults(), Ruleset{
		{Permission: "bash", Pattern: "*", Action: Deny},
	})
	err := svc.Ask(context.Background(), tool.AskRequest{Permission: "bash", Patterns: []string{"rm"}})
	var denied *DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	dec, ok := events[0].(protocol.PermissionDecided)
	if !ok || dec.Action != "deny" {
		t.Fatalf("got %#v", events[0])
	}
}
