package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/internal/protocol"
	"github.com/jonathanung/strike/internal/rootstart"
)

// hubStarter implements rootstart.Starter on a LiveHub. Schedules and
// triggers both use NewHubStarter so a root is opened the same way:
// background spawn, permission posture, optional workflow, then the prompt.
type hubStarter struct {
	hub *LiveHub
}

// NewHubStarter returns nil when hub is nil (attach-only).
func NewHubStarter(hub *LiveHub) rootstart.Starter {
	if hub == nil {
		return nil
	}
	return hubStarter{hub: hub}
}

func (h hubStarter) StartRoot(ctx context.Context, req rootstart.Request) (rootstart.Result, error) {
	created, err := h.hub.Spawn(ctx)
	if err != nil {
		return rootstart.Result{}, err
	}
	out := rootstart.Result{SessionID: created.ID}
	live := h.hub.LiveFor(created.ID)
	if live == nil {
		return out, fmt.Errorf("spawned root is not live")
	}
	mode := strings.TrimSpace(req.PermissionMode)
	if mode == "" && req.Unattended {
		mode = rootstart.SafestMode
	}
	if mode != "" {
		parsed, ok := protocol.ParsePermissionMode(mode)
		if !ok {
			return out, fmt.Errorf("unknown permission mode %q", mode)
		}
		if err := live.Submit(ctx, protocol.SetPermissionMode{Mode: parsed}); err != nil {
			return out, err
		}
	}
	if name := strings.TrimSpace(req.Workflow); name != "" {
		if err := live.Submit(ctx, protocol.StartWorkflow{Name: name}); err != nil {
			return out, err
		}
	}
	if text := strings.TrimSpace(req.Prompt); text != "" {
		if err := live.Submit(ctx, protocol.UserInput{Text: text}); err != nil {
			return out, err
		}
	}
	if title := strings.TrimSpace(req.Title); title != "" {
		h.hub.SetTitle(created.ID, title)
	}
	return out, nil
}
