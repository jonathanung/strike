// Package question implements the ask service that suspends a tool call until
// the user answers one or more prompts. Used by internal/engine; internal/tui
// never imports it — a pending ask reaches the frontend only as a
// protocol.QuestionAsked event.
package question

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// RejectedError is returned from Ask when the user dismisses the prompts
// (empty answers) rather than answering them.
type RejectedError struct {
	Message string
}

func (e *RejectedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "The user dismissed this question."
}

type pending struct {
	prompts     []protocol.QuestionPrompt
	correlation protocol.Correlation
	ch          chan protocol.QuestionReply

	// announced is set after QuestionAsked emission returns. Reply queues
	// QuestionResolved until then so a blocked or reentrant emitter cannot
	// publish Resolved first.
	announced   bool
	hasDeferred bool
}

// resolvedEmission is a QuestionResolved to publish outside the mutex.
type resolvedEmission struct {
	id          string
	correlation protocol.Correlation
}

// Service suspends on a channel when user answers are needed. It is safe for
// concurrent use and supports multiple pending asks.
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

// Ask emits QuestionAsked and blocks until a matching Reply or ctx cancel.
// On cancel it emits QuestionResolved so the frontend can close the prompt.
func (s *Service) Ask(ctx context.Context, corr protocol.Correlation, prompts []protocol.QuestionPrompt) ([]string, error) {
	s.mu.Lock()
	s.nextID++
	// Session-scope IDs so concurrent parent/child engines never collide when
	// replies fan out across services.
	id := fmt.Sprintf("q_%d", s.nextID)
	if sid := strings.TrimSpace(corr.SessionID); sid != "" {
		id = sid + ":" + id
	}
	p := &pending{
		prompts:     prompts,
		correlation: corr,
		ch:          make(chan protocol.QuestionReply, 1),
	}
	s.pending[id] = p
	s.mu.Unlock()

	s.emit(protocol.QuestionAsked{
		Correlation: corr,
		RequestID:   id,
		Questions:   prompts,
	})

	// Mark announced only after QuestionAsked returns. Any Reply that ran
	// while unannounced left a deferred resolution for us to emit now.
	s.mu.Lock()
	p.announced = true
	var deferred *resolvedEmission
	if p.hasDeferred {
		deferred = &resolvedEmission{id: id, correlation: p.correlation}
		p.hasDeferred = false
	}
	s.mu.Unlock()
	if deferred != nil {
		s.emit(protocol.QuestionResolved{
			Correlation: deferred.correlation,
			RequestID:   deferred.id,
		})
	}

	select {
	case <-ctx.Done():
		s.mu.Lock()
		_, stillPending := s.pending[id]
		if stillPending {
			delete(s.pending, id)
		}
		s.mu.Unlock()
		// Always emit Resolved after Asked so the TUI can close the prompt.
		// Skip only if Reply already took the entry and emitted (or deferred).
		if stillPending {
			s.emit(protocol.QuestionResolved{
				Correlation: corr,
				RequestID:   id,
			})
		}
		return nil, ctx.Err()
	case reply := <-p.ch:
		if len(prompts) > 0 && len(reply.Answers) == 0 {
			return nil, &RejectedError{Message: "The user dismissed this question."}
		}
		if len(reply.Answers) != len(prompts) {
			return nil, fmt.Errorf("question: got %d answers, want %d", len(reply.Answers), len(prompts))
		}
		return reply.Answers, nil
	}
}

// Reply resolves a pending ask. Unknown request IDs are ignored.
//
// QuestionResolved is emitted immediately only for asks whose QuestionAsked
// has already finished. Otherwise the resolution is queued on the pending
// entry and emitted by Ask after the opening event returns. All emitter calls
// run outside the mutex; Reply never waits on announcement.
func (s *Service) Reply(r protocol.QuestionReply) {
	s.mu.Lock()
	p, ok := s.pending[r.RequestID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.pending, r.RequestID)

	var emitNow *resolvedEmission
	if p.announced {
		emitNow = &resolvedEmission{id: r.RequestID, correlation: p.correlation}
	} else {
		p.hasDeferred = true
	}
	s.mu.Unlock()

	// Emit Resolved before waking the waiter so consumers observe it first.
	if emitNow != nil {
		s.emit(protocol.QuestionResolved{
			Correlation: emitNow.correlation,
			RequestID:   emitNow.id,
		})
	}
	p.ch <- r
}
