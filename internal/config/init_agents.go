package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrAgentsExists is returned by WriteAgentsMD when AGENTS.md already exists
// and force is false.
var ErrAgentsExists = errors.New("AGENTS.md already exists")

// AgentsMDName is the project instruction file written by /init.
const AgentsMDName = "AGENTS.md"

// AgentsMDPath returns the absolute path where project AGENTS.md would live.
func AgentsMDPath(workDir string) string {
	return filepath.Join(filepath.Clean(workDir), AgentsMDName)
}

// AgentsMDExists reports whether a non-empty AGENTS.md is present under workDir.
func AgentsMDExists(workDir string) (bool, error) {
	if strings.TrimSpace(workDir) == "" {
		return false, fmt.Errorf("work directory is empty")
	}
	info, err := os.Stat(AgentsMDPath(workDir))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", AgentsMDName)
	}
	return info.Size() > 0, nil
}

// GenerateAgentsMD builds a sensible AGENTS.md body from a light, local-only
// scan of workDir. It never reads secret-shaped files and skips common
// ignored/noise paths.
func GenerateAgentsMD(workDir string) (string, error) {
	workDir = filepath.Clean(strings.TrimSpace(workDir))
	if workDir == "" || workDir == "." {
		abs, err := os.Getwd()
		if err != nil {
			return "", err
		}
		workDir = abs
	}
	info, err := os.Stat(workDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("work directory is not a directory: %s", workDir)
	}

	name := filepath.Base(workDir)
	if name == "" || name == string(filepath.Separator) || name == "." {
		name = "project"
	}

	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(name)
	b.WriteString("\n\n")

	if summary := readmeSummary(workDir); summary != "" {
		b.WriteString(summary)
		b.WriteString("\n\n")
	} else {
		b.WriteString("Project instructions for coding agents.\n\n")
	}

	if stack := detectStack(workDir); len(stack) > 0 {
		b.WriteString("## Stack\n\n")
		for _, line := range stack {
			b.WriteString("- ")
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if layout := listLayout(workDir); len(layout) > 0 {
		b.WriteString("## Layout\n\n")
		for _, line := range layout {
			b.WriteString("- `")
			b.WriteString(line)
			b.WriteString("`\n")
		}
		b.WriteString("\n")
	}

	b.WriteString("## Verification\n\n")
	if cmds := detectVerifyCommands(workDir); len(cmds) > 0 {
		b.WriteString("```sh\n")
		for _, c := range cmds {
			b.WriteString(c)
			b.WriteString("\n")
		}
		b.WriteString("```\n\n")
	} else {
		b.WriteString("Run the project's usual test/lint/build commands before claiming done.\n\n")
	}

	b.WriteString("## Notes for agents\n\n")
	b.WriteString("- Prefer the smallest correct change; match surrounding style.\n")
	b.WriteString("- Do not commit secrets or write real credentials into fixtures.\n")
	b.WriteString("- Respect `.gitignore`; never dump `.env`, keys, or credentials into this file.\n")
	b.WriteString("- Update this file when project conventions change (`/init` can regenerate with confirm).\n")
	return b.String(), nil
}

// WriteAgentsMD generates and writes AGENTS.md under workDir. When force is
// false and a non-empty file already exists, it returns ErrAgentsExists.
// created is true when the file did not previously exist (or was empty).
func WriteAgentsMD(workDir string, force bool) (path string, created bool, err error) {
	workDir = filepath.Clean(strings.TrimSpace(workDir))
	if workDir == "" {
		return "", false, fmt.Errorf("work directory is empty")
	}
	path = AgentsMDPath(workDir)
	exists, err := AgentsMDExists(workDir)
	if err != nil {
		return "", false, err
	}
	if exists && !force {
		return path, false, ErrAgentsExists
	}
	body, err := GenerateAgentsMD(workDir)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", false, err
	}
	// Write via temp + rename so a failed write does not truncate an existing file.
	tmp, err := os.CreateTemp(workDir, ".AGENTS.md.*.tmp")
	if err != nil {
		return "", false, err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.WriteString(body); err != nil {
		_ = tmp.Close()
		return "", false, err
	}
	if err := tmp.Close(); err != nil {
		return "", false, err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return "", false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", false, err
	}
	cleanup = false
	return path, !exists, nil
}

func readmeSummary(workDir string) string {
	for _, name := range []string{"README.md", "README", "readme.md"} {
		data, err := os.ReadFile(filepath.Join(workDir, name))
		if err != nil || len(data) == 0 {
			continue
		}
		// Cap read body so huge READMEs cannot blow the template.
		text := string(data)
		if len(text) > 8<<10 {
			text = text[:8<<10]
		}
		text = scrubSecretSpans(text)
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "![") {
				continue
			}
			if strings.HasPrefix(line, "[!") || strings.HasPrefix(line, "[![") {
				continue
			}
			// Drop pure link/badge lines.
			if strings.HasPrefix(line, "[") && strings.Contains(line, "](") && !strings.Contains(line, " ") {
				continue
			}
			if len(line) > 240 {
				line = line[:237] + "..."
			}
			return line
		}
	}
	return ""
}

func detectStack(workDir string) []string {
	var out []string
	if data, err := os.ReadFile(filepath.Join(workDir, "go.mod")); err == nil {
		mod := parseGoModule(string(data))
		if mod != "" {
			out = append(out, "Go module `"+mod+"`")
		} else {
			out = append(out, "Go (`go.mod`)")
		}
	}
	if fileExists(workDir, "package.json") {
		out = append(out, "Node.js (`package.json`)")
	}
	if fileExists(workDir, "Cargo.toml") {
		out = append(out, "Rust (`Cargo.toml`)")
	}
	if fileExists(workDir, "pyproject.toml") || fileExists(workDir, "requirements.txt") || fileExists(workDir, "setup.py") {
		out = append(out, "Python")
	}
	if fileExists(workDir, "Gemfile") {
		out = append(out, "Ruby (`Gemfile`)")
	}
	if fileExists(workDir, "composer.json") {
		out = append(out, "PHP (`composer.json`)")
	}
	if fileExists(workDir, "build.gradle") || fileExists(workDir, "pom.xml") {
		out = append(out, "JVM (Gradle/Maven)")
	}
	if fileExists(workDir, "Makefile") || fileExists(workDir, "makefile") {
		out = append(out, "Make")
	}
	return out
}

func parseGoModule(data string) string {
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

func detectVerifyCommands(workDir string) []string {
	data, err := os.ReadFile(filepath.Join(workDir, "Makefile"))
	if err != nil {
		data, err = os.ReadFile(filepath.Join(workDir, "makefile"))
	}
	if err != nil {
		if fileExists(workDir, "package.json") {
			return []string{"npm test", "npm run lint"}
		}
		if fileExists(workDir, "go.mod") {
			return []string{"go test ./...", "go vet ./...", "go build ./..."}
		}
		return nil
	}
	targets := makefileTargets(string(data))
	prefer := []string{"test", "vet", "lint", "build", "check", "cover"}
	var cmds []string
	seen := map[string]bool{}
	for _, name := range prefer {
		if targets[name] && !seen[name] {
			cmds = append(cmds, "make "+name)
			seen[name] = true
		}
	}
	if len(cmds) == 0 {
		// Stable subset of discovered targets.
		names := make([]string, 0, len(targets))
		for n := range targets {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if len(cmds) >= 4 {
				break
			}
			if strings.HasPrefix(n, ".") || strings.Contains(n, "%") {
				continue
			}
			cmds = append(cmds, "make "+n)
		}
	}
	return cmds
}

func makefileTargets(data string) map[string]bool {
	out := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '\t' || line[0] == ' ' || line[0] == '#' {
			continue
		}
		if strings.HasPrefix(line, ".") && strings.Contains(line, ":") && !strings.HasPrefix(line, ".PHONY") {
			// skip special targets except we still parse .PHONY later if needed
		}
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		left := strings.TrimSpace(line[:idx])
		if left == "" || strings.ContainsAny(left, "=$") {
			continue
		}
		for _, part := range strings.Fields(left) {
			part = strings.TrimSpace(part)
			if part == "" || part == ".PHONY" {
				continue
			}
			out[part] = true
		}
	}
	return out
}

func listLayout(workDir string) []string {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return nil
	}
	ignore := layoutIgnoreSet(workDir)
	var dirs, files []string
	for _, e := range entries {
		name := e.Name()
		if shouldSkipLayoutName(name, ignore) {
			continue
		}
		// Never surface secret-shaped names even if not gitignored.
		if secretShapedName(name) {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, name+"/")
			continue
		}
		// Prefer notable top-level files.
		lower := strings.ToLower(name)
		switch {
		case lower == "makefile", lower == "dockerfile", strings.HasPrefix(lower, "readme"),
			strings.HasSuffix(lower, ".md"), lower == "go.mod", lower == "package.json",
			lower == "cargo.toml", lower == "pyproject.toml", lower == "license", lower == "license.md":
			files = append(files, name)
		}
	}
	sort.Strings(dirs)
	sort.Strings(files)
	out := append(dirs, files...)
	const maxEntries = 24
	if len(out) > maxEntries {
		out = append(out[:maxEntries], "…")
	}
	return out
}

func layoutIgnoreSet(workDir string) map[string]bool {
	// Built-in noise + secret paths; always skipped.
	set := map[string]bool{
		".git": true, ".hg": true, ".svn": true, ".jj": true,
		"node_modules": true, "vendor": true, "dist": true, "build": true,
		"target": true, "out": true, "bin": true, "coverage": true,
		"__pycache__": true, ".venv": true, "venv": true, ".tox": true,
		".idea": true, ".vscode": true, ".DS_Store": true,
		".strike": true, // may hold local state; optional hints stay separate
	}
	data, err := os.ReadFile(filepath.Join(workDir, ".gitignore"))
	if err != nil {
		return set
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Only simple top-level patterns (no globs mid-path).
		if strings.HasPrefix(line, "!") || strings.Contains(line, "/") && !strings.HasPrefix(line, "/") && !strings.HasSuffix(line, "/") {
			// keep simple: bare names and trailing-slash dirs
			if strings.Count(line, "/") > 1 {
				continue
			}
		}
		line = strings.TrimPrefix(line, "/")
		line = strings.TrimSuffix(line, "/")
		if line == "" || strings.ContainsAny(line, "*?[") {
			continue
		}
		if !strings.Contains(line, "/") {
			set[line] = true
		}
	}
	return set
}

func shouldSkipLayoutName(name string, ignore map[string]bool) bool {
	if name == "" || name == AgentsMDName {
		return true
	}
	if ignore[name] {
		return true
	}
	// Hidden dirs/files except a few project markers already filtered above.
	if strings.HasPrefix(name, ".") {
		return true
	}
	return false
}

func secretShapedName(name string) bool {
	lower := strings.ToLower(name)
	switch {
	case lower == ".env", strings.HasPrefix(lower, ".env."):
		return true
	case strings.Contains(lower, "secret"), strings.Contains(lower, "credential"):
		return true
	case strings.HasSuffix(lower, ".pem"), strings.HasSuffix(lower, ".key"):
		return true
	case lower == "id_rsa", lower == "id_ed25519", lower == "auth.json":
		return true
	case strings.HasSuffix(lower, ".p12"), strings.HasSuffix(lower, ".pfx"):
		return true
	}
	return false
}

func fileExists(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name))
	return err == nil && info.Mode().IsRegular()
}

// scrubSecretSpans redacts credential-shaped spans from free text used in the
// generated template (README summary). Conservative patterns only.
func scrubSecretSpans(s string) string {
	// Longer prefixes first so sk-ant- is not double-eaten by a shorter token.
	prefixes := []string{"sk-ant-", "sk-proj-", "sk-live-", "ghp_", "gho_", "ghs_", "xoxb-", "xoxp-"}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		for _, prefix := range prefixes {
			line = redactPrefixedTokens(line, prefix)
		}
		lower := strings.ToLower(line)
		for _, key := range []string{"api_key=", "api-key=", "password=", "secret=", "token="} {
			if j := strings.Index(lower, key); j >= 0 {
				start := j + len(key)
				end := start
				for end < len(line) && line[end] != ' ' && line[end] != '"' && line[end] != '\'' {
					end++
				}
				if end > start {
					line = line[:start] + "…" + line[end:]
					lower = strings.ToLower(line)
				}
			}
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func redactPrefixedTokens(line, prefix string) string {
	var b strings.Builder
	rest := line
	for {
		idx := strings.Index(rest, prefix)
		if idx < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:idx])
		b.WriteString(prefix)
		b.WriteString("…")
		end := idx + len(prefix)
		for end < len(rest) {
			c := rest[end]
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
				end++
				continue
			}
			break
		}
		rest = rest[end:]
	}
}
