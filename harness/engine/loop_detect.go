package engine

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// toolLoopReason tokens on ToolLoopDetected / stopReason mapping.
const (
	toolLoopIdentical   = "identical_calls"
	toolLoopOscillating = "oscillating_failures"
)

// toolCallObs is one settled tool observation for loop detection.
type toolCallObs struct {
	sig  string // name + args fingerprint
	name string
	ok   bool
	code string
}

// toolLoopDetector tracks recent tool outcomes within one turn and trips when
// the model repeats identical failing calls or oscillates between two failures.
type toolLoopDetector struct {
	threshold int
	window    int
	recent    []toolCallObs
	tripped   bool
	reason    string
	toolName  string
	count     int
}

func newToolLoopDetector(threshold, window int) *toolLoopDetector {
	if threshold < 1 {
		threshold = tool.DefaultToolLoopThreshold
	}
	if window < threshold {
		window = threshold * 2
	}
	if window < tool.DefaultToolLoopWindow {
		window = tool.DefaultToolLoopWindow
	}
	return &toolLoopDetector{threshold: threshold, window: window}
}

func (d *toolLoopDetector) reset() {
	if d == nil {
		return
	}
	d.recent = d.recent[:0]
	d.tripped = false
	d.reason = ""
	d.toolName = ""
	d.count = 0
}

// observe records a settled tool call. ok=true clears consecutive-fail pressure
// for that signature but still occupies the window (for oscillation).
func (d *toolLoopDetector) observe(name string, args json.RawMessage, ok bool, code string) (tripped bool, reason string, count int) {
	if d == nil {
		return false, "", 0
	}
	if d.tripped {
		return true, d.reason, d.count
	}
	name = strings.TrimSpace(name)
	sig := tool.CallSignature(name, args)
	obs := toolCallObs{sig: sig, name: name, ok: ok, code: strings.TrimSpace(code)}
	d.recent = append(d.recent, obs)
	if len(d.recent) > d.window {
		d.recent = d.recent[len(d.recent)-d.window:]
	}

	// Identical failing tool+args: count trailing failures with same sig.
	if !ok {
		n := 0
		for i := len(d.recent) - 1; i >= 0; i-- {
			r := d.recent[i]
			if r.sig != sig {
				break
			}
			if r.ok {
				break
			}
			n++
		}
		if n >= d.threshold {
			d.tripped = true
			d.reason = toolLoopIdentical
			d.toolName = name
			d.count = n
			return true, d.reason, d.count
		}
	}

	// Oscillation: last 2*threshold observations alternate between two distinct
	// failing signatures (A B A B …) with no successes.
	need := d.threshold * 2
	if need >= 4 && len(d.recent) >= need {
		tail := d.recent[len(d.recent)-need:]
		a, b := tail[0], tail[1]
		if !a.ok && !b.ok && a.sig != b.sig {
			osc := true
			for i, r := range tail {
				want := a
				if i%2 == 1 {
					want = b
				}
				if r.ok || r.sig != want.sig {
					osc = false
					break
				}
			}
			if osc {
				d.tripped = true
				d.reason = toolLoopOscillating
				d.toolName = a.name + "|" + b.name
				d.count = need
				return true, d.reason, d.count
			}
		}
	}
	return false, "", 0
}

// wouldTrip reports whether observing this failing call would trip the detector
// (used to short-circuit Execute when the model re-issues a looping call).
func (d *toolLoopDetector) wouldTrip(name string, args json.RawMessage) bool {
	if d == nil || d.tripped {
		return d != nil && d.tripped
	}
	// Clone-lite: count trailing identical fails for this sig.
	sig := tool.CallSignature(strings.TrimSpace(name), args)
	n := 0
	for i := len(d.recent) - 1; i >= 0; i-- {
		r := d.recent[i]
		if r.sig != sig {
			break
		}
		if r.ok {
			break
		}
		n++
	}
	return n+1 >= d.threshold
}

func (e *Engine) toolRetryDelay(nextAttempt int) time.Duration {
	if e.opts.ToolRetryBackoff != nil {
		return e.opts.ToolRetryBackoff(nextAttempt)
	}
	return tool.ToolRetryDelay(nextAttempt, tool.DefaultToolRetryBaseDelay, tool.DefaultToolRetryMaxDelay)
}

func (e *Engine) noteToolLoop(corr protocol.Correlation, name, reason string, count int) {
	if reason == "" {
		reason = toolLoopIdentical
	}
	msg := "repeated identical failing tool calls"
	if reason == toolLoopOscillating {
		msg = "oscillating failing tool calls"
	}
	e.toolLoopStop = reason
	e.emit(protocol.ToolLoopDetected{
		Correlation: corr,
		Reason:      reason,
		ToolName:    name,
		Count:       count,
		Message:     msg,
	})
}
