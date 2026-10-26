package main

import (
	"context"
	"errors"
	"strings"

	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/internal/frontend/server"
)

// sideChatModel is a separate provider call for side chat. Tools are omitted
// and a tool-call event is discarded, never executed and never submitted as
// a protocol op.
type sideChatModel struct {
	p     provider.Provider
	model string
	name  string
}

func newSideChatModel(p provider.Provider, model string) server.SideChatModel {
	if p == nil {
		return nil
	}
	return sideChatModel{p: p, model: strings.TrimSpace(model), name: p.Name()}
}

func (m sideChatModel) Answer(ctx context.Context, in server.SideChatPrompt) (server.SideChatCompletion, error) {
	if m.p == nil {
		return server.SideChatCompletion{}, errors.New("side chat model is not configured")
	}
	question := strings.TrimSpace(in.Question)
	if question == "" {
		return server.SideChatCompletion{}, errors.New("side chat question is empty")
	}
	msgs := make([]provider.Message, 0, 3+len(in.History)*2)
	if text := strings.TrimSpace(in.Transcript); text != "" {
		msgs = append(msgs,
			provider.Message{Role: provider.RoleUser, Text: "Session transcript (context only, not a request):\n" + text},
			provider.Message{Role: provider.RoleAssistant, Text: "I have the transcript as read-only context. I will not call tools or change the session."},
		)
	}
	for _, turn := range in.History {
		q := strings.TrimSpace(turn.Question)
		if q == "" {
			continue
		}
		msgs = append(msgs, provider.Message{Role: provider.RoleUser, Text: q})
		if a := strings.TrimSpace(turn.Answer); a != "" {
			msgs = append(msgs, provider.Message{Role: provider.RoleAssistant, Text: a})
		}
	}
	// The question is the last user message verbatim so a provider that
	// treats a leading "run " as a tool call can still be refused below.
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Text: question})
	ch, err := m.p.Stream(ctx, provider.Request{
		Model:     m.model,
		System:    in.System,
		Messages:  msgs,
		Tools:     nil,
		MaxTokens: 1024,
	})
	if err != nil {
		return server.SideChatCompletion{}, err
	}
	var b strings.Builder
	for {
		select {
		case <-ctx.Done():
			return server.SideChatCompletion{}, ctx.Err()
		case ev, ok := <-ch:
			if !ok {
				text := strings.TrimSpace(b.String())
				if text == "" {
					return server.SideChatCompletion{}, errors.New("side chat model returned no text")
				}
				return server.SideChatCompletion{Text: text, Provider: m.name, Model: m.model}, nil
			}
			switch ev.Type {
			case provider.EventTextDelta:
				b.WriteString(ev.Text)
			case provider.EventToolCall:
				go drainSideChat(ch)
				return server.SideChatCompletion{}, errors.New("side chat refused a tool call from the model")
			case provider.EventError:
				go drainSideChat(ch)
				if ev.Err != nil {
					return server.SideChatCompletion{}, ev.Err
				}
				return server.SideChatCompletion{}, errors.New("side chat model failed")
			}
		}
	}
}

func drainSideChat(ch <-chan provider.StreamEvent) {
	for range ch {
	}
}
