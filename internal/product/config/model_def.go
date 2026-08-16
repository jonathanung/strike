package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// ModelLimit is optional token ceilings for a configured model.
type ModelLimit struct {
	Context int `json:"context,omitempty"`
	Output  int `json:"output,omitempty"`
}

// ModelDef is one rich model entry from providers.jsonc (nested object) or a
// legacy string id promoted to a bare def.
type ModelDef struct {
	ID       string                    `json:"-"`
	Name     string                    `json:"name,omitempty"`
	Limit    *ModelLimit               `json:"limit,omitempty"`
	Options  map[string]any            `json:"options,omitempty"`
	Variants map[string]map[string]any `json:"variants,omitempty"`
}

// Validate checks a model def after ID is set from the map key or legacy list.
func (m ModelDef) Validate() error {
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("model id is required")
	}
	if m.Limit != nil {
		if m.Limit.Context < 0 {
			return fmt.Errorf("limit.context must be >= 0, got %d", m.Limit.Context)
		}
		if m.Limit.Output < 0 {
			return fmt.Errorf("limit.output must be >= 0, got %d", m.Limit.Output)
		}
	}
	for id := range m.Variants {
		if strings.TrimSpace(id) == "" {
			return errors.New("variants must not contain empty ids")
		}
	}
	return nil
}

// VariantIDs returns sorted variant keys.
func (m ModelDef) VariantIDs() []string {
	if len(m.Variants) == 0 {
		return nil
	}
	ids := make([]string, 0, len(m.Variants))
	for id := range m.Variants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// FindModelDef returns the def with the given id, if any.
func FindModelDef(defs []ModelDef, id string) (ModelDef, bool) {
	id = strings.TrimSpace(id)
	for _, d := range defs {
		if d.ID == id {
			return d, true
		}
	}
	return ModelDef{}, false
}

// ModelDefsContext returns config context limit when set and > 0.
func ModelDefsContext(defs []ModelDef, model string) (int, bool) {
	d, ok := FindModelDef(defs, model)
	if !ok || d.Limit == nil || d.Limit.Context <= 0 {
		return 0, false
	}
	return d.Limit.Context, true
}

// ModelDefsOutput returns config output limit when set and > 0.
func ModelDefsOutput(defs []ModelDef, model string) (int, bool) {
	d, ok := FindModelDef(defs, model)
	if !ok || d.Limit == nil || d.Limit.Output <= 0 {
		return 0, false
	}
	return d.Limit.Output, true
}

// mergeModelDefs overlays layer onto base by id (layer wins whole def).
// Order: base ids in order, then new layer-only ids in layer order.
func mergeModelDefs(base, layer []ModelDef) []ModelDef {
	if len(layer) == 0 {
		return cloneModelDefs(base)
	}
	if len(base) == 0 {
		return cloneModelDefs(layer)
	}
	index := make(map[string]int, len(base))
	out := cloneModelDefs(base)
	for i, d := range out {
		index[d.ID] = i
	}
	for _, d := range layer {
		if d.ID == "" {
			continue
		}
		if i, ok := index[d.ID]; ok {
			out[i] = cloneModelDef(d)
			continue
		}
		index[d.ID] = len(out)
		out = append(out, cloneModelDef(d))
	}
	return out
}

// mergeOverlayMaps merges provider→defs maps; later layer wins per model id.
// Provider keys are canonicalized (gemini → google).
func mergeOverlayMaps(base, layer map[string][]ModelDef) map[string][]ModelDef {
	if len(layer) == 0 {
		return cloneOverlayMap(base)
	}
	out := cloneOverlayMap(base)
	if out == nil {
		out = make(map[string][]ModelDef, len(layer))
	}
	for prov, defs := range layer {
		id := CanonicalProviderID(prov)
		out[id] = mergeModelDefs(out[id], defs)
	}
	return out
}

func cloneOverlayMap(in map[string][]ModelDef) map[string][]ModelDef {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]ModelDef, len(in))
	for k, v := range in {
		out[CanonicalProviderID(k)] = cloneModelDefs(v)
	}
	return out
}

func cloneModelDefs(in []ModelDef) []ModelDef {
	if len(in) == 0 {
		return nil
	}
	out := make([]ModelDef, len(in))
	for i, d := range in {
		out[i] = cloneModelDef(d)
	}
	return out
}

func cloneModelDef(d ModelDef) ModelDef {
	out := ModelDef{
		ID:   d.ID,
		Name: d.Name,
	}
	if d.Limit != nil {
		lim := *d.Limit
		out.Limit = &lim
	}
	if len(d.Options) > 0 {
		out.Options = cloneAnyMap(d.Options)
	}
	if len(d.Variants) > 0 {
		out.Variants = make(map[string]map[string]any, len(d.Variants))
		for k, v := range d.Variants {
			out.Variants[k] = cloneAnyMap(v)
		}
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// modelsJSON unmarshals providers.jsonc "models" as []string or object map.
type modelsJSON struct {
	defs []ModelDef
}

func (m *modelsJSON) UnmarshalJSON(data []byte) error {
	data = bytesTrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	switch data[0] {
	case '[':
		var ids []string
		if err := json.Unmarshal(data, &ids); err != nil {
			return err
		}
		out := make([]ModelDef, 0, len(ids))
		seen := map[string]struct{}{}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id == "" {
				return errors.New("models must not contain empty ids")
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, ModelDef{ID: id})
		}
		m.defs = out
		return nil
	case '{':
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		keys := make([]string, 0, len(raw))
		for k := range raw {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]ModelDef, 0, len(keys))
		for _, id := range keys {
			id = strings.TrimSpace(id)
			if id == "" {
				return errors.New("models must not contain empty ids")
			}
			body := bytesTrimSpace(raw[id])
			var def ModelDef
			if len(body) > 0 && string(body) != "null" {
				if err := json.Unmarshal(body, &def); err != nil {
					return fmt.Errorf("model %q: %w", id, err)
				}
			}
			def.ID = id
			if err := def.Validate(); err != nil {
				return fmt.Errorf("model %q: %w", id, err)
			}
			out = append(out, def)
		}
		m.defs = out
		return nil
	default:
		return errors.New("models must be a JSON array or object")
	}
}

// VariantEffort extracts a protocol.Effort from a variant/options bag.
// Looks at reasoningEffort then effort. Unknown values yield ok=false.
func VariantEffort(opts map[string]any) (protocol.Effort, bool) {
	if len(opts) == 0 {
		return "", false
	}
	for _, key := range []string{"reasoningEffort", "effort"} {
		v, ok := opts[key]
		if !ok || v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		level, ok := protocol.ParseEffort(s)
		if !ok || level == protocol.EffortDefault {
			continue
		}
		return level, true
	}
	return "", false
}

// ResolveVariant returns the option bag for variant on def, if present.
func ResolveVariant(def ModelDef, variant string) (map[string]any, bool) {
	variant = strings.TrimSpace(variant)
	if variant == "" || len(def.Variants) == 0 {
		return nil, false
	}
	opts, ok := def.Variants[variant]
	if !ok {
		return nil, false
	}
	return cloneAnyMap(opts), true
}
