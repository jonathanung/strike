package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike/harness/tool"
)

func TestPackConnectorRefcountsAcrossSessions(t *testing.T) {
	var attaches, detaches []string
	borrowed := map[string]bool{"global": true}
	c := newPackConnector(
		func(s string) (ServerConfig, bool) { return ServerConfig{Name: s}, s != "unknown" },
		func(_ context.Context, cfg ServerConfig) (bool, error) {
			attaches = append(attaches, cfg.Name)
			if cfg.Name == "broken" {
				return false, errors.New("mcp broken: connection refused")
			}
			return !borrowed[cfg.Name], nil
		},
		func(name string) { detaches = append(detaches, name) },
	)

	for _, s := range []string{"s1", "s2"} {
		if err := c.Connect(s, "docs"); err != nil {
			t.Fatal(err)
		}
	}
	if len(attaches) != 1 {
		t.Fatalf("attaches = %v, want one shared attach", attaches)
	}
	c.Disconnect("s1", "docs")
	if len(detaches) != 0 {
		t.Fatalf("detached while s2 still holds docs: %v", detaches)
	}
	c.Disconnect("s2", "docs")
	if len(detaches) != 1 || detaches[0] != "docs" {
		t.Fatalf("detaches = %v", detaches)
	}

	// A server configured outside Powers is borrowed and never detached.
	if err := c.Connect("s1", "global"); err != nil {
		t.Fatal(err)
	}
	c.Disconnect("s1", "global")
	if len(detaches) != 1 {
		t.Fatalf("borrowed server was detached: %v", detaches)
	}

	if err := c.Connect("s1", "broken"); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("broken connect err = %v", err)
	}
	if err := c.Connect("s1", "unknown"); err == nil {
		t.Fatal("unresolvable server should fail")
	}
	// Releasing something never connected is a no-op.
	c.Disconnect("s1", "broken")
	if len(detaches) != 1 {
		t.Fatalf("detaches = %v", detaches)
	}
}

func TestAttachDetachServer(t *testing.T) {
	cmd, args, env := helperCommand(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	m := NewManager()
	defer m.Close()
	if _, err := m.AttachServer(ctx, ServerConfig{Name: "fake", Command: cmd, Args: args, Env: env}); err == nil {
		t.Fatal("attach before a registry is set should fail")
	}
	reg := tool.NewRegistry()
	m.SetRegistry(reg)

	attached, err := m.AttachServer(ctx, ServerConfig{Name: "fake", Command: cmd, Args: args, Env: env})
	if err != nil || !attached {
		t.Fatalf("AttachServer = %v %v", attached, err)
	}
	if _, ok := reg.Get("mcp_fake_echo"); !ok {
		t.Fatal("bridged tool not registered")
	}
	// Already up: not ours to own.
	if again, err := m.AttachServer(ctx, ServerConfig{Name: "fake", Command: cmd, Args: args, Env: env}); err != nil || again {
		t.Fatalf("second attach = %v %v", again, err)
	}

	m.DetachServer("fake")
	if _, ok := reg.Get("mcp_fake_echo"); ok {
		t.Fatal("bridged tool should be unregistered after detach")
	}
	if st := m.Statuses(); len(st) != 0 {
		t.Fatalf("detached server should leave no status row: %+v", st)
	}

	// A server that cannot start is reported and forgotten.
	_, err = m.AttachServer(ctx, ServerConfig{Name: "missing", Command: "/nonexistent/strike-mcp-test-binary"})
	if err == nil {
		t.Fatal("attach of a missing command should fail")
	}
	if st := m.Statuses(); len(st) != 0 {
		t.Fatalf("failed attach left a status row: %+v", st)
	}
}
