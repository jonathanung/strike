// Package elicit suspends an in-flight MCP tool call until the user answers
// the server's elicitation/create request (#1353). It mirrors the question
// service: one ElicitationRequested out, one ElicitationReply in, keyed by a
// session-scoped request id, and every request is closed by exactly one
// ElicitationResolved — including when the turn is cancelled, so a paused
// call never hangs.
//
// An elicitation answer is data for a call that is already permitted and
// running. It never grants or changes a permission.
package elicit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/jonathanung/strike/pkg/protocol"
)

// Request is one elicitation/create from an MCP server.
type Request struct {
	Server          string
	Message         string
	Mode            string // protocol.ElicitationModeForm | protocol.ElicitationModeURL
	RequestedSchema json.RawMessage
	URL             string
}

// Response is what the server receives: action plus form content on accept.
type Response struct {
	Action  string
	Content json.RawMessage
}

type pending struct {
	correlation protocol.Correlation
	ch          chan protocol.ElicitationReply

	// announced is set after ElicitationRequested emission returns. Reply
	// queues the resolution until then so Resolved never precedes Requested.
	announced      bool
	hasDeferred    bool
	deferredAction string
}

// Service is safe for concurrent use and supports multiple pending requests.
type Service struct {
	emit func(protocol.Event)

	mu      sync.Mutex
	pending map[string]*pending
	nextID  int
}

// New creates a Service. emit publishes events toward the frontend.
func New(emit func(protocol.Event)) *Service {
	return &Service{emit: emit, pending: map[string]*pending{}}
}

// NormalizeAction maps a client action onto the three MCP answers. Anything
// unknown is a cancel: the safest answer a server can receive.
func NormalizeAction(action string) string {
	switch strings.TrimSpace(action) {
	case protocol.ElicitationAccept:
		return protocol.ElicitationAccept
	case protocol.ElicitationDecline:
		return protocol.ElicitationDecline
	default:
		return protocol.ElicitationCancel
	}
}

// contentObject returns raw when it is a JSON object, else nil.
func contentObject(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil
	}
	return json.RawMessage(trimmed)
}

// Ask emits ElicitationRequested and blocks until a matching Reply or ctx
// cancel. Cancel settles as action cancel and still emits Resolved.
func (s *Service) Ask(ctx context.Context, corr protocol.Correlation, req Request) Response {
	mode := req.Mode
	if mode != protocol.ElicitationModeURL {
		mode = protocol.ElicitationModeForm
	}
	s.mu.Lock()
	s.nextID++
	id := fmt.Sprintf("elicit_%d", s.nextID)
	if sid := strings.TrimSpace(corr.SessionID); sid != "" {
		id = sid + ":" + id
	}
	p := &pending{correlation: corr, ch: make(chan protocol.ElicitationReply, 1)}
	s.pending[id] = p
	s.mu.Unlock()

	ev := protocol.ElicitationRequested{
		Correlation: corr,
		RequestID:   id,
		Server:      req.Server,
		Message:     req.Message,
		Mode:        mode,
	}
	if mode == protocol.ElicitationModeURL {
		ev.URL = req.URL
	} else {
		ev.RequestedSchema = req.RequestedSchema
	}
	s.emit(ev)

	s.mu.Lock()
	p.announced = true
	deferred, action := p.hasDeferred, p.deferredAction
	p.hasDeferred = false
	s.mu.Unlock()
	if deferred {
		s.emit(protocol.ElicitationResolved{Correlation: corr, RequestID: id, Action: action})
	}

	select {
	case <-ctx.Done():
		s.mu.Lock()
		_, open := s.pending[id]
		delete(s.pending, id)
		s.mu.Unlock()
		if open {
			s.emit(protocol.ElicitationResolved{Correlation: corr, RequestID: id, Action: protocol.ElicitationCancel})
		}
		return Response{Action: protocol.ElicitationCancel}
	case reply := <-p.ch:
		action := NormalizeAction(reply.Action)
		out := Response{Action: action}
		if action == protocol.ElicitationAccept && mode == protocol.ElicitationModeForm {
			out.Content = contentObject(reply.Content)
			if out.Content == nil {
				out.Content = json.RawMessage(`{}`)
			}
		}
		return out
	}
}

// Reply resolves a pending request. Unknown request ids are ignored.
func (s *Service) Reply(r protocol.ElicitationReply) {
	action := NormalizeAction(r.Action)
	s.mu.Lock()
	p, ok := s.pending[r.RequestID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.pending, r.RequestID)
	announced := p.announced
	if !announced {
		p.hasDeferred = true
		p.deferredAction = action
	}
	s.mu.Unlock()
	if announced {
		s.emit(protocol.ElicitationResolved{Correlation: p.correlation, RequestID: r.RequestID, Action: action})
	}
	p.ch <- r
}
