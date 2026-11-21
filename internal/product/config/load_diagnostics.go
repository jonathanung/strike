package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/safefile"
	"github.com/jonathanung/strike/pkg/redact"
)

// Load diagnostic codes for skill and agent files (#1386).
const (
	DiagUnreadable           = "unreadable"
	DiagEmpty                = "empty"
	DiagMalformedFrontmatter = "malformed_frontmatter"
	DiagMissingDescription   = "missing_description"
	// DiagInvalid is a strict-root validation error. It failed the whole load.
	DiagInvalid = "invalid"
	// DiagSkipped is a validation error in an external root (.claude,
	// .opencode). The file was skipped; other files still loaded.
	DiagSkipped = "skipped"
)

// LoadDiagnostic is one per-file finding from the skill or agent loader. It
// follows the plugin.Diagnostic shape (severity, code, message, path). It
// carries the file path and the loader's error string only, never file
// contents.
type LoadDiagnostic struct {
	Kind     string // skill | agent
	Severity string // error | warning
	Code     string
	Path     string
	Message  string
	// Fatal is true when this error failed the whole load (strict root).
	Fatal bool
	// Fingerprint changes when the file changes (path, size, mtime), so a
	// frontend can warn once until the file is edited.
	Fingerprint string
}

// String formats a diagnostic for stderr and tests.
func (d LoadDiagnostic) String() string {
	return fmt.Sprintf("%s[%s]: %s %s: %s", d.Severity, d.Code, d.Kind, d.Path, d.Message)
}

// fileNote is a non-fatal finding from parsing one markdown file.
type fileNote struct {
	code string
	msg  string
}

// loadCollector gathers diagnostics during a load. A nil collector discards
// them, so the plain loaders keep their old behaviour and cost.
type loadCollector struct {
	kind  string
	diags []LoadDiagnostic
}

func (c *loadCollector) add(path, severity, code, msg string, fatal bool) {
	if c == nil {
		return
	}
	c.diags = append(c.diags, LoadDiagnostic{
		Kind:        c.kind,
		Severity:    severity,
		Code:        code,
		Path:        path,
		Message:     redact.String(msg),
		Fatal:       fatal,
		Fingerprint: fileFingerprint(path),
	})
}

// addNotes records per-file notes for path, found under source root (the
// discovery directory it was listed from; "" for plugin files). prefix is
// prepended to each message (for example "plugin acme.x: ").
func (c *loadCollector) addNotes(root, path, prefix string, notes []fileNote) {
	// Claude command files often have no frontmatter; a description is
	// optional there. Only the .claude/commands source root is exempt, not
	// any folder that happens to be named "commands".
	commands := isClaudeCommandsRoot(root)
	for _, n := range notes {
		if n.code == DiagMissingDescription && commands {
			continue
		}
		severity := "warning"
		if n.code == DiagUnreadable {
			severity = "error"
		}
		c.add(path, severity, n.code, prefix+n.msg, false)
	}
}

// isClaudeCommandsRoot reports whether root is a <dir>/.claude/commands
// discovery root.
func isClaudeCommandsRoot(root string) bool {
	if root == "" {
		return false
	}
	root = filepath.Clean(root)
	return filepath.Base(root) == "commands" && filepath.Base(filepath.Dir(root)) == ".claude"
}

func (c *loadCollector) addFatal(path string, err error) {
	c.add(path, "error", DiagInvalid, err.Error()+"; this error fails the whole "+c.kindLabel()+" load at daemon start or reload", true)
}

func (c *loadCollector) addSkipped(path string, err error) {
	c.add(path, "warning", DiagSkipped, err.Error()+"; the file was skipped", false)
}

// stderrf keeps the plain loaders' stderr warnings. A collecting load
// (the cockpit's rescan) returns load failures as diagnostics instead, so a
// page load does not repeat them on the daemon's stderr. Plugin name
// collisions are not load failures and are not reported there.
func (c *loadCollector) stderrf(format string, args ...any) {
	if c == nil {
		fmt.Fprintf(os.Stderr, format, args...)
	}
}

func (c *loadCollector) kindLabel() string {
	if c == nil {
		return ""
	}
	return c.kind
}

func hasNote(notes []fileNote, code string) bool {
	for _, n := range notes {
		if n.code == code {
			return true
		}
	}
	return false
}

func fileFingerprint(path string) string {
	h := sha256.New()
	h.Write([]byte(path))
	if info, err := os.Stat(path); err == nil {
		fmt.Fprintf(h, "\x00%d\x00%d", info.Size(), info.ModTime().UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// readMarkdownDoc reads a skill or agent file and splits its frontmatter.
// ok is false when the file is skipped (unreadable or empty body); notes
// say why. A file that opens with "---" but whose fences parseFrontmatter
// does not recognize still loads with the whole file as the body, as
// before, but is reported.
func readMarkdownDoc(path string) (meta map[string]string, body string, nested permission.Ruleset, notes []fileNote, ok bool) {
	// Refuse FIFOs, devices and sockets before opening: reading a FIFO
	// blocks, and a rescan runs on a page load (as harness/safefile does
	// for tools).
	if err := safefile.CheckLeaf(path, false); err != nil {
		return nil, "", nil, []fileNote{{code: DiagUnreadable, msg: "cannot read file: " + leafErrReason(err)}}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", nil, []fileNote{{code: DiagUnreadable, msg: "cannot read file: " + readErrReason(err)}}, false
	}
	text := string(data)
	meta, body, nested = parseFrontmatter(text)
	if meta == nil && strings.HasPrefix(text, "---") {
		notes = append(notes, fileNote{
			code: DiagMalformedFrontmatter,
			msg:  "frontmatter is not closed by a '---' line (or uses CRLF line endings); the whole file, fences included, is used as the body",
		})
	}
	if body == "" {
		notes = append(notes, fileNote{code: DiagEmpty, msg: "file has no body after frontmatter; it was skipped"})
		return meta, "", nested, notes, false
	}
	return meta, body, nested, notes, true
}

// leafErrReason names why safefile.CheckLeaf refused a path, without the
// path itself (the diagnostic already carries it).
func leafErrReason(err error) string {
	switch {
	case safefile.IsCode(err, safefile.CodeSpecialFile):
		return "not a regular file (FIFO, device, or socket)"
	case safefile.IsCode(err, safefile.CodeNotRegular):
		return "not a regular file"
	default:
		return readErrReason(err)
	}
}

func readErrReason(err error) string {
	switch {
	case os.IsPermission(err):
		return "permission denied"
	case os.IsNotExist(err):
		return "file does not exist"
	default:
		return redact.Error(err)
	}
}
