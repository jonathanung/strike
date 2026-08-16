// Package rpc implements strike's stdio JSON-RPC 2.0 transport for the
// Op/Event protocol (newline-delimited JSON, same framing as MCP stdio).
//
// Clients submit ops as JSON-RPC requests (method = op type, or method "op"
// with an OpEnvelope params object). The server emits engine events as
// JSON-RPC notifications with method "event" and params = Event envelope.
//
// This is the transport swap of the WebSocket ops path in internal/frontend/server:
// same OpEnvelope / Event envelope payloads, different framing.
package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

const (
	jsonrpcVersion = "2.0"
	maxLineBytes   = 16 << 20 // 16 MiB per message (matches MCP stdio)

	// Standard JSON-RPC 2.0 error codes.
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	// Application / server range.
	CodeServerError = -32000
)

// SubmitFunc delivers a decoded op to the engine.
type SubmitFunc func(ctx context.Context, op protocol.Op) error

// StatusFunc returns an optional status snapshot for initialize/status.
type StatusFunc func() map[string]any

// Options configures a stdio JSON-RPC session.
type Options struct {
	// SessionID is reported on initialize / rpc.ready (durable session id).
	SessionID string
	// ProtocolVersion defaults to protocol.Version when empty.
	ProtocolVersion string
	// Status, when set, is included in initialize/status results.
	Status StatusFunc
	// SubmitTimeout bounds engine op queue wait (default 5s, matching Live.Submit).
	SubmitTimeout time.Duration
	// Lifecycle, when set, serves session.list/get/fork/load/… host methods.
	// When nil, those methods return structured unsupported errors.
	Lifecycle Lifecycle
}

// Server is a single-session JSON-RPC bridge over a byte stream.
type Server struct {
	r      io.Reader
	w      io.Writer
	submit SubmitFunc
	opts   Options

	writeMu sync.Mutex

	// lastSubmit is wall time of the most recent successful op submit; used to
	// extend the post-shutdown event drain so a turn started just before
	// shutdown still flushes (engine cold-start can exceed a short idle).
	mu         sync.Mutex
	lastSubmit time.Time
}

// New creates a Server. r is typically stdin; w is typically stdout (must not
// be mixed with human-readable logs — put those on stderr).
func New(r io.Reader, w io.Writer, submit SubmitFunc, opts Options) *Server {
	if opts.ProtocolVersion == "" {
		opts.ProtocolVersion = protocol.Version
	}
	if opts.SubmitTimeout <= 0 {
		opts.SubmitTimeout = 5 * time.Second
	}
	return &Server{r: r, w: w, submit: submit, opts: opts}
}

// request is an inbound JSON-RPC 2.0 message. ID absent/null means notification.
type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *ErrorObject    `json:"error,omitempty"`
}

// ErrorObject is a JSON-RPC 2.0 error payload.
type ErrorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// Run serves until stdin EOF, a successful shutdown, or ctx cancel.
// Engine events on events are written as "event" notifications. Closing
// events is fine; Run continues reading ops until shutdown/EOF.
//
// After shutdown/EOF, Run briefly drains remaining events so a client that
// submits a turn and immediately shuts down still receives in-flight output.
func (s *Server) Run(ctx context.Context, events <-chan protocol.Event) error {
	if s == nil || s.submit == nil {
		return errors.New("rpc: server not configured")
	}

	// Announce readiness (mirrors the WebSocket status hello).
	if err := s.notify("rpc.ready", s.helloParams()); err != nil {
		return err
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.readLoop(ctx)
	}()

	for {
		select {
		case <-ctx.Done():
			_ = s.drainEvents(events, 50*time.Millisecond, 200*time.Millisecond)
			// Drain reader error without blocking forever.
			select {
			case err := <-errCh:
				if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, errShutdown) {
					return errors.Join(ctx.Err(), err)
				}
			case <-time.After(100 * time.Millisecond):
			}
			return ctx.Err()
		case err := <-errCh:
			if err == nil || errors.Is(err, io.EOF) || errors.Is(err, errShutdown) {
				// Flush in-flight turn output (idle gap vs hard cap).
				idle, max := s.drainBudget()
				_ = s.drainEvents(events, idle, max)
				return nil
			}
			return err
		case ev, ok := <-events:
			if !ok {
				// Events closed; keep serving ops until shutdown/EOF.
				events = nil
				continue
			}
			if err := s.notifyEvent(ev); err != nil {
				return err
			}
		}
	}
}

// drainEvents writes queued events until the channel is idle for idle, max
// elapses, or the channel closes. idle is reset on each event so a streaming
// turn is fully flushed; max bounds worst-case wait when the engine is stuck.
func (s *Server) drainEvents(events <-chan protocol.Event, idle, max time.Duration) error {
	if events == nil {
		return nil
	}
	if idle <= 0 {
		idle = 50 * time.Millisecond
	}
	if max <= 0 {
		max = idle
	}
	deadline := time.NewTimer(max)
	defer deadline.Stop()
	timer := time.NewTimer(idle)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if err := s.notifyEvent(ev); err != nil {
				return err
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idle)
		case <-timer.C:
			return nil
		case <-deadline.C:
			return nil
		}
	}
}

var errShutdown = errors.New("rpc shutdown")

func (s *Server) helloParams() map[string]any {
	out := map[string]any{
		"protocolVersion": s.opts.ProtocolVersion,
		"sessionId":       s.opts.SessionID,
	}
	if s.opts.Status != nil {
		if st := s.opts.Status(); st != nil {
			out["status"] = st
		}
	}
	// Always advertise lifecycle capability bits so clients can discover
	// unsupported operations without probing each method.
	caps := protocol.LifecycleCapabilities{
		ActiveSessionID: s.opts.SessionID,
		EngineRewind:    true, // live session accepts protocol.Rewind when submit works
	}
	if s.opts.Lifecycle != nil {
		caps = s.opts.Lifecycle.Capabilities()
		if caps.ActiveSessionID == "" {
			caps.ActiveSessionID = s.opts.SessionID
		}
	}
	out["lifecycle"] = caps
	return out
}

func (s *Server) readLoop(ctx context.Context) error {
	sc := bufio.NewScanner(s.r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if err := s.handleLine(ctx, line); err != nil {
			if errors.Is(err, errShutdown) {
				return errShutdown
			}
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

func (s *Server) handleLine(ctx context.Context, line []byte) error {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		// Per JSON-RPC, parse errors use id = null.
		return s.writeResponse(response{
			JSONRPC: jsonrpcVersion,
			ID:      json.RawMessage("null"),
			Error:   &ErrorObject{Code: CodeParseError, Message: "parse error: " + err.Error()},
		})
	}

	if req.JSONRPC != "" && req.JSONRPC != jsonrpcVersion {
		return s.replyError(req.ID, CodeInvalidRequest, "jsonrpc must be \"2.0\"", nil)
	}
	if req.Method == "" {
		return s.replyError(req.ID, CodeInvalidRequest, "missing method", nil)
	}

	isNotification := len(req.ID) == 0 || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		if isNotification {
			return nil
		}
		return s.replyResult(req.ID, s.helloParams())

	case "shutdown", "exit":
		if !isNotification {
			if err := s.replyResult(req.ID, map[string]any{"ok": true}); err != nil {
				return err
			}
		}
		return errShutdown

	case "status", "status.get":
		if isNotification {
			return nil
		}
		params := s.helloParams()
		return s.replyResult(req.ID, params)

	case "op":
		op, err := decodeOpEnvelopeParams(req.Params)
		if err != nil {
			if isNotification {
				return nil
			}
			return s.replyError(req.ID, CodeInvalidParams, err.Error(), nil)
		}
		return s.submitOp(ctx, req.ID, isNotification, op)

	default:
		// Host-level session lifecycle (list/fork/load/…) — not engine ops.
		if isLifecycleMethod(req.Method) {
			if isNotification {
				return nil
			}
			return s.handleLifecycle(ctx, req.ID, req.Method, req.Params)
		}
		// Method name is an op type string (user.input, interrupt, …).
		op, err := decodeOpMethod(req.Method, req.Params)
		if err != nil {
			if errors.Is(err, errUnknownMethod) {
				if isNotification {
					return nil
				}
				return s.replyError(req.ID, CodeMethodNotFound, err.Error(), nil)
			}
			if isNotification {
				return nil
			}
			return s.replyError(req.ID, CodeInvalidParams, err.Error(), nil)
		}
		return s.submitOp(ctx, req.ID, isNotification, op)
	}
}

func (s *Server) submitOp(ctx context.Context, id json.RawMessage, isNotification bool, op protocol.Op) error {
	submitCtx, cancel := context.WithTimeout(ctx, s.opts.SubmitTimeout)
	defer cancel()
	if err := s.submit(submitCtx, op); err != nil {
		if isNotification {
			return nil
		}
		return s.replyError(id, CodeServerError, err.Error(), nil)
	}
	s.mu.Lock()
	s.lastSubmit = time.Now()
	s.mu.Unlock()
	if isNotification {
		return nil
	}
	return s.replyResult(id, map[string]any{"ok": true})
}

// drainBudget picks post-EOF/shutdown flush timings. A recent op submit gets a
// longer window so engine startup + streaming deltas are not dropped.
func (s *Server) drainBudget() (idle, max time.Duration) {
	s.mu.Lock()
	last := s.lastSubmit
	s.mu.Unlock()
	if !last.IsZero() && time.Since(last) < 10*time.Second {
		return 400 * time.Millisecond, 8 * time.Second
	}
	return 50 * time.Millisecond, 200 * time.Millisecond
}

var errUnknownMethod = errors.New("method not found")

func decodeOpEnvelopeParams(params json.RawMessage) (protocol.Op, error) {
	if len(params) == 0 || string(params) == "null" {
		return nil, fmt.Errorf("params required for method op")
	}
	var env protocol.OpEnvelope
	if err := json.Unmarshal(params, &env); err != nil {
		return nil, fmt.Errorf("invalid op envelope: %w", err)
	}
	op, err := env.Decode()
	if err != nil {
		return nil, err
	}
	return op, nil
}

func decodeOpMethod(method string, params json.RawMessage) (protocol.Op, error) {
	// Reject methods that are clearly not op types before Decode.
	env := protocol.OpEnvelope{Type: method, Version: protocol.Version}
	if len(params) > 0 && string(params) != "null" {
		env.Data = params
	}
	op, err := env.Decode()
	if err != nil {
		// Decode errors for unknown type strings look like:
		// protocol: unknown op envelope type "…"
		if isUnknownOpType(err) {
			return nil, fmt.Errorf("%w: %s", errUnknownMethod, method)
		}
		return nil, err
	}
	return op, nil
}

func isUnknownOpType(err error) bool {
	return err != nil && strings.Contains(err.Error(), "unknown op envelope type")
}

func (s *Server) notifyEvent(ev protocol.Event) error {
	env, err := protocol.Wrap(ev)
	if err != nil {
		// Skip unknown event types rather than killing the session.
		return nil
	}
	return s.notify("event", env)
}

func (s *Server) notify(method string, params any) error {
	return s.writeJSON(notification{
		JSONRPC: jsonrpcVersion,
		Method:  method,
		Params:  params,
	})
}

func (s *Server) replyResult(id json.RawMessage, result any) error {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return s.writeResponse(response{
		JSONRPC: jsonrpcVersion,
		ID:      id,
		Result:  result,
	})
}

func (s *Server) replyError(id json.RawMessage, code int, message string, data any) error {
	if len(id) == 0 {
		// Notifications never get error responses.
		return nil
	}
	return s.writeResponse(response{
		JSONRPC: jsonrpcVersion,
		ID:      id,
		Error:   &ErrorObject{Code: code, Message: message, Data: data},
	})
}

func (s *Server) writeResponse(resp response) error {
	return s.writeJSON(resp)
}

func (s *Server) writeJSON(v any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.w.Write(data)
	return err
}
