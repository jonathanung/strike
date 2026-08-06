package engine

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/ledger"
	"github.com/jonathanung/strike-cli/internal/memory"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// MemorySource is the engine-facing surface for auto-loading tagged project
// memory into the system prompt. *memory.Store satisfies this via List.
type MemorySource interface {
	List(tag string) ([]memory.Entry, error)
}

// LedgerSource is the engine-facing surface for auto-loading active decision
// ledger entries into the system prompt. *ledger.Store satisfies this via
// ActiveSlice.
type LedgerSource interface {
	ActiveSlice(path, taskID string) ([]ledger.Entry, error)
}

//go:embed prompt/shared.txt
var sharedPrompt string

//go:embed prompt/default.txt
var defaultProviderPrompt string

//go:embed prompt/anthropic.txt
var anthropicProviderPrompt string

//go:embed prompt/openai.txt
var openaiProviderPrompt string

//go:embed prompt/xai.txt
var xaiProviderPrompt string

//go:embed prompt/plan.txt
var planPrompt string

//go:embed prompt/lean_strict.txt
var leanStrictPrompt string

//go:embed prompt/lean_strict_full.txt
var leanStrictFullPrompt string

//go:embed prompt/lean_strategic.txt
var leanStrategicPrompt string

func normPrompt(s string) string {
	return strings.TrimSpace(s) + "\n"
}

// SharedSystemPrompt is the always-on baseline (identity, ADHD response
// contract, doing-tasks). Effective tool guidance is a separate layer.
var SharedSystemPrompt = normPrompt(sharedPrompt)

// DefaultSystemPrompt is shared + generic provider notes — used when no
// provider is selected yet and as the build-agent fallback content.
var DefaultSystemPrompt = joinPromptLayers(SharedSystemPrompt, normPrompt(defaultProviderPrompt))

// PlanSystemPrompt is the read-only plan overlay (composed with shared +
// provider at request time for the plan agent).
var PlanSystemPrompt = normPrompt(planPrompt)

// LeanStrictSystemPrompt is the implementer YAGNI ladder (build/general/debugger).
var LeanStrictSystemPrompt = normPrompt(leanStrictPrompt)

// LeanStrictFullSystemPrompt is the stronger implementer ladder (leanCode=full).
var LeanStrictFullSystemPrompt = normPrompt(leanStrictFullPrompt)

// LeanStrategicSystemPrompt is scaling-aware lean guidance (plan/orchestrator).
var LeanStrategicSystemPrompt = normPrompt(leanStrategicPrompt)

// Prompt composition layer model (system string only; conversation history is
// separate and inspected as message counts):
//
//  1. shared            append   builtin baseline
//  2. tools             append   effective registry guidance (name + purpose;
//                                agent/permission/depth/MCP aware)
//  3. overlay slot      replace  exactly one of: provider | config systemPrompt
//                                (build only) | agent persona
//  4. phase slot        replace  phase context, else plan overlay when agent
//                                is plan; neither when inactive
//  5. lean code         append   agent-scoped lean guidance when leanCode≠off
//  6. environment       append   cwd / model / date
//  7. instructions      append   each AGENTS.md/CLAUDE.md block
//  8. project memory    append   tagged entries (instruction|preference|
//                                project-convention), capped; untrusted
//  9. decision ledger   append   active decisions/assumptions/constraints,
//                                capped; untrusted (prefer ledger over prose)
//
// Skills are user-turn content (slash render → UserInput), not system layers.
// Untagged memory, other tags, and issues stay tool/turn-local (memory_read /
// issue_read). Full ledger history stays on ledger_read. @file attachments
// remain turn-local. Tool results live in provider message history.

// ProviderSystemPrompt returns the provider-specific overlay for a strike
// provider name and/or model id (opencode-style selection).
func ProviderSystemPrompt(provider, model string) string {
	switch providerPromptKind(provider, model) {
	case "anthropic":
		return normPrompt(anthropicProviderPrompt)
	case "openai":
		return normPrompt(openaiProviderPrompt)
	case "xai":
		return normPrompt(xaiProviderPrompt)
	default:
		return normPrompt(defaultProviderPrompt)
	}
}

func providerPromptKind(provider, model string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	m := strings.ToLower(strings.TrimSpace(model))
	switch p {
	case "anthropic":
		return "anthropic"
	case "openai", "chatgpt":
		return "openai"
	case "xai":
		return "xai"
	}
	switch {
	case strings.Contains(m, "claude"):
		return "anthropic"
	case strings.Contains(m, "grok"):
		return "xai"
	case strings.Contains(m, "gpt"),
		strings.Contains(m, "o1"),
		strings.Contains(m, "o3"),
		strings.Contains(m, "o4"):
		return "openai"
	default:
		return "default"
	}
}

func joinPromptLayers(layers ...string) string {
	parts := make([]string, 0, len(layers))
	for _, layer := range layers {
		if s := strings.TrimSpace(layer); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

// ResolveLeanCode maps config/engine LeanCode to off|lite|full.
// Empty and unknown values default to lite.
func ResolveLeanCode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "false", "0", "no", "never", "none":
		return "off"
	case "full", "on", "true", "1", "yes":
		return "full"
	case "lite", "light", "default", "":
		return "lite"
	default:
		return "lite"
	}
}

// LeanCodeStrength is the agent-scoped lean guidance tier, or empty when none.
// Exported for tests.
func LeanCodeStrength(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "", "build", "general", "debugger":
		return "strict"
	case "plan", "orchestrator":
		return "strategic"
	default:
		return ""
	}
}

// LeanCodePrompt returns the lean-code overlay text for a mode and agent.
// Empty when disabled or the agent is out of scope (explore/reviewer/…).
func LeanCodePrompt(mode, agent string) string {
	text, _ := leanCodeLayer(mode, agent)
	return text
}

func leanCodeLayer(mode, agent string) (text, source string) {
	resolved := ResolveLeanCode(mode)
	if resolved == "off" {
		return "", ""
	}
	strength := LeanCodeStrength(agent)
	switch strength {
	case "strict":
		if resolved == "full" {
			return LeanStrictFullSystemPrompt, "builtin:lean/strict+full"
		}
		return LeanStrictSystemPrompt, "builtin:lean/strict"
	case "strategic":
		return LeanStrategicSystemPrompt, "builtin:lean/strategic"
	default:
		return "", ""
	}
}

// promptLayer is one ordered system-prompt segment with provenance.
type promptLayer struct {
	Kind   string
	Source string
	Mode   string // append | replace
	Text   string
}

// composeSystemLayers returns the ordered raw composition before pin/exclude
// filters. Callers that need the model-facing set should use systemLayers.
func (e *Engine) composeSystemLayers() []promptLayer {
	layers := make([]promptLayer, 0, 8)
	layers = append(layers, promptLayer{
		Kind:   protocol.PromptLayerShared,
		Source: "builtin:shared",
		Mode:   protocol.PromptLayerAppend,
		Text:   SharedSystemPrompt,
	})
	layers = appendToolGuidanceLayer(e, layers)

	// Delegated children get explicit structured completion-handoff guidance.
	if e.opts.Depth > 0 {
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerTools,
			Source: "builtin:completion-handoff",
			Mode:   protocol.PromptLayerAppend,
			Text:   childHandoffSystemPrompt,
		})
		if !e.opts.ContextBundle.Empty() {
			layers = append(layers, promptLayer{
				Kind:   protocol.PromptLayerTools,
				Source: "builtin:context-bundle-guide",
				Mode:   protocol.PromptLayerAppend,
				Text:   childContextBundleSystemPrompt,
			})
			if body := formatContextBundlePromptLayer(e.opts.ContextBundle); body != "" {
				layers = append(layers, promptLayer{
					Kind:   protocol.PromptLayerTools,
					Source: "builtin:context-bundle",
					Mode:   protocol.PromptLayerAppend,
					Text:   body,
				})
			}
		}
	}

	switch {
	case e.agent.Name == "build" && strings.TrimSpace(e.opts.SystemPrompt) != "":
		// Config systemPrompt replaces the provider overlay for build only.
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerConfig,
			Source: "config:systemPrompt",
			Mode:   protocol.PromptLayerReplace,
			Text:   e.opts.SystemPrompt,
		})
	case strings.TrimSpace(e.agent.Prompt) != "":
		// Custom agent (or user-defined build/plan.md) supplies the persona layer.
		name := strings.TrimSpace(e.agent.Name)
		if name == "" {
			name = "agent"
		}
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerPersona,
			Source: "agent:" + name,
			Mode:   protocol.PromptLayerReplace,
			Text:   e.agent.Prompt,
		})
	default:
		kind := providerPromptKind(e.provName, e.model)
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerProvider,
			Source: "provider:" + kind,
			Mode:   protocol.PromptLayerReplace,
			Text:   ProviderSystemPrompt(e.provName, e.model),
		})
	}

	// Phase context (or built-in plan overlay while plan agent/phase is active).
	// Prefer the active phase layer so workflow files can replace the default
	// plan copy without corrupting conversation history.
	if phaseCtx := e.phaseContextPrompt(); phaseCtx != "" {
		source := "phase"
		if phase, ok := e.currentPhase(); ok {
			wf := strings.TrimSpace(e.workflow.Name)
			ph := strings.TrimSpace(phase.Name)
			switch {
			case wf != "" && ph != "":
				source = "phase:" + wf + "/" + ph
			case ph != "":
				source = "phase:" + ph
			case wf != "":
				source = "phase:" + wf
			}
		}
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerPhase,
			Source: source,
			Mode:   protocol.PromptLayerReplace,
			Text:   phaseCtx,
		})
	} else if e.agent.Name == "plan" {
		// Plan agent without an active phase still gets the read-only overlay.
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerPlan,
			Source: "builtin:plan",
			Mode:   protocol.PromptLayerReplace,
			Text:   PlanSystemPrompt,
		})
	}

	// After plan handoff, inject the exact approved plan for the implementer.
	if handoff := e.planHandoffPrompt(); handoff != "" {
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerPlan,
			Source: "plan:handoff",
			Mode:   protocol.PromptLayerAppend,
			Text:   handoff,
		})
	}

	if text, source := leanCodeLayer(e.opts.LeanCode, e.agent.Name); text != "" {
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerLean,
			Source: source,
			Mode:   protocol.PromptLayerAppend,
			Text:   text,
		})
	}

	if env := e.environmentPrompt(); env != "" {
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerEnvironment,
			Source: "env",
			Mode:   protocol.PromptLayerAppend,
			Text:   env,
		})
	}
	for _, inst := range e.opts.Instructions {
		if s := strings.TrimSpace(inst); s == "" {
			continue
		}
		source, _ := instructionProvenance(inst)
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerInstruction,
			Source: source,
			Mode:   protocol.PromptLayerAppend,
			Text:   inst,
		})
	}
	if text, source := e.projectMemoryLayer(); text != "" {
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerMemory,
			Source: source,
			Mode:   protocol.PromptLayerAppend,
			Text:   text,
		})
	}
	if text, source := e.decisionLedgerLayer(); text != "" {
		layers = append(layers, promptLayer{
			Kind:   protocol.PromptLayerLedger,
			Source: source,
			Mode:   protocol.PromptLayerAppend,
			Text:   text,
		})
	}
	return layers
}

// projectMemoryLayer builds the auto-loaded tagged memory segment. Empty when
// Memory is nil, list fails, or no eligible entries fit the cap.
func (e *Engine) projectMemoryLayer() (text, source string) {
	if e.opts.Memory == nil {
		return "", ""
	}
	text, omitted, err := memory.AutoLoadLayer(e.opts.Memory)
	if err != nil || strings.TrimSpace(text) == "" {
		return "", ""
	}
	source = "memory:autoload"
	if omitted > 0 {
		source = fmt.Sprintf("memory:autoload+omitted:%d", omitted)
	}
	return text, source
}

// decisionLedgerLayer builds the auto-loaded active ledger segment (full
// active slice; path/task filters stay on ledger_read). Empty when Ledger is
// nil or no active entries fit the cap.
func (e *Engine) decisionLedgerLayer() (text, source string) {
	if e.opts.Ledger == nil {
		return "", ""
	}
	// Empty path/task → all active entries (global + scoped). Callers that need
	// a scoped slice use ledger_read with path/task_id.
	text, omitted, err := ledger.AutoLoadLayer(e.opts.Ledger, "", "")
	if err != nil || strings.TrimSpace(text) == "" {
		return "", ""
	}
	source = "ledger:autoload"
	if omitted > 0 {
		source = fmt.Sprintf("ledger:autoload+omitted:%d", omitted)
	}
	return text, source
}

// system returns the composed system prompt for the next provider request.
func (e *Engine) system() string {
	return joinPromptLayerTexts(e.systemLayers())
}

func joinPromptLayerTexts(layers []promptLayer) string {
	parts := make([]string, 0, len(layers))
	for _, layer := range layers {
		if s := strings.TrimSpace(layer.Text); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n\n") + "\n"
}

func instructionProvenance(block string) (source, body string) {
	const prefix = "Instructions from: "
	s := strings.TrimSpace(block)
	if !strings.HasPrefix(s, prefix) {
		return "instruction", s
	}
	rest := strings.TrimPrefix(s, prefix)
	path, body, ok := strings.Cut(rest, "\n")
	path = strings.TrimSpace(path)
	if path == "" {
		return "instruction", s
	}
	if !ok {
		return "file:" + path, ""
	}
	return "file:" + path, strings.TrimSpace(body)
}

func (e *Engine) environmentPrompt() string {
	workDir := e.opts.WorkDir
	if workDir == "" {
		if wd, err := os.Getwd(); err == nil {
			workDir = wd
		}
	}
	root := e.opts.ProjectRoot
	if root == "" {
		root = workDir
	}
	isGit := "no"
	if root != "" {
		if st, err := os.Stat(filepath.Join(root, ".git")); err == nil && (st.IsDir() || st.Mode().IsRegular()) {
			isGit = "yes"
		}
	}
	modelLine := "You are powered by an unset model."
	if e.provName != "" || e.model != "" {
		id := e.model
		if e.provName != "" && e.model != "" {
			id = e.provName + "/" + e.model
		} else if e.provName != "" {
			id = e.provName
		}
		modelLine = fmt.Sprintf("You are powered by the model named %s. The exact model ID is %s", e.model, id)
	}
	return strings.TrimSpace(fmt.Sprintf(`%s
Here is some useful information about the environment you are running in:
<env>
  Working directory: %s
  Workspace root folder: %s
  Is directory a git repo: %s
  Platform: %s
  Today's date: %s
</env>
Each bash and path-based tool call starts in the working directory above. Shell cd inside one bash invocation does not persist to later tool calls.`, modelLine, workDir, root, isGit, runtime.GOOS, time.Now().Format("Mon Jan 2 2006")))
}

// effectiveSnapshot is a redacted inspect view of system composition plus
// estimate-labeled request-slice token attribution.
type effectiveSnapshot struct {
	Layers         []protocol.PromptLayerInfo
	System         string // exact joined system text last sent (or current)
	SystemChars    int
	MessageCount   int
	FromLastStream bool
	Attribution    protocol.RequestTokenAttribution
	ExcludedKinds  []string
	PinnedKinds    []string
	ShedKinds      []string
}

func (e *Engine) recordStreamEffective(layers []promptLayer, system string, tools []provider.ToolSchema, shedKinds []string) {
	snap := e.buildEffectiveSnapshot(layers, system, tools, e.messages, true, shedKinds)
	e.effectiveMu.Lock()
	e.lastEffective = snap
	e.effectiveMu.Unlock()
}

func (e *Engine) currentEffectiveSnapshot() effectiveSnapshot {
	layers, shed := e.systemLayersWithMeta()
	system := joinPromptLayerTexts(layers)
	tools, _ := e.effectiveToolSchemas()
	return e.buildEffectiveSnapshot(layers, system, tools, e.messages, false, shed)
}

func (e *Engine) lastOrCurrentEffective() effectiveSnapshot {
	e.effectiveMu.Lock()
	snap := e.lastEffective
	e.effectiveMu.Unlock()
	if snap.SystemChars > 0 || len(snap.Layers) > 0 || snap.Attribution.Total.Known {
		return snap
	}
	return e.currentEffectiveSnapshot()
}

func (e *Engine) buildEffectiveSnapshot(layers []promptLayer, system string, tools []provider.ToolSchema, msgs []provider.Message, fromStream bool, shedKinds []string) effectiveSnapshot {
	infos := make([]protocol.PromptLayerInfo, 0, len(layers))
	for _, layer := range layers {
		text := strings.TrimSpace(layer.Text)
		chars := utf8.RuneCountInString(text)
		infos = append(infos, protocol.PromptLayerInfo{
			Kind:      layer.Kind,
			Source:    redactSecrets(layer.Source),
			Mode:      layer.Mode,
			Chars:     chars,
			EstTokens: estTokensFromChars(chars),
			Pinned:    e.kindPinned(layer.Kind),
			Preview:   layerPreview(text),
		})
	}
	return effectiveSnapshot{
		Layers:         infos,
		System:         system,
		SystemChars:    utf8.RuneCountInString(strings.TrimSpace(system)),
		MessageCount:   len(msgs),
		FromLastStream: fromStream,
		Attribution:    estimateRequestAttribution(system, tools, msgs),
		ExcludedKinds:  sortedKindKeys(e.excludedKinds),
		PinnedKinds:    sortedKindKeys(e.pinnedKinds),
		ShedKinds:      append([]string(nil), shedKinds...),
	}
}

const layerPreviewRunes = 120

func layerPreview(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// Single-line preview for inspect UI.
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.Join(strings.Fields(text), " ")
	text = redactSecrets(text)
	if utf8.RuneCountInString(text) <= layerPreviewRunes {
		return text
	}
	runes := []rune(text)
	return string(runes[:layerPreviewRunes]) + "…"
}

// RedactSecrets replaces credential-shaped substrings with a placeholder.
// Delegates to pkg/redact (shared with timeline export, session scrub, #796).
// Exported for tests.
func RedactSecrets(s string) string {
	return redact.String(s)
}

func redactSecrets(s string) string {
	return redact.String(s)
}
