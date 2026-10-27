package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/internal/daemon/rootstart"
	"github.com/jonathanung/strike/internal/protocol"
)

// NewRootLauncher returns the triggers' unattended-session starter for hub.
// A nil hub still returns a launcher; StartRoot fails until a hub exists.
// (The landed scheduler uses internal/rootstart's NewHubStarter; this is
// the authored #1282 launcher, kept as authored except for the
// SpawnUnattended composition below.)
func NewRootLauncher(hub *LiveHub) rootstart.Launcher {
	return hubLauncher{hub: hub}
}

type hubLauncher struct {
	hub *LiveHub
}

func (h hubLauncher) StartRoot(ctx context.Context, req rootstart.Request) (rootstart.Result, error) {
	if h.hub == nil {
		return rootstart.Result{}, errors.New("root creation unavailable")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return rootstart.Result{}, errors.New("prompt is empty")
	}
	mode, err := rootstart.ResolveMode(req.PermissionMode)
	if err != nil {
		return rootstart.Result{}, err
	}
	continueID := strings.TrimSpace(req.ContinueID)
	var id string
	created := false
	if continueID != "" {
		res, err := h.hub.Resume(ctx, continueID)
		if err != nil {
			return rootstart.Result{}, err
		}
		id = res.ID
	} else {
		// Re-land composition (vs landed #1418): SpawnUnattended, not
		// hub.Create. Create uses the attended spawn, which keeps the
		// process --auto / --dangerously-skip-permissions allow-all layer
		// (SetPermissionMode cannot remove it, so a "plan" trigger could
		// still run bash) and switches the cockpit's active root.
		// SpawnUnattended strips the bypass and stays in the background;
		// it falls back to the plain spawn when no unattended spawn is
		// wired. Authored semantics (new root, title, mode, prompt) are
		// unchanged.
		res, err := h.hub.SpawnUnattended(ctx)
		if err != nil {
			return rootstart.Result{}, err
		}
		id = res.ID
		created = true
	}
	if title := strings.TrimSpace(req.Title); title != "" {
		h.hub.SetTitle(id, title)
	}
	live := h.hub.LiveFor(id)
	if live == nil {
		return rootstart.Result{}, fmt.Errorf("root %s is not live", id)
	}
	if err := live.Submit(ctx, protocol.SetPermissionMode{Mode: mode}); err != nil {
		return rootstart.Result{}, err
	}
	if err := live.Submit(ctx, protocol.UserInput{Text: prompt}); err != nil {
		return rootstart.Result{}, err
	}
	return rootstart.Result{
		SessionID:      id,
		Created:        created,
		PermissionMode: string(mode),
	}, nil
}
