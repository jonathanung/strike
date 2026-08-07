package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Transport identifiers for ServerConfig.Transport.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// CatalogKind identifies which catalog changed.
type CatalogKind string

const (
	CatalogTools     CatalogKind = "tools"
	CatalogPrompts   CatalogKind = "prompts"
	CatalogResources CatalogKind = "resources"
)

// session is one connected MCP server (stdio subprocess or HTTP endpoint).
type session interface {
	Name() string
	Closed() bool
	Caps() ServerCaps
	ListTools(ctx context.Context) ([]toolInfo, error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (callToolResult, error)
	ListPrompts(ctx context.Context) ([]promptInfo, error)
	GetPrompt(ctx context.Context, name string, args map[string]string) (getPromptResult, error)
	ListResources(ctx context.Context) ([]resourceInfo, error)
	ReadResource(ctx context.Context, uri string) (readResourceResult, error)
	// OnNotification registers a handler for server→client notifications.
	// Replaces any previous handler. nil clears.
	OnNotification(fn func(method string, params json.RawMessage))
	Close() error
	deadErr() error
}

// ServerConfig is one MCP server (stdio command or remote HTTP endpoint).
type ServerConfig struct {
	// Name is the config key (used in tool namespaces and status).
	Name string
	// Transport is "stdio" (default) or "http" (streamable HTTP).
	Transport string
	// Stdio
	Command string
	Args    []string
	// Env overlays process environment; values are never logged.
	Env map[string]string
	// WorkDir is the subprocess working directory (empty = inherit).
	WorkDir string
	// HTTP (streamable HTTP MCP endpoint)
	URL string
	// Headers are sent on every HTTP request (Authorization, etc.). Never logged.
	Headers map[string]string
	// OAuth enables HTTP OAuth discovery/login/refresh when set.
	// Access tokens are never logged; stored under OAuthTokenFile when set.
	OAuth *OAuthConfig
}

// OAuthConfig configures HTTP MCP OAuth 2.0 (authorization code + refresh).
type OAuthConfig struct {
	// ClientID is the public OAuth client id (required when OAuth enabled).
	ClientID string `json:"clientId,omitempty"`
	// ClientSecret is optional (confidential clients); never logged.
	ClientSecret string `json:"clientSecret,omitempty"`
	// Scopes space-separated; empty uses server default when known.
	Scopes string `json:"scopes,omitempty"`
	// AuthorizeURL / TokenURL / RevokeURL override discovery when set.
	AuthorizeURL string `json:"authorizeUrl,omitempty"`
	TokenURL     string `json:"tokenUrl,omitempty"`
	RevokeURL    string `json:"revokeUrl,omitempty"`
	// DiscoveryURL is optional protected-resource or AS metadata URL.
	// When empty, derived from the MCP URL origin (/.well-known/oauth-authorization-server).
	DiscoveryURL string `json:"discoveryUrl,omitempty"`
	// TokenFile stores refresh/access tokens (0600). Empty disables persistence.
	TokenFile string `json:"tokenFile,omitempty"`
	// RedirectURL for loopback auth code (default http://127.0.0.1:0/callback).
	RedirectURL string `json:"redirectUrl,omitempty"`
}

// Start connects to a server using cfg.Transport and completes initialize.
func Start(ctx context.Context, cfg ServerConfig) (session, error) {
	switch normalizeTransport(cfg.Transport) {
	case TransportStdio, "":
		return startStdio(ctx, cfg)
	case TransportHTTP:
		return startHTTP(ctx, cfg)
	default:
		return nil, fmt.Errorf("mcp %s: unknown transport %q (want stdio|http)", cfg.Name, cfg.Transport)
	}
}

func normalizeTransport(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	switch t {
	case "", TransportStdio:
		return TransportStdio
	case TransportHTTP, "streamable-http", "streamable_http":
		return TransportHTTP
	case "sse":
		// Streamable HTTP supersedes legacy HTTP+SSE; accept the alias.
		return TransportHTTP
	default:
		return t
	}
}

func (cfg ServerConfig) transport() string {
	t := normalizeTransport(cfg.Transport)
	if t == TransportStdio && strings.TrimSpace(cfg.URL) != "" && strings.TrimSpace(cfg.Command) == "" {
		return TransportHTTP
	}
	return t
}

// displayEndpoint is a non-secret endpoint label for /mcp status.
func (cfg ServerConfig) displayEndpoint() string {
	switch cfg.transport() {
	case TransportHTTP:
		return strings.TrimSpace(cfg.URL)
	default:
		return strings.TrimSpace(cfg.Command)
	}
}

func validateServerConfig(cfg ServerConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("mcp: empty server name")
	}
	switch cfg.transport() {
	case TransportHTTP:
		if strings.TrimSpace(cfg.URL) == "" {
			return fmt.Errorf("mcp %s: empty url", cfg.Name)
		}
		if cfg.OAuth != nil && strings.TrimSpace(cfg.OAuth.ClientID) == "" &&
			strings.TrimSpace(cfg.OAuth.TokenURL) == "" && strings.TrimSpace(cfg.OAuth.DiscoveryURL) == "" {
			// Allow OAuth struct with only TokenFile for refresh-of-existing; require client id for login.
		}
	default:
		if cfg.Command == "" {
			return fmt.Errorf("mcp %s: empty command", cfg.Name)
		}
	}
	return nil
}
