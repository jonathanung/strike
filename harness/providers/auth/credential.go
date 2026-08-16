package auth

import "time"

type CredentialType string

const (
	TypeAPIKey CredentialType = "api"
	TypeOAuth  CredentialType = "oauth"
)

// Credential is a persisted provider credential. JSON tags are part of the
// ~/.strike/auth.json wire format and must stay stable.
type Credential struct {
	Type CredentialType `json:"type"`
	// APIKey is set for TypeAPIKey, and may also be set on a TypeOAuth
	// credential when the OAuth flow yielded an exchanged API key
	// (OpenAI's ChatGPT login does this).
	APIKey    string    `json:"apiKey,omitempty"`
	Access    string    `json:"access,omitempty"`
	Refresh   string    `json:"refresh,omitempty"`
	IDToken   string    `json:"idToken,omitempty"`
	ExpiresAt time.Time `json:"expiresAt,omitempty"`
	// AccountID is the ChatGPT account id (OpenAI subscription mode).
	AccountID string `json:"accountId,omitempty"`
}
