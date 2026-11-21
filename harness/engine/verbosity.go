package engine

import (
	"fmt"
	"strings"

	"github.com/jonathanung/strike/pkg/protocol"
)

// Verbosity (#1369) is delivered as a session instruction layer. No provider
// adapter in this tree exposes a native output-length parameter, so the
// instruction is the only honest delivery; VerbositySelected reports it.
const (
	verbosityConciseInstruction  = "Response style for this session: concise. Answer in as few words as the task allows. Lead with the answer, skip preamble, recaps, and optional detail. Use short lists only when they are clearer than a sentence. This governs the length of your replies, not how carefully you work."
	verbosityDetailedInstruction = "Response style for this session: detailed. Explain your reasoning, the trade-offs you considered, and relevant context. Walk through non-obvious steps and mention caveats. Stay organised with headings or lists where they help. This governs the length of your replies, not how carefully you work."
	verbosityLayerSource         = "session:verbosity"
)

// verbosityInstruction is the system-prompt text for a style; empty for the
// default style, which sends nothing.
func verbosityInstruction(level protocol.Verbosity) string {
	switch level {
	case protocol.VerbosityConcise:
		return verbosityConciseInstruction
	case protocol.VerbosityDetailed:
		return verbosityDetailedInstruction
	default:
		return ""
	}
}

// verbosityDelivery reports how a style reaches the model.
func verbosityDelivery(level protocol.Verbosity) string {
	if verbosityInstruction(level) == "" {
		return protocol.VerbosityDeliveryNone
	}
	return protocol.VerbosityDeliveryInstruction
}

func verbosityNames() string {
	names := []string{"default"}
	for _, level := range protocol.Verbosities() {
		names = append(names, string(level))
	}
	return strings.Join(names, "|")
}

// setVerbosity records the session response style and confirms it. It takes
// effect on the next provider request; past turns are untouched.
func (e *Engine) setVerbosity(level protocol.Verbosity) {
	parsed, ok := protocol.ParseVerbosity(string(level))
	if !ok {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("unknown verbosity %q (want %s)", level, verbosityNames()),
		})
		return
	}
	e.verbosity = parsed
	e.emitSelected(protocol.VerbositySelected{
		Correlation: e.sessionCorr(),
		Level:       parsed,
		Delivery:    verbosityDelivery(parsed),
	})
}

// verbosityLayer is the session-style system layer, or false when the style
// is the default.
func (e *Engine) verbosityLayer() (promptLayer, bool) {
	text := verbosityInstruction(e.verbosity)
	if text == "" {
		return promptLayer{}, false
	}
	return promptLayer{
		Kind:   protocol.PromptLayerInstruction,
		Source: verbosityLayerSource,
		Mode:   protocol.PromptLayerAppend,
		Text:   text,
	}, true
}
