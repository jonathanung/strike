package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// Destructive shell builtins/commands whose path operands are checked against
// the workspace by checkBashWorkspaceBoundary. Best-effort static text guard
// only — not an OS sandbox; many command forms are not inspected.
var destructiveBashCmds = map[string]struct{}{
	"rm":     {},
	"rmdir":  {},
	"unlink": {},
	"shred":  {},
	"mv":     {},
	"chmod":  {},
	"chown":  {},
	"chgrp":  {},
}

// checkBashWorkspaceBoundary rejects known destructive filesystem ops that
// target paths outside workDir when those paths are statically parseable.
// Best-effort guard only: not a security boundary and not a full shell parser.
func checkBashWorkspaceBoundary(command, workDir string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	rootReal, err := workspaceRootReal(workDir)
	if err != nil {
		return err
	}
	cwd := rootReal
	for _, stmt := range splitBashStatements(command) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		words := shellWords(stmt)
		if len(words) == 0 {
			continue
		}
		// Strip leading ENV=val assignments.
		i := 0
		for i < len(words) && isEnvAssign(words[i]) {
			i++
		}
		if i >= len(words) {
			continue
		}
		cmd := commandBase(words[i])
		args := words[i+1:]

		switch cmd {
		case "cd":
			target := homeDir()
			if len(args) > 0 {
				target = expandTilde(args[0])
			}
			next, err := absFrom(cwd, target)
			if err != nil {
				return err
			}
			// cd itself is not destructive; track logical cwd for later path ops.
			// If the directory exists, prefer the real path for subsequent joins.
			if real, e := filepath.EvalSymlinks(next); e == nil {
				cwd = real
			} else {
				cwd = next
			}
		default:
			if _, ok := destructiveBashCmds[cmd]; !ok {
				continue
			}
			paths := destructivePathArgs(cmd, args)
			if len(paths) == 0 {
				// e.g. `rm -rf` with no operands — shell will fail; no escape.
				continue
			}
			for _, p := range paths {
				if err := assertDestructivePathInWorkspace(rootReal, cwd, p); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func assertDestructivePathInWorkspace(rootReal, cwd, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Reject unexpanded shell variables / substitutions in destructive path
	// position — we cannot prove they stay inside the workspace.
	if strings.ContainsAny(raw, "$`") || strings.Contains(raw, "$(") {
		return fmt.Errorf("destructive command path %q is not statically bound to the workspace (variables/substitutions are not allowed)", raw)
	}
	expanded := expandTilde(raw)
	abs, err := absFrom(cwd, expanded)
	if err != nil {
		return err
	}
	if isDangerousRemovalPath(abs) {
		return fmt.Errorf("destructive command refused: %q is a critical system path", abs)
	}
	if _, _, err := resolveInWorkspace(rootReal, abs); err != nil {
		if esc, ok := err.(*WorkspaceEscapeError); ok {
			return fmt.Errorf("destructive command path %q escapes workspace root %q", esc.Path, esc.Root)
		}
		return fmt.Errorf("destructive command path %q escapes workspace root %q", abs, rootReal)
	}
	return nil
}

// destructivePathArgs extracts path operands for known destructive commands.
func destructivePathArgs(cmd string, args []string) []string {
	positionals := positionalArgs(args)
	switch cmd {
	case "chmod", "chown", "chgrp":
		// First positional is mode/owner; remaining are paths.
		if len(positionals) <= 1 {
			return nil
		}
		return positionals[1:]
	default:
		// rm, rmdir, unlink, shred, mv: all positionals are paths.
		return positionals
	}
}

// positionalArgs returns non-flag arguments, honoring `--` end-of-options.
func positionalArgs(args []string) []string {
	var out []string
	afterDash := false
	for _, a := range args {
		if afterDash {
			out = append(out, a)
			continue
		}
		if a == "--" {
			afterDash = true
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			// skip -f, -rf, --force, --reference=FILE (path after = is a flag form)
			continue
		}
		out = append(out, a)
	}
	return out
}

func commandBase(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	// path/to/rm → rm
	base := filepath.Base(cmd)
	return strings.ToLower(base)
}

func isEnvAssign(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	i := strings.IndexByte(s, '=')
	if i <= 0 {
		return false
	}
	for _, r := range s[:i] {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return false
		}
	}
	return true
}

func absFrom(cwd, p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("path is empty")
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	if cwd == "" {
		return filepath.Clean(p), nil
	}
	return filepath.Join(cwd, p), nil
}

func expandTilde(p string) string {
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") || (filepath.Separator == '\\' && strings.HasPrefix(p, `~\`)) {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	return "/"
}

// isDangerousRemovalPath blocks catastrophic targets (/, /tmp, $HOME, …)
// regardless of workspace membership.
func isDangerousRemovalPath(abs string) bool {
	clean := filepath.Clean(abs)
	if clean == "" {
		return true
	}
	// Normalize to forward slashes for stable checks.
	n := filepath.ToSlash(clean)
	if n == "*" || strings.HasSuffix(n, "/*") {
		return true
	}
	if n == "/" {
		return true
	}
	// Direct children of root: /usr, /tmp, /home, /etc, …
	if dir := filepath.ToSlash(filepath.Dir(clean)); dir == "/" && n != "/" {
		return true
	}
	home := filepath.ToSlash(filepath.Clean(homeDir()))
	if home != "" && home != "." && n == home {
		return true
	}
	return false
}

// splitBashStatements splits on top-level ; && || | and newlines (not inside
// quotes or simple $() / “ / {}). Not a full shell parser — fail-closed path
// checks still run on each extracted statement's words.
func splitBashStatements(command string) []string {
	var parts []string
	var b strings.Builder
	flush := func() {
		s := strings.TrimSpace(b.String())
		b.Reset()
		if s != "" {
			parts = append(parts, s)
		}
	}
	inSingle, inDouble, escaped := false, false, false
	depthParen, depthBrace := 0, 0
	for i := 0; i < len(command); i++ {
		c := command[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if inSingle {
			b.WriteByte(c)
			if c == '\'' {
				inSingle = false
			}
			continue
		}
		if inDouble {
			if c == '\\' {
				b.WriteByte(c)
				escaped = true
				continue
			}
			b.WriteByte(c)
			if c == '"' {
				inDouble = false
			}
			continue
		}
		switch c {
		case '\\':
			b.WriteByte(c)
			escaped = true
		case '\'':
			b.WriteByte(c)
			inSingle = true
		case '"':
			b.WriteByte(c)
			inDouble = true
		case '(':
			b.WriteByte(c)
			depthParen++
		case ')':
			b.WriteByte(c)
			if depthParen > 0 {
				depthParen--
			}
		case '{':
			b.WriteByte(c)
			depthBrace++
		case '}':
			b.WriteByte(c)
			if depthBrace > 0 {
				depthBrace--
			}
		case '\n', ';':
			if depthParen == 0 && depthBrace == 0 {
				flush()
			} else {
				b.WriteByte(c)
			}
		case '&':
			if depthParen == 0 && depthBrace == 0 && i+1 < len(command) && command[i+1] == '&' {
				flush()
				i++
				continue
			}
			b.WriteByte(c)
		case '|':
			if depthParen == 0 && depthBrace == 0 {
				// Treat | and || as statement separators for path scanning.
				if i+1 < len(command) && command[i+1] == '|' {
					i++
				}
				flush()
				continue
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return parts
}

// shellWords splits a statement into words with basic single/double quote
// support. Does not expand variables.
func shellWords(s string) []string {
	var words []string
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		words = append(words, b.String())
		b.Reset()
	}
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
				continue
			}
			b.WriteByte(c)
			continue
		}
		if inDouble {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inDouble = false
				continue
			}
			b.WriteByte(c)
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case ' ', '\t', '\n', '\r':
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return words
}
