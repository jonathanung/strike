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
	"rm":       {},
	"rmdir":    {},
	"unlink":   {},
	"shred":    {},
	"mv":       {},
	"chmod":    {},
	"chown":    {},
	"chgrp":    {},
	"cp":       {},
	"dd":       {},
	"truncate": {},
	"tee":      {},
	"install":  {},
	"ln":       {},
}

// checkBashMaxDepth caps recursion through wrappers and command substitutions.
const checkBashMaxDepth = 8

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
	return checkBashCommand(command, rootReal, &cwd, 0)
}

func checkBashCommand(command string, rootReal string, cwd *string, depth int) error {
	if depth > checkBashMaxDepth {
		return fmt.Errorf("destructive command guard: nesting too deep to verify workspace binding")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	// Recurse into $(...) and backticks so outer command name cannot hide work.
	// Subs run in a subshell — do not let their cd mutate the parent cwd.
	for _, sub := range extractCommandSubstitutions(command) {
		subCwd := *cwd
		if err := checkBashCommand(sub, rootReal, &subCwd, depth+1); err != nil {
			return err
		}
	}
	for _, stmt := range splitBashStatements(command) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		if err := checkBashStatement(stmt, rootReal, cwd, depth); err != nil {
			return err
		}
	}
	return nil
}

func checkBashStatement(stmt string, rootReal string, cwd *string, depth int) error {
	redirPaths, rest := peelRedirections(stmt)
	for _, p := range redirPaths {
		if err := assertDestructivePathInWorkspace(rootReal, *cwd, p); err != nil {
			return err
		}
	}
	words := shellWords(rest)
	if len(words) == 0 {
		return nil
	}
	// Strip leading ENV=val assignments.
	i := 0
	for i < len(words) && isEnvAssign(words[i]) {
		i++
	}
	if i >= len(words) {
		return nil
	}
	return checkBashWords(words[i:], rootReal, cwd, depth)
}

func checkBashWords(words []string, rootReal string, cwd *string, depth int) error {
	if depth > checkBashMaxDepth {
		return fmt.Errorf("destructive command guard: nesting too deep to verify workspace binding")
	}
	if len(words) == 0 {
		return nil
	}
	cmd := commandBase(words[0])
	args := words[1:]

	switch cmd {
	case "cd":
		target := homeDir()
		if len(args) > 0 {
			target = expandTilde(args[0])
		}
		next, err := absFrom(*cwd, target)
		if err != nil {
			return err
		}
		// cd itself is not destructive; track logical cwd for later path ops.
		// If the directory exists, prefer the real path for subsequent joins.
		if real, e := filepath.EvalSymlinks(next); e == nil {
			*cwd = real
		} else {
			*cwd = next
		}
		return nil

	case "sh", "bash", "zsh", "dash":
		return checkShellCWrapper(args, rootReal, cwd, depth+1)

	case "env":
		return checkEnvWrapper(args, rootReal, cwd, depth+1)

	case "eval":
		return checkBashCommand(strings.Join(args, " "), rootReal, cwd, depth+1)

	case "nohup", "nice", "time":
		return checkBashWords(stripLeadingWrapperFlags(cmd, args), rootReal, cwd, depth+1)

	case "timeout":
		return checkTimeoutWrapper(args, rootReal, cwd, depth+1)

	case "xargs":
		return checkXargsWrapper(args, rootReal, cwd, depth+1)

	case "find":
		return checkFindCommand(args, rootReal, cwd, depth+1)

	case "sed":
		if sedInPlace(args) {
			return checkDestructivePaths(sedPathArgs(args), rootReal, *cwd)
		}
		return nil

	case "perl":
		if perlInPlace(args) {
			return checkDestructivePaths(perlInPlacePathArgs(args), rootReal, *cwd)
		}
		if interpreterOneLiner("perl", args) {
			return unboundedInterpreterError("perl")
		}
		return nil

	case "rsync":
		if rsyncDelete(args) {
			return checkDestructivePaths(positionalArgs(args), rootReal, *cwd)
		}
		return nil

	case "dd":
		return checkDestructivePaths(ddOutputPaths(args), rootReal, *cwd)

	case "python", "python2", "python3", "node", "nodejs", "ruby", "php":
		if interpreterOneLiner(cmd, args) {
			return unboundedInterpreterError(cmd)
		}
		return nil
	}

	if _, ok := destructiveBashCmds[cmd]; !ok {
		return nil
	}
	return checkDestructivePaths(destructivePathArgs(cmd, args), rootReal, *cwd)
}

func checkDestructivePaths(paths []string, rootReal, cwd string) error {
	if len(paths) == 0 {
		// e.g. `rm -rf` with no operands — shell will fail; no escape.
		return nil
	}
	for _, p := range paths {
		if err := assertDestructivePathInWorkspace(rootReal, cwd, p); err != nil {
			return err
		}
	}
	return nil
}

func unboundedInterpreterError(cmd string) error {
	return fmt.Errorf("interpreter one-liner %q is not statically bound to the workspace (variables/substitutions/one-liners are not allowed)", cmd)
}

// checkShellCWrapper handles sh/bash/zsh -c 'script' [args].
// depth is already incremented by the caller for this nesting level.
func checkShellCWrapper(args []string, rootReal string, cwd *string, depth int) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "--command":
			if i+1 >= len(args) {
				return nil
			}
			return checkBashCommand(args[i+1], rootReal, cwd, depth)
		case strings.HasPrefix(a, "-c") && a != "-c" && !strings.HasPrefix(a, "--"):
			// bash -c'script' combined form (rare)
			return checkBashCommand(a[2:], rootReal, cwd, depth)
		case a == "--":
			continue
		case strings.HasPrefix(a, "-"):
			// login/interactive flags, -o, etc.
			if a == "-o" || a == "-O" {
				i++
			}
			continue
		default:
			// Script path form (bash script.sh) — not scanned as shell text.
			return nil
		}
	}
	return nil
}

func checkEnvWrapper(args []string, rootReal string, cwd *string, depth int) error {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if a == "-u" || a == "--unset" || a == "-C" || a == "--chdir" {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-u") && a != "-u" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			// -i, -0, --ignore-environment, …
			i++
			continue
		}
		if isEnvAssign(a) {
			i++
			continue
		}
		return checkBashWords(args[i:], rootReal, cwd, depth)
	}
	if i < len(args) {
		return checkBashWords(args[i:], rootReal, cwd, depth)
	}
	return nil
}

func checkTimeoutWrapper(args []string, rootReal string, cwd *string, depth int) error {
	// timeout [options] DURATION COMMAND...
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			// Options with args: -k/--kill-after, -s/--signal
			name := a
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				i++
				continue
			}
			switch name {
			case "-k", "--kill-after", "-s", "--signal":
				i += 2
			default:
				i++
			}
			continue
		}
		// Duration token then command.
		i++
		if i >= len(args) {
			return nil
		}
		return checkBashWords(args[i:], rootReal, cwd, depth)
	}
	if i < len(args) {
		return checkBashWords(args[i:], rootReal, cwd, depth)
	}
	return nil
}

func checkXargsWrapper(args []string, rootReal string, cwd *string, depth int) error {
	// xargs [options] [command [initial-arguments]]
	// Default command is echo — non-destructive. If a command is present, scan it.
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				i++
				continue
			}
			// Common xargs options that take a separate argument.
			switch a {
			case "-n", "-P", "-s", "-E", "-e", "-I", "-i", "-L", "-l",
				"--max-args", "--max-procs", "--max-chars", "--eof",
				"--replace", "--max-lines", "-a", "--arg-file",
				"-d", "--delimiter":
				i += 2
			default:
				// Combined short form like -n1 or unknown flag.
				if len(a) > 2 && a[1] != '-' && strings.ContainsAny(a[2:], "0123456789") {
					i++
					continue
				}
				i++
			}
			continue
		}
		break
	}
	if i >= len(args) {
		return nil
	}
	return checkBashWords(args[i:], rootReal, cwd, depth)
}

func stripLeadingWrapperFlags(cmd string, args []string) []string {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			return args[i+1:]
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		// nice -n N, nice --adjustment=N
		if cmd == "nice" && (a == "-n" || a == "--adjustment") {
			i += 2
			continue
		}
		if cmd == "nice" && strings.HasPrefix(a, "--adjustment=") {
			i++
			continue
		}
		// nice -N (numeric adjustment)
		if cmd == "nice" && len(a) > 1 && isAllDigits(a[1:]) {
			i++
			continue
		}
		i++
	}
	return args[i:]
}

func checkFindCommand(args []string, rootReal string, cwd *string, depth int) error {
	// find [paths...] [expression]
	var paths []string
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			i++
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			break
		}
		paths = append(paths, a)
		i++
	}
	destructive := false
	for i < len(args) {
		a := args[i]
		switch a {
		case "-delete":
			destructive = true
			i++
		case "-exec", "-execdir", "-ok", "-okdir":
			i++
			start := i
			for i < len(args) && args[i] != ";" && args[i] != "+" {
				i++
			}
			execWords := make([]string, 0, i-start)
			for _, w := range args[start:i] {
				if w == "{}" || strings.HasPrefix(w, "{}") {
					continue
				}
				execWords = append(execWords, w)
			}
			if len(execWords) > 0 {
				if err := checkBashWords(execWords, rootReal, cwd, depth); err != nil {
					return err
				}
				if wordsLookDestructive(execWords) {
					destructive = true
				}
			}
			if i < len(args) {
				i++ // consume ; or +
			}
		default:
			i++
		}
	}
	if !destructive {
		return nil
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}
	return checkDestructivePaths(paths, rootReal, *cwd)
}

func wordsLookDestructive(words []string) bool {
	if len(words) == 0 {
		return false
	}
	cmd := commandBase(words[0])
	if _, ok := destructiveBashCmds[cmd]; ok {
		return true
	}
	switch cmd {
	case "sed":
		return sedInPlace(words[1:])
	case "perl":
		return perlInPlace(words[1:])
	case "rsync":
		return rsyncDelete(words[1:])
	case "find":
		// nested find — treat as needing path check if we got here via -exec
		return true
	}
	return false
}

func sedInPlace(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-i" || a == "--in-place" || strings.HasPrefix(a, "--in-place=") {
			return true
		}
		// GNU sed -iSUFFIX / combined -i.bak
		if strings.HasPrefix(a, "-i") && a != "-i" && !strings.HasPrefix(a, "--") {
			return true
		}
	}
	return false
}

// sedPathArgs returns file operands for in-place sed (best-effort).
func sedPathArgs(args []string) []string {
	hasExpr := false
	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if a == "-e" || a == "--expression" || a == "-f" || a == "--file" {
			hasExpr = true
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "--expression=") || strings.HasPrefix(a, "--file=") {
			hasExpr = true
			continue
		}
		if a == "-i" || a == "--in-place" {
			// BSD sed -i '' takes empty extension as next arg.
			if a == "-i" && i+1 < len(args) && args[i+1] == "" {
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "-i") && a != "-i" && !strings.HasPrefix(a, "--") {
			continue
		}
		if strings.HasPrefix(a, "--in-place=") {
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			// -n, -r, -E, -z, combined -eSCRIPT, etc.
			if strings.HasPrefix(a, "-e") && a != "-e" {
				hasExpr = true
			}
			if strings.HasPrefix(a, "-f") && a != "-f" {
				hasExpr = true
			}
			continue
		}
		positionals = append(positionals, a)
	}
	if hasExpr {
		return positionals
	}
	// First positional is the script; remainder are files.
	if len(positionals) <= 1 {
		return nil
	}
	return positionals[1:]
}

func perlInPlace(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		// -i / -i.bak
		if a == "-i" || (strings.HasPrefix(a, "-i") && !strings.HasPrefix(a, "--")) {
			return true
		}
		// Clustered short switches containing i: -pi, -nli, -pie, …
		if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) >= 3 {
			body := a[1:]
			// Stop at extension suffix after i (e.g. -pi.bak → body pi.bak).
			for _, r := range body {
				if r == 'i' {
					return true
				}
				if r == '.' {
					break
				}
				if !unicode.IsLetter(r) {
					break
				}
			}
		}
	}
	return false
}

func perlInPlacePathArgs(args []string) []string {
	// After -e script and flags, remaining positionals are files.
	hasE := false
	var positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if a == "-e" || a == "-E" {
			hasE = true
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if strings.HasPrefix(a, "-e") && a != "-e" {
			hasE = true
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			continue
		}
		positionals = append(positionals, a)
	}
	if hasE {
		return positionals
	}
	if len(positionals) <= 1 {
		return nil
	}
	return positionals[1:]
}

func rsyncDelete(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--delete" || a == "--delete-before" || a == "--delete-during" ||
			a == "--delete-after" || a == "--delete-delay" || a == "--delete-excluded" {
			return true
		}
		if strings.HasPrefix(a, "--delete-") {
			return true
		}
	}
	return false
}

func ddOutputPaths(args []string) []string {
	var out []string
	for _, a := range args {
		if strings.HasPrefix(a, "of=") {
			out = append(out, a[3:])
		}
	}
	return out
}

func interpreterOneLiner(cmd string, args []string) bool {
	switch commandBase(cmd) {
	case "python", "python2", "python3":
		return hasOpt(args, "-c")
	case "node", "nodejs":
		return hasOpt(args, "-e") || hasOpt(args, "-p") || hasOpt(args, "--eval") || hasOpt(args, "--print")
	case "ruby":
		return hasOpt(args, "-e")
	case "perl":
		return hasOpt(args, "-e") || hasOpt(args, "-E")
	case "php":
		return hasOpt(args, "-r")
	default:
		return false
	}
}

func hasOpt(args []string, opt string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return false
		}
		if a == opt {
			return true
		}
		// Combined -eCODE / -cCODE
		if !strings.HasPrefix(opt, "--") && strings.HasPrefix(a, opt) && a != opt && !strings.HasPrefix(a, "--") {
			// -eCODE or -cCODE (single-letter opt)
			if len(opt) == 2 && strings.HasPrefix(a, opt) {
				return true
			}
		}
		if strings.HasPrefix(a, opt+"=") {
			return true
		}
	}
	return false
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
	switch cmd {
	case "chmod", "chown", "chgrp":
		// First positional is mode/owner; remaining are paths.
		positionals := positionalArgs(args)
		if len(positionals) <= 1 {
			return nil
		}
		return positionals[1:]
	case "dd":
		return ddOutputPaths(args)
	default:
		// rm, rmdir, unlink, shred, mv, cp, tee, truncate, install, ln
		return positionalArgs(args)
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

// peelRedirections extracts output-redirection targets (>, >>, 2>, &>, …)
// and returns them plus the statement with those redirections removed so
// command words parse cleanly. Fd-to-fd dups like 2>&1 are ignored.
func peelRedirections(stmt string) (paths []string, rest string) {
	var b strings.Builder
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(stmt); i++ {
		c := stmt[i]
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
		if c == '\\' {
			b.WriteByte(c)
			escaped = true
			continue
		}
		if c == '\'' {
			b.WriteByte(c)
			inSingle = true
			continue
		}
		if c == '"' {
			b.WriteByte(c)
			inDouble = true
			continue
		}

		// Optional leading fd digits before > / >> (e.g. 2>/tmp/x).
		// Only at a word boundary so "file2>out" is word "file2" + ">out",
		// not fd 2 on a truncated "file".
		j := i
		atWordStart := j == 0 || stmt[j-1] == ' ' || stmt[j-1] == '\t' || stmt[j-1] == '\n' || stmt[j-1] == '\r'
		if atWordStart {
			for j < len(stmt) && stmt[j] >= '0' && stmt[j] <= '9' {
				j++
			}
		}
		if j < len(stmt) && stmt[j] == '>' {
			ps, _, newI, ok := consumeRedirTarget(stmt, j)
			if ok {
				for _, p := range ps {
					if p != "" {
						paths = append(paths, p)
					}
				}
				i = newI - 1
				continue
			}
		}
		// &> and &>> (no fd prefix)
		if c == '&' && i+1 < len(stmt) && stmt[i+1] == '>' {
			ps, _, newI, ok := consumeRedirTarget(stmt, i+1)
			if ok {
				for _, p := range ps {
					if p != "" {
						paths = append(paths, p)
					}
				}
				i = newI - 1
				continue
			}
		}
		b.WriteByte(c)
	}
	return paths, b.String()
}

// consumeRedirTarget starts at '>' and returns path targets (0 or 1), unused
// rest fragment, index after the redirection, and success.
func consumeRedirTarget(stmt string, gt int) (paths []string, _ string, next int, ok bool) {
	if gt >= len(stmt) || stmt[gt] != '>' {
		return nil, "", gt, false
	}
	i := gt + 1
	if i < len(stmt) && stmt[i] == '>' {
		i++ // >>
	}
	// Fd dup: >&1 or >&-
	if i < len(stmt) && stmt[i] == '&' {
		i++
		if i < len(stmt) && (stmt[i] == '-' || (stmt[i] >= '0' && stmt[i] <= '9')) {
			for i < len(stmt) && stmt[i] >= '0' && stmt[i] <= '9' {
				i++
			}
			if i > gt+2 && (stmt[i-1] == '-' || (stmt[i-1] >= '0' && stmt[i-1] <= '9')) {
				return nil, "", i, true // fd dup — no path
			}
		}
		// >&file (bash)
		for i < len(stmt) && (stmt[i] == ' ' || stmt[i] == '\t') {
			i++
		}
		path, end := readShellWord(stmt, i)
		return []string{path}, "", end, true
	}
	for i < len(stmt) && (stmt[i] == ' ' || stmt[i] == '\t') {
		i++
	}
	if i >= len(stmt) {
		return nil, "", i, true
	}
	path, end := readShellWord(stmt, i)
	return []string{path}, "", end, true
}

// readShellWord reads one shell word starting at i (respecting quotes).
func readShellWord(s string, i int) (word string, next int) {
	var b strings.Builder
	inSingle, inDouble, escaped := false, false, false
	for i < len(s) {
		c := s[i]
		if escaped {
			b.WriteByte(c)
			escaped = false
			i++
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
				i++
				continue
			}
			b.WriteByte(c)
			i++
			continue
		}
		if inDouble {
			if c == '\\' {
				escaped = true
				i++
				continue
			}
			if c == '"' {
				inDouble = false
				i++
				continue
			}
			b.WriteByte(c)
			i++
			continue
		}
		switch c {
		case '\\':
			escaped = true
			i++
		case '\'':
			inSingle = true
			i++
		case '"':
			inDouble = true
			i++
		case ' ', '\t', '\n', '\r':
			return b.String(), i
		case '|', '&', ';', '<', '>', '(', ')':
			return b.String(), i
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), i
}

// extractCommandSubstitutions returns inner scripts of $(...) and `...`
// at the top level of command (quote-aware, nested-paren aware for $()).
func extractCommandSubstitutions(command string) []string {
	var out []string
	inSingle, inDouble, escaped := false, false, false
	for i := 0; i < len(command); i++ {
		c := command[i]
		if escaped {
			escaped = false
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
			}
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
			// Command subs expand inside double quotes.
			if c == '$' && i+1 < len(command) && command[i+1] == '(' {
				inner, end, ok := readDollarParen(command, i+2)
				if ok {
					out = append(out, inner)
					i = end
				}
				continue
			}
			if c == '`' {
				inner, end, ok := readBacktick(command, i+1)
				if ok {
					out = append(out, inner)
					i = end
				}
				continue
			}
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '`':
			inner, end, ok := readBacktick(command, i+1)
			if ok {
				out = append(out, inner)
				i = end
			}
		case '$':
			if i+1 < len(command) && command[i+1] == '(' {
				inner, end, ok := readDollarParen(command, i+2)
				if ok {
					out = append(out, inner)
					i = end
				}
			}
		}
	}
	return out
}

func readDollarParen(s string, start int) (inner string, endIdx int, ok bool) {
	depth := 1
	inSingle, inDouble, escaped := false, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inSingle {
			if c == '\'' {
				inSingle = false
			}
			continue
		}
		if inDouble {
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inDouble = false
			}
			continue
		}
		switch c {
		case '\\':
			escaped = true
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[start:i], i, true
			}
		}
	}
	return "", start, false
}

func readBacktick(s string, start int) (inner string, endIdx int, ok bool) {
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '`' {
			return s[start:i], i, true
		}
	}
	return "", start, false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
