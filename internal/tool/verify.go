package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	verifyDefaultTimeout   = 15 * time.Minute
	verifyMaxTimeout       = 30 * time.Minute
	verifyMaxProcessOutput = 256_000
	verifyMaxSnippetRunes  = 400
	verifyMaxSnippetLines  = 8
	verifyMaxFailures      = 50
)

// verifyInspectRel are the project docs this tool reads. Missing files are
// still listed in the coded error so the model does not invent a workflow.
var verifyInspectRel = []string{
	"AGENTS.md",
	"Makefile",
	"makefile",
	"GNUmakefile",
	filepath.Join(".github", "workflows", "ci.yml"),
}

var (
	verifyTickRe       = regexp.MustCompile("`([^`]+)`")
	verifyTierTokenRe  = regexp.MustCompile(`(?i)\b(?:tier\s+)?([ABC])\b`)
	verifyInheritRe    = regexp.MustCompile(`(?i)tier\s+([ABC])\b`)
	verifyFailTestRe   = regexp.MustCompile(`^--- FAIL: (\S+)`)
	verifyFailPkgTabRe = regexp.MustCompile(`^FAIL\t(\S+)`)
	verifyBareInvRe    = regexp.MustCompile(`(?:^|[^\w./-])(gofmt|go|make|golangci-lint|staticcheck|npm|yarn|pnpm|bun|cargo|pytest|python3|python)\s+[^;\n$'"()]+`)
)

// Known command verbs extracted from project docs. Paths and prose ticks are dropped.
var verifyCommandVerbs = map[string]struct{}{
	"gofmt": {}, "go": {}, "make": {}, "golangci-lint": {}, "staticcheck": {},
	"npm": {}, "yarn": {}, "pnpm": {}, "bun": {}, "npx": {},
	"cargo": {}, "pytest": {}, "python": {}, "python3": {},
	"just": {}, "task": {}, "mage": {}, "docker": {},
	"mvn": {}, "gradle": {}, "cmake": {}, "ninja": {},
}

type verifyTool struct {
	run verifyCommandRunner
}

// verifyCommandRunner executes one documented command. Tests inject a fake.
type verifyCommandRunner func(ctx context.Context, tc *Context, command string, timeout time.Duration) (ProcessResult, error)

// NewVerify returns the project verification tool. Deferred when deferTools is on.
func NewVerify() Tool { return verifyTool{} }

func (verifyTool) Name() string { return "verify" }

func (verifyTool) Contract() Contract {
	return staticContract(SideEffectProcess, IdempotencyUnsafe)
}

func (verifyTool) Description() string {
	return `Run a documented project verification tier (A/B/C) and return failures only.

Selects the gate from project docs (AGENTS.md, Makefile, CI). Does not invent a
test workflow. Passing runs stay compact (no full-suite dump). Failures include
package, test name, a bounded snippet, and the exact command used.

Usage notes:
  - tier is required: A, B, or C (from project verification docs).
  - Missing or unparseable docs return a coded error listing what was inspected.
  - No auto-fix and no focused rerun loop.
  - Optional timeoutMs (default 900000, max 1800000) covers the whole gate.
  - When deferred tool schemas are enabled, discover via toolsearch ("verify").`
}

func (verifyTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"tier": {"type": "string", "enum": ["A", "B", "C", "a", "b", "c"], "description": "Verification tier from project docs"},
			"timeoutMs": {"type": "integer", "description": "Timeout in milliseconds for the whole gate (default 900000, max 1800000)"}
		},
		"required": ["tier"]
	}`)
}

type verifyArgs struct {
	Tier      string `json:"tier"`
	TimeoutMs int    `json:"timeoutMs"`
}

type verifyFailure struct {
	Package string `json:"package,omitempty"`
	Test    string `json:"test,omitempty"`
	Snippet string `json:"snippet"`
	Command string `json:"command"`
}

type verifyPayload struct {
	OK        bool            `json:"ok"`
	Tier      string          `json:"tier"`
	Commands  []string        `json:"commands"`
	Failures  []verifyFailure `json:"failures"`
	Inspected []string        `json:"inspected,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
	Note      string          `json:"note,omitempty"`
}

type verifyDocs struct {
	Inspected []string
	Existing  []string
	Tiers     map[string][]string
}

func (t verifyTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	if tc == nil || strings.TrimSpace(tc.WorkDir) == "" {
		return Result{}, ErrPrecondition("work directory is empty")
	}
	var a verifyArgs
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &a); err != nil {
			return Result{}, ErrInvalidArgs("invalid arguments: " + err.Error())
		}
	}
	tier, err := normalizeVerifyTier(a.Tier)
	if err != nil {
		return Result{}, err
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "verify",
		Patterns:   []string{tier},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	docs := inspectVerifyDocs(tc.WorkDir)
	commands, err := selectTierCommands(docs, tier)
	if err != nil {
		return Result{}, err
	}

	timeout := verifyDefaultTimeout
	if a.TimeoutMs > 0 {
		timeout = min(time.Duration(a.TimeoutMs)*time.Millisecond, verifyMaxTimeout)
	}

	tc.MarkUncovered("verify")

	run := t.run
	if run == nil {
		run = runVerifyCommand
	}

	deadline := time.Now().Add(timeout)
	var failures []verifyFailure
	truncated := false
	for _, cmd := range commands {
		if err := ctx.Err(); err != nil {
			return Result{}, ErrCanceled("verify canceled")
		}
		remain := time.Until(deadline)
		if remain <= 0 {
			return Result{}, ErrTimeout("verify timed out")
		}
		proc, err := run(ctx, tc, cmd, remain)
		if err != nil {
			return Result{}, err
		}
		switch proc.Status {
		case ProcessStatusTimeout:
			return Result{}, ErrTimeout("verify timed out running " + cmd)
		case ProcessStatusCanceled:
			return Result{}, ErrCanceled("verify canceled")
		}
		got := parseVerifyFailures(cmd, proc.Output, proc.ExitCode)
		if proc.Truncated {
			truncated = true
		}
		failures = append(failures, got...)
		if len(failures) > verifyMaxFailures {
			failures = failures[:verifyMaxFailures]
			truncated = true
			break
		}
	}

	payload := verifyPayload{
		OK:        len(failures) == 0,
		Tier:      tier,
		Commands:  commands,
		Failures:  failures,
		Inspected: docs.Existing,
		Truncated: truncated,
	}
	if payload.Failures == nil {
		payload.Failures = []verifyFailure{}
	}
	return verifyResult(payload)
}

func normalizeVerifyTier(raw string) (string, error) {
	tier := strings.ToUpper(strings.TrimSpace(raw))
	switch tier {
	case "A", "B", "C":
		return tier, nil
	case "":
		return "", ErrInvalidArgs("tier is required")
	default:
		return "", ErrInvalidArgs("tier must be A, B, or C")
	}
}

func inspectVerifyDocs(workDir string) verifyDocs {
	docs := verifyDocs{Tiers: map[string][]string{}}
	var ciText, makeText string
	for _, rel := range verifyInspectRel {
		docs.Inspected = append(docs.Inspected, rel)
		body, ok := readVerifyDoc(workDir, rel)
		if !ok {
			continue
		}
		docs.Existing = append(docs.Existing, rel)
		base := filepath.Base(rel)
		switch {
		case strings.EqualFold(base, "AGENTS.md"):
			parsed := parseVerifyTiers(body)
			for tier, cmds := range parsed {
				if len(docs.Tiers[tier]) == 0 {
					docs.Tiers[tier] = cmds
				}
			}
		case strings.EqualFold(base, "ci.yml"):
			ciText = body
		case strings.EqualFold(base, "Makefile"), strings.EqualFold(base, "makefile"), strings.EqualFold(base, "GNUmakefile"):
			if makeText == "" {
				makeText = body
			}
		}
	}
	enrich := collectDocumentedInvocations(ciText + "\n" + makeText)
	for tier, cmds := range docs.Tiers {
		docs.Tiers[tier] = enrichVerifyCommands(cmds, enrich)
	}
	return docs
}

func readVerifyDoc(workDir, rel string) (string, bool) {
	abs, _, err := resolveInWorkspace(workDir, rel)
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", false
	}
	return string(data), true
}

func selectTierCommands(docs verifyDocs, tier string) ([]string, error) {
	tier, err := normalizeVerifyTier(tier)
	if err != nil {
		return nil, err
	}
	cmds := uniqueNonEmpty(docs.Tiers[tier])
	if len(cmds) == 0 {
		return nil, missingVerifyDocsError(docs, tier)
	}
	return cmds, nil
}

func missingVerifyDocsError(docs verifyDocs, tier string) *CodedError {
	var b strings.Builder
	b.WriteString("no documented verification commands for tier ")
	b.WriteString(tier)
	b.WriteString("; inspected ")
	if len(docs.Inspected) == 0 {
		b.WriteString("(none)")
	} else {
		b.WriteString(strings.Join(docs.Inspected, ", "))
	}
	if len(docs.Existing) == 0 {
		b.WriteString(" (none present)")
	} else {
		b.WriteString(" (found ")
		b.WriteString(strings.Join(docs.Existing, ", "))
		b.WriteString(")")
	}
	b.WriteString("; will not guess a test command")
	return ErrPrecondition(b.String())
}

func parseVerifyTiers(md string) map[string][]string {
	cells := map[string]string{}
	for _, line := range splitLines(md) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			continue
		}
		fields := splitMarkdownRow(line)
		if len(fields) < 2 {
			continue
		}
		if isMarkdownSeparator(fields) {
			continue
		}
		tier := tierFromTableCell(fields[0])
		if tier == "" {
			continue
		}
		gate := fields[len(fields)-1]
		if _, exists := cells[tier]; !exists {
			cells[tier] = gate
		}
	}
	cmds := map[string][]string{}
	inherit := map[string]string{}
	for tier, cell := range cells {
		cmds[tier] = extractVerifyCommands(cell)
		if m := verifyInheritRe.FindStringSubmatch(cell); m != nil {
			parent := strings.ToUpper(m[1])
			if parent != tier {
				inherit[tier] = parent
			}
		}
	}
	// Resolve "Tier B +" so C includes B's commands first.
	seen := map[string]bool{}
	var resolve func(string) []string
	resolve = func(tier string) []string {
		if seen[tier] {
			return cmds[tier]
		}
		seen[tier] = true
		parent, ok := inherit[tier]
		if !ok {
			return cmds[tier]
		}
		return uniqueNonEmpty(append(append([]string{}, resolve(parent)...), cmds[tier]...))
	}
	out := map[string][]string{}
	for tier := range cmds {
		out[tier] = resolve(tier)
	}
	return out
}

func splitMarkdownRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func isMarkdownSeparator(fields []string) bool {
	if len(fields) == 0 {
		return false
	}
	for _, f := range fields {
		s := strings.TrimSpace(f)
		s = strings.Trim(s, ":")
		s = strings.ReplaceAll(s, "-", "")
		if s != "" {
			return false
		}
	}
	return true
}

func tierFromTableCell(cell string) string {
	plain := strings.NewReplacer("*", "", "`", "", "_", "").Replace(strings.TrimSpace(cell))
	plain = strings.TrimSpace(plain)
	if plain == "A" || plain == "B" || plain == "C" {
		return plain
	}
	if m := verifyTierTokenRe.FindStringSubmatch(plain); m != nil && strings.EqualFold(strings.TrimSpace(plain), m[0]) {
		return strings.ToUpper(m[1])
	}
	return ""
}

func extractVerifyCommands(cell string) []string {
	var out []string
	for _, m := range verifyTickRe.FindAllStringSubmatch(cell, -1) {
		raw := strings.TrimSpace(m[1])
		for _, part := range strings.Split(raw, "&&") {
			part = strings.TrimSpace(part)
			if isVerifyCommand(part) {
				out = append(out, part)
			}
		}
	}
	return uniqueNonEmpty(out)
}

func isVerifyCommand(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	fields := strings.Fields(s)
	first := fields[0]
	if strings.HasPrefix(first, ".") || strings.HasPrefix(first, "/") || strings.HasPrefix(first, "-") {
		return false
	}
	if strings.HasSuffix(first, "/") {
		return false
	}
	if strings.Contains(first, "/") {
		return false
	}
	if ext := filepath.Ext(first); ext != "" {
		return false
	}
	_, ok := verifyCommandVerbs[first]
	return ok
}

func collectDocumentedInvocations(text string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(text) == "" {
		return out
	}
	for _, m := range verifyBareInvRe.FindAllStringSubmatch(text, -1) {
		full := strings.TrimSpace(m[0])
		full = strings.TrimLeft(full, " \t$(")
		fields := strings.Fields(full)
		if len(fields) < 2 {
			continue
		}
		verb := fields[0]
		if _, ok := verifyCommandVerbs[verb]; !ok {
			continue
		}
		inv := strings.Join(fields, " ")
		inv = strings.TrimRight(inv, ",;:")
		if !isVerifyCommand(inv) {
			continue
		}
		if _, exists := out[verb]; !exists {
			out[verb] = inv
		}
	}
	return out
}

func enrichVerifyCommands(cmds []string, invocations map[string]string) []string {
	if len(cmds) == 0 || len(invocations) == 0 {
		return cmds
	}
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		if len(strings.Fields(c)) == 1 {
			if full, ok := invocations[c]; ok {
				out = append(out, full)
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func uniqueNonEmpty(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func parseVerifyFailures(command, output string, exitCode int) []verifyFailure {
	if exitCode == 0 {
		return nil
	}
	var out []verifyFailure
	var pending *verifyFailure
	var snippet []string

	flush := func() {
		if pending == nil {
			return
		}
		pending.Snippet = boundVerifySnippet(strings.Join(snippet, "\n"))
		out = append(out, *pending)
		pending = nil
		snippet = nil
	}

	for _, line := range splitLines(output) {
		if m := verifyFailTestRe.FindStringSubmatch(line); m != nil {
			flush()
			pending = &verifyFailure{Test: m[1], Command: command}
			snippet = []string{line}
			continue
		}
		if m := verifyFailPkgTabRe.FindStringSubmatch(line); m != nil {
			pkg := m[1]
			if pending != nil {
				pending.Package = pkg
				flush()
				continue
			}
			assigned := false
			for i := range out {
				if out[i].Package == "" && out[i].Command == command {
					out[i].Package = pkg
					assigned = true
				}
			}
			if !assigned {
				out = append(out, verifyFailure{
					Package: pkg,
					Snippet: boundVerifySnippet(line),
					Command: command,
				})
			}
			continue
		}
		if pending != nil {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "FAIL") ||
				strings.HasPrefix(line, "ok\t") || strings.HasPrefix(line, "PASS") {
				flush()
				continue
			}
			if trim != "" && len(snippet) < verifyMaxSnippetLines {
				snippet = append(snippet, line)
			}
		}
	}
	flush()

	if len(out) == 0 {
		out = append(out, verifyFailure{
			Snippet: boundVerifySnippet(failureExcerpt(output)),
			Command: command,
		})
	}
	if len(out) > verifyMaxFailures {
		return out[:verifyMaxFailures]
	}
	return out
}

func failureExcerpt(output string) string {
	lines := splitLines(output)
	if len(lines) > verifyMaxSnippetLines {
		lines = lines[len(lines)-verifyMaxSnippetLines:]
	}
	return strings.Join(lines, "\n")
}

func boundVerifySnippet(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	lines := splitLines(s)
	if len(lines) > verifyMaxSnippetLines {
		lines = lines[:verifyMaxSnippetLines]
		s = strings.Join(lines, "\n")
	}
	return clipRunes(s, verifyMaxSnippetRunes)
}

func runVerifyCommand(ctx context.Context, tc *Context, command string, timeout time.Duration) (ProcessResult, error) {
	lease, err := acquireBashLease(ctx, tc, command)
	if err != nil {
		return ProcessResult{}, err
	}
	defer lease.Release()

	env, err := bashEnv(tc)
	if err != nil {
		return ProcessResult{}, ErrInvalidArgs(err.Error())
	}
	if timeout <= 0 {
		timeout = verifyDefaultTimeout
	}
	return RunProcess(ctx, ProcessSpec{
		Argv:      []string{"bash", "-c", command},
		Dir:       tc.WorkDir,
		Env:       env,
		Timeout:   timeout,
		MaxOutput: verifyMaxProcessOutput,
		Combine:   true,
		Sandbox:   bashSandboxPolicy(tc),
	}, tc.Process)
}

func verifyResult(payload verifyPayload) (Result, error) {
	raw, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("encode verify: %w", err)
	}
	meta, _ := json.Marshal(payload)
	return Result{
		Title:    verifyTitle(payload),
		Output:   string(raw) + "\n",
		Metadata: meta,
	}, nil
}

func verifyTitle(p verifyPayload) string {
	if p.OK {
		n := len(p.Commands)
		noun := "command"
		if n != 1 {
			noun = "commands"
		}
		return fmt.Sprintf("verify %s: passed (%d %s)", p.Tier, n, noun)
	}
	n := len(p.Failures)
	noun := "failure"
	if n != 1 {
		noun = "failures"
	}
	title := fmt.Sprintf("verify %s: %d %s", p.Tier, n, noun)
	if p.Truncated {
		title += " truncated"
	}
	return title
}
