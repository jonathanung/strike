package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// GlobalMCPFilePath prefers mcp.jsonc then mcp.json under ~/.strike.
// Empty string means the path cannot be resolved.
func GlobalMCPFilePath() string {
	root := GlobalRoot()
	if root == "" {
		return ""
	}
	return firstExisting(
		filepath.Join(root, "mcp.jsonc"),
		filepath.Join(root, "mcp.json"),
	)
}

// ProjectMCPFilePath prefers mcp.jsonc then mcp.json under <workDir>/.strike.
func ProjectMCPFilePath(workDir string) string {
	if workDir == "" {
		return ""
	}
	root := projectRoot(workDir)
	return firstExisting(
		filepath.Join(root, "mcp.jsonc"),
		filepath.Join(root, "mcp.json"),
	)
}

// mcpFileCandidates returns paths to try for a .strike root (jsonc then json).
func mcpFileCandidates(dir string) []string {
	if dir == "" {
		return nil
	}
	return []string{
		filepath.Join(dir, "mcp.jsonc"),
		filepath.Join(dir, "mcp.json"),
	}
}

// loadMCPFileLayer loads the first existing mcp.jsonc/json in dir.
// Missing dir/files yield a zero MCPConfig (nil Servers) and nil error.
func loadMCPFileLayer(dir string) (MCPConfig, error) {
	for _, path := range mcpFileCandidates(dir) {
		mc, err := ReadMCPFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return MCPConfig{}, fmt.Errorf("%s: %w", path, err)
		}
		return mc, nil
	}
	return MCPConfig{}, nil
}

// ReadMCPFile parses mcp.jsonc/json from path.
func ReadMCPFile(path string) (MCPConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return MCPConfig{}, err
	}
	return ParseMCPFile(data)
}

// ParseMCPFile decodes mcp.jsonc/json bytes. Accepted shapes:
//
//	{"servers": {"name": {…}}}   // same as config "mcp" object
//	{"name": {…}}                // bare server map
//
// Empty input is a no-op (nil Servers). Empty object {} is an explicit empty
// server map (clears lower layers via mergeMCP replace semantics).
func ParseMCPFile(data []byte) (MCPConfig, error) {
	stripped, err := stripJSONC(data)
	if err != nil {
		return MCPConfig{}, err
	}
	stripped = bytesTrimSpace(stripped)
	if len(stripped) == 0 {
		return MCPConfig{}, nil
	}
	if stripped[0] != '{' {
		return MCPConfig{}, fmt.Errorf("mcp file must be a JSON object")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(stripped, &raw); err != nil {
		return MCPConfig{}, err
	}
	if len(raw) == 0 {
		return MCPConfig{Servers: map[string]MCPServer{}}, nil
	}
	if srvRaw, ok := raw["servers"]; ok {
		var servers map[string]MCPServer
		if err := json.Unmarshal(srvRaw, &servers); err != nil {
			return MCPConfig{}, fmt.Errorf("servers: %w", err)
		}
		if servers == nil {
			servers = map[string]MCPServer{}
		}
		return MCPConfig{Servers: cloneMCPServers(servers)}, nil
	}
	servers := make(map[string]MCPServer, len(raw))
	for name, v := range raw {
		var s MCPServer
		if err := json.Unmarshal(v, &s); err != nil {
			return MCPConfig{}, fmt.Errorf("server %q: %w", name, err)
		}
		servers[name] = s
	}
	return MCPConfig{Servers: cloneMCPServers(servers)}, nil
}
