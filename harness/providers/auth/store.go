package auth

import "strings"

// Store is the credential persistence surface the flows and factory need.
// Strike's on-disk ~/.strike/auth.json implements this; tests use an in-memory
// stand-in. Path and file mode stay in the product store.
type Store interface {
	Get(provider string) (Credential, bool)
	Set(provider string, c Credential) error
}

// CanonicalProvider maps shipped aliases onto the storage/list id
// (gemini → google). Empty input stays empty after trim/lowercase.
func CanonicalProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "gemini" {
		return "google"
	}
	return provider
}

func canonicalProvider(provider string) string {
	return CanonicalProvider(provider)
}
