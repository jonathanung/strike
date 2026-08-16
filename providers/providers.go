// Package providers constructs vendor LLM adapters from a credential source.
//
// The engine takes a SelectFunc and does not import this module. Import this
// package (not internal/) to build Anthropic/OpenAI/ChatGPT/Google adapters
// and to run OAuth/PKCE/device flows.
package providers

import (
	"context"

	"github.com/jonathanung/strike-cli/provider"
	"github.com/jonathanung/strike-cli/providers/anthropic"
	"github.com/jonathanung/strike-cli/providers/chatgpt"
	"github.com/jonathanung/strike-cli/providers/factory"
	"github.com/jonathanung/strike-cli/providers/google"
	"github.com/jonathanung/strike-cli/providers/openaicompat"
)

// Select constructs a named adapter (builtin, endpoint overlay, or custom).
func Select(name string, opts factory.Options) (provider.Provider, string, error) {
	return factory.Select(name, opts)
}

// NewAnthropic constructs the Anthropic Messages adapter from an API key.
func NewAnthropic(apiKey string) (provider.Provider, error) {
	return anthropic.New(apiKey)
}

// NewOpenAI constructs the OpenAI platform chat-completions adapter.
func NewOpenAI(bearer func(context.Context) (string, error)) provider.Provider {
	return openaicompat.NewOpenAI(bearer)
}

// NewGoogle constructs the Google AI Studio adapter.
func NewGoogle(source func(context.Context) (string, error)) provider.Provider {
	return google.New(source)
}

// NewChatGPT constructs the ChatGPT subscription adapter.
func NewChatGPT(source chatgpt.TokenSource) provider.Provider {
	return chatgpt.New(source)
}
