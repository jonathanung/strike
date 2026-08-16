// Package actionfacts projects bash/tool inputs into bounded semantic facts
// for in-process permission evaluation (#888).
//
// Design (clean-room; not a port of any third-party source):
//   - Never eval/exec, expand variables, read files, or open the network.
//   - Attacker-controlled parse failures are Parse statuses/issues, never
//     returned as Go errors that could influence control flow via message text.
//   - Authoritative() means the entire action was statically proven complete.
//   - EnforcementEligible() is required before facts may drive a synchronous
//     deny; non-authoritative facts are diagnostic only.
//   - Callers must not dual-evaluate the same permission rule on both fact
//     keys and the raw pattern for a deny decision — see permission.EvaluateDetailed.
package actionfacts

import "strings"

// Input is the material available at a tool-call permission boundary.
type Input struct {
	// Tool is the permission/tool name (e.g. "bash", "webfetch", "read").
	Tool string
	// Command is a raw shell command string (bash tool).
	Command string
	// Argv is a structured argument vector when the caller already has one.
	Argv []string
	// CWD is the session working directory (optional; used for display only).
	CWD string
}

// Facts is the statically proven subset of one action.
type Facts struct {
	Tool     string
	CWD      string
	Parse    ParseResult
	Commands []CommandFact
	Paths    []PathFact
	Network  []NetworkFact
}

// Authoritative reports whether the entire action was fully projected.
// Permission evaluation may use fact keys only when this is true (and
// EnforcementEligible for deny). Otherwise keep legacy pattern matching.
func (f Facts) Authoritative() bool {
	return f.Parse.Status == StatusComplete
}

// EnforcementEligible reports whether semantic matches may participate in a
// synchronous deny. Preview/uncertain/partial parses never authorize blocking.
func (f Facts) EnforcementEligible() bool {
	if !f.Authoritative() || len(f.Commands) == 0 {
		return false
	}
	for _, c := range f.Commands {
		if c.Effect != EffectExecute {
			return false
		}
		if !c.ArgvComplete || c.Program == "" {
			return false
		}
	}
	return true
}

// ParseResult describes how much of the action could be projected safely.
type ParseResult struct {
	Status  ParseStatus
	Dialect Dialect
	Issues  []IssueCode
}

// ParseStatus is a closed set of parser outcomes.
type ParseStatus string

const (
	StatusNotApplicable ParseStatus = "not_applicable"
	StatusComplete      ParseStatus = "complete"
	StatusPartial       ParseStatus = "partial"
	StatusUnsupported   ParseStatus = "unsupported"
	StatusInvalid       ParseStatus = "invalid"
	StatusLimitExceeded ParseStatus = "limit_exceeded"
)

// Dialect identifies the command grammar used for projection.
type Dialect string

const (
	DialectNone  Dialect = "none"
	DialectArgv  Dialect = "argv"
	DialectPOSIX Dialect = "posix"
)

// IssueCode is a value-free diagnostic (never embeds input fragments).
type IssueCode string

const (
	IssueInvalidSyntax        IssueCode = "invalid_syntax"
	IssueInvalidUTF8          IssueCode = "invalid_utf8"
	IssueDynamicWord          IssueCode = "dynamic_word"
	IssueUnsupportedConstruct IssueCode = "unsupported_construct"
	IssueOpaqueArtifact       IssueCode = "opaque_artifact"
	IssueInputLimit           IssueCode = "input_limit"
	IssueNodeLimit            IssueCode = "node_limit"
	IssueDepthLimit           IssueCode = "depth_limit"
	IssueInternalFailure      IssueCode = "internal_parser_failure"
	IssueEmptyInput           IssueCode = "empty_input"
)

// CommandFact is one statically identified command invocation.
type CommandFact struct {
	ID           int
	PipelineID   int
	Effect       CommandEffect
	Program      string   // basename-ish program name (curl, git, rm)
	Argv         []string // static argv when ArgvComplete
	ArgvComplete bool
	Operations   []OperationKind
}

// CommandEffect distinguishes real execution from uncertain/preview forms.
type CommandEffect string

const (
	EffectExecute   CommandEffect = "execute"
	EffectUncertain CommandEffect = "uncertain"
)

// OperationKind is a small semantic vocabulary for known programs.
type OperationKind string

const (
	OpExecute OperationKind = "execute"
	OpRead    OperationKind = "read"
	OpWrite   OperationKind = "write"
	OpDelete  OperationKind = "delete"
	OpFetch   OperationKind = "fetch"
	OpConnect OperationKind = "connect"
	OpUpload  OperationKind = "upload"
	OpDecode  OperationKind = "decode"
	OpSearch  OperationKind = "search"
	OpList    OperationKind = "list"
)

// PathFact identifies a statically proven path operand.
type PathFact struct {
	CommandID int
	Access    PathAccess
	Value     string
}

// PathAccess is how a path is used.
type PathAccess string

const (
	PathRead    PathAccess = "read"
	PathWrite   PathAccess = "write"
	PathAppend  PathAccess = "append"
	PathDelete  PathAccess = "delete"
	PathExecute PathAccess = "execute"
)

// NetworkFact identifies a static network destination.
type NetworkFact struct {
	CommandID int
	Action    NetworkAction
	Host      string
	Port      int
	Scheme    string
}

// NetworkAction is the network intent.
type NetworkAction string

const (
	NetConnect  NetworkAction = "connect"
	NetDownload NetworkAction = "download"
	NetUpload   NetworkAction = "upload"
)

// MatchKeys returns bounded strings suitable for permission rule globs when
// facts are enforcement-eligible. Keys never include raw secret-bearing
// payloads beyond path/host/command shapes already present in argv.
//
// Forms:
//   - joined static argv per command ("curl https://example.com")
//   - program name ("curl")
//   - command-class key ("curl *") so common rules like `bash`/`curl *` match
//     even when argv contains `/` (doublestar `*` does not cross `/`)
//   - path values ("/tmp/x", ".env")
//   - "host:<hostname>" and bare hostname
func MatchKeys(f Facts) []string {
	if !f.EnforcementEligible() {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if len(s) > maxMatchKeyBytes {
			s = s[:maxMatchKeyBytes]
		}
		if _, ok := seen[s]; ok {
			return
		}
		if len(out) >= maxMatchKeysTotal {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	for _, c := range f.Commands {
		if c.ArgvComplete && len(c.Argv) > 0 {
			add(strings.Join(c.Argv, " "))
		}
		add(c.Program)
		if c.Program != "" {
			// Align with session "always" grants (first-word class) and config
			// idioms like {permission:bash, pattern:"rm *"}.
			add(c.Program + " *")
		}
	}
	for _, p := range f.Paths {
		add(p.Value)
	}
	for _, n := range f.Network {
		if n.Host != "" {
			add(n.Host)
			add("host:" + n.Host)
		}
	}
	return out
}

// Summary returns a short, redaction-friendly description for explain/audit.
// It never embeds full command text — only counts, programs, hosts, and a
// capped path sample.
func Summary(f Facts) string {
	if f.Parse.Status == "" || f.Parse.Status == StatusNotApplicable {
		return ""
	}
	var b strings.Builder
	b.WriteString("parse=")
	b.WriteString(string(f.Parse.Status))
	if f.Parse.Dialect != "" && f.Parse.Dialect != DialectNone {
		b.WriteString(" dialect=")
		b.WriteString(string(f.Parse.Dialect))
	}
	if f.Authoritative() {
		b.WriteString(" authoritative")
	}
	if f.EnforcementEligible() {
		b.WriteString(" enforce")
	}
	if len(f.Parse.Issues) > 0 {
		b.WriteString(" issues=")
		for i, iss := range f.Parse.Issues {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(string(iss))
			if i >= 4 {
				b.WriteString(",…")
				break
			}
		}
	}
	if progs := uniquePrograms(f.Commands); len(progs) > 0 {
		b.WriteString(" cmds=")
		for i, p := range progs {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(p)
			if i >= 7 {
				b.WriteString(",…")
				break
			}
		}
	}
	if len(f.Paths) > 0 {
		b.WriteString(" paths=")
		b.WriteString(itoa(len(f.Paths)))
	}
	if len(f.Network) > 0 {
		b.WriteString(" net=")
		for i, n := range f.Network {
			if i > 0 {
				b.WriteByte(',')
			}
			if n.Host != "" {
				b.WriteString(n.Host)
			} else {
				b.WriteString("?")
			}
			if i >= 3 {
				b.WriteString(",…")
				break
			}
		}
	}
	return b.String()
}

func uniquePrograms(cmds []CommandFact) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, c := range cmds {
		p := c.Program
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
