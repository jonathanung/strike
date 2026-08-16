package admission

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/jonathanung/strike-cli/internal/trust/security"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// MCPTool is the metadata surface scanned before tools bind into the registry.
type MCPTool struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema object (may be empty).
	InputSchema json.RawMessage
}

// MCPSubject is one MCP server presented for admission.
type MCPSubject struct {
	Name      string
	Transport string // stdio|http
	Endpoint  string // command or URL (non-secret label)
	Tools     []MCPTool
}

var (
	reMCPNetwork = regexp.MustCompile(`(?i)\b(http|https|fetch|request|curl|wget|browser|websocket|webfetch|web_search|websearch|url)\b`)
	reMCPShell   = regexp.MustCompile(`(?i)\b(shell|bash|zsh|sh|exec|execute|eval|spawn|terminal|run_command|run-command|process)\b`)
	reMCPBroadFS = regexp.MustCompile(`(?i)\b(read_file|write_file|delete_file|list_dir|filesystem|any\s+path|arbitrary\s+path|full\s+access)\b`)
	reCredKey    = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|secret|password|passwd|authorization|private[_-]?key|credential)`)
)

// ScanMCP returns findings for one server (metadata + static tool checks).
func ScanMCP(sub MCPSubject) []security.Finding {
	var out []security.Finding
	name := strings.TrimSpace(sub.Name)
	if name == "" {
		name = "unnamed"
	}
	surface := "mcp"

	if strings.EqualFold(strings.TrimSpace(sub.Transport), "http") {
		ep := strings.TrimSpace(sub.Endpoint)
		if ep != "" && !isLocalHTTPEndpoint(ep) {
			out = append(out, security.Finding{
				Rule:     "mcp.remote_http",
				Surface:  surface,
				Target:   name,
				Message:  "MCP server uses remote HTTP transport",
				Severity: security.SeverityMedium,
				Evidence: clipEvidence(ep, 120),
			})
		}
	}

	for _, t := range sub.Tools {
		tname := strings.TrimSpace(t.Name)
		desc := strings.TrimSpace(t.Description)
		blob := tname + " " + desc

		if reMCPShell.MatchString(blob) {
			out = append(out, security.Finding{
				Rule:     "mcp.shell_tool",
				Surface:  surface,
				Target:   name,
				Message:  "tool resembles shell/exec capability",
				Severity: security.SeverityCritical,
				Evidence: clipEvidence(tname, 64),
			})
		}
		if reMCPNetwork.MatchString(blob) {
			out = append(out, security.Finding{
				Rule:     "mcp.network_tool",
				Surface:  surface,
				Target:   name,
				Message:  "tool resembles network/egress capability",
				Severity: security.SeverityHigh,
				Evidence: clipEvidence(tname, 64),
			})
		}
		if reMCPBroadFS.MatchString(blob) {
			sev := security.SeverityHigh
			// Soften when schema requires a path-like property (still elevated).
			if schemaRequiresPath(t.InputSchema) {
				sev = security.SeverityMedium
			}
			out = append(out, security.Finding{
				Rule:     "mcp.broad_fs_tool",
				Surface:  surface,
				Target:   name,
				Message:  "tool resembles over-broad filesystem access",
				Severity: sev,
				Evidence: clipEvidence(tname, 64),
			})
		}
		out = append(out, scanSchemaCredentials(surface, name, tname, t.InputSchema)...)
	}
	return out
}

// AdmitMCP scans and decides for one MCP server.
func AdmitMCP(pol Policy, sub MCPSubject) Verdict {
	findings := ScanMCP(sub)
	return pol.Decide("mcp", strings.TrimSpace(sub.Name), findings)
}

func isLocalHTTPEndpoint(ep string) bool {
	lower := strings.ToLower(ep)
	return strings.Contains(lower, "://127.0.0.1") ||
		strings.Contains(lower, "://localhost") ||
		strings.Contains(lower, "://[::1]") ||
		strings.HasPrefix(lower, "http://127.0.0.1") ||
		strings.HasPrefix(lower, "https://127.0.0.1")
}

func schemaRequiresPath(schema json.RawMessage) bool {
	if len(schema) == 0 {
		return false
	}
	var obj struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &obj); err != nil {
		return false
	}
	for _, r := range obj.Required {
		lr := strings.ToLower(r)
		if lr == "path" || lr == "filepath" || lr == "file_path" || lr == "uri" {
			return true
		}
	}
	return false
}

func scanSchemaCredentials(surface, server, tool string, schema json.RawMessage) []security.Finding {
	if len(schema) == 0 {
		return nil
	}
	// Walk defaults and const string values for credential shapes.
	var root any
	if err := json.Unmarshal(schema, &root); err != nil {
		return nil
	}
	var hits []string
	walkSchemaSecrets(root, "", &hits)
	if len(hits) == 0 {
		return nil
	}
	return []security.Finding{{
		Rule:     "mcp.credential_default",
		Surface:  surface,
		Target:   server,
		Message:  "tool schema embeds credential-shaped default/const",
		Severity: security.SeverityCritical,
		Evidence: clipEvidence(tool+": "+hits[0], 80),
	}}
}

func walkSchemaSecrets(v any, key string, hits *[]string) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			lk := strings.ToLower(k)
			if lk == "default" || lk == "const" || lk == "examples" {
				collectSecretStrings(child, key, hits)
			}
			// Property names that look like secrets with string defaults nearby.
			if reCredKey.MatchString(k) {
				if def, ok := t["default"].(string); ok && looksCredential(def) {
					*hits = append(*hits, k)
				}
			}
			walkSchemaSecrets(child, k, hits)
		}
	case []any:
		for _, child := range t {
			walkSchemaSecrets(child, key, hits)
		}
	}
}

func collectSecretStrings(v any, key string, hits *[]string) {
	switch t := v.(type) {
	case string:
		if looksCredential(t) {
			*hits = append(*hits, key)
		}
	case []any:
		for _, c := range t {
			collectSecretStrings(c, key, hits)
		}
	case map[string]any:
		for _, c := range t {
			collectSecretStrings(c, key, hits)
		}
	}
}

func looksCredential(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len(s) < 8 {
		return false
	}
	if redact.ContainsSecret(s) {
		return true
	}
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "sk-") || strings.HasPrefix(lower, "ghp_") || strings.HasPrefix(lower, "akia") {
		return true
	}
	if strings.Contains(s, "BEGIN ") && strings.Contains(s, "PRIVATE KEY") {
		return true
	}
	return false
}

func clipEvidence(s string, n int) string {
	s = strings.TrimSpace(s)
	s = redact.String(s)
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
