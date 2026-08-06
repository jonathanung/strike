package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type agentOwnershipTool struct{}

func NewAgentOwnership() Tool { return agentOwnershipTool{} }

func (agentOwnershipTool) Name() string { return "agent_ownership" }

func (agentOwnershipTool) Description() string {
	return `Query or manage multi-agent file ownership for the session team.

Actions:
- list (default): ownership/overlap map — paths touched or leased, holders, active overlaps
- lease: claim a path prefix for this session (mode exclusive|shared; default exclusive)
- release: drop a lease on path (omit path to release all leases held by this session)

Write tools (edit/write/apply_patch/notebook_edit) auto-register touches. Overlap
policy is session.overlapPolicy (warn default; block refuses conflicting writes).
Use list before parallel fan-out; use lease when a child should own a package tree.
Accepts files_changed-style path lists via engine handoff merge when available.
Not available outside a team session.`
}

func (agentOwnershipTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"description": "list | lease | release (default list)",
				"enum": ["list", "lease", "release"]
			},
			"path": {
				"type": "string",
				"description": "Path or prefix for lease/release (workspace-relative or absolute)"
			},
			"mode": {
				"type": "string",
				"description": "Lease mode: exclusive (default) or shared",
				"enum": ["exclusive", "shared"]
			}
		},
		"additionalProperties": false
	}`)
}

type agentOwnershipArgs struct {
	Action string `json:"action"`
	Path   string `json:"path"`
	Mode   string `json:"mode"`
}

func (agentOwnershipTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a agentOwnershipArgs
	if len(args) > 0 && string(args) != "null" && string(args) != "{}" {
		if err := json.Unmarshal(args, &a); err != nil {
			return Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "agent_ownership",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	action := strings.ToLower(strings.TrimSpace(a.Action))
	if action == "" {
		action = "list"
	}

	switch action {
	case "list":
		if tc.OwnershipQuery == nil {
			// Fall back to local Ownership when engine callback unset (tests).
			if tc.Ownership == nil {
				return Result{}, fmt.Errorf("agent_ownership is not available")
			}
			snap := tc.Ownership.Snapshot()
			return ownershipListResult(snap)
		}
		snap, err := tc.OwnershipQuery(ctx)
		if err != nil {
			return Result{}, err
		}
		return ownershipListResult(snap)

	case "lease":
		path := strings.TrimSpace(a.Path)
		if path == "" {
			return Result{}, fmt.Errorf("path is required for lease")
		}
		exclusive := true
		switch strings.ToLower(strings.TrimSpace(a.Mode)) {
		case "", LeaseExclusive:
			exclusive = true
		case LeaseShared:
			exclusive = false
		default:
			return Result{}, fmt.Errorf("mode must be exclusive or shared")
		}
		if tc.OwnershipLease != nil {
			res, err := tc.OwnershipLease(ctx, path, exclusive)
			if err != nil {
				return Result{}, err
			}
			return ownershipLeaseResult(res, exclusive)
		}
		if tc.Ownership == nil || tc.SessionID == "" {
			return Result{}, fmt.Errorf("agent_ownership lease is not available")
		}
		abs, display := resolveOwnershipPath(tc.WorkDir, path)
		res := tc.Ownership.AcquireLease(tc.SessionID, tc.MemberName, abs, display, exclusive)
		if res.Blocked {
			return Result{}, fmt.Errorf("%s", res.Warning)
		}
		return ownershipLeaseResult(res, exclusive)

	case "release":
		if tc.OwnershipReleaseLease != nil {
			if err := tc.OwnershipReleaseLease(ctx, strings.TrimSpace(a.Path)); err != nil {
				return Result{}, err
			}
		} else if tc.Ownership != nil && tc.SessionID != "" {
			p := strings.TrimSpace(a.Path)
			if p == "" {
				tc.Ownership.ReleaseAllLeases(tc.SessionID)
			} else {
				abs, _ := resolveOwnershipPath(tc.WorkDir, p)
				tc.Ownership.ReleaseLease(tc.SessionID, abs)
			}
		} else {
			return Result{}, fmt.Errorf("agent_ownership release is not available")
		}
		out, _ := json.Marshal(map[string]any{
			"action": "release",
			"path":   strings.TrimSpace(a.Path),
			"status": "ok",
		})
		title := "ownership release"
		if p := strings.TrimSpace(a.Path); p != "" {
			title = "ownership release " + p
		}
		return Result{Title: title, Output: string(out)}, nil

	default:
		return Result{}, fmt.Errorf("unknown action %q (want list|lease|release)", a.Action)
	}
}

func ownershipListResult(snap OwnershipSnapshot) (Result, error) {
	if snap.Claims == nil {
		snap.Claims = []PathClaim{}
	}
	out, err := json.Marshal(snap)
	if err != nil {
		return Result{}, err
	}
	n := len(snap.Claims)
	title := fmt.Sprintf("ownership %d", n)
	if len(snap.Overlaps) > 0 {
		title = fmt.Sprintf("ownership %d (%d overlaps)", n, len(snap.Overlaps))
	}
	return Result{Title: title, Output: string(out)}, nil
}

func ownershipLeaseResult(res TouchResult, exclusive bool) (Result, error) {
	mode := LeaseShared
	if exclusive {
		mode = LeaseExclusive
	}
	payload := map[string]any{
		"action": "lease",
		"path":   res.Display,
		"mode":   mode,
		"status": "ok",
	}
	if res.Overlap {
		payload["overlap"] = true
		payload["warning"] = res.Warning
		payload["holders"] = res.Holders
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return Result{}, err
	}
	title := "ownership lease " + res.Display
	output := string(out)
	if res.Warning != "" {
		output = AppendOverlapWarning(output, res.Warning)
	}
	return Result{Title: title, Output: output}, nil
}
