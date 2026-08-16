package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// Lifecycle is the host-level session lifecycle surface for strike rpc.
// Implementations typically wrap internal/persist/session.Manager + host.Sessions.
// Nil on Options means lifecycle methods return structured unsupported errors.
type Lifecycle interface {
	Capabilities() protocol.LifecycleCapabilities
	List(ctx context.Context, rootsOnly bool) ([]protocol.SessionSummary, error)
	Get(ctx context.Context, id string) (protocol.SessionSummary, error)
	Fork(ctx context.Context, id string) (protocol.SessionSummary, error)
	ForkAt(ctx context.Context, id string, keepEvents int) (protocol.SessionSummary, error)
	Load(ctx context.Context, id string) (protocol.SessionLoadResult, error)
	RewindPoints(ctx context.Context, id string) ([]protocol.RewindPoint, error)
	Replay(ctx context.Context, id string) ([]byte, error)
}

// lifecycleErrorData is the JSON-RPC error.data payload for lifecycle failures.
type lifecycleErrorData struct {
	Code      string `json:"code"`
	SessionID string `json:"sessionId,omitempty"`
}

func (s *Server) handleLifecycle(ctx context.Context, id json.RawMessage, method string, params json.RawMessage) error {
	if s.opts.Lifecycle == nil {
		return s.replyLifecycleUnsupported(id, method)
	}
	switch method {
	case protocol.LifecycleMethodCapabilities:
		return s.replyResult(id, s.opts.Lifecycle.Capabilities())
	case protocol.LifecycleMethodList:
		// Default roots-only (picker/resume shape). Explicit false includes children.
		rootsOnly := true
		if len(params) > 0 && string(params) != "null" {
			var check struct {
				RootsOnly *bool `json:"rootsOnly"`
			}
			if err := json.Unmarshal(params, &check); err != nil {
				return s.replyError(id, CodeInvalidParams, "invalid params: "+err.Error(), nil)
			}
			if check.RootsOnly != nil {
				rootsOnly = *check.RootsOnly
			}
		}
		list, err := s.opts.Lifecycle.List(ctx, rootsOnly)
		if err != nil {
			return s.replyLifecycleErr(id, err)
		}
		if list == nil {
			list = []protocol.SessionSummary{}
		}
		return s.replyResult(id, protocol.SessionListResult{Sessions: list})
	case protocol.LifecycleMethodGet:
		sid, err := decodeSessionID(params)
		if err != nil {
			return s.replyError(id, CodeInvalidParams, err.Error(), nil)
		}
		sum, err := s.opts.Lifecycle.Get(ctx, sid)
		if err != nil {
			return s.replyLifecycleErr(id, err)
		}
		return s.replyResult(id, sum)
	case protocol.LifecycleMethodFork:
		sid, err := decodeSessionID(params)
		if err != nil {
			return s.replyError(id, CodeInvalidParams, err.Error(), nil)
		}
		sum, err := s.opts.Lifecycle.Fork(ctx, sid)
		if err != nil {
			return s.replyLifecycleErr(id, err)
		}
		return s.replyResult(id, sum)
	case protocol.LifecycleMethodForkAt:
		var p protocol.SessionForkAtParams
		if err := decodeLifecycleParams(params, &p); err != nil {
			return s.replyError(id, CodeInvalidParams, err.Error(), nil)
		}
		p.ID = strings.TrimSpace(p.ID)
		if p.ID == "" {
			return s.replyError(id, CodeInvalidParams, "id is required", nil)
		}
		sum, err := s.opts.Lifecycle.ForkAt(ctx, p.ID, p.KeepEvents)
		if err != nil {
			return s.replyLifecycleErr(id, err)
		}
		return s.replyResult(id, sum)
	case protocol.LifecycleMethodLoad:
		sid, err := decodeSessionID(params)
		if err != nil {
			return s.replyError(id, CodeInvalidParams, err.Error(), nil)
		}
		res, err := s.opts.Lifecycle.Load(ctx, sid)
		if err != nil {
			return s.replyLifecycleErr(id, err)
		}
		return s.replyResult(id, res)
	case protocol.LifecycleMethodRewindPoints:
		sid, err := decodeSessionID(params)
		if err != nil {
			return s.replyError(id, CodeInvalidParams, err.Error(), nil)
		}
		points, err := s.opts.Lifecycle.RewindPoints(ctx, sid)
		if err != nil {
			return s.replyLifecycleErr(id, err)
		}
		if points == nil {
			points = []protocol.RewindPoint{}
		}
		return s.replyResult(id, protocol.SessionRewindPointsResult{Points: points})
	case protocol.LifecycleMethodReplay:
		sid, err := decodeSessionID(params)
		if err != nil {
			return s.replyError(id, CodeInvalidParams, err.Error(), nil)
		}
		raw, err := s.opts.Lifecycle.Replay(ctx, sid)
		if err != nil {
			return s.replyLifecycleErr(id, err)
		}
		return s.replyResult(id, protocol.SessionReplayResult{ID: sid, JSONL: raw})
	default:
		return s.replyLifecycleUnsupported(id, method)
	}
}

func (s *Server) replyLifecycleUnsupported(id json.RawMessage, method string) error {
	return s.replyError(id, CodeMethodNotFound, "method not supported: "+method, lifecycleErrorData{
		Code: protocol.ErrorCodeUnsupported,
	})
}

func (s *Server) replyLifecycleErr(id json.RawMessage, err error) error {
	var le *protocol.LifecycleError
	if errors.As(err, &le) && le != nil {
		code := CodeServerError
		switch le.Code {
		case protocol.ErrorCodeSessionNotFound, protocol.ErrorCodeInvalidSession:
			code = CodeInvalidParams
		case protocol.ErrorCodeUnsupported:
			code = CodeMethodNotFound
		case protocol.ErrorCodeSessionBusy:
			code = CodeServerError
		case protocol.ErrorCodeSessionCorrupt:
			code = CodeServerError
		}
		return s.replyError(id, code, le.Message, lifecycleErrorData{
			Code:      le.Code,
			SessionID: le.SessionID,
		})
	}
	return s.replyError(id, CodeServerError, err.Error(), nil)
}

func decodeLifecycleParams(params json.RawMessage, dst any) error {
	if len(params) == 0 || string(params) == "null" {
		return nil
	}
	if err := json.Unmarshal(params, dst); err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	return nil
}

func decodeSessionID(params json.RawMessage) (string, error) {
	var p protocol.SessionIDParams
	if err := decodeLifecycleParams(params, &p); err != nil {
		return "", err
	}
	id := strings.TrimSpace(p.ID)
	if id == "" {
		return "", fmt.Errorf("id is required")
	}
	return id, nil
}

func isLifecycleMethod(method string) bool {
	switch method {
	case protocol.LifecycleMethodCapabilities,
		protocol.LifecycleMethodList,
		protocol.LifecycleMethodGet,
		protocol.LifecycleMethodFork,
		protocol.LifecycleMethodForkAt,
		protocol.LifecycleMethodLoad,
		protocol.LifecycleMethodRewindPoints,
		protocol.LifecycleMethodReplay:
		return true
	default:
		return false
	}
}
