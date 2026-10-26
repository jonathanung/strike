package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/internal/daemon/rootstart"
	"github.com/jonathanung/strike/internal/protocol"
)

// NewRootLauncher returns the shared unattended-session starter for hub.
// A nil hub still returns a launcher; StartRoot fails until a hub exists.
// Scheduler (#1276) and triggers both use this instead of spawning roots.
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
		res, err := h.hub.Create(ctx)
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
