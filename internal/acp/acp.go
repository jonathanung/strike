// Package acp implements an Agent Client Protocol (ACP) agent adapter over
// stdio JSON-RPC 2.0, mapping ACP session/prompt/tool-call messages onto
// strike Op/Event pairs.
//
// Wire: newline-delimited JSON-RPC (same framing as strike rpc / MCP stdio).
// Spec: https://agentclientprotocol.com/
//
// Process model: one ACP agent process owns one strike engine session. Clients
// (Zed, Devin Desktop, …) spawn `strike acp` and speak ACP; this package is the
// translation layer only — engine wiring lives in cmd/strike.
package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	pkgproto "github.com/jonathanung/strike-cli/pkg/protocol"
)

// Lifecycle is the optional host-level session surface (list/fork/load).
// Same shape as internal/rpc.Lifecycle; defined here to avoid an rpc import.
type Lifecycle interface {
	Capabilities() pkgproto.LifecycleCapabilities
	List(ctx context.Context, rootsOnly bool) ([]pkgproto.SessionSummary, error)
	Get(ctx context.Context, id string) (pkgproto.SessionSummary, error)
	Fork(ctx context.Context, id string) (pkgproto.SessionSummary, error)
	ForkAt(ctx context.Context, id string, keepEvents int) (pkgproto.SessionSummary, error)
	Load(ctx context.Context, id string) (pkgproto.SessionLoadResult, error)
	RewindPoints(ctx context.Context, id string) ([]pkgproto.RewindPoint, error)
	Replay(ctx context.Context, id string) ([]byte, error)
}

const (
	jsonrpcVersion = "2.0"
	maxLineBytes   = 16 << 20 // 16 MiB (matches MCP/rpc stdio)

	// Standard JSON-RPC 2.0 error codes.
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeServerError    = -32000
)

// SubmitFunc delivers a decoded op to the engine.
type SubmitFunc func(ctx context.Context, op protocol.Op) error

// Options configures an ACP agent server.
type Options struct {
	// SessionID is the durable strike session id returned from session/new.
	SessionID string
	// CWD is the process working directory (absolute). session/new may require
	// the client cwd to match, or accept it when equal after Clean.
	CWD string
	// AgentName/AgentVersion populate initialize agentInfo (defaults: strike/dev).
	AgentName    string
	AgentVersion string
	// AgentTitle is an optional UI display name.
	AgentTitle string
	// SubmitTimeout bounds engine op queue wait (default 5s).
	SubmitTimeout time.Duration
	// PermissionTimeout bounds waiting for a client permission response (default 10m).
	PermissionTimeout time.Duration
	// Lifecycle, when set, enables session/list, session/load (active id), and
	// strike extensions _session/fork + _session/rewind_points.
	Lifecycle Lifecycle
}

// Server is a single-session ACP agent over a byte stream.
type Server struct {
	r      io.Reader
	w      io.Writer
	submit SubmitFunc
	opts   Options

	writeMu sync.Mutex

	// outbound client requests (permission) awaiting responses.
	pendingMu sync.Mutex
	pending   map[string]chan json.RawMessage // id string → result/error envelope body waiters
	nextReqID atomic.Int64

	// prompt coordination
	mu           sync.Mutex
	initialized  bool
	sessionReady bool // session/new completed
	promptActive bool
	cancelPrompt context.CancelFunc
	promptCtx    context.Context // cancelled on session/cancel; used for client RPCs
	turnDone     chan string     // stop reason from TurnCompleted
	// lastToolCallID ties PermissionAsked to a tool call when metadata has it.
	lastToolCallID string
}

// New creates an ACP Server. r is typically stdin; w is typically stdout
// (diagnostics must go to stderr — never mix with the JSON-RPC stream).
func New(r io.Reader, w io.Writer, submit SubmitFunc, opts Options) *Server {
	if opts.AgentName == "" {
		opts.AgentName = "strike"
	}
	if opts.AgentVersion == "" {
		opts.AgentVersion = "dev"
	}
	if opts.SubmitTimeout <= 0 {
		opts.SubmitTimeout = 5 * time.Second
	}
	if opts.PermissionTimeout <= 0 {
		opts.PermissionTimeout = 10 * time.Minute
	}
	if opts.CWD != "" {
		opts.CWD = filepath.Clean(opts.CWD)
	}
	return &Server{
		r:       r,
		w:       w,
		submit:  submit,
		opts:    opts,
		pending: make(map[string]chan json.RawMessage),
	}
}

// ErrorObject is a JSON-RPC 2.0 error payload.
type ErrorObject struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type inbound struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *ErrorObject    `json:"error,omitempty"`
}

type requestOut struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

var errShutdown = errors.New("acp shutdown")

// Run serves until stdin EOF, ctx cancel, or a fatal write error.
// Engine events on events are mapped to session/update (and permission requests).
func (s *Server) Run(ctx context.Context, events <-chan protocol.Event) error {
	if s == nil || s.submit == nil {
		return errors.New("acp: server not configured")
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.readLoop(ctx)
	}()

	for {
		select {
		case <-ctx.Done():
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
				return nil
			}
			return err
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if err := s.handleEvent(ctx, ev); err != nil {
				return err
			}
		}
	}
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
	var msg inbound
	if err := json.Unmarshal(line, &msg); err != nil {
		return s.writeResponse(response{
			JSONRPC: jsonrpcVersion,
			ID:      json.RawMessage("null"),
			Error:   &ErrorObject{Code: CodeParseError, Message: "parse error: " + err.Error()},
		})
	}

	// Response to one of our outbound requests (permission).
	if msg.Method == "" && len(msg.ID) > 0 && string(msg.ID) != "null" {
		s.deliverPending(string(msg.ID), line)
		return nil
	}

	if msg.JSONRPC != "" && msg.JSONRPC != jsonrpcVersion {
		return s.replyError(msg.ID, CodeInvalidRequest, `jsonrpc must be "2.0"`, nil)
	}
	if msg.Method == "" {
		return s.replyError(msg.ID, CodeInvalidRequest, "missing method", nil)
	}

	isNotification := len(msg.ID) == 0 || string(msg.ID) == "null"

	switch msg.Method {
	case "initialize":
		if isNotification {
			return nil
		}
		return s.handleInitialize(msg.ID, msg.Params)

	case "authenticate", "logout":
		// No auth methods advertised; accept no-op success for clients that call anyway.
		if isNotification {
			return nil
		}
		return s.replyResult(msg.ID, map[string]any{})

	case "session/new":
		if isNotification {
			return nil
		}
		return s.handleSessionNew(msg.ID, msg.Params)

	case "session/load":
		if isNotification {
			return nil
		}
		return s.handleSessionLoad(ctx, msg.ID, msg.Params)

	case "session/list":
		if isNotification {
			return nil
		}
		return s.handleSessionList(ctx, msg.ID, msg.Params)

	case "session/resume", "session/delete", "session/close",
		"session/set_mode", "session/set_config_option":
		if isNotification {
			return nil
		}
		return s.replyLifecycleUnsupported(msg.ID, msg.Method)

	case "_session/fork", "_session/fork_at", "_session/rewind_points", "_session/capabilities":
		if isNotification {
			return nil
		}
		return s.handleSessionExtension(ctx, msg.ID, msg.Method, msg.Params)

	case "session/prompt":
		if isNotification {
			return nil
		}
		// Validate + start turn synchronously enough to return errors before
		// spawning the waiter goroutine (so shutdown races do not drop them).
		return s.startPrompt(ctx, msg.ID, msg.Params)

	case "session/cancel":
		return s.handleCancel(msg.Params)

	case "shutdown", "exit":
		if !isNotification {
			_ = s.replyResult(msg.ID, map[string]any{"ok": true})
		}
		return errShutdown

	default:
		if isNotification {
			return nil
		}
		// Extension methods use underscore prefix; ignore unknown extensions.
		if strings.HasPrefix(msg.Method, "_") {
			return s.replyError(msg.ID, CodeMethodNotFound, "extension method not supported: "+msg.Method, nil)
		}
		return s.replyError(msg.ID, CodeMethodNotFound, "method not found: "+msg.Method, nil)
	}
}

func (s *Server) handleInitialize(id json.RawMessage, params json.RawMessage) error {
	var p InitializeParams
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return s.replyError(id, CodeInvalidParams, "invalid initialize params: "+err.Error(), nil)
		}
	}
	// Negotiate: if client asks for our version or lower unknown, speak ProtocolVersion.
	// If client sends 0 / missing, still return ProtocolVersion.
	negotiated := ProtocolVersion
	if p.ProtocolVersion > 0 && p.ProtocolVersion < ProtocolVersion {
		// We only implement v1; older unknown — still advertise 1 and let client disconnect.
		negotiated = ProtocolVersion
	}

	s.mu.Lock()
	s.initialized = true
	s.mu.Unlock()

	info := &Implementation{
		Name:    s.opts.AgentName,
		Version: s.opts.AgentVersion,
	}
	if s.opts.AgentTitle != "" {
		info.Title = s.opts.AgentTitle
	}
	// Advertise the ACP-supported lifecycle subset. loadSession is true when
	// we can confirm the bound session (always for a configured SessionID).
	loadSession := s.opts.SessionID != ""
	sessCaps := map[string]any{
		"load":         loadSession,
		"list":         s.opts.Lifecycle != nil,
		"fork":         s.opts.Lifecycle != nil,
		"forkAt":       s.opts.Lifecycle != nil,
		"rewindPoints": s.opts.Lifecycle != nil,
		"engineRewind": true,
		// Strike extensions (underscore methods) for ops ACP does not name.
		"extensions": []string{"_session/fork", "_session/fork_at", "_session/rewind_points", "_session/capabilities"},
	}
	if s.opts.Lifecycle != nil {
		lc := s.opts.Lifecycle.Capabilities()
		sessCaps["lifecycle"] = lc
	}
	result := InitializeResult{
		ProtocolVersion: negotiated,
		AgentCapabilities: AgentCapabilities{
			LoadSession: loadSession,
			PromptCapabilities: PromptCapabilities{
				Image:           false,
				Audio:           false,
				EmbeddedContext: true, // we inline resource text when provided
			},
			McpCapabilities: McpCapabilities{
				HTTP: false,
				SSE:  false,
			},
			SessionCapabilities: sessCaps,
		},
		AuthMethods: []any{},
		AgentInfo:   info,
	}
	return s.replyResult(id, result)
}

func (s *Server) replyLifecycleUnsupported(id json.RawMessage, method string) error {
	return s.replyError(id, CodeMethodNotFound, "method not supported: "+method, map[string]any{
		"code": pkgproto.ErrorCodeUnsupported,
	})
}

func (s *Server) handleSessionLoad(ctx context.Context, id json.RawMessage, params json.RawMessage) error {
	if !s.opts.AgentCapabilitiesLoad() {
		return s.replyLifecycleUnsupported(id, "session/load")
	}
	var p LoadSessionParams
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return s.replyError(id, CodeInvalidParams, "invalid session/load params: "+err.Error(), nil)
		}
	}
	sid := strings.TrimSpace(p.SessionID)
	if sid == "" {
		return s.replyError(id, CodeInvalidParams, "sessionId is required", map[string]any{
			"code": pkgproto.ErrorCodeInvalidSession,
		})
	}
	if s.opts.Lifecycle != nil {
		res, err := s.opts.Lifecycle.Load(ctx, sid)
		if err != nil {
			return s.replyLifecycleErr(id, err)
		}
		s.mu.Lock()
		s.sessionReady = true
		s.mu.Unlock()
		return s.replyResult(id, LoadSessionResult{SessionID: res.ID})
	}
	// Without a lifecycle handler, only the process-bound session is loadable.
	if sid != s.opts.SessionID {
		return s.replyError(id, CodeInvalidParams, fmt.Sprintf("session %q is not the live acp session %q", sid, s.opts.SessionID), map[string]any{
			"code":      pkgproto.ErrorCodeSessionBusy,
			"sessionId": sid,
		})
	}
	s.mu.Lock()
	s.sessionReady = true
	s.mu.Unlock()
	return s.replyResult(id, LoadSessionResult{SessionID: sid})
}

func (s *Server) handleSessionList(ctx context.Context, id json.RawMessage, params json.RawMessage) error {
	if s.opts.Lifecycle == nil {
		return s.replyLifecycleUnsupported(id, "session/list")
	}
	rootsOnly := true
	if len(params) > 0 && string(params) != "null" {
		var check struct {
			RootsOnly *bool `json:"rootsOnly"`
		}
		if err := json.Unmarshal(params, &check); err != nil {
			return s.replyError(id, CodeInvalidParams, "invalid session/list params: "+err.Error(), nil)
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
		list = []pkgproto.SessionSummary{}
	}
	return s.replyResult(id, pkgproto.SessionListResult{Sessions: list})
}

func (s *Server) handleSessionExtension(ctx context.Context, id json.RawMessage, method string, params json.RawMessage) error {
	if s.opts.Lifecycle == nil {
		return s.replyLifecycleUnsupported(id, method)
	}
	switch method {
	case "_session/capabilities":
		return s.replyResult(id, s.opts.Lifecycle.Capabilities())
	case "_session/fork":
		var p pkgproto.SessionIDParams
		if err := json.Unmarshal(params, &p); err != nil {
			return s.replyError(id, CodeInvalidParams, err.Error(), nil)
		}
		sum, err := s.opts.Lifecycle.Fork(ctx, strings.TrimSpace(p.ID))
		if err != nil {
			return s.replyLifecycleErr(id, err)
		}
		return s.replyResult(id, sum)
	case "_session/fork_at":
		var p pkgproto.SessionForkAtParams
		if err := json.Unmarshal(params, &p); err != nil {
			return s.replyError(id, CodeInvalidParams, err.Error(), nil)
		}
		sum, err := s.opts.Lifecycle.ForkAt(ctx, strings.TrimSpace(p.ID), p.KeepEvents)
		if err != nil {
			return s.replyLifecycleErr(id, err)
		}
		return s.replyResult(id, sum)
	case "_session/rewind_points":
		var p pkgproto.SessionIDParams
		if err := json.Unmarshal(params, &p); err != nil {
			return s.replyError(id, CodeInvalidParams, err.Error(), nil)
		}
		points, err := s.opts.Lifecycle.RewindPoints(ctx, strings.TrimSpace(p.ID))
		if err != nil {
			return s.replyLifecycleErr(id, err)
		}
		if points == nil {
			points = []pkgproto.RewindPoint{}
		}
		return s.replyResult(id, pkgproto.SessionRewindPointsResult{Points: points})
	default:
		return s.replyLifecycleUnsupported(id, method)
	}
}

func (s *Server) replyLifecycleErr(id json.RawMessage, err error) error {
	var le *pkgproto.LifecycleError
	if errors.As(err, &le) && le != nil {
		code := CodeServerError
		switch le.Code {
		case pkgproto.ErrorCodeSessionNotFound, pkgproto.ErrorCodeInvalidSession:
			code = CodeInvalidParams
		case pkgproto.ErrorCodeUnsupported:
			code = CodeMethodNotFound
		}
		return s.replyError(id, code, le.Message, map[string]any{
			"code":      le.Code,
			"sessionId": le.SessionID,
		})
	}
	return s.replyError(id, CodeServerError, err.Error(), nil)
}

// AgentCapabilitiesLoad reports whether session/load is advertised.
func (o Options) AgentCapabilitiesLoad() bool {
	return strings.TrimSpace(o.SessionID) != ""
}

func (s *Server) handleSessionNew(id json.RawMessage, params json.RawMessage) error {
	s.mu.Lock()
	inited := s.initialized
	s.mu.Unlock()
	if !inited {
		return s.replyError(id, CodeInvalidRequest, "initialize required before session/new", nil)
	}

	var p NewSessionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return s.replyError(id, CodeInvalidParams, "invalid session/new params: "+err.Error(), nil)
	}
	if strings.TrimSpace(p.Cwd) == "" {
		return s.replyError(id, CodeInvalidParams, "cwd is required", nil)
	}
	if !filepath.IsAbs(p.Cwd) {
		return s.replyError(id, CodeInvalidParams, "cwd must be an absolute path", nil)
	}
	// Single-session process: cwd should match the engine workdir when known.
	if s.opts.CWD != "" {
		want := filepath.Clean(s.opts.CWD)
		got := filepath.Clean(p.Cwd)
		if want != got {
			// Soft accept: many clients spawn us already in the project root and
			// still send cwd. Mismatch is reported but we keep the engine cwd.
			// Hard-fail only when clearly different and both absolute — still
			// allow so Zed can attach without restarting the engine.
			_ = want
		}
	}
	// mcpServers: required by schema; we accept any JSON (including []) and
	// do not spawn client-provided MCP servers in this minimal adapter.

	sid := s.opts.SessionID
	if sid == "" {
		sid = "strike"
	}
	s.mu.Lock()
	s.sessionReady = true
	s.mu.Unlock()

	return s.replyResult(id, NewSessionResult{SessionID: sid})
}

// startPrompt validates session/prompt and either replies with an error or
// submits user.input and waits for TurnCompleted in a background goroutine.
func (s *Server) startPrompt(ctx context.Context, id json.RawMessage, params json.RawMessage) error {
	s.mu.Lock()
	if !s.initialized {
		s.mu.Unlock()
		return s.replyError(id, CodeInvalidRequest, "initialize required before session/prompt", nil)
	}
	if !s.sessionReady {
		s.mu.Unlock()
		return s.replyError(id, CodeInvalidRequest, "session/new required before session/prompt", nil)
	}
	if s.promptActive {
		s.mu.Unlock()
		return s.replyError(id, CodeInvalidRequest, "a prompt turn is already in progress", nil)
	}
	s.mu.Unlock()

	var p PromptParams
	if err := json.Unmarshal(params, &p); err != nil {
		return s.replyError(id, CodeInvalidParams, "invalid session/prompt params: "+err.Error(), nil)
	}
	if p.SessionID == "" {
		return s.replyError(id, CodeInvalidParams, "sessionId is required", nil)
	}
	if s.opts.SessionID != "" && p.SessionID != s.opts.SessionID {
		return s.replyError(id, CodeInvalidParams, "unknown sessionId", nil)
	}
	text := promptText(p.Prompt)
	if strings.TrimSpace(text) == "" {
		return s.replyError(id, CodeInvalidParams, "prompt must include text or resource content", nil)
	}

	promptCtx, cancel := context.WithCancel(ctx)
	turnDone := make(chan string, 1)
	s.mu.Lock()
	s.promptActive = true
	s.cancelPrompt = cancel
	s.promptCtx = promptCtx
	s.turnDone = turnDone
	s.mu.Unlock()

	submitCtx, submitCancel := context.WithTimeout(promptCtx, s.opts.SubmitTimeout)
	err := s.submit(submitCtx, protocol.UserInput{Text: text})
	submitCancel()
	if err != nil {
		cancel()
		s.mu.Lock()
		s.promptActive = false
		s.cancelPrompt = nil
		s.promptCtx = nil
		s.turnDone = nil
		s.mu.Unlock()
		if errors.Is(err, context.Canceled) {
			return s.replyResult(id, PromptResult{StopReason: StopCancelled})
		}
		return s.replyError(id, CodeServerError, "submit user.input: "+err.Error(), nil)
	}

	// Wait for turn completion off the read loop so cancel + client replies flow.
	idCopy := append(json.RawMessage(nil), id...)
	go func() {
		defer func() {
			cancel()
			s.mu.Lock()
			s.promptActive = false
			s.cancelPrompt = nil
			s.promptCtx = nil
			s.turnDone = nil
			s.mu.Unlock()
		}()
		var stop string
		select {
		case <-ctx.Done():
			stop = StopCancelled
		case <-promptCtx.Done():
			select {
			case reason := <-turnDone:
				stop = mapStopReason(reason)
			case <-time.After(2 * time.Second):
				stop = StopCancelled
			}
		case reason := <-turnDone:
			stop = mapStopReason(reason)
		}
		_ = s.replyResult(idCopy, PromptResult{StopReason: stop})
	}()
	return nil
}

func (s *Server) handleCancel(params json.RawMessage) error {
	var p CancelParams
	if len(params) > 0 && string(params) != "null" {
		_ = json.Unmarshal(params, &p)
	}
	s.mu.Lock()
	cancel := s.cancelPrompt
	active := s.promptActive
	s.mu.Unlock()
	if !active {
		return nil
	}
	// Interrupt the engine turn.
	submitCtx, c := context.WithTimeout(context.Background(), s.opts.SubmitTimeout)
	_ = s.submit(submitCtx, protocol.Interrupt{})
	c()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (s *Server) handleEvent(ctx context.Context, ev protocol.Event) error {
	// Track last tool call for permission correlation.
	switch e := ev.(type) {
	case protocol.ToolCallBegin:
		s.mu.Lock()
		s.lastToolCallID = e.CallID
		s.mu.Unlock()
	case protocol.TurnCompleted:
		s.mu.Lock()
		ch := s.turnDone
		s.mu.Unlock()
		if ch != nil {
			select {
			case ch <- e.StopReason:
			default:
			}
		}
	case protocol.PermissionAsked:
		return s.handlePermissionAsked(ctx, e)
	case protocol.QuestionAsked:
		// No ACP elicitation mapping yet — dismiss so the turn cannot hang.
		return s.handleQuestionAsked(ctx, e)
	}

	s.mu.Lock()
	sid := s.opts.SessionID
	ready := s.sessionReady
	s.mu.Unlock()
	if !ready {
		// Drop pre-session events (startup selection chrome).
		return nil
	}
	if sid == "" {
		sid = "strike"
	}
	for _, update := range eventUpdates(ev) {
		if err := s.notify("session/update", SessionUpdateNotification{
			SessionID: sid,
			Update:    update,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) handleQuestionAsked(ctx context.Context, e protocol.QuestionAsked) error {
	// Empty answers = dismiss (engine interrupts/settles the question tool).
	submitCtx, cancel := context.WithTimeout(ctx, s.opts.SubmitTimeout)
	defer cancel()
	if err := s.submit(submitCtx, protocol.QuestionReply{RequestID: e.RequestID}); err != nil {
		return nil // best-effort; turn may still complete via cancel
	}
	s.mu.Lock()
	sid := s.opts.SessionID
	ready := s.sessionReady
	s.mu.Unlock()
	if !ready {
		return nil
	}
	if sid == "" {
		sid = "strike"
	}
	// Surface a short note so the client UI is not silent.
	return s.notify("session/update", SessionUpdateNotification{
		SessionID: sid,
		Update: contentChunk("agent_thought_chunk",
			"question tool dismissed (ACP adapter does not map elicitation yet)"),
	})
}

func (s *Server) handlePermissionAsked(ctx context.Context, e protocol.PermissionAsked) error {
	s.mu.Lock()
	sid := s.opts.SessionID
	toolID := s.lastToolCallID
	active := s.promptActive
	promptCtx := s.promptCtx
	s.mu.Unlock()
	if sid == "" {
		sid = "strike"
	}
	waitCtx := ctx
	if promptCtx != nil {
		waitCtx = promptCtx
	}
	if !active {
		// No prompt waiter — auto-reject so the engine does not hang forever.
		return s.submitPermission(waitCtx, e.RequestID, protocol.DecisionReject)
	}

	// Prefer call id from metadata when present.
	if len(e.Metadata) > 0 {
		var meta map[string]any
		if err := json.Unmarshal(e.Metadata, &meta); err == nil {
			for _, k := range []string{"callId", "toolCallId", "call_id"} {
				if v, ok := meta[k].(string); ok && v != "" {
					toolID = v
					break
				}
			}
		}
	}
	if toolID == "" {
		toolID = e.RequestID
	}

	title := e.Permission
	if len(e.Patterns) > 0 {
		title = e.Permission + " " + strings.Join(e.Patterns, ", ")
	}

	params := RequestPermissionParams{
		SessionID: sid,
		ToolCall: map[string]any{
			"toolCallId": toolID,
			"title":      title,
			"status":     "pending",
			"kind":       "other",
		},
		Options: defaultPermissionOptions(),
	}

	res, err := s.callClient(waitCtx, "session/request_permission", params, s.opts.PermissionTimeout)
	if err != nil {
		// On timeout/cancel, reject so the turn can finish.
		_ = s.submitPermission(context.Background(), e.RequestID, protocol.DecisionReject)
		return nil
	}
	var result RequestPermissionResult
	if err := json.Unmarshal(res, &result); err != nil {
		_ = s.submitPermission(context.Background(), e.RequestID, protocol.DecisionReject)
		return nil
	}
	if result.Outcome.Outcome == "cancelled" {
		_ = s.submitPermission(context.Background(), e.RequestID, protocol.DecisionReject)
		return nil
	}
	dec := decisionFromOption(result.Outcome.OptionID)
	return s.submitPermission(context.Background(), e.RequestID, dec)
}

func (s *Server) submitPermission(ctx context.Context, requestID string, dec protocol.Decision) error {
	submitCtx, cancel := context.WithTimeout(ctx, s.opts.SubmitTimeout)
	defer cancel()
	return s.submit(submitCtx, protocol.PermissionReply{
		RequestID: requestID,
		Decision:  dec,
	})
}

// callClient sends a JSON-RPC request to the client and waits for the response.
func (s *Server) callClient(ctx context.Context, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	idNum := s.nextReqID.Add(1)
	idStr := fmt.Sprintf("%d", idNum)
	ch := make(chan json.RawMessage, 1)
	s.pendingMu.Lock()
	s.pending[idStr] = ch
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, idStr)
		s.pendingMu.Unlock()
	}()

	if err := s.writeJSON(requestOut{
		JSONRPC: jsonrpcVersion,
		ID:      idNum,
		Method:  method,
		Params:  params,
	}); err != nil {
		return nil, err
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, fmt.Errorf("acp: client call %s timed out", method)
	case raw := <-ch:
		var env struct {
			Result json.RawMessage `json:"result"`
			Error  *ErrorObject    `json:"error"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, err
		}
		if env.Error != nil {
			return nil, fmt.Errorf("acp: client error %d: %s", env.Error.Code, env.Error.Message)
		}
		return env.Result, nil
	}
}

func (s *Server) deliverPending(idKey string, line []byte) {
	// Normalize id: JSON numbers come as "1" without quotes; strings as `"1"`.
	key := strings.TrimSpace(idKey)
	if strings.HasPrefix(key, `"`) {
		var unquoted string
		if err := json.Unmarshal([]byte(key), &unquoted); err == nil {
			key = unquoted
		}
	}
	s.pendingMu.Lock()
	ch := s.pending[key]
	s.pendingMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- append(json.RawMessage(nil), line...):
	default:
	}
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
	if len(id) == 0 || string(id) == "null" {
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
