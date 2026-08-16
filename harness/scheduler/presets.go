package scheduler

import (
	"fmt"
	"slices"
	"strings"
)

// Preset is a versioned, named bundle of suggested limits and command rules for
// a resource-heavy build or test system.
//
// Expansion produces ordinary Limits and CommandRule values. Runtime admission
// still uses only Compile → Effective → Classify / PoolsForCommand — there is
// no second matching path for presets.
type Preset struct {
	// ID is the stable config key (e.g. "cmake", "cargo").
	ID string `json:"id"`
	// Version is the schema generation of this preset's rules/limits.
	// Bump when generated patterns or suggested limits change meaningfully.
	Version int `json:"version"`
	// Name is a short display label for FTUE and settings UIs.
	Name string `json:"name"`
	// Rationale explains when enabling the preset is useful.
	Rationale string `json:"rationale"`
	// DefaultClass is the primary class for the tool family (usually build).
	DefaultClass Class `json:"defaultClass"`
	// Limits are optional suggested pool capacities (positive ints only).
	// Omitted keys stay unlimited unless another layer sets them.
	Limits Limits `json:"limits,omitempty"`
	// Rules are the generated classification patterns (order matters;
	// broader build rules first, specific test rules last for last-match-wins).
	Rules []CommandRule `json:"rules"`
}

// presetSource returns the provenance stamp written onto expanded rules.
func presetSource(p Preset) string {
	return fmt.Sprintf("preset:%s@v%d", p.ID, p.Version)
}

// shippedPresets is the built-in catalog. Order is display order for UIs.
// Keep IDs stable — they are config API.
var shippedPresets = []Preset{
	{
		ID:           "cmake",
		Version:      1,
		Name:         "CMake",
		Rationale:    "CMake configure/build and ctest are CPU- and I/O-heavy; cap concurrent builds and tests.",
		DefaultClass: ClassBuild,
		Limits:       Limits{PoolBuild: 2, PoolTest: 2},
		Rules: []CommandRule{
			{Pattern: "cmake *", Class: ClassBuild},
			{Pattern: "cmake", Class: ClassBuild},
			{Pattern: "ctest *", Class: ClassTest},
			{Pattern: "ctest", Class: ClassTest},
		},
	},
	{
		ID:           "ninja",
		Version:      1,
		Name:         "Ninja",
		Rationale:    "Ninja drives highly parallel native builds; limit concurrent ninja invocations.",
		DefaultClass: ClassBuild,
		Limits:       Limits{PoolBuild: 2},
		Rules: []CommandRule{
			{Pattern: "ninja *", Class: ClassBuild},
			{Pattern: "ninja", Class: ClassBuild},
		},
	},
	{
		ID:           "gradle",
		Version:      1,
		Name:         "Gradle",
		Rationale:    "Gradle/Gradle Wrapper builds and test tasks are memory-heavy JVM work.",
		DefaultClass: ClassBuild,
		Limits:       Limits{PoolBuild: 2, PoolTest: 2},
		Rules: []CommandRule{
			{Pattern: "gradle *", Class: ClassBuild},
			{Pattern: "gradle", Class: ClassBuild},
			{Pattern: "gradlew *", Class: ClassBuild},
			{Pattern: "gradlew", Class: ClassBuild},
			{Pattern: "./gradlew *", Class: ClassBuild},
			{Pattern: "./gradlew", Class: ClassBuild},
			// Test tasks last so they win over the broad build globs.
			{Pattern: "gradle test*", Class: ClassTest},
			{Pattern: "gradle check*", Class: ClassTest},
			{Pattern: "gradlew test*", Class: ClassTest},
			{Pattern: "gradlew check*", Class: ClassTest},
			{Pattern: "./gradlew test*", Class: ClassTest},
			{Pattern: "./gradlew check*", Class: ClassTest},
		},
	},
	{
		ID:           "bazel",
		Version:      1,
		Name:         "Bazel",
		Rationale:    "Bazel/Bazelisk builds and tests can saturate CPU and remote cache bandwidth.",
		DefaultClass: ClassBuild,
		Limits:       Limits{PoolBuild: 2, PoolTest: 2},
		Rules: []CommandRule{
			{Pattern: "bazel *", Class: ClassBuild},
			{Pattern: "bazel", Class: ClassBuild},
			{Pattern: "bazelisk *", Class: ClassBuild},
			{Pattern: "bazelisk", Class: ClassBuild},
			{Pattern: "bazel test *", Class: ClassTest},
			{Pattern: "bazel test", Class: ClassTest},
			{Pattern: "bazelisk test *", Class: ClassTest},
			{Pattern: "bazelisk test", Class: ClassTest},
			{Pattern: "bazel coverage *", Class: ClassTest},
			{Pattern: "bazelisk coverage *", Class: ClassTest},
		},
	},
	{
		ID:           "maven",
		Version:      1,
		Name:         "Maven",
		Rationale:    "Maven/Maven Wrapper package and verify goals are heavy JVM builds and tests.",
		DefaultClass: ClassBuild,
		Limits:       Limits{PoolBuild: 2, PoolTest: 2},
		Rules: []CommandRule{
			{Pattern: "mvn *", Class: ClassBuild},
			{Pattern: "mvn", Class: ClassBuild},
			{Pattern: "mvnw *", Class: ClassBuild},
			{Pattern: "mvnw", Class: ClassBuild},
			{Pattern: "./mvnw *", Class: ClassBuild},
			{Pattern: "./mvnw", Class: ClassBuild},
			{Pattern: "mvn test*", Class: ClassTest},
			{Pattern: "mvn verify*", Class: ClassTest},
			{Pattern: "mvnw test*", Class: ClassTest},
			{Pattern: "mvnw verify*", Class: ClassTest},
			{Pattern: "./mvnw test*", Class: ClassTest},
			{Pattern: "./mvnw verify*", Class: ClassTest},
		},
	},
	{
		ID:           "cargo",
		Version:      1,
		Name:         "Cargo",
		Rationale:    "Rust cargo build/check/clippy and test use significant CPU and target-dir I/O.",
		DefaultClass: ClassBuild,
		Limits:       Limits{PoolBuild: 2, PoolTest: 2},
		Rules: []CommandRule{
			{Pattern: "cargo *", Class: ClassBuild},
			{Pattern: "cargo", Class: ClassBuild},
			{Pattern: "cargo test *", Class: ClassTest},
			{Pattern: "cargo test", Class: ClassTest},
			{Pattern: "cargo nextest *", Class: ClassTest},
			{Pattern: "cargo nextest", Class: ClassTest},
		},
	},
	{
		ID:           "npm",
		Version:      1,
		Name:         "npm / yarn / pnpm / bun",
		Rationale:    "JS package-manager install, build, and test scripts are common agent bottlenecks.",
		DefaultClass: ClassBuild,
		Limits:       Limits{PoolBuild: 2, PoolTest: 2},
		Rules: []CommandRule{
			// Package managers (install/build/run default to build).
			{Pattern: "npm *", Class: ClassBuild},
			{Pattern: "npm", Class: ClassBuild},
			{Pattern: "yarn *", Class: ClassBuild},
			{Pattern: "yarn", Class: ClassBuild},
			{Pattern: "pnpm *", Class: ClassBuild},
			{Pattern: "pnpm", Class: ClassBuild},
			{Pattern: "bun *", Class: ClassBuild},
			{Pattern: "bun", Class: ClassBuild},
			// Test scripts last (last-match-wins).
			{Pattern: "npm test*", Class: ClassTest},
			{Pattern: "npm run test*", Class: ClassTest},
			{Pattern: "yarn test*", Class: ClassTest},
			{Pattern: "yarn run test*", Class: ClassTest},
			{Pattern: "pnpm test*", Class: ClassTest},
			{Pattern: "pnpm run test*", Class: ClassTest},
			{Pattern: "bun test*", Class: ClassTest},
			{Pattern: "bun run test*", Class: ClassTest},
		},
	},
}

// Catalog returns a defensive copy of every shipped preset in stable order.
func Catalog() []Preset {
	out := make([]Preset, len(shippedPresets))
	for i, p := range shippedPresets {
		out[i] = clonePreset(p)
	}
	return out
}

// Lookup returns the shipped preset with the given ID (case-sensitive).
func Lookup(id string) (Preset, bool) {
	id = strings.TrimSpace(id)
	for _, p := range shippedPresets {
		if p.ID == id {
			return clonePreset(p), true
		}
	}
	return Preset{}, false
}

// KnownPresetIDs returns stable IDs in catalog order (for docs/validation).
func KnownPresetIDs() []string {
	ids := make([]string, len(shippedPresets))
	for i, p := range shippedPresets {
		ids[i] = p.ID
	}
	return ids
}

// ExpandPresets resolves preset IDs into merged suggested limits and ordered
// command rules. Expansion is deterministic: catalog order among selected IDs
// (not the order of ids), duplicate IDs are ignored after the first, and each
// rule is stamped with Source "preset:<id>@v<version>".
//
// Unknown or empty IDs return an error naming the offender. The result is plain
// Limits / CommandRule data for Compile — callers must not special-case presets
// at classify time.
func ExpandPresets(ids []string) (Limits, []CommandRule, error) {
	if len(ids) == 0 {
		return nil, nil, nil
	}
	// Dedupe while preserving first-seen order for error reporting, then
	// expand in catalog order for stable output regardless of request order.
	seen := make(map[string]struct{}, len(ids))
	var selected []string
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return nil, nil, fmt.Errorf("scheduler: empty preset id")
		}
		if _, ok := Lookup(id); !ok {
			return nil, nil, fmt.Errorf("scheduler: unknown preset %q (want %s)", id, strings.Join(KnownPresetIDs(), "|"))
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		selected = append(selected, id)
	}
	// Catalog order for deterministic limits/rules.
	var limits Limits
	var rules []CommandRule
	for _, p := range shippedPresets {
		if _, ok := seen[p.ID]; !ok {
			continue
		}
		limits = MergeLimits(limits, p.Limits)
		src := presetSource(p)
		for _, r := range p.Rules {
			rules = append(rules, CommandRule{
				Pattern: r.Pattern,
				Class:   r.Class,
				Source:  src,
			})
		}
	}
	// selected is retained so empty selection after trim still errors above.
	_ = selected
	return CloneLimits(limits), rules, nil
}

// CompileWithPresets expands presetIDs, merges user limits on top of suggested
// preset limits, appends user rules after preset rules, then Compile.
//
// Layering: preset limits ← user limits (per-pool override); preset rules then
// user rules (last-match-wins so user rules can reclassify).
func CompileWithPresets(presetIDs []string, userLimits Limits, userRules []CommandRule, source string) (*Effective, error) {
	pl, pr, err := ExpandPresets(presetIDs)
	if err != nil {
		return nil, err
	}
	limits := MergeLimits(pl, userLimits)
	rules := append(pr, CloneCommandRules(userRules)...)
	return Compile(limits, rules, source)
}

// MergePresetIDs concatenates base and layer preset ID lists, dropping
// duplicates (first occurrence wins). Empty strings are skipped. Neither
// slice is mutated.
func MergePresetIDs(base, layer []string) []string {
	if len(base) == 0 && len(layer) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(base)+len(layer))
	out := make([]string, 0, len(base)+len(layer))
	for _, raw := range append(slices.Clone(base), layer...) {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ValidatePresetIDs rejects unknown or empty preset IDs. source is included in
// error messages (config path or layer label).
func ValidatePresetIDs(ids []string, source string) error {
	if len(ids) == 0 {
		return nil
	}
	src := strings.TrimSpace(source)
	seen := make(map[string]struct{}, len(ids))
	for i, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			return presetIDErr(src, i, "empty preset id")
		}
		if _, ok := Lookup(id); !ok {
			return presetIDErr(src, i, fmt.Sprintf("unknown preset %q (want %s)", id, strings.Join(KnownPresetIDs(), "|")))
		}
		if _, ok := seen[id]; ok {
			// Duplicates are harmless at expand time; still reject at load so
			// config stays tidy and FTUE re-writes stay idempotent.
			return presetIDErr(src, i, fmt.Sprintf("duplicate preset %q", id))
		}
		seen[id] = struct{}{}
	}
	return nil
}

func presetIDErr(source string, index int, msg string) error {
	loc := fmt.Sprintf("presets[%d]", index)
	if source == "" {
		return fmt.Errorf("scheduler: %s: %s", loc, msg)
	}
	return fmt.Errorf("%s: scheduler: %s: %s", source, loc, msg)
}

func clonePreset(p Preset) Preset {
	return Preset{
		ID:           p.ID,
		Version:      p.Version,
		Name:         p.Name,
		Rationale:    p.Rationale,
		DefaultClass: p.DefaultClass,
		Limits:       CloneLimits(p.Limits),
		Rules:        CloneCommandRules(p.Rules),
	}
}
