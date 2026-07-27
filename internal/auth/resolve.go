package auth

import (
	"context"
	"fmt"
	"os"
	"time"
)

// Resolution order for credentials: environment variable, then stored API
// key, then stored OAuth tokens (refreshed when close to expiry).

var envVars = map[string]string{
	"anthropic": "ANTHROPIC_API_KEY",
	"openai":    "OPENAI_API_KEY",
	"xai":       "XAI_API_KEY",
	"gemini":    "GEMINI_API_KEY",
	"kimi":      "KIMI_API_KEY",
	"deepseek":  "DEEPSEEK_API_KEY",
}

// refreshFlows maps providers to the flow used for OAuth token refresh.
var refreshFlows = map[string]FlowConfig{
	"openai": OpenAIFlow(),
	"xai":    XAIFlow(),
}

// Refresh slightly before expiry so a long tool call doesn't have to
// recover from a mid-flight 401.
const refreshSkew = 2 * time.Minute

// APIKey resolves a plain API key (env or stored) for a provider.
func APIKey(provider string, store *Store) (string, bool) {
	return APIKeyEnv(provider, store, envVars[provider])
}

// APIKeyEnv resolves a plain API key using an explicit environment variable
// name (for custom providers' apiKeyEnv), then the stored credential.
// Empty envName skips the environment lookup.
func APIKeyEnv(provider string, store *Store, envName string) (string, bool) {
	if envName != "" {
		if key := os.Getenv(envName); key != "" {
			return key, true
		}
	}
	if cred, ok := store.Get(provider); ok && cred.APIKey != "" {
		return cred.APIKey, true
	}
	return "", false
}

// BearerSourceEnv is BearerSource with a custom environment variable name
// checked before the auth store (used by custom/self-hosted providers).
func BearerSourceEnv(provider string, store *Store, envName string) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if key, ok := APIKeyEnv(provider, store, envName); ok {
			return key, nil
		}
		cred, err := freshOAuth(ctx, store, provider)
		if err != nil {
			hint := "run `/auth " + provider + " key` or set an API key in /settings"
			if envName != "" {
				hint = "set " + envName + " or " + hint
			}
			return "", fmt.Errorf("no credentials for %s: %s", provider, hint)
		}
		return cred.Access, nil
	}
}

// freshOAuth returns the stored OAuth credential for a provider, refreshed
// (and persisted) when close to expiry.
func freshOAuth(ctx context.Context, store *Store, provider string) (Credential, error) {
	cred, ok := store.Get(provider)
	if !ok || cred.Type != TypeOAuth || cred.Access == "" {
		return Credential{}, fmt.Errorf("no OAuth credentials for %s — run `strike auth login %s` (or /auth %s in the TUI)", provider, provider, provider)
	}
	if time.Until(cred.ExpiresAt) > refreshSkew {
		return cred, nil
	}
	flow, ok := refreshFlows[provider]
	if !ok || cred.Refresh == "" {
		return cred, nil // no refresh path; let the API surface a 401
	}
	tokens, err := flow.Refresh(ctx, cred.Refresh)
	if err != nil {
		return Credential{}, fmt.Errorf("refreshing %s token (run `strike auth login %s` if this persists): %w", provider, provider, err)
	}
	cred.Access = tokens.Access
	cred.Refresh = tokens.Refresh
	cred.ExpiresAt = tokens.ExpiresAt
	if tokens.IDToken != "" {
		cred.IDToken = tokens.IDToken
	}
	if err := store.Set(provider, cred); err != nil {
		return Credential{}, fmt.Errorf("persisting refreshed %s token: %w", provider, err)
	}
	return cred, nil
}

// BearerSource returns a per-request credential resolver for a provider:
// API key when available, otherwise OAuth access token with auto-refresh.
func BearerSource(provider string, store *Store) func(ctx context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if key, ok := APIKey(provider, store); ok {
			return key, nil
		}
		cred, err := freshOAuth(ctx, store, provider)
		if err != nil {
			env := envVars[provider]
			return "", fmt.Errorf("no credentials for %s: set %s or run `strike auth login %s`", provider, env, provider)
		}
		return cred.Access, nil
	}
}

// ChatGPTSource resolves the access token + ChatGPT account id for OpenAI
// subscription mode (the chatgpt.com/backend-api/codex transport). It
// deliberately ignores API keys: subscription traffic must use the OAuth
// access token.
func ChatGPTSource(store *Store) func(ctx context.Context) (string, string, error) {
	return func(ctx context.Context) (string, string, error) {
		cred, err := freshOAuth(ctx, store, "openai")
		if err != nil {
			return "", "", err
		}
		accountID := cred.AccountID
		if accountID == "" {
			accountID = AccountIDFromToken(cred.IDToken)
		}
		if accountID == "" {
			accountID = AccountIDFromToken(cred.Access)
		}
		if accountID == "" {
			return "", "", fmt.Errorf("could not determine ChatGPT account id from stored tokens — run `strike auth login openai` again")
		}
		return cred.Access, accountID, nil
	}
}
