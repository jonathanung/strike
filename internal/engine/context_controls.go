package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// Fit-pressure thresholds as fractions of the known context window.
// Soft warn fires before hard provider overflow when possible.
const (
	fitWarnRatio     = 0.80
	fitCriticalRatio = 0.95
)

// optionalShedKinds may be auto-dropped under fit pressure when not pinned.
// Core layers (shared, tools, persona/provider/config, environment, phase/plan)
// are never auto-shed — only user exclude can drop those (except shared/tools/env).
var optionalShedKinds = map[string]struct{}{
	protocol.PromptLayerMemory:      {},
	protocol.PromptLayerLean:        {},
	protocol.PromptLayerInstruction: {},
}

// neverExcludeKinds cannot be user-excluded (baseline always present).
var neverExcludeKinds = map[string]struct{}{
	protocol.PromptLayerShared:      {},
	protocol.PromptLayerTools:       {},
	protocol.PromptLayerEnvironment: {},
}

// handleSetContextControls updates pin/exclude sets and emits confirmation.
func (e *Engine) handleSetContextControls(op protocol.SetContextControls) {
	if op.SetExclude {
		e.excludedKinds = normalizeKindSet(op.ExcludeKinds, true)
	}
	if op.SetPin {
		e.pinnedKinds = normalizeKindSet(op.PinKinds, false)
	}
	e.emit(protocol.ContextControlsSelected{
		Correlation:   e.sessionCorr(),
		ExcludedKinds: sortedKindKeys(e.excludedKinds),
		PinnedKinds:   sortedKindKeys(e.pinnedKinds),
	})
}

// normalizeKindSet trims, lowercases-preserving known kinds, drops empties and
// (when forExclude) never-exclude kinds.
func normalizeKindSet(kinds []string, forExclude bool) map[string]struct{} {
	if len(kinds) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(kinds))
	for _, k := range kinds {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if forExclude {
			if _, blocked := neverExcludeKinds[k]; blocked {
				continue
			}
		}
		out[k] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func sortedKindKeys(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (e *Engine) kindExcluded(kind string) bool {
	_, ok := e.excludedKinds[kind]
	return ok
}

func (e *Engine) kindPinned(kind string) bool {
	_, ok := e.pinnedKinds[kind]
	return ok
}

// filterContextLayers applies user exclude then optional fit-pressure shed.
// Returns the layers to send and the kinds auto-shed this composition.
func (e *Engine) filterContextLayers(layers []promptLayer, shedOptional bool) (out []promptLayer, shed []string) {
	out = make([]promptLayer, 0, len(layers))
	shedSet := make(map[string]struct{})
	for _, layer := range layers {
		if e.kindExcluded(layer.Kind) {
			continue
		}
		if shedOptional {
			if _, optional := optionalShedKinds[layer.Kind]; optional && !e.kindPinned(layer.Kind) {
				shedSet[layer.Kind] = struct{}{}
				continue
			}
		}
		out = append(out, layer)
	}
	return out, sortedKindKeys(shedSet)
}

// systemLayers returns the filtered composition for the next provider request.
// Under fit pressure, optional non-pinned layers are shed after user excludes.
func (e *Engine) systemLayers() []promptLayer {
	raw := e.composeSystemLayers()
	filtered, _ := e.filterContextLayers(raw, false)
	if !e.shouldShedOptional(filtered) {
		return filtered
	}
	shed, _ := e.filterContextLayers(raw, true)
	return shed
}

// systemLayersWithMeta returns composition plus shed kinds for inspect/stream.
func (e *Engine) systemLayersWithMeta() (layers []promptLayer, shedKinds []string) {
	raw := e.composeSystemLayers()
	filtered, _ := e.filterContextLayers(raw, false)
	if !e.shouldShedOptional(filtered) {
		return filtered, nil
	}
	return e.filterContextLayers(raw, true)
}

// shouldShedOptional reports whether optional non-pinned layers should drop
// under current occupancy pressure (known window only).
func (e *Engine) shouldShedOptional(layers []promptLayer) bool {
	window := e.contextWindow()
	if window <= 0 {
		return false
	}
	// Only shed when something optional+unpinned is present.
	hasOptional := false
	for _, layer := range layers {
		if _, ok := optionalShedKinds[layer.Kind]; ok && !e.kindPinned(layer.Kind) {
			hasOptional = true
			break
		}
	}
	if !hasOptional {
		return false
	}
	system := joinPromptLayerTexts(layers)
	tools, _ := e.effectiveToolSchemas()
	est := estimateRequestAttribution(system, tools, e.messages).Total
	if !est.Known || est.N <= 0 {
		return false
	}
	return float64(est.N) >= fitWarnRatio*float64(window)
}

// maybeEmitFitWarning emits a timeline ContextFitWarning when projected
// occupancy crosses soft budgets. Uses pre-shed composition so auto-shed
// does not hide the pressure signal. At most one warning per turn (highest
// level seen). Called after prune/compact, before Stream.
func (e *Engine) maybeEmitFitWarning(turnID string) {
	window := e.contextWindow()
	if window <= 0 {
		return
	}
	// Already warned at critical this turn — nothing higher to emit.
	if e.fitWarnedTurnID == turnID && e.fitWarnedLevel == protocol.ContextFitCritical {
		return
	}
	// Pre-shed (user excludes only) — warn on true pressure before optional drop.
	raw := e.composeSystemLayers()
	preShed, _ := e.filterContextLayers(raw, false)
	system := joinPromptLayerTexts(preShed)
	tools, _ := e.effectiveToolSchemas()
	attr := estimateRequestAttribution(system, tools, e.messages)
	if !attr.Total.Known || attr.Total.N <= 0 {
		return
	}
	used := attr.Total.N
	// Prefer provider-reported occupancy when higher (more conservative).
	if e.lastUsedKnown && e.lastUsed > used {
		used = e.lastUsed
	}
	ratio := float64(used) / float64(window)
	var level string
	switch {
	case ratio >= fitCriticalRatio:
		level = protocol.ContextFitCritical
	case ratio >= fitWarnRatio:
		level = protocol.ContextFitWarn
	default:
		return
	}
	// Skip duplicate same-or-lower level within the turn.
	if e.fitWarnedTurnID == turnID {
		if e.fitWarnedLevel == level || e.fitWarnedLevel == protocol.ContextFitCritical {
			return
		}
		// Only escalate warn → critical.
		if e.fitWarnedLevel == protocol.ContextFitWarn && level != protocol.ContextFitCritical {
			return
		}
	}
	corr := e.baseCorr()
	corr.TurnID = turnID
	pct := int(fitWarnRatio * 100)
	if level == protocol.ContextFitCritical {
		pct = int(fitCriticalRatio * 100)
	}
	msg := fmt.Sprintf("context fit %s: projected ~%s tok is ≥%d%% of the %s window (est.)",
		level, formatTokShort(used), pct, formatTokShort(window))
	// Note auto-shed when it will apply on the upcoming Stream.
	if _, shed := e.filterContextLayers(raw, true); len(shed) > 0 && e.shouldShedOptional(preShed) {
		msg += "; shedding " + strings.Join(shed, ", ")
	}
	e.fitWarnedTurnID = turnID
	e.fitWarnedLevel = level
	e.emit(protocol.ContextFitWarning{
		Correlation:     corr,
		EstimatedTokens: used,
		ContextLimit:    window,
		Level:           level,
		Message:         msg,
		Source:          protocol.UsageSourceEstimated,
	})
}

func formatTokShort(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
