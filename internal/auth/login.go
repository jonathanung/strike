package auth

import (
	"context"
	"os"
)

// CompleteLogin persists OAuth tokens for a provider and returns a short
// human-readable outcome. For OpenAI it also attempts the id_token → API-key
// exchange, since the ChatGPT access token doesn't work on api.openai.com.
// Shared by the CLI (`strike auth login`) and the TUI (`/auth`).
func CompleteLogin(ctx context.Context, store *Store, provider string, tokens *Tokens) (string, error) {
	cred := Credential{
		Type:      TypeOAuth,
		Access:    tokens.Access,
		Refresh:   tokens.Refresh,
		IDToken:   tokens.IDToken,
		ExpiresAt: tokens.ExpiresAt,
	}
	message := "Logged in to " + provider
	if provider == "openai" {
		// Subscription mode: requests go to the ChatGPT backend with the
		// access token + account id, billed to the ChatGPT plan.
		cred.AccountID = AccountIDFromToken(tokens.IDToken)
		if cred.AccountID == "" {
			cred.AccountID = AccountIDFromToken(tokens.Access)
		}
		if cred.AccountID != "" {
			message += " (ChatGPT subscription mode)"
		}
		// Best-effort: also exchange for a platform API key as a fallback
		// (used only when subscription routing isn't possible).
		if tokens.IDToken != "" {
			if key, err := OpenAIExchangeAPIKey(ctx, tokens.IDToken); err == nil {
				cred.APIKey = key
			}
		}
		if cred.AccountID == "" {
			message += " — warning: no ChatGPT account id in tokens; subscription mode unavailable"
		}
	}
	if err := store.Set(provider, cred); err != nil {
		return "", err
	}
	return message, nil
}

// Describe returns a short credential status for a provider, for status
// displays: which env var is active, or what kind of credential is stored.
func Describe(provider string, store *Store) string {
	for _, env := range envNames(provider) {
		if os.Getenv(env) != "" {
			return env
		}
	}
	cred, ok := store.Get(provider)
	if !ok {
		return "none"
	}
	switch {
	case cred.Type == TypeAPIKey:
		return "api key"
	case cred.APIKey != "":
		return "oauth+key"
	default:
		return "oauth"
	}
}
