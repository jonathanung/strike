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

// session is one connected MCP server (stdio subprocess or HTTP endpoint).
type session interface {
	Name() string
	Closed() bool
	ListTools(ctx context.Context) ([]toolInfo, error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (callToolResult, error)
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
	return normalizeTransport(cfg.Transport)
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
	default:
		if cfg.Command == "" {
			return fmt.Errorf("mcp %s: empty command", cfg.Name)
		}
	}
	return nil
}
