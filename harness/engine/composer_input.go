package engine

import (
	"context"
	"strings"

	"github.com/jonathanung/strike/pkg/protocol"
)

// InputAction is how Options.PrepareInput wants a composer message handled.
type InputAction string

const (
	// InputPass leaves the text unchanged.
	InputPass InputAction = ""
	// InputText uses ModelText for the model and Display for the transcript.
	InputText InputAction = "text"
	// InputShell runs Shell and does not start a model turn.
	InputShell InputAction = "shell"
	// InputError rejects the message.
	InputError InputAction = "error"
	// InputOp replaces the message with Op.
	InputOp InputAction = "op"
)

// InputPrep is the daemon expansion of one user.input or steer text.
type InputPrep struct {
	Action    InputAction
	ModelText string
	Display   string
	Shell     string
	Error     string
	Op        protocol.Op
	Notices   []string
}

// consumeComposer applies Options.PrepareInput.
// handled is true when the message became a shell run, a replacement op,
// or an error. Otherwise model is the turn text and display is the
// transcript line (empty display means show model).
func (e *Engine) consumeComposer(ctx context.Context, text string) (model, display string, handled bool) {
	model = text
	if e == nil || e.opts.PrepareInput == nil || strings.TrimSpace(text) == "" {
		return model, "", false
	}
	prep := e.opts.PrepareInput(text, e.opts.WorkDir)
	switch prep.Action {
	case InputPass:
		return model, "", false
	case InputError:
		msg := strings.TrimSpace(prep.Error)
		if msg == "" {
			msg = "composer input was rejected"
		}
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     msg,
			Code:        protocol.ErrorCodeInvalidArgs,
		})
		return "", "", true
	case InputOp:
		if prep.Op == nil {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "composer command did not resolve to an action",
				Code:        protocol.ErrorCodeInvalidArgs,
			})
			return "", "", true
		}
		op := prep.Op
		if _, isInput := op.(protocol.UserInput); isInput {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "composer command cannot expand to another user message",
				Code:        protocol.ErrorCodeInvalidArgs,
			})
			return "", "", true
		}
		if _, isSteer := op.(protocol.Steer); isSteer {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "composer command cannot expand to steer",
				Code:        protocol.ErrorCodeInvalidArgs,
			})
			return "", "", true
		}
		if sel, ok := op.(protocol.SelectModel); ok && strings.TrimSpace(sel.Provider) == "" {
			sel.Provider = e.provName
			op = sel
		}
		e.handleOp(ctx, op)
		return "", "", true
	case InputShell:
		command := strings.TrimSpace(prep.Shell)
		if command == "" {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "empty command — use !cmd to run a shell command",
				Code:        protocol.ErrorCodeInvalidArgs,
			})
			return "", "", true
		}
		shown := strings.TrimSpace(prep.Display)
		if shown == "" {
			shown = "!" + command
		}
		e.runComposerShell(ctx, shown, command)
		return "", "", true
	case InputText:
		for _, notice := range prep.Notices {
			notice = strings.TrimSpace(notice)
			if notice == "" {
				continue
			}
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "composer: " + notice,
				Code:        "composer_notice",
			})
		}
		if strings.TrimSpace(prep.ModelText) != "" {
			model = prep.ModelText
		}
		return model, prep.Display, false
	default:
		return model, "", false
	}
}
