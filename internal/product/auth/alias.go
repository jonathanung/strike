package auth

import (
	"context"

	provauth "github.com/jonathanung/strike-cli/providers/auth"
)

// Credential types live in providers/auth so the providers module can resolve
// keys without importing internal/. JSON tags are unchanged.

type Credential = provauth.Credential
type CredentialType = provauth.CredentialType

const (
	TypeAPIKey = provauth.TypeAPIKey
	TypeOAuth  = provauth.TypeOAuth
)

type Tokens = provauth.Tokens
type FlowConfig = provauth.FlowConfig
type PendingLogin = provauth.PendingLogin
type DeviceConfig = provauth.DeviceConfig
type DeviceCode = provauth.DeviceCode

func OpenAIFlow() FlowConfig      { return provauth.OpenAIFlow() }
func XAIFlow() FlowConfig         { return provauth.XAIFlow() }
func XAIDeviceFlow() DeviceConfig { return provauth.XAIDeviceFlow() }

func OpenAIExchangeAPIKey(ctx context.Context, idToken string) (string, error) {
	return provauth.OpenAIExchangeAPIKey(ctx, idToken)
}

func AccountIDFromToken(token string) string { return provauth.AccountIDFromToken(token) }

func CompleteLogin(ctx context.Context, store *Store, provider string, tokens *Tokens) (string, error) {
	return provauth.CompleteLogin(ctx, store, provider, tokens)
}

func Describe(provider string, store *Store) string {
	return provauth.Describe(provider, store)
}

func APIKey(provider string, store *Store) (string, bool) {
	return provauth.APIKey(provider, store)
}

func APIKeyEnv(provider string, store *Store, envName string) (string, bool) {
	return provauth.APIKeyEnv(provider, store, envName)
}

func BearerSource(provider string, store *Store) func(ctx context.Context) (string, error) {
	return provauth.BearerSource(provider, store)
}

func BearerSourceEnv(provider string, store *Store, envName string) func(ctx context.Context) (string, error) {
	return provauth.BearerSourceEnv(provider, store, envName)
}

func ChatGPTSource(store *Store) func(ctx context.Context) (string, string, error) {
	return provauth.ChatGPTSource(store)
}

var _ provauth.Store = (*Store)(nil)
