// Package triggers expands composer text on the daemon so every client
// shares one behaviour for /command, @file, $skill, and !shell.
//
// The TUI already expands @file and runs ! before user.input. Expansion
// here is idempotent for fences the TUI (or a previous pass) already
// attached, and ! / /command are classified only when the message itself
// starts with the trigger. Unknown $name stays literal text.
package triggers

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/pkg/protocol"
)

// Action is what the daemon should do with one composer message.
type Action string

const (
	// ActionPass leaves the text unchanged.
	ActionPass Action = ""
	// ActionText starts or queues a turn with ModelText. Display is the
	// transcript line when it differs (the user keeps @path / $skill).
	ActionText Action = "text"
	// ActionShell runs Shell through the permission model and does not
	// start a model turn.
	ActionShell Action = "shell"
	// ActionError rejects the message. Error is the user-facing reason.
	ActionError Action = "error"
	// ActionOp replaces the message with Op (a real protocol op).
	ActionOp Action = "op"
)

// Skill is a loaded prompt template. Render may be nil.
type Skill struct {
	Name        string
	Description string
	Render      func(args string) string
}

// Outcome is the expansion of one composer message.
type Outcome struct {
	Action    Action
	ModelText string
	Display   string
	Shell     string
	Error     string
	Op        protocol.Op
	Notices   []string
}

// Command is one real daemon slash action for the composer catalog.
// Insert is what the client puts in the draft. Prefix inserts (trailing
// space) still need an argument before they become an op.
type Command struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
	Insert string `json:"insert"`
}

// ScopedRead is the slice of host.Files used for @file. Nil means mentions
// are left in the text and reported in Notices.
type ScopedRead interface {
	ReadScoped(path string) (host.FileContent, error)
}

// Commands is the daemon slash catalog. Every insert is either a complete
// op or a prefix the expander answers with a usage error (never a fake
// success, and never forwarded to the model).
func Commands() []Command {
	var out []Command
	for _, mode := range protocol.PermissionModes() {
		label := "/mode " + string(mode)
		out = append(out, Command{
			ID:     "mode-" + string(mode),
			Label:  label,
			Detail: "Set the session approval posture",
			Insert: label,
		})
	}
	for _, level := range protocol.Efforts() {
		label := "/effort " + string(level)
		out = append(out, Command{
			ID:     "effort-" + string(level),
			Label:  label,
			Detail: "Set the reasoning dial",
			Insert: label,
		})
	}
	for _, mode := range protocol.Autonomies() {
		label := "/autonomy " + string(mode)
		out = append(out, Command{
			ID:     "autonomy-" + string(mode),
			Label:  label,
			Detail: "Set who clears phase exits",
			Insert: label,
		})
	}
	out = append(out,
		Command{ID: "fast-on", Label: "/fast on", Detail: "Turn fast mode on", Insert: "/fast on"},
		Command{ID: "fast-off", Label: "/fast off", Detail: "Turn fast mode off", Insert: "/fast off"},
		Command{ID: "stop", Label: "/stop", Detail: "Interrupt the running turn", Insert: "/stop"},
		Command{ID: "compact", Label: "/compact", Detail: "Compact model history", Insert: "/compact"},
		Command{ID: "compact-trim", Label: "/compact trim", Detail: "Compact by dropping older turns", Insert: "/compact trim"},
		Command{ID: "compact-summarize", Label: "/compact summarize", Detail: "Compact with a summary", Insert: "/compact summarize"},
		Command{ID: "rewind", Label: "/rewind", Detail: "Undo the last turn in the transcript", Insert: "/rewind"},
		Command{ID: "rewind-files", Label: "/rewind files", Detail: "Undo the last turn and restore its files", Insert: "/rewind files"},
		Command{ID: "workflow-stop", Label: "/workflow stop", Detail: "Stop the active workflow", Insert: "/workflow stop"},
		Command{ID: "provider", Label: "/provider", Detail: "Switch provider", Insert: "/provider "},
		Command{ID: "model", Label: "/model", Detail: "Switch model", Insert: "/model "},
		Command{ID: "agent", Label: "/agent", Detail: "Switch agent", Insert: "/agent "},
		Command{ID: "workflow-start", Label: "/workflow start", Detail: "Start a workflow by name", Insert: "/workflow start "},
	)
	return out
}

// Prepare classifies text. skills are matched by name. files may be nil.
func Prepare(text string, files ScopedRead, skills []Skill) Outcome {
	raw := strings.TrimSpace(text)
	if raw == "" {
		return Outcome{}
	}
	if strings.HasPrefix(raw, "!") {
		return prepareShell(raw)
	}
	if strings.HasPrefix(raw, "/") {
		return prepareSlash(raw, files, skills)
	}
	if name, args, ok := leadingSkill(raw); ok {
		if sk, found := lookupSkill(skills, name); found {
			return renderSkill(sk, args, raw, files)
		}
		// Unknown $name is ordinary text ($5, a ticker, a typo).
		return expandPlain(raw, files)
	}
	return expandPlain(raw, files)
}

func prepareShell(raw string) Outcome {
	command := strings.TrimSpace(strings.TrimPrefix(raw, "!"))
	if command == "" {
		return Outcome{Action: ActionError, Error: "empty command — use !cmd to run a shell command"}
	}
	return Outcome{Action: ActionShell, Shell: command, Display: raw}
}

func prepareSlash(raw string, files ScopedRead, skills []Skill) Outcome {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return Outcome{Action: ActionError, Error: "unknown command"}
	}
	name := fields[0]
	args := strings.TrimSpace(strings.TrimPrefix(raw, name))
	switch name {
	case "/mode":
		if len(fields) < 2 {
			return usage("usage: /mode <default|plan|soft-approve|accept-edits|yolo>")
		}
		mode, ok := protocol.ParsePermissionMode(fields[1])
		if !ok {
			return usage("unknown mode " + fields[1] + " — want default, plan, soft-approve, accept-edits, yolo")
		}
		return opOutcome(protocol.SetPermissionMode{Mode: mode})
	case "/effort":
		if len(fields) < 2 {
			return usage("usage: /effort <off|low|medium|high|xhigh|max>")
		}
		level, ok := protocol.ParseEffort(fields[1])
		if !ok || level == protocol.EffortDefault {
			return usage("unknown effort " + fields[1])
		}
		return opOutcome(protocol.SetEffort{Level: level})
	case "/autonomy":
		if len(fields) < 2 {
			return usage("usage: /autonomy <supervised|agent|checks|skip-all>")
		}
		mode, ok := protocol.ParseAutonomy(fields[1])
		if !ok {
			return usage("unknown autonomy " + fields[1])
		}
		return opOutcome(protocol.SetAutonomy{Mode: mode})
	case "/fast":
		if len(fields) < 2 {
			return usage("usage: /fast <on|off>")
		}
		switch strings.ToLower(fields[1]) {
		case "on", "true", "1":
			return opOutcome(protocol.SetFast{Enabled: true})
		case "off", "false", "0":
			return opOutcome(protocol.SetFast{Enabled: false})
		default:
			return usage("usage: /fast <on|off>")
		}
	case "/agent":
		if args == "" {
			return usage("usage: /agent <name>")
		}
		return opOutcome(protocol.SelectAgent{Name: args})
	case "/model":
		if len(fields) < 2 {
			return usage("usage: /model <id> or /model <provider> <id>")
		}
		if len(fields) >= 3 {
			return opOutcome(protocol.SelectModel{Provider: fields[1], Model: fields[2]})
		}
		return opOutcome(protocol.SelectModel{Model: fields[1]})
	case "/provider":
		if len(fields) < 2 {
			return usage("usage: /provider <name> [model]")
		}
		sel := protocol.SelectModel{Provider: fields[1]}
		if len(fields) > 2 {
			sel.Model = fields[2]
		}
		return opOutcome(sel)
	case "/compact":
		strategy := ""
		if len(fields) > 1 {
			strategy = strings.ToLower(fields[1])
			if strategy != "trim" && strategy != "summarize" {
				return usage("usage: /compact [trim|summarize]")
			}
		}
		return opOutcome(protocol.Compact{Strategy: strategy})
	case "/interrupt", "/stop":
		return opOutcome(protocol.Interrupt{})
	case "/rewind", "/undo":
		restore := false
		if len(fields) > 1 {
			switch strings.ToLower(fields[1]) {
			case "chat", "history":
				restore = false
			case "files", "disk", "all":
				restore = true
			default:
				return usage("usage: /rewind [chat|files]")
			}
		}
		return opOutcome(protocol.Rewind{RestoreFiles: restore})
	case "/workflow":
		if len(fields) >= 2 && strings.EqualFold(fields[1], "stop") {
			return opOutcome(protocol.StopWorkflow{})
		}
		if len(fields) >= 2 && strings.EqualFold(fields[1], "start") {
			wf := strings.TrimSpace(strings.TrimPrefix(args, fields[1]))
			wf = strings.TrimSpace(wf)
			if wf == "" {
				return usage("usage: /workflow start <name>")
			}
			return opOutcome(protocol.StartWorkflow{Name: wf})
		}
		return usage("usage: /workflow start <name> | /workflow stop")
	default:
		skillName := strings.TrimPrefix(name, "/")
		if sk, found := lookupSkill(skills, skillName); found {
			return renderSkill(sk, args, raw, files)
		}
		return usage("unknown command " + name)
	}
}

func usage(msg string) Outcome {
	return Outcome{Action: ActionError, Error: msg}
}

func opOutcome(op protocol.Op) Outcome {
	return Outcome{Action: ActionOp, Op: op}
}

func leadingSkill(raw string) (name, args string, ok bool) {
	if !strings.HasPrefix(raw, "$") {
		return "", "", false
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return "", "", false
	}
	token := fields[0]
	name = strings.TrimPrefix(token, "$")
	if !skillToken(name) {
		return "", "", false
	}
	args = strings.TrimSpace(strings.TrimPrefix(raw, token))
	return name, args, true
}

func skillToken(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '-' || r == '_':
			if i == 0 {
				return false
			}
		case unicode.IsLetter(r) || unicode.IsDigit(r):
		default:
			return false
		}
	}
	return true
}

func lookupSkill(skills []Skill, name string) (Skill, bool) {
	for _, sk := range skills {
		if sk.Name == name {
			return sk, true
		}
	}
	return Skill{}, false
}

func renderSkill(sk Skill, args, display string, files ScopedRead) Outcome {
	if sk.Render == nil {
		return usage("skill " + sk.Name + " has no template")
	}
	prompt := strings.TrimSpace(sk.Render(args))
	if prompt == "" {
		return usage("skill " + sk.Name + " rendered empty")
	}
	expanded, notices := expandFileMentions(prompt, files)
	return Outcome{Action: ActionText, ModelText: expanded, Display: display, Notices: notices}
}

func expandPlain(raw string, files ScopedRead) Outcome {
	expanded, notices := expandFileMentions(raw, files)
	if expanded == raw && len(notices) == 0 {
		return Outcome{}
	}
	return Outcome{Action: ActionText, ModelText: expanded, Display: raw, Notices: notices}
}

type fileMention struct {
	Path  string
	Start int
	End   int
}

// findFileMentions matches the TUI rule: '@' at the start or after
// whitespace, then path runes. Emails (user@host) are not mentions.
// ".." and empty paths are skipped.
func findFileMentions(s string) []fileMention {
	if s == "" {
		return nil
	}
	var out []fileMention
	i := 0
	for i < len(s) {
		at := strings.IndexByte(s[i:], '@')
		if at < 0 {
			break
		}
		at += i
		if at > 0 {
			prev, _ := utf8.DecodeLastRuneInString(s[:at])
			if prev == utf8.RuneError || !unicode.IsSpace(prev) {
				i = at + 1
				continue
			}
		}
		pathStart := at + 1
		if pathStart >= len(s) {
			break
		}
		pathEnd := pathStart
		for pathEnd < len(s) {
			r, size := utf8.DecodeRuneInString(s[pathEnd:])
			if size <= 0 || (r == utf8.RuneError && size == 1) || !isFileMentionPathRune(r) {
				break
			}
			pathEnd += size
		}
		if pathEnd == pathStart {
			i = at + 1
			continue
		}
		path := strings.ReplaceAll(s[pathStart:pathEnd], "\\", "/")
		path = strings.TrimPrefix(path, "./")
		if path == "" || path == "." || path == ".." || strings.HasPrefix(path, "../") {
			i = pathEnd
			continue
		}
		out = append(out, fileMention{Path: path, Start: at, End: pathEnd})
		i = pathEnd
	}
	return out
}

func isFileMentionPathRune(r rune) bool {
	switch r {
	case '/', '\\', '.', '-', '_', '+', '~':
		return true
	default:
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}
}

func expandFileMentions(text string, files ScopedRead) (string, []string) {
	spans := findFileMentions(text)
	if len(spans) == 0 {
		return text, nil
	}
	if files == nil {
		return text, []string{"file mentions were not expanded (files unavailable)"}
	}
	seen := make(map[string]struct{}, len(spans))
	var blocks strings.Builder
	var notices []string
	for _, sp := range spans {
		if _, ok := seen[sp.Path]; ok {
			continue
		}
		seen[sp.Path] = struct{}{}
		if alreadyAttached(text, sp.Path) {
			continue
		}
		fc, err := files.ReadScoped(sp.Path)
		if err != nil {
			notices = append(notices, fmt.Sprintf("@%s: %v", sp.Path, err))
			continue
		}
		displayPath := fc.Path
		if displayPath == "" {
			displayPath = sp.Path
		}
		if alreadyAttached(text, displayPath) {
			continue
		}
		if fc.Skip {
			msg := fc.Notice
			if msg == "" {
				msg = "skipped"
			}
			notices = append(notices, fmt.Sprintf("@%s: %s", displayPath, msg))
			continue
		}
		if fc.Notice != "" {
			notices = append(notices, fmt.Sprintf("@%s: %s", displayPath, fc.Notice))
		}
		kind := "file"
		if strings.HasSuffix(displayPath, "/") {
			kind = "folder"
		}
		fmt.Fprintf(&blocks, "\n\n--- %s: %s ---\n%s", kind, displayPath, fc.Content)
		if !strings.HasSuffix(fc.Content, "\n") {
			blocks.WriteByte('\n')
		}
		fmt.Fprintf(&blocks, "--- end %s: %s ---", kind, displayPath)
	}
	if blocks.Len() == 0 {
		return text, notices
	}
	return text + blocks.String(), notices
}

func alreadyAttached(text, path string) bool {
	if path == "" {
		return false
	}
	return strings.Contains(text, "--- file: "+path+" ---") || strings.Contains(text, "--- folder: "+path+" ---")
}
