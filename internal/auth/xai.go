package auth

// xAI Grok OAuth — reuses the public Grok-CLI client registration (xAI's
// auth server rejects loopback OAuth from non-allowlisted clients; this is
// the client id xAI ships for desktop flows, the same one opencode reuses).
// The 127.0.0.1:56121 redirect is part of that registration.
const (
	xaiClientID     = "b1a00492-073a-47ea-816f-4c329264a828"
	xaiAuthBase     = "https://auth.x.ai"
	xaiScope        = "openid profile email offline_access grok-cli:access api:access"
	xaiRedirectPort = 56121
)

func XAIFlow() FlowConfig {
	return FlowConfig{
		AuthorizeURL: xaiAuthBase + "/oauth2/authorize",
		TokenURL:     xaiAuthBase + "/oauth2/token",
		ClientID:     xaiClientID,
		Scope:        xaiScope,
		RedirectHost: "127.0.0.1",
		RedirectPort: xaiRedirectPort,
		RedirectPath: "/callback",
		IncludeNonce: true,
		// plan=generic opts into xAI's generic OAuth plan tier; without it
		// the consent screen rejects loopback OAuth for this client.
		// referrer attributes strike-originated logins in xAI's logs.
		ExtraParams: map[string]string{
			"plan":     "generic",
			"referrer": "strike",
		},
	}
}

// XAIDeviceFlow is the headless path (VPS/SSH/containers) — RFC 8628.
func XAIDeviceFlow() DeviceConfig {
	return DeviceConfig{
		DeviceURL: xaiAuthBase + "/oauth2/device/code",
		TokenURL:  xaiAuthBase + "/oauth2/token",
		ClientID:  xaiClientID,
		Scope:     xaiScope,
	}
}
