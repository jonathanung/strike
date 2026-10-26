package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/internal/frontend/server"
)

// sideChatModel is a separate provider call for side chat. Tools are omitted
// and a tool-call event is discarded, never executed and never submitted as
// a protocol op.
// sideChatSelect builds a provider for the live root's current name. It is
// the same factory the engine uses for select.model, called on a separate
// instance so the ask does not share the running turn's stream.
type sideChatSelect func(name string) (provider.Provider, string, error)

type sideChatModel struct {
	p     provider.Provider
	model string
	name  string
	pick  sideChatSelect
}

func newSideChatModel(p provider.Provider, model string, sel sideChatSelect) server.SideChatModel {
	if p == nil {
		return nil
	}
	return sideChatModel{p: p, model: strings.TrimSpace(model), name: p.Name(), pick: sel}
}

// resolve follows the live root. An empty prompt provider keeps the boot
// instance. A different provider is selected again so the ask uses that
// vendor, still with no tools.
func (m sideChatModel) resolve(in server.SideChatPrompt) (provider.Provider, string, string, error) {
	p, model, name := m.p, m.model, m.name
	wantP := strings.TrimSpace(in.Provider)
	wantM := strings.TrimSpace(in.Model)
	if wantP != "" && !strings.EqualFold(wantP, name) {
		if m.pick == nil {
			return nil, "", "", fmt.Errorf("side chat cannot switch to provider %s", wantP)
		}
		np, defModel, err := m.pick(wantP)
		if err != nil {
			return nil, "", "", err
		}
		if np == nil {
			return nil, "", "", fmt.Errorf("side chat provider %s is not configured", wantP)
		}
		p = np
		name = np.Name()
		if strings.TrimSpace(defModel) != "" {
			model = strings.TrimSpace(defModel)
		}
	}
	if wantM != "" {
		model = wantM
	}
	return p, model, name, nil
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
	p, model, name, err := m.resolve(in)
	if err != nil {
		return server.SideChatCompletion{}, err
	}
	ch, err := p.Stream(ctx, provider.Request{
		Model:     model,
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
			// The provider send is unbuffered. Drain so a cancelled ask
			// cannot leave the stream goroutine blocked on EventDone.
			go drainSideChat(ch)
			return server.SideChatCompletion{}, ctx.Err()
		case ev, ok := <-ch:
			if !ok {
				text := strings.TrimSpace(b.String())
				if text == "" {
					return server.SideChatCompletion{}, errors.New("side chat model returned no text")
				}
				return server.SideChatCompletion{Text: text, Provider: name, Model: model}, nil
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
