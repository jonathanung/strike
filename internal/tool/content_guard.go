package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// ContentGuard modes (config contentGuard.mode). Empty means default.
const (
	ContentGuardModeOff     = "off"
	ContentGuardModeDefault = "default"
	ContentGuardModeAsk     = "ask"
	ContentGuardModeDeny    = "deny"
)

// Content finding kinds and severities.
const (
	ContentKindCredential    = "credential"
	ContentKindDangerousSink = "dangerous_sink"

	ContentSeverityDeny = "deny"
	ContentSeverityAsk  = "ask"
)

// Permission name used when content-guard severity is ask (allow-once / path allow).
const PermissionContentGuard = "content_guard"

// ContentGuardSettings is the write-time content scanner dial on tool.Context
// (from config contentGuard + managed ceiling). Zero value enables default
// posture (credential deny, dangerous-sink ask).
type ContentGuardSettings struct {
	// Mode is off|default|ask|deny. Empty means default.
	Mode string
	// PathAllow is doublestar globs over slash-normalized relative paths that
	// skip the guard (project/global config). Empty means no path skips.
	PathAllow []string
	// ForcedDeny is set when managed/MDM locks contentGuard to deny so session
	// grants and yolo cannot widen credential/sink blocks.
	ForcedDeny bool
}

// contentFinding is one write-guard hit (credential or dangerous sink).
type contentFinding struct {
	RuleID   string `json:"ruleId"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
}

// NormalizeContentGuardMode returns the canonical mode token.
// Unknown non-empty values fall back to default.
func NormalizeContentGuardMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ContentGuardModeDefault:
		return ContentGuardModeDefault
	case ContentGuardModeOff:
		return ContentGuardModeOff
	case ContentGuardModeAsk:
		return ContentGuardModeAsk
	case ContentGuardModeDeny:
		return ContentGuardModeDeny
	default:
		return ContentGuardModeDefault
	}
}

// checkContentGuard scans proposed file content before it reaches disk.
// rel is the workspace-relative path (slash form preferred). On deny returns
// *CodedError with CodeContentGuardDenied. On ask severity, raises a
// content_guard permission ask (yolo may upgrade; managed ForcedDeny never).
// Safe on a nil Context (uses default settings, no ask → deny on ask severity).
func checkContentGuard(ctx context.Context, tc *Context, rel, content string) error {
	settings := ContentGuardSettings{}
	if tc != nil {
		settings = tc.ContentGuard
	}
	mode := NormalizeContentGuardMode(settings.Mode)
	if settings.ForcedDeny {
		mode = ContentGuardModeDeny
	}
	if mode == ContentGuardModeOff && !settings.ForcedDeny {
		return nil
	}
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	// pathAllow is an operator escape for false positives. Managed ForcedDeny
	// is a hard ceiling — project pathAllow must not widen it.
	if !settings.ForcedDeny && contentGuardPathAllowed(rel, settings.PathAllow) {
		return nil
	}

	findings := scanContentFindings(content)
	if len(findings) == 0 {
		return nil
	}

	// Apply mode: default keeps per-rule severity; ask upgrades deny→ask;
	// deny upgrades ask→deny. ForcedDeny already forced mode=deny.
	for i := range findings {
		switch mode {
		case ContentGuardModeAsk:
			findings[i].Severity = ContentSeverityAsk
		case ContentGuardModeDeny:
			findings[i].Severity = ContentSeverityDeny
		}
	}

	// Prefer deny findings first (hard block before any ask).
	var denies, asks []contentFinding
	for _, f := range findings {
		if f.Severity == ContentSeverityDeny {
			denies = append(denies, f)
		} else {
			asks = append(asks, f)
		}
	}
	if len(denies) > 0 {
		return contentGuardDeniedError(rel, denies)
	}
	if len(asks) == 0 {
		return nil
	}
	return contentGuardAsk(ctx, tc, rel, asks)
}

func contentGuardDeniedError(rel string, findings []contentFinding) error {
	primary := findings[0]
	msg := fmt.Sprintf("content guard denied writing %s: %s (rule %s)",
		relOrFile(rel), primary.Reason, primary.RuleID)
	if len(findings) > 1 {
		msg += fmt.Sprintf(" (+%d more)", len(findings)-1)
	}
	details, _ := json.Marshal(map[string]any{
		"path":     rel,
		"findings": findings,
	})
	return &CodedError{
		Code:      CodeContentGuardDenied,
		Message:   msg,
		Retryable: false,
		Details:   details,
	}
}

func contentGuardAsk(ctx context.Context, tc *Context, rel string, findings []contentFinding) error {
	if tc == nil || tc.Ask == nil {
		// No interactive path: fail closed on ask-severity hits.
		return contentGuardDeniedError(rel, findings)
	}
	primary := findings[0]
	meta, _ := json.Marshal(map[string]any{
		"path":     rel,
		"findings": findings,
		"guard":    "content",
	})
	// Pattern encodes path + rule so always-grants can be path-scoped.
	// Always persists the path so subsequent writes to the same file skip asks
	// after an explicit allow (audit via permission.decided).
	pat := rel
	if pat == "" {
		pat = primary.RuleID
	}
	return tc.Ask(ctx, AskRequest{
		Permission: PermissionContentGuard,
		Patterns:   []string{pat},
		Always:     []string{pat},
		Metadata:   meta,
	})
}

func relOrFile(rel string) string {
	if rel == "" {
		return "(file)"
	}
	return rel
}

func contentGuardPathAllowed(rel string, globs []string) bool {
	if rel == "" || len(globs) == 0 {
		return false
	}
	rel = filepath.ToSlash(rel)
	base := filepath.Base(rel)
	for _, g := range globs {
		g = filepath.ToSlash(strings.TrimSpace(g))
		if g == "" {
			continue
		}
		if ok, _ := doublestar.Match(g, rel); ok {
			return true
		}
		if ok, _ := doublestar.Match(g, base); ok {
			return true
		}
	}
	return false
}

// scanContentFindings runs credential + dangerous-sink scanners.
func scanContentFindings(content string) []contentFinding {
	if content == "" {
		return nil
	}
	var out []contentFinding
	for _, f := range redact.Findings(content) {
		out = append(out, contentFinding{
			RuleID:   f.RuleID,
			Kind:     ContentKindCredential,
			Severity: ContentSeverityDeny, // default posture
			Reason:   credentialReason(f.RuleID),
		})
	}
	for _, f := range scanDangerousSinks(content) {
		out = append(out, f)
	}
	return out
}

func credentialReason(ruleID string) string {
	switch ruleID {
	case redact.RulePEMPrivateKey:
		return "PEM private key material"
	case redact.RuleAWSAccessKeyID:
		return "AWS access key id"
	case redact.RuleAnthropicKey, redact.RuleOpenAIKey, redact.RuleXAIKey:
		return "provider API key shape"
	case redact.RuleGitHubToken:
		return "GitHub token shape"
	case redact.RuleSlackToken:
		return "Slack token shape"
	default:
		return "credential-shaped content (" + ruleID + ")"
	}
}

// Dangerous sink rule ids (strike-owned; language-limited v1).
const (
	RuleSinkPythonEval       = "sink_python_eval_exec"
	RuleSinkPythonOSSystem   = "sink_python_os_system"
	RuleSinkPythonSubprocess = "sink_python_subprocess_shell"
	RuleSinkJSEval           = "sink_js_eval"
	RuleSinkJSChildProcess   = "sink_js_child_process"
	RuleSinkJSNewFunction    = "sink_js_new_function"
)

type sinkRule struct {
	id     string
	re     *regexp.Regexp
	reason string
}

// High-confidence dangerous sinks only — prefer false negatives over blocking
// ordinary application code. Severity is ask under default mode.
var dangerousSinkRules = []sinkRule{
	{id: RuleSinkPythonEval, re: regexp.MustCompile(`(?m)(?:^|[^.\w])(?:eval|exec)\s*\(`), reason: "Python eval/exec call"},
	{id: RuleSinkPythonOSSystem, re: regexp.MustCompile(`\bos\.system\s*\(`), reason: "Python os.system call"},
	{id: RuleSinkPythonSubprocess, re: regexp.MustCompile(`\bsubprocess\.(?:call|run|Popen)\s*\([^)]*shell\s*=\s*True`), reason: "Python subprocess with shell=True"},
	{id: RuleSinkJSEval, re: regexp.MustCompile(`(?m)(?:^|[^\w$.])eval\s*\(`), reason: "JavaScript eval call"},
	{id: RuleSinkJSNewFunction, re: regexp.MustCompile(`\bnew\s+Function\s*\(`), reason: "JavaScript new Function(...)"},
	{id: RuleSinkJSChildProcess, re: regexp.MustCompile(`\b(?:child_process|require\s*\(\s*['"]child_process['"]\s*\))\s*\.\s*(?:exec|execSync|spawn)\s*\(`), reason: "Node child_process exec/spawn"},
}

func scanDangerousSinks(content string) []contentFinding {
	var out []contentFinding
	seen := make(map[string]struct{}, len(dangerousSinkRules))
	for _, r := range dangerousSinkRules {
		if !r.re.MatchString(content) {
			continue
		}
		if _, ok := seen[r.id]; ok {
			continue
		}
		seen[r.id] = struct{}{}
		out = append(out, contentFinding{
			RuleID:   r.id,
			Kind:     ContentKindDangerousSink,
			Severity: ContentSeverityAsk,
			Reason:   r.reason,
		})
	}
	return out
}
