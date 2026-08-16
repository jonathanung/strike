package scheduler

import (
	"fmt"
	"strings"
)

// Effective is the compiled, process-local scheduler policy after layering
// global and project config.
//
// Limits apply only inside one Strike OS process; separate Strike programs do
// not coordinate leases or share capacity.
type Effective struct {
	// Limits is the merged per-pool capacity map (positive ints only).
	// Omitted pools are unlimited.
	Limits Limits
	// Rules is the ordered, compiled command classification list
	// (global then project; last match wins at Classify time).
	Rules []CompiledRule
}

// Compile builds an Effective policy from merged limits and ordered rules.
// source is a fallback provenance label for rules that lack CommandRule.Source
// (normally each rule is stamped at config load).
//
// Validation failures name the offending pool or rule index and source so
// callers can fail before engine startup.
func Compile(limits Limits, rules []CommandRule, source string) (*Effective, error) {
	if err := ValidateLimits(limits, source); err != nil {
		return nil, err
	}
	// Prefer per-rule Source; fall back to source for unstamped rules.
	stamped := CloneCommandRules(rules)
	for i := range stamped {
		if strings.TrimSpace(stamped[i].Source) == "" {
			stamped[i].Source = source
		}
	}
	compiled, err := CompileRules(stamped)
	if err != nil {
		return nil, err
	}
	return &Effective{
		Limits: CloneLimits(limits),
		Rules:  compiled,
	}, nil
}

// SchedulerConfig returns a Config suitable for New, covering all default
// pools. Omitted limits remain unlimited (capacity 0).
func (e *Effective) SchedulerConfig() Config {
	if e == nil {
		return Config{}
	}
	return Config{Pools: poolsFromLimits(e.Limits)}
}

// Classify returns the class for command (last-match-wins; default general).
func (e *Effective) Classify(command string) Class {
	if e == nil {
		return ClassGeneral
	}
	return Classify(command, e.Rules)
}

// ClassifyDetail returns the class and winning rule (nil rule → default).
func (e *Effective) ClassifyDetail(command string) (Class, *CompiledRule) {
	if e == nil {
		return ClassGeneral, nil
	}
	return ClassifyDetail(command, e.Rules)
}

// PoolsForCommand returns admission pool names for a bash command.
func (e *Effective) PoolsForCommand(command string) []string {
	if e == nil {
		return PoolsForClass(ClassGeneral)
	}
	return PoolsForCommand(command, e.Rules)
}

// Report returns a stable, human-readable summary of effective limits and
// rules with source provenance (for startup logs / diagnostics).
func (e *Effective) Report() string {
	if e == nil {
		return "scheduler: (nil effective policy)"
	}
	var b strings.Builder
	b.WriteString("scheduler: in-process limits only (no cross-process coordination)\n")
	b.WriteString("limits:\n")
	for _, name := range DefaultPoolNames {
		if e.Limits != nil {
			if v, ok := e.Limits[name]; ok {
				fmt.Fprintf(&b, "  %s: %d\n", name, v)
				continue
			}
		}
		fmt.Fprintf(&b, "  %s: unlimited\n", name)
	}
	if len(e.Rules) == 0 {
		b.WriteString("commands: (none; all commands classify as general)\n")
		return b.String()
	}
	b.WriteString("commands: (ordered; last match wins)\n")
	for i, r := range e.Rules {
		src := r.Source
		if src == "" {
			src = "unknown"
		}
		fmt.Fprintf(&b, "  [%d] %q → %s (%s)\n", i, r.Pattern, r.Class, src)
	}
	return b.String()
}
