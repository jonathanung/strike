package scheduler

import (
	"fmt"
	"slices"
	"strings"
)

// Limits maps pool name → positive capacity for layered configuration.
//
// Omitted keys preserve lower-layer values when merging, and preserve
// unlimited behavior when no layer sets the pool. An explicit non-positive
// value is invalid (use omission for unlimited, not 0).
type Limits map[string]int

// knownLimitPools is the set of pool names accepted in config limits.
var knownLimitPools = map[string]struct{}{
	PoolProcess:   {},
	PoolBuild:     {},
	PoolTest:      {},
	PoolModel:     {},
	PoolContainer: {},
}

// MergeLimits overlays layer onto base per pool. Keys present in layer replace
// base; omitted keys keep the base value. Neither map is mutated.
func MergeLimits(base, layer Limits) Limits {
	if len(base) == 0 && len(layer) == 0 {
		return nil
	}
	out := make(Limits, len(base)+len(layer))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range layer {
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CloneLimits returns a shallow copy of l (nil stays nil).
func CloneLimits(l Limits) Limits {
	if len(l) == 0 {
		return nil
	}
	out := make(Limits, len(l))
	for k, v := range l {
		out[k] = v
	}
	return out
}

// ValidateLimits rejects unknown pool names and non-positive capacities.
// source is included in error messages (file path or layer label).
func ValidateLimits(l Limits, source string) error {
	if len(l) == 0 {
		return nil
	}
	// Stable error order.
	names := make([]string, 0, len(l))
	for name := range l {
		names = append(names, name)
	}
	slices.Sort(names)
	src := strings.TrimSpace(source)
	for _, name := range names {
		cap := l[name]
		if name == "" {
			return limitErr(src, `empty pool name in limits`)
		}
		if _, ok := knownLimitPools[name]; !ok {
			return limitErr(src, fmt.Sprintf("unknown scheduler pool %q in limits (want %s)", name, strings.Join(DefaultPoolNames, "|")))
		}
		if cap <= 0 {
			return limitErr(src, fmt.Sprintf("scheduler limit %q must be a positive integer (got %d); omit the key for unlimited", name, cap))
		}
	}
	return nil
}

func limitErr(source, msg string) error {
	if source == "" {
		return fmt.Errorf("scheduler: %s", msg)
	}
	return fmt.Errorf("%s: scheduler: %s", source, msg)
}

// poolsFromLimits builds a New Config.Pools map covering DefaultPoolNames.
// Omitted limits are unlimited (capacity 0). Unknown keys are ignored; call
// ValidateLimits first for config input.
func poolsFromLimits(l Limits) map[string]int {
	pools := make(map[string]int, len(DefaultPoolNames))
	for _, name := range DefaultPoolNames {
		if l != nil {
			if v, ok := l[name]; ok {
				pools[name] = v
				continue
			}
		}
		pools[name] = 0 // unlimited
	}
	return pools
}
