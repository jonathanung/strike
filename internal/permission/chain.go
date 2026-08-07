package permission

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// Tool-chain correlation (issue #891): content-free rolling state over the
// active turn so multi-step abuse can be asked/denied even when each hop
// alone would pass. No tool output bodies are stored.

// DefaultChainMaxNodes caps pending correlation nodes (unbounded-growth guard).
const DefaultChainMaxNodes = 64

// DefaultChainWindow is how many recent steps a rule may look back.
const DefaultChainWindow = 8

// DefaultChainRetryThreshold is identical denial count that trips retry-storm.
const DefaultChainRetryThreshold = 3

// PathClass is a content-free sensitivity/executability class for a path pattern.
type PathClass string

const (
	PathClassNormal     PathClass = "normal"
	PathClassSensitive  PathClass = "sensitive"
	PathClassExecutable PathClass = "executable"
)

// StepClass groups tools by effect class for correlation rules.
type StepClass string

const (
	StepClassRead    StepClass = "read"
	StepClassWrite   StepClass = "write"
	StepClassNetwork StepClass = "network"
	StepClassBash    StepClass = "bash"
	StepClassOther   StepClass = "other"
)

// Chain rule identifiers (stable for audit / docs).
const (
	ChainRuleSensitiveReadEgress = "sensitive_read_egress"
	ChainRuleWriteExecBash       = "write_exec_bash"
	ChainRuleRetryStorm          = "retry_storm"
)

// chainNode is one content-free observation in the rolling window.
type chainNode struct {
	seq       int
	tool      string // permission name (read, bash, webfetch, …)
	class     StepClass
	pathClass PathClass
	// pathKey is a normalized relative path for write→bash matching only.
	// Never included in user-facing reasons for sensitive classes.
	pathKey string
	// sig is permission + first pattern (for retry-storm identity).
	sig    string
	denied bool
}

// ChainHit is a matched correlation rule.
type ChainHit struct {
	Rule    string
	Action  Action // Ask or Deny
	ChainID string
	// Summary cites prior step tool names + classes only (no secret bytes).
	Summary string
	// Prior tools (name only) that contributed to the match, oldest first.
	PriorTools []string
}

// Correlator maintains content-free multi-step state for one permission Service.
// Safe for concurrent use.
type Correlator struct {
	mu sync.Mutex

	maxNodes       int
	window         int
	retryThreshold int

	nodes      []chainNode
	nextSeq    int
	nextChain  int
	turnActive bool
}

// NewCorrelator returns a correlator with default caps.
func NewCorrelator() *Correlator {
	return &Correlator{
		maxNodes:       DefaultChainMaxNodes,
		window:         DefaultChainWindow,
		retryThreshold: DefaultChainRetryThreshold,
	}
}

// BeginTurn clears rolling state for a new user turn.
func (c *Correlator) BeginTurn() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes = c.nodes[:0]
	c.nextSeq = 0
	c.turnActive = true
}

// EndTurn clears rolling state (turn complete or interrupt).
func (c *Correlator) EndTurn() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodes = c.nodes[:0]
	c.nextSeq = 0
	c.turnActive = false
}

// Reset is an alias for EndTurn (session teardown / tests).
func (c *Correlator) Reset() { c.EndTurn() }

// Len returns the number of retained nodes (tests / metrics).
func (c *Correlator) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.nodes)
}

// Check evaluates correlation rules for an incoming ask against prior state.
// Does not record the step. Returns nil when no rule matches.
func (c *Correlator) Check(permission string, patterns []string) *ChainHit {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.turnActive && len(c.nodes) == 0 {
		// Still allow check when nodes exist (tests without BeginTurn).
	}
	tool := strings.TrimSpace(permission)
	if tool == "" {
		return nil
	}
	class := classifyStep(tool)
	pathClass, pathKey := classifyPatterns(patterns)
	sig := stepSig(tool, patterns)

	// Rule order: hard deny first (retry storm), then ask-oriented chains.
	if hit := c.checkRetryStormLocked(tool, sig); hit != nil {
		return hit
	}
	if hit := c.checkWriteExecBashLocked(tool, class, patterns); hit != nil {
		return hit
	}
	if hit := c.checkSensitiveReadEgressLocked(tool, class, pathClass); hit != nil {
		return hit
	}
	_ = pathKey
	return nil
}

// Record appends a settled step (allow or deny). Caps at maxNodes.
func (c *Correlator) Record(permission string, patterns []string, denied bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tool := strings.TrimSpace(permission)
	if tool == "" {
		return
	}
	class := classifyStep(tool)
	pathClass, pathKey := classifyPatterns(patterns)
	// Only retain path keys needed for write→bash (executable writes).
	if class != StepClassWrite || pathClass != PathClassExecutable {
		pathKey = ""
	}
	c.nextSeq++
	n := chainNode{
		seq:       c.nextSeq,
		tool:      tool,
		class:     class,
		pathClass: pathClass,
		pathKey:   pathKey,
		sig:       stepSig(tool, patterns),
		denied:    denied,
	}
	c.nodes = append(c.nodes, n)
	if c.maxNodes > 0 && len(c.nodes) > c.maxNodes {
		c.nodes = c.nodes[len(c.nodes)-c.maxNodes:]
	}
}

func (c *Correlator) allocChainIDLocked() string {
	c.nextChain++
	return fmt.Sprintf("chain_%d", c.nextChain)
}

func (c *Correlator) checkRetryStormLocked(tool, sig string) *ChainHit {
	if c.retryThreshold < 1 || sig == "" {
		return nil
	}
	// Count trailing identical denials with same sig.
	n := 0
	var priors []string
	for i := len(c.nodes) - 1; i >= 0; i-- {
		node := c.nodes[i]
		if node.sig != sig || !node.denied {
			break
		}
		n++
		priors = append([]string{node.tool}, priors...)
	}
	// Trip when this call would be the Nth+ identical denial (prior >= threshold).
	// Also trip when prior denials already met threshold (keep blocking).
	if n < c.retryThreshold {
		return nil
	}
	id := c.allocChainIDLocked()
	return &ChainHit{
		Rule:       ChainRuleRetryStorm,
		Action:     Deny,
		ChainID:    id,
		Summary:    fmt.Sprintf("tool-chain %s: %d identical %s denials in-turn (retry storm)", id, n, tool),
		PriorTools: priors,
	}
}

func (c *Correlator) checkSensitiveReadEgressLocked(tool string, class StepClass, pathClass PathClass) *ChainHit {
	if class != StepClassNetwork && class != StepClassBash {
		return nil
	}
	// Look back within window for a non-denied sensitive read.
	start := 0
	if c.window > 0 && len(c.nodes) > c.window {
		start = len(c.nodes) - c.window
	}
	var priors []string
	found := false
	for i := start; i < len(c.nodes); i++ {
		node := c.nodes[i]
		if node.denied {
			continue
		}
		if node.class == StepClassRead && node.pathClass == PathClassSensitive {
			found = true
			priors = append(priors, node.tool+"(sensitive)")
		}
	}
	if !found {
		return nil
	}
	id := c.allocChainIDLocked()
	return &ChainHit{
		Rule:       ChainRuleSensitiveReadEgress,
		Action:     Ask,
		ChainID:    id,
		Summary:    fmt.Sprintf("tool-chain %s: read(sensitive) then %s within %d steps", id, tool, c.window),
		PriorTools: priors,
	}
}

func (c *Correlator) checkWriteExecBashLocked(tool string, class StepClass, patterns []string) *ChainHit {
	if class != StepClassBash {
		return nil
	}
	cmd := firstPatternRaw(patterns)
	if cmd == "" {
		return nil
	}
	start := 0
	if c.window > 0 && len(c.nodes) > c.window {
		start = len(c.nodes) - c.window
	}
	// Prefer "immediate" : last few writes; still scan window.
	var priors []string
	matchedPath := false
	for i := start; i < len(c.nodes); i++ {
		node := c.nodes[i]
		if node.denied || node.class != StepClassWrite || node.pathClass != PathClassExecutable {
			continue
		}
		if node.pathKey == "" {
			continue
		}
		if bashReferencesPath(cmd, node.pathKey) {
			matchedPath = true
			priors = append(priors, node.tool+"(executable)")
		}
	}
	if !matchedPath {
		return nil
	}
	id := c.allocChainIDLocked()
	return &ChainHit{
		Rule:       ChainRuleWriteExecBash,
		Action:     Ask,
		ChainID:    id,
		Summary:    fmt.Sprintf("tool-chain %s: write(executable) then bash execute of that path", id),
		PriorTools: priors,
	}
}

func stepSig(permission string, patterns []string) string {
	// Use raw first non-empty pattern for identity (not explain's "*" default).
	pat := ""
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p != "" {
			pat = p
			break
		}
	}
	return strings.TrimSpace(permission) + "\x00" + pat
}

func firstPatternRaw(patterns []string) string {
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p != "" {
			return p
		}
	}
	return ""
}

// classifyStep maps a permission/tool name to a step class.
func classifyStep(permission string) StepClass {
	switch strings.ToLower(strings.TrimSpace(permission)) {
	case "read", "glob", "grep":
		// glob/grep are discovery; only "read" counts as secret-bearing for rules.
		if strings.EqualFold(permission, "read") {
			return StepClassRead
		}
		return StepClassOther
	case "write", "edit", "apply_patch", "notebook_edit", "move", "delete":
		if strings.EqualFold(permission, "write") || strings.EqualFold(permission, "edit") {
			return StepClassWrite
		}
		return StepClassOther
	case "webfetch", "websearch":
		return StepClassNetwork
	case "bash":
		return StepClassBash
	default:
		// MCP network-ish tools are not classified without facts (non-goal).
		return StepClassOther
	}
}

// classifyPatterns returns the strongest path class and a normalized path key.
func classifyPatterns(patterns []string) (PathClass, string) {
	best := PathClassNormal
	key := ""
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" || p == "*" {
			continue
		}
		pc := ClassifyPath(p)
		if pc == PathClassSensitive {
			return PathClassSensitive, normalizePathKey(p)
		}
		if pc == PathClassExecutable && best != PathClassSensitive {
			best = PathClassExecutable
			key = normalizePathKey(p)
		}
		if key == "" && pc == PathClassNormal {
			// keep first path-like key for potential future rules
			if looksLikePath(p) {
				key = normalizePathKey(p)
			}
		}
	}
	return best, key
}

// ClassifyPath assigns a content-free path class from a permission pattern.
// Exported for tests and explain helpers.
func ClassifyPath(pattern string) PathClass {
	p := strings.TrimSpace(pattern)
	if p == "" {
		return PathClassNormal
	}
	// Bash commands are not paths.
	if strings.ContainsAny(p, " \t|&;<>") && !strings.Contains(p, "/") {
		return PathClassNormal
	}
	lower := strings.ToLower(filepath.ToSlash(p))
	base := strings.ToLower(path.Base(lower))

	if isSensitivePath(lower, base) {
		return PathClassSensitive
	}
	if isExecutablePath(lower, base) {
		return PathClassExecutable
	}
	return PathClassNormal
}

func isSensitivePath(lower, base string) bool {
	// Exact / suffix sensitive basenames.
	sensitiveBases := []string{
		".env", ".netrc", ".npmrc", ".pypirc", ".pgpass",
		"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519",
		"credentials", "credentials.json", "auth.json",
		"service-account.json", "secret.yaml", "secret.yml",
	}
	for _, s := range sensitiveBases {
		if base == s {
			return true
		}
	}
	// .env.* and *.env (but not .env.example as less sensitive — still flag .env*)
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	if strings.HasSuffix(base, ".env") && base != "example.env" {
		return true
	}
	// Extension-based secrets
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".jks"} {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	// Path segment heuristics (no raw content).
	sensitiveSeg := []string{
		"/secrets/", "/secret/", "/.ssh/", "/.aws/", "/.gnupg/",
		"/credentials/", "/private-keys/", "/private_keys/",
	}
	// Ensure leading slash form for segment match
	padded := lower
	if !strings.HasPrefix(padded, "/") {
		padded = "/" + padded
	}
	if !strings.HasSuffix(padded, "/") {
		padded = padded + "/"
	}
	for _, seg := range sensitiveSeg {
		if strings.Contains(padded, seg) {
			return true
		}
	}
	// kube config
	if strings.Contains(lower, "/.kube/config") || base == "kubeconfig" {
		return true
	}
	if strings.Contains(lower, "/.aws/credentials") {
		return true
	}
	// basename tokens (avoid matching "nonsecret")
	for _, tok := range []string{"secret", "credential", "password", "passwd", "token"} {
		if base == tok || strings.HasPrefix(base, tok+".") || strings.HasSuffix(base, "."+tok) ||
			strings.Contains(base, tok+"s.") || strings.Contains(base, "-"+tok) || strings.Contains(base, "_"+tok) {
			// Filter common false positives
			if strings.Contains(base, "nonsecret") || strings.Contains(base, "unsecret") {
				continue
			}
			return true
		}
	}
	return false
}

func isExecutablePath(lower, base string) bool {
	execExt := []string{
		".sh", ".bash", ".zsh", ".ksh", ".csh",
		".py", ".rb", ".pl", ".ps1", ".cmd", ".bat",
		".js", ".mjs", ".ts",
	}
	for _, ext := range execExt {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	// bin/ or scripts/ path segments
	padded := lower
	if !strings.HasPrefix(padded, "/") {
		padded = "/" + padded
	}
	if strings.Contains(padded, "/bin/") || strings.Contains(padded, "/scripts/") {
		// skip obvious non-exec docs under those dirs
		if strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".txt") || strings.HasSuffix(base, ".json") {
			return false
		}
		return true
	}
	return false
}

func looksLikePath(p string) bool {
	if strings.Contains(p, "/") || strings.Contains(p, `\`) {
		return true
	}
	// bare filename with extension
	return strings.Contains(p, ".") && !strings.ContainsAny(p, " \t")
}

func normalizePathKey(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	p = strings.TrimPrefix(p, "./")
	return path.Clean(p)
}

// bashReferencesPath reports whether a bash command likely executes or sources pathKey.
// Content-free: token/path match only, no shell evaluation.
func bashReferencesPath(command, pathKey string) bool {
	cmd := strings.TrimSpace(command)
	key := normalizePathKey(pathKey)
	if cmd == "" || key == "" {
		return false
	}
	base := path.Base(key)
	// Normalize command for simple contains checks.
	c := filepath.ToSlash(cmd)

	candidates := []string{
		key,
		"./" + key,
		base,
		"./" + base,
	}
	// Also try if key has directories — bash may use only basename.
	for _, cand := range candidates {
		if cand == "" || cand == "." {
			continue
		}
		if commandHasPathToken(c, cand) {
			return true
		}
	}
	return false
}

func commandHasPathToken(command, token string) bool {
	// Fast path: exact command is the path or ./path
	trim := strings.TrimSpace(command)
	if trim == token || trim == "./"+strings.TrimPrefix(token, "./") {
		return true
	}
	// Split on common shell metacharacters into rough tokens.
	fields := strings.FieldsFunc(command, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '|', '&', ';', '(', ')', '<', '>':
			return true
		default:
			return false
		}
	})
	for _, f := range fields {
		f = strings.Trim(f, "'\"")
		f = strings.TrimPrefix(f, "./")
		tok := strings.TrimPrefix(token, "./")
		if f == tok || f == token {
			return true
		}
		// bash script.sh / sh -c is separate; "bash path" / "sh path" / "source path"
	}
	// Patterns: bash <path>, sh <path>, source <path>, . <path>
	lower := strings.ToLower(command)
	for _, prefix := range []string{"bash ", "sh ", "zsh ", "source ", ". "} {
		if idx := strings.Index(lower, prefix); idx >= 0 {
			rest := strings.TrimSpace(command[idx+len(prefix):])
			// first arg
			arg := rest
			if sp := strings.IndexAny(rest, " \t|&;<>"); sp >= 0 {
				arg = rest[:sp]
			}
			arg = strings.Trim(arg, "'\"")
			arg = strings.TrimPrefix(filepath.ToSlash(arg), "./")
			tok := strings.TrimPrefix(token, "./")
			if arg == tok || path.Base(arg) == path.Base(tok) && path.Base(tok) != "" && path.Base(tok) != "." {
				// Require full key match or basename when unique enough
				if arg == tok || arg == path.Base(tok) {
					return true
				}
			}
		}
	}
	return false
}
