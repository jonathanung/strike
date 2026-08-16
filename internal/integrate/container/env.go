package container

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CollectForwardedEnv returns host env entries whose keys match any pattern
// (filepath.Match). Patterns that match nothing produce a warning string.
func CollectForwardedEnv(patterns []string) (envVars []string, warnings []string) {
	if len(patterns) == 0 {
		return nil, nil
	}
	hostEnv := make(map[string]string)
	for _, entry := range os.Environ() {
		idx := strings.Index(entry, "=")
		if idx < 0 {
			continue
		}
		hostEnv[entry[:idx]] = entry
	}
	seen := make(map[string]bool)
	matched := make(map[string]bool)
	var keys []string
	for _, pattern := range patterns {
		for key, entry := range hostEnv {
			ok, err := filepath.Match(pattern, key)
			if err != nil || !ok {
				continue
			}
			matched[pattern] = true
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
				envVars = append(envVars, entry)
			}
		}
	}
	// Stable order for tests.
	sort.Slice(envVars, func(i, j int) bool {
		return envVars[i] < envVars[j]
	})
	_ = keys
	for _, pattern := range patterns {
		if !matched[pattern] {
			warnings = append(warnings, fmt.Sprintf(
				"forward_env pattern %q did not match any host environment variables",
				pattern,
			))
		}
	}
	return envVars, warnings
}

// ParseEnvFile reads a Docker-compatible KEY=VALUE file.
func ParseEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read env file %s: %w", path, err)
	}
	result := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		result[line[:idx]] = line[idx+1:]
	}
	return result, scanner.Err()
}

// ValidateRequiredEnv ensures each name is present on the host or in envFile.
func ValidateRequiredEnv(required []string, envFilePath, repoDir string) error {
	if len(required) == 0 {
		return nil
	}
	available := make(map[string]bool)
	for _, entry := range os.Environ() {
		if idx := strings.Index(entry, "="); idx >= 0 {
			available[entry[:idx]] = true
		}
	}
	if envFilePath != "" {
		resolved := envFilePath
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(repoDir, envFilePath)
		}
		fileVars, err := ParseEnvFile(resolved)
		if err != nil {
			return fmt.Errorf("parse env file: %w", err)
		}
		for k := range fileVars {
			available[k] = true
		}
	}
	for _, v := range required {
		if !available[v] {
			return fmt.Errorf("required environment variable %s is not set (needed for container launch)", v)
		}
	}
	return nil
}

// resolveProxyEnv returns proxy KEY=VALUE entries for the container.
func resolveProxyEnv(n Network) []string {
	httpP, httpsP, noP := n.HTTPProxy, n.HTTPSProxy, n.NoProxy
	if httpP == "" {
		httpP = firstNonEmpty(os.Getenv("HTTP_PROXY"), os.Getenv("http_proxy"))
	}
	if httpsP == "" {
		httpsP = firstNonEmpty(os.Getenv("HTTPS_PROXY"), os.Getenv("https_proxy"))
	}
	if noP == "" {
		noP = firstNonEmpty(os.Getenv("NO_PROXY"), os.Getenv("no_proxy"))
	}
	var out []string
	if httpP != "" {
		out = append(out, "HTTP_PROXY="+httpP, "http_proxy="+httpP)
	}
	if httpsP != "" {
		out = append(out, "HTTPS_PROXY="+httpsP, "https_proxy="+httpsP)
	}
	if noP != "" {
		out = append(out, "NO_PROXY="+noP, "no_proxy="+noP)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
