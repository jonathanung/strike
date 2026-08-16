package actionfacts

import (
	"path"
	"strings"
	"unicode/utf8"
)

// Analyze derives a bounded, deterministic semantic projection.
// It never returns attacker-controlled parser errors and never evaluates
// commands, expands variables, reads files, or opens network connections.
func Analyze(input Input) (facts Facts) {
	defer func() {
		if recover() != nil {
			facts = Facts{
				Tool: safeTool(input.Tool),
				Parse: ParseResult{
					Status:  StatusInvalid,
					Dialect: DialectNone,
					Issues:  []IssueCode{IssueInternalFailure},
				},
			}
		}
	}()
	return analyze(input)
}

func analyze(input Input) Facts {
	tool := strings.TrimSpace(input.Tool)
	cwd := input.CWD
	if len(cwd) > maxScalarBytes {
		cwd = ""
	}

	out := newBuilder(DialectNone)

	// Selected non-shell tools: project path/URL args (not shell grammar).
	if isStructuredTool(tool) {
		return analyzeToolPattern(tool, toolArgv(input), cwd)
	}

	// Structured argv without a shell string (e.g. tests / future runners).
	if len(input.Argv) > 0 && strings.TrimSpace(input.Command) == "" {
		if iss := validateArgv(input.Argv); iss != "" {
			out.mark(statusForIssue(iss), iss)
			return out.finish(tool, cwd)
		}
		out.dialect = DialectArgv
		parseArgvCommand(&out, input.Argv, 0)
		return out.finish(tool, cwd)
	}

	cmd := input.Command
	if strings.TrimSpace(cmd) == "" {
		out.mark(StatusInvalid, IssueEmptyInput)
		return out.finish(tool, cwd)
	}
	if iss := validateCommandText(cmd); iss != "" {
		out.mark(statusForIssue(iss), iss)
		return out.finish(tool, cwd)
	}

	out.dialect = DialectPOSIX
	parsePOSIX(&out, cmd, 0)
	return out.finish(tool, cwd)
}

func isStructuredTool(tool string) bool {
	switch strings.ToLower(strings.TrimSpace(tool)) {
	case "webfetch", "websearch", "browser", "read", "glob", "grep", "edit", "write", "delete", "move":
		return true
	default:
		return false
	}
}

func toolArgv(input Input) []string {
	if len(input.Argv) > 0 {
		return input.Argv
	}
	if strings.TrimSpace(input.Command) != "" {
		return []string{input.Command}
	}
	return nil
}

func analyzeToolPattern(tool string, argv []string, cwd string) Facts {
	out := newBuilder(DialectArgv)
	var value string
	if len(argv) > 0 {
		value = argv[0]
	}
	if strings.TrimSpace(value) == "" {
		out.status = StatusNotApplicable
		return out.finish(tool, cwd)
	}
	if iss := validateScalar(value); iss != "" {
		out.mark(statusForIssue(iss), iss)
		return out.finish(tool, cwd)
	}
	id := out.nextID()
	switch strings.ToLower(tool) {
	case "webfetch", "websearch", "browser":
		cmd := CommandFact{
			ID:           id,
			Effect:       EffectExecute,
			Program:      tool,
			Argv:         []string{tool, value},
			ArgvComplete: true,
			Operations:   []OperationKind{OpFetch},
		}
		if !out.addCommand(cmd) {
			return out.finish(tool, cwd)
		}
		if host, scheme, port, ok := parseURLHost(value); ok {
			out.addNetwork(NetworkFact{
				CommandID: id,
				Action:    NetDownload,
				Host:      host,
				Port:      port,
				Scheme:    scheme,
			})
		} else {
			out.mark(StatusPartial, IssueUnsupportedConstruct)
		}
	case "read", "glob", "grep", "edit", "write", "delete", "move":
		access := PathRead
		op := OpRead
		switch strings.ToLower(tool) {
		case "edit", "write":
			access = PathWrite
			op = OpWrite
		case "delete":
			access = PathDelete
			op = OpDelete
		case "glob", "grep":
			op = OpSearch
		}
		cmd := CommandFact{
			ID:           id,
			Effect:       EffectExecute,
			Program:      tool,
			Argv:         []string{tool, value},
			ArgvComplete: true,
			Operations:   []OperationKind{op},
		}
		if !out.addCommand(cmd) {
			return out.finish(tool, cwd)
		}
		out.addPath(PathFact{CommandID: id, Access: access, Value: value})
	default:
		out.status = StatusNotApplicable
	}
	return out.finish(tool, cwd)
}

// builder accumulates parse state.
type builder struct {
	status   ParseStatus
	dialect  Dialect
	issues   []IssueCode
	commands []CommandFact
	paths    []PathFact
	network  []NetworkFact
	next     int
	pipeID   int
}

func newBuilder(d Dialect) builder {
	return builder{status: StatusNotApplicable, dialect: d, next: 1}
}

func (b *builder) nextID() int {
	id := b.next
	b.next++
	return id
}

func (b *builder) nextPipe() int {
	b.pipeID++
	return b.pipeID
}

func (b *builder) addIssue(iss IssueCode) {
	if iss == "" {
		return
	}
	for _, x := range b.issues {
		if x == iss {
			return
		}
	}
	b.issues = append(b.issues, iss)
}

func (b *builder) mark(st ParseStatus, iss IssueCode) {
	b.addIssue(iss)
	b.status = mergeStatus(b.status, st)
}

func (b *builder) addCommand(c CommandFact) bool {
	if len(b.commands) >= maxCommands {
		b.mark(StatusLimitExceeded, IssueNodeLimit)
		return false
	}
	b.commands = append(b.commands, c)
	if b.status == StatusNotApplicable {
		b.status = StatusComplete
	}
	return true
}

func (b *builder) addPath(p PathFact) {
	if len(b.paths) >= maxPaths {
		b.mark(StatusLimitExceeded, IssueNodeLimit)
		return
	}
	if len(p.Value) > maxScalarBytes {
		b.mark(StatusLimitExceeded, IssueInputLimit)
		return
	}
	b.paths = append(b.paths, p)
}

func (b *builder) addNetwork(n NetworkFact) {
	if len(b.network) >= maxNetwork {
		b.mark(StatusLimitExceeded, IssueNodeLimit)
		return
	}
	b.network = append(b.network, n)
}

func (b *builder) finish(tool, cwd string) Facts {
	if b.status == StatusNotApplicable && len(b.commands) > 0 {
		b.status = StatusComplete
	}
	if b.status == StatusComplete {
		for _, c := range b.commands {
			if c.Effect != EffectExecute || !c.ArgvComplete || c.Program == "" {
				b.status = StatusPartial
				b.addIssue(IssueUnsupportedConstruct)
				break
			}
		}
	}
	return Facts{
		Tool:     safeTool(tool),
		CWD:      cwd,
		Parse:    ParseResult{Status: b.status, Dialect: b.dialect, Issues: append([]IssueCode(nil), b.issues...)},
		Commands: b.commands,
		Paths:    b.paths,
		Network:  b.network,
	}
}

// mergeStatus keeps the worse outcome (higher rank wins).
func mergeStatus(cur, next ParseStatus) ParseStatus {
	if statusRank(next) > statusRank(cur) {
		return next
	}
	return cur
}

func statusRank(s ParseStatus) int {
	switch s {
	case StatusNotApplicable:
		return 0
	case StatusComplete:
		return 1
	case StatusPartial:
		return 2
	case StatusUnsupported:
		return 3
	case StatusInvalid:
		return 4
	case StatusLimitExceeded:
		return 5
	default:
		return 2
	}
}

func statusForIssue(iss IssueCode) ParseStatus {
	switch iss {
	case IssueInputLimit, IssueNodeLimit, IssueDepthLimit:
		return StatusLimitExceeded
	case IssueInvalidSyntax, IssueInvalidUTF8, IssueEmptyInput:
		return StatusInvalid
	case IssueUnsupportedConstruct, IssueOpaqueArtifact, IssueDynamicWord:
		return StatusPartial
	default:
		return StatusPartial
	}
}

func validateCommandText(s string) IssueCode {
	if len(s) > maxCommandBytes {
		return IssueInputLimit
	}
	if !utf8.ValidString(s) {
		return IssueInvalidUTF8
	}
	if strings.TrimSpace(s) == "" {
		return IssueEmptyInput
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0 {
			return IssueInvalidSyntax
		}
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			return IssueInvalidSyntax
		}
	}
	return ""
}

func validateArgv(argv []string) IssueCode {
	if len(argv) == 0 {
		return IssueEmptyInput
	}
	if len(argv) > maxArgvItems {
		return IssueInputLimit
	}
	total := 0
	for _, a := range argv {
		if !utf8.ValidString(a) {
			return IssueInvalidUTF8
		}
		if len(a) > maxScalarBytes {
			return IssueInputLimit
		}
		if strings.IndexByte(a, 0) >= 0 {
			return IssueInvalidSyntax
		}
		total += len(a)
		if total > maxArgvBytes {
			return IssueInputLimit
		}
	}
	if strings.TrimSpace(argv[0]) == "" {
		return IssueInvalidSyntax
	}
	return ""
}

func validateScalar(s string) IssueCode {
	if len(s) > maxScalarBytes {
		return IssueInputLimit
	}
	if !utf8.ValidString(s) {
		return IssueInvalidUTF8
	}
	if strings.IndexByte(s, 0) >= 0 {
		return IssueInvalidSyntax
	}
	return ""
}

func safeTool(t string) string {
	t = strings.TrimSpace(t)
	if len(t) > maxScalarBytes || !utf8.ValidString(t) {
		return ""
	}
	return t
}

func programName(argv0 string) string {
	if argv0 == "" {
		return ""
	}
	base := path.Base(strings.ReplaceAll(argv0, "\\", "/"))
	return base
}
