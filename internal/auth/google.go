package auth

import "os"

// Google OAuth 2.0 installed-app flow with PKCE for Gemini API access.
//
// Unlike OpenAI and xAI, there is no public Google OAuth desktop client id
// that strike can ship. Users must create an OAuth 2.0 Client ID
// (type "Desktop app") in the Google Cloud Console and supply it via
// GOOGLE_CLIENT_ID (and optionally GOOGLE_CLIENT_SECRET). The redirect
// host:port is part of that registration and defaults to localhost:8765.
//
// Required scope for the Gemini API on generativelanguage.googleapis.com
// is https://www.googleapis.com/auth/cloud-platform (or the narrower
// https://www.googleapis.com/auth/generative-language when available).

const (
	googleAuthBase     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL     = "https://oauth2.googleapis.com/token"
	googleScope        = "https://www.googleapis.com/auth/cloud-platform"
	googleRedirectPort = 8765
)

// GoogleFlow returns the OAuth 2.0 + PKCE flow config for Google accounts.
// GOOGLE_CLIENT_ID must be set in the environment. GOOGLE_CLIENT_SECRET is
// optional for the PKCE flow but may be required by some client configurations.
// When the env vars are unset the flow still returns, but Begin/Login will
// produce an authorize URL that redirects back to Google's error page.
func GoogleFlow() FlowConfig {
	extra := map[string]string{
		"access_type": "offline", // force refresh token
		"prompt":      "consent",
	}
	return FlowConfig{
		AuthorizeURL: googleAuthBase,
		TokenURL:     googleTokenURL,
		ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
		Scope:        googleScope,
		RedirectHost: "localhost",
		RedirectPort: googleRedirectPort,
		RedirectPath: "/oauth/callback",
		ExtraParams:  extra,
	}
}
