package actionfacts

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// token kinds from the shell lexer.
type tokKind int

const (
	tokWord tokKind = iota
	tokPipe
	tokAndIf // &&
	tokOrIf  // ||
	tokSemi  // ;
	tokNewl
	tokRedirect // includes the operator; word follows separately
	tokBackground
	tokEOF
)

type token struct {
	kind    tokKind
	val     string // word text (unquoted concatenation) or redirect op
	static  bool   // word has no dynamic expansions
	qsingle bool   // entirely single-quoted (inert)
}

// parsePOSIX projects a POSIX-like shell command string into facts.
func parsePOSIX(b *builder, src string, depth int) {
	if depth > maxWrapperDepth {
		b.mark(StatusLimitExceeded, IssueDepthLimit)
		return
	}
	tokens, ok := lexPOSIX(b, src)
	if !ok {
		return
	}
	splitAndParse(b, tokens, depth)
}

func splitAndParse(b *builder, tokens []token, depth int) {
	// Split into pipelines on && || ; newline &
	var seg []token
	flush := func() {
		if len(seg) == 0 {
			return
		}
		parsePipeline(b, seg, depth)
		seg = nil
	}
	for _, t := range tokens {
		switch t.kind {
		case tokAndIf, tokOrIf, tokSemi, tokNewl, tokBackground:
			flush()
		case tokEOF:
			// skip
		default:
			seg = append(seg, t)
		}
	}
	flush()
}

func parsePipeline(b *builder, tokens []token, depth int) {
	pipeID := b.nextPipe()
	var cmdToks []token
	flush := func() {
		if len(cmdToks) == 0 {
			return
		}
		parseSimple(b, cmdToks, pipeID, depth)
		cmdToks = nil
	}
	for _, t := range tokens {
		if t.kind == tokPipe {
			flush()
			continue
		}
		cmdToks = append(cmdToks, t)
	}
	flush()
}

func parseSimple(b *builder, tokens []token, pipeID, depth int) {
	var argv []string
	complete := true
	var redirects []redirect

	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		switch t.kind {
		case tokRedirect:
			if i+1 >= len(tokens) || tokens[i+1].kind != tokWord {
				b.mark(StatusPartial, IssueInvalidSyntax)
				complete = false
				continue
			}
			i++
			w := tokens[i]
			if !w.static {
				b.mark(StatusPartial, IssueDynamicWord)
				complete = false
				continue
			}
			redirects = append(redirects, redirect{op: t.val, target: w.val})
		case tokWord:
			if !t.static {
				b.mark(StatusPartial, IssueDynamicWord)
				complete = false
				// Still record a placeholder so structure is visible but not authoritative.
				argv = append(argv, t.val)
				continue
			}
			argv = append(argv, t.val)
		default:
			b.mark(StatusPartial, IssueUnsupportedConstruct)
			complete = false
		}
	}

	if len(argv) == 0 {
		// Redirect-only or empty — not an executable command we can enforce on.
		if len(redirects) > 0 {
			b.mark(StatusPartial, IssueUnsupportedConstruct)
		}
		return
	}

	prog := programName(argv[0])
	id := b.nextID()
	effect := EffectExecute
	if !complete {
		effect = EffectUncertain
	}
	cf := CommandFact{
		ID:           id,
		PipelineID:   pipeID,
		Effect:       effect,
		Program:      prog,
		Argv:         append([]string(nil), argv...),
		ArgvComplete: complete,
		Operations:   []OperationKind{OpExecute},
	}

	// Nested shell: bash/sh -c '...'
	if complete && isShellProg(prog) {
		if body, ok := shellDashC(argv); ok {
			if !b.addCommand(cf) {
				return
			}
			// Classify outer as wrapper; recurse into body.
			parsePOSIX(b, body, depth+1)
			applyRedirects(b, id, redirects)
			return
		}
		// script file or unknown shell invocation → opaque
		b.mark(StatusPartial, IssueOpaqueArtifact)
		cf.Effect = EffectUncertain
		cf.ArgvComplete = false
	}

	// eval → never authoritative
	if prog == "eval" {
		b.mark(StatusPartial, IssueUnsupportedConstruct)
		cf.Effect = EffectUncertain
		cf.ArgvComplete = false
		complete = false
	}

	// base64 decode often used in bypass chains — record decode op; still
	// complete if argv is static, but piping to shell is a separate command.
	if prog == "base64" {
		cf.Operations = append(cf.Operations, OpDecode)
	}

	if !b.addCommand(cf) {
		return
	}
	if complete {
		classifyCommand(b, &b.commands[len(b.commands)-1])
	}
	applyRedirects(b, id, redirects)
}

type redirect struct {
	op     string
	target string
}

func applyRedirects(b *builder, cmdID int, redirects []redirect) {
	for _, r := range redirects {
		access := PathWrite
		switch r.op {
		case "<", "0<":
			access = PathRead
		case ">>", "1>>", "2>>":
			access = PathAppend
		case ">", "1>", "2>", ">&", ">|":
			access = PathWrite
		default:
			// e.g. << heredoc — unsupported for enforcement
			b.mark(StatusPartial, IssueUnsupportedConstruct)
			continue
		}
		if r.target == "" || strings.ContainsAny(r.target, "*?[") {
			// Globs in redirect targets are not statically one path.
			b.mark(StatusPartial, IssueDynamicWord)
			continue
		}
		b.addPath(PathFact{CommandID: cmdID, Access: access, Value: r.target})
	}
}

func isShellProg(p string) bool {
	switch p {
	case "bash", "sh", "dash", "zsh", "ksh", "mksh":
		return true
	}
	return false
}

// shellDashC returns the -c body when argv is a clear non-interactive form.
func shellDashC(argv []string) (string, bool) {
	// Conservative: only plain `sh -c body` / `bash -c body` with optional
	// leading end-of-options. Anything else (login, rcfile, script path) is opaque.
	if len(argv) < 3 {
		return "", false
	}
	i := 1
	for i < len(argv) {
		a := argv[i]
		if a == "--" {
			i++
			break
		}
		if a == "-c" {
			if i+1 >= len(argv) {
				return "", false
			}
			body := argv[i+1]
			if strings.TrimSpace(body) == "" {
				return "", false
			}
			// Disallow further shell option soup after -c body for authority.
			// `bash -c body name args` is OK (body is still index i+1).
			return body, true
		}
		// Allow a small set of harmless short flags without values.
		if strings.HasPrefix(a, "-") && a != "-" {
			// Reject anything that might load startup files or scripts.
			for _, ch := range a[1:] {
				switch ch {
				case 'e', 'u', 'x', 'v', 'f', 'n', 'b', 'C', 'h', 'p', 't':
					// common set flags — still OK before -c
				case 'c':
					// combined -ec etc.
					if i+1 >= len(argv) {
						return "", false
					}
					return argv[i+1], true
				case 'i', 'l', 's', 'r': // interactive/login/stdin/restricted
					return "", false
				default:
					return "", false
				}
			}
			i++
			continue
		}
		// Positional script path — opaque.
		return "", false
	}
	return "", false
}

func parseArgvCommand(b *builder, argv []string, depth int) {
	if depth > maxWrapperDepth {
		b.mark(StatusLimitExceeded, IssueDepthLimit)
		return
	}
	prog := programName(argv[0])
	id := b.nextID()
	cf := CommandFact{
		ID:           id,
		PipelineID:   b.nextPipe(),
		Effect:       EffectExecute,
		Program:      prog,
		Argv:         append([]string(nil), argv...),
		ArgvComplete: true,
		Operations:   []OperationKind{OpExecute},
	}
	if isShellProg(prog) {
		if body, ok := shellDashC(argv); ok {
			if !b.addCommand(cf) {
				return
			}
			parsePOSIX(b, body, depth+1)
			return
		}
		b.mark(StatusPartial, IssueOpaqueArtifact)
		cf.Effect = EffectUncertain
		cf.ArgvComplete = false
	}
	if prog == "eval" {
		b.mark(StatusPartial, IssueUnsupportedConstruct)
		cf.Effect = EffectUncertain
		cf.ArgvComplete = false
	}
	if !b.addCommand(cf) {
		return
	}
	if cf.ArgvComplete {
		classifyCommand(b, &b.commands[len(b.commands)-1])
	}
}

// lexPOSIX tokenizes src. On failure it marks the builder and returns ok=false.
func lexPOSIX(b *builder, src string) ([]token, bool) {
	var tokens []token
	i := 0
	emit := func(t token) bool {
		if len(tokens) >= maxTokens {
			b.mark(StatusLimitExceeded, IssueNodeLimit)
			return false
		}
		tokens = append(tokens, t)
		return true
	}
	for i < len(src) {
		// whitespace
		if src[i] == ' ' || src[i] == '\t' || src[i] == '\r' {
			i++
			continue
		}
		if src[i] == '\n' {
			if !emit(token{kind: tokNewl}) {
				return nil, false
			}
			i++
			continue
		}
		// comment
		if src[i] == '#' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		}
		// operators
		if i+1 < len(src) {
			two := src[i : i+2]
			switch two {
			case "&&":
				if !emit(token{kind: tokAndIf}) {
					return nil, false
				}
				i += 2
				continue
			case "||":
				if !emit(token{kind: tokOrIf}) {
					return nil, false
				}
				i += 2
				continue
			case ">>", ">&", ">|", "<<", "<>":
				if two == "<<" || two == "<>" {
					// heredoc / read-write — unsupported
					b.mark(StatusPartial, IssueUnsupportedConstruct)
				}
				if !emit(token{kind: tokRedirect, val: two}) {
					return nil, false
				}
				i += 2
				// skip optional clobber digits already handled; << may have -
				if two == "<<" && i < len(src) && src[i] == '-' {
					i++
				}
				continue
			case "2>", "1>", "0<", "2<":
				// handled below as digit+op if needed
			}
		}
		switch src[i] {
		case '|':
			if !emit(token{kind: tokPipe}) {
				return nil, false
			}
			i++
			continue
		case ';':
			if !emit(token{kind: tokSemi}) {
				return nil, false
			}
			i++
			continue
		case '&':
			if !emit(token{kind: tokBackground}) {
				return nil, false
			}
			i++
			continue
		case '>':
			if !emit(token{kind: tokRedirect, val: ">"}) {
				return nil, false
			}
			i++
			continue
		case '<':
			if !emit(token{kind: tokRedirect, val: "<"}) {
				return nil, false
			}
			i++
			continue
		}
		// fd redirects: 2>, 2>>, 1>, etc.
		if src[i] >= '0' && src[i] <= '9' {
			j := i
			for j < len(src) && src[j] >= '0' && src[j] <= '9' {
				j++
			}
			if j < len(src) && (src[j] == '>' || src[j] == '<') {
				opStart := j
				j++
				if j < len(src) && src[j] == '>' {
					j++
				}
				op := src[i:j]
				// Only treat as redirect if next is not part of a word (or end).
				// `2>file` and `2> file` both OK; `20` alone is a word.
				if !emit(token{kind: tokRedirect, val: op}) {
					return nil, false
				}
				i = j
				_ = opStart
				continue
			}
		}

		// word
		word, static, next, ok := readWord(b, src, i)
		if !ok {
			return nil, false
		}
		if !emit(token{kind: tokWord, val: word, static: static}) {
			return nil, false
		}
		i = next
	}
	return tokens, true
}

// readWord reads one shell word starting at i.
func readWord(b *builder, src string, i int) (word string, static bool, next int, ok bool) {
	var buf strings.Builder
	static = true
	for i < len(src) {
		c := src[i]
		if isWordStop(src, i) {
			break
		}
		switch c {
		case '\\':
			if i+1 >= len(src) {
				b.mark(StatusPartial, IssueInvalidSyntax)
				static = false
				i++
				break
			}
			// Escaped newline = line continue
			if src[i+1] == '\n' {
				i += 2
				continue
			}
			buf.WriteByte(src[i+1])
			i += 2
		case '\'':
			i++
			for i < len(src) && src[i] != '\'' {
				buf.WriteByte(src[i])
				i++
			}
			if i >= len(src) {
				b.mark(StatusInvalid, IssueInvalidSyntax)
				return "", false, i, false
			}
			i++ // closing '
		case '"':
			i++
			for i < len(src) && src[i] != '"' {
				if src[i] == '\\' && i+1 < len(src) {
					n := src[i+1]
					if n == '"' || n == '\\' || n == '$' || n == '`' || n == '\n' {
						if n != '\n' {
							buf.WriteByte(n)
						}
						i += 2
						continue
					}
				}
				if src[i] == '$' || src[i] == '`' {
					static = false
					b.mark(StatusPartial, IssueDynamicWord)
					// consume rest of double-quoted roughly
					if src[i] == '`' {
						i++
						for i < len(src) && src[i] != '`' {
							i++
						}
						if i < len(src) {
							i++
						}
						continue
					}
					// $
					i = skipDollar(src, i)
					continue
				}
				buf.WriteByte(src[i])
				i++
			}
			if i >= len(src) {
				b.mark(StatusInvalid, IssueInvalidSyntax)
				return "", false, i, false
			}
			i++ // closing "
		case '`':
			static = false
			b.mark(StatusPartial, IssueDynamicWord)
			i++
			for i < len(src) && src[i] != '`' {
				if src[i] == '\\' && i+1 < len(src) {
					i += 2
					continue
				}
				i++
			}
			if i < len(src) {
				i++
			}
			buf.WriteString("``")
		case '$':
			static = false
			b.mark(StatusPartial, IssueDynamicWord)
			i = skipDollar(src, i)
			buf.WriteByte('$')
		case '~':
			// bare tilde expansion is dynamic (home)
			if buf.Len() == 0 {
				static = false
				b.mark(StatusPartial, IssueDynamicWord)
			}
			buf.WriteByte(c)
			i++
		default:
			// Reject ANSI-C $'...' already handled via $; process subst
			if c == '(' || c == ')' || c == '{' || c == '}' {
				// unquoted paren often means subshell / func — unsupported
				if c == '(' {
					static = false
					b.mark(StatusPartial, IssueUnsupportedConstruct)
				}
			}
			r, size := utf8.DecodeRuneInString(src[i:])
			if r == utf8.RuneError && size == 1 {
				b.mark(StatusInvalid, IssueInvalidUTF8)
				return "", false, i, false
			}
			buf.WriteRune(r)
			i += size
		}
	}
	if buf.Len() == 0 && static {
		// empty word from "" or ''
		return "", true, i, true
	}
	return buf.String(), static, i, true
}

func isWordStop(src string, i int) bool {
	if i >= len(src) {
		return true
	}
	c := src[i]
	switch c {
	case ' ', '\t', '\n', '\r', '|', '&', ';', '<', '>', '(', ')':
		return true
	}
	// digit+redirect only checked at word start in lexer
	return false
}

func skipDollar(src string, i int) int {
	// i points at $
	if i+1 >= len(src) {
		return i + 1
	}
	switch src[i+1] {
	case '{':
		i += 2
		depth := 1
		for i < len(src) && depth > 0 {
			if src[i] == '{' {
				depth++
			} else if src[i] == '}' {
				depth--
			}
			i++
		}
		return i
	case '(':
		// $(...) or $((...))
		i += 2
		if i < len(src) && src[i] == '(' {
			// arithmetic
			i++
			depth := 2
			for i < len(src) && depth > 0 {
				if src[i] == '(' {
					depth++
				} else if src[i] == ')' {
					depth--
				}
				i++
			}
			return i
		}
		depth := 1
		for i < len(src) && depth > 0 {
			if src[i] == '(' {
				depth++
			} else if src[i] == ')' {
				depth--
			} else if src[i] == '\\' && i+1 < len(src) {
				i += 2
				continue
			} else if src[i] == '\'' {
				i++
				for i < len(src) && src[i] != '\'' {
					i++
				}
				if i < len(src) {
					i++
				}
				continue
			} else if src[i] == '"' {
				i++
				for i < len(src) && src[i] != '"' {
					if src[i] == '\\' && i+1 < len(src) {
						i += 2
						continue
					}
					i++
				}
				if i < len(src) {
					i++
				}
				continue
			}
			i++
		}
		return i
	case '\'', '"':
		// $'...' / $"..." — treat as dynamic
		return i + 1
	default:
		// $name or $1
		i++
		if i < len(src) && (unicode.IsLetter(rune(src[i])) || src[i] == '_') {
			for i < len(src) && (unicode.IsLetter(rune(src[i])) || unicode.IsDigit(rune(src[i])) || src[i] == '_') {
				i++
			}
			return i
		}
		// special $$, $?, $#, $*, $@, $0-9
		if i < len(src) {
			i++
		}
		return i
	}
}
