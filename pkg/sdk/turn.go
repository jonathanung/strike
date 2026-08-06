package sdk

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// Default permission reject message when Turn.OnPermission is nil.
const defaultPermissionReject = "sdk: permission ask denied (set Turn.OnPermission or allow tools via engine policy)"

// Turn configures one user prompt and how interactive asks are resolved.
type Turn struct {
	// Text is the user message. Required unless Images alone are set; empty
	// text with no images returns an error from RunTurn.
	Text string
	// Images are optional multimodal attachments on the user message.
	Images []protocol.ImageAttachment

	// OnPermission resolves PermissionAsked. Nil rejects every ask with
	// DecisionReject and defaultPermissionReject as feedback.
	OnPermission func(protocol.PermissionAsked) protocol.PermissionReply
	// OnQuestion resolves QuestionAsked. Nil replies with empty answers
	// (engine treats that as cancel/reject depending on tool).
	OnQuestion func(protocol.QuestionAsked) protocol.QuestionReply

	// OnEvent is an optional observer for every event during the turn
	// (including those handled internally). It must not block indefinitely.
	OnEvent func(protocol.Event)
}

// TurnResult is the outcome of [Client.RunTurn].
type TurnResult struct {
	// Text is the concatenation of TextDelta chunks for this turn.
	Text string
	// StopReason is TurnCompleted.StopReason when the turn finished cleanly.
	StopReason string
	// Err is a non-nil engine or turn failure. RunTurn also returns this as
	// its error value; it is duplicated here for callers that keep the
	// partial Text.
	Err error
}

// RunTurn submits one user prompt and blocks until the turn completes, the
// event stream closes, or ctx is cancelled.
//
// On ctx cancel it best-effort sends Interrupt and continues draining until
// TurnCompleted or events close, then returns context.Canceled (partial Text
// is still populated on TurnResult).
//
// Permission and question asks are answered via Turn hooks (or defaults).
// Other ops (model select, compact, …) are not issued by RunTurn — use Send.
func (c *Client) RunTurn(ctx context.Context, turn Turn) (TurnResult, error) {
	if c == nil {
		return TurnResult{}, errors.New("sdk: nil client")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(turn.Text) == "" && len(turn.Images) == 0 {
		return TurnResult{}, errors.New("sdk: empty turn text")
	}

	if err := c.Send(ctx, protocol.UserInput{Text: turn.Text, Images: turn.Images}); err != nil {
		return TurnResult{}, err
	}

	var (
		out       strings.Builder
		result    TurnResult
		turnErr   error
		interrupt bool
	)

	for {
		select {
		case <-ctx.Done():
			if !interrupt {
				interrupt = true
				_ = c.Send(context.Background(), protocol.Interrupt{})
			}
			// Keep draining events after interrupt (do not spin on ctx).
			ev, ok := <-c.events
			if !ok {
				result.Text = out.String()
				if turnErr != nil {
					result.Err = turnErr
					return result, turnErr
				}
				result.Err = context.Canceled
				return result, context.Canceled
			}
			done, err := c.handleTurnEvent(context.Background(), turn, ev, &out, &result, &turnErr)
			if done {
				result.Text = out.String()
				if interrupt {
					if err == nil {
						result.Err = context.Canceled
						return result, context.Canceled
					}
					// Prefer the turn error if the engine reported one.
					return result, err
				}
				return result, err
			}
		case ev, ok := <-c.events:
			if !ok {
				result.Text = out.String()
				if turnErr != nil {
					result.Err = turnErr
					return result, turnErr
				}
				result.Err = ErrClosed
				return result, ErrClosed
			}
			done, err := c.handleTurnEvent(ctx, turn, ev, &out, &result, &turnErr)
			if done {
				return result, err
			}
		}
	}
}

func (c *Client) handleTurnEvent(
	ctx context.Context,
	turn Turn,
	ev protocol.Event,
	out *strings.Builder,
	result *TurnResult,
	turnErr *error,
) (done bool, err error) {
	if turn.OnEvent != nil {
		turn.OnEvent(ev)
	}
	switch e := ev.(type) {
	case protocol.TextDelta:
		out.WriteString(e.Text)
	case protocol.PermissionAsked:
		reply := protocol.PermissionReply{
			RequestID: e.RequestID,
			Decision:  protocol.DecisionReject,
			Message:   defaultPermissionReject,
		}
		if turn.OnPermission != nil {
			reply = turn.OnPermission(e)
			if reply.RequestID == "" {
				reply.RequestID = e.RequestID
			}
		}
		if sendErr := c.Send(ctx, reply); sendErr != nil && !errors.Is(sendErr, context.Canceled) {
			result.Text = out.String()
			result.Err = sendErr
			return true, sendErr
		}
	case protocol.QuestionAsked:
		reply := protocol.QuestionReply{RequestID: e.RequestID}
		if turn.OnQuestion != nil {
			reply = turn.OnQuestion(e)
			if reply.RequestID == "" {
				reply.RequestID = e.RequestID
			}
		}
		if sendErr := c.Send(ctx, reply); sendErr != nil && !errors.Is(sendErr, context.Canceled) {
			result.Text = out.String()
			result.Err = sendErr
			return true, sendErr
		}
	case protocol.EngineError:
		*turnErr = errors.New(e.Message)
	case protocol.TurnCompleted:
		result.Text = out.String()
		result.StopReason = e.StopReason
		if *turnErr != nil {
			result.Err = *turnErr
			return true, *turnErr
		}
		if e.StopReason == "error" {
			result.Err = errors.New("turn ended with error")
			return true, result.Err
		}
		return true, nil
	}
	return false, nil
}

// CollectText concatenates TextDelta events from events until the channel
// closes. It does not submit ops. Useful when draining a finished session.
func CollectText(events <-chan protocol.Event) string {
	var b strings.Builder
	for ev := range events {
		if d, ok := ev.(protocol.TextDelta); ok {
			b.WriteString(d.Text)
		}
	}
	return b.String()
}

// FormatEvent returns a short debug label for an event (type name + brief
// detail). Stable enough for logs; not a wire format.
func FormatEvent(ev protocol.Event) string {
	if ev == nil {
		return "<nil>"
	}
	switch e := ev.(type) {
	case protocol.TextDelta:
		return fmt.Sprintf("text.delta len=%d", len(e.Text))
	case protocol.TurnStarted:
		return "turn.started"
	case protocol.TurnCompleted:
		return fmt.Sprintf("turn.completed stop=%q", e.StopReason)
	case protocol.EngineError:
		return fmt.Sprintf("engine.error %s", e.Message)
	case protocol.PermissionAsked:
		return fmt.Sprintf("permission.asked id=%s perm=%s", e.RequestID, e.Permission)
	case protocol.QuestionAsked:
		return fmt.Sprintf("question.asked id=%s n=%d", e.RequestID, len(e.Questions))
	case protocol.ToolCallBegin:
		return fmt.Sprintf("tool.begin %s", e.Name)
	case protocol.ToolCallEnd:
		return fmt.Sprintf("tool.end %s err=%v", e.Title, e.IsError)
	default:
		return fmt.Sprintf("%T", ev)
	}
}
