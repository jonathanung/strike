package local

import (
	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/scheduler"
)

// schedulerPresetCatalog adapts scheduler.Catalog to host.SchedulerPresets.
type schedulerPresetCatalog struct{}

func (schedulerPresetCatalog) List() []host.SchedulerPreset {
	src := scheduler.Catalog()
	out := make([]host.SchedulerPreset, 0, len(src))
	for _, p := range src {
		out = append(out, presetToHost(p))
	}
	return out
}

func (schedulerPresetCatalog) Get(id string) (host.SchedulerPreset, bool) {
	p, ok := scheduler.Lookup(id)
	if !ok {
		return host.SchedulerPreset{}, false
	}
	return presetToHost(p), true
}

func presetToHost(p scheduler.Preset) host.SchedulerPreset {
	out := host.SchedulerPreset{
		ID:           p.ID,
		Version:      p.Version,
		Name:         p.Name,
		Rationale:    p.Rationale,
		DefaultClass: string(p.DefaultClass),
	}
	if len(p.Limits) > 0 {
		out.Limits = make(map[string]int, len(p.Limits))
		for k, v := range p.Limits {
			out.Limits[k] = v
		}
	}
	if len(p.Rules) > 0 {
		out.Rules = make([]host.SchedulerPresetRule, len(p.Rules))
		for i, r := range p.Rules {
			out.Rules[i] = host.SchedulerPresetRule{
				Pattern: r.Pattern,
				Class:   string(r.Class),
			}
		}
	}
	return out
}
