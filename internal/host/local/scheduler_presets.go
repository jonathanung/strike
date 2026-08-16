package local

import (
	"github.com/jonathanung/strike-cli/harness/scheduler"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/host"
)

// schedulerPresetCatalog adapts scheduler.Catalog + global config to
// host.SchedulerPresets.
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

func (schedulerPresetCatalog) Global() (host.SchedulerGlobalState, error) {
	cfg, err := config.ReadGlobalDefaults()
	if err != nil {
		return host.SchedulerGlobalState{}, err
	}
	return schedulerConfigToHost(cfg.Scheduler), nil
}

func (schedulerPresetCatalog) ApplyGlobalPresets(ids []string) error {
	return config.SetGlobalSchedulerPresets(ids)
}

func schedulerConfigToHost(sc config.SchedulerConfig) host.SchedulerGlobalState {
	out := host.SchedulerGlobalState{}
	if len(sc.Presets) > 0 {
		out.Presets = append([]string(nil), sc.Presets...)
	}
	if len(sc.Limits) > 0 {
		out.Limits = make(map[string]int, len(sc.Limits))
		for k, v := range sc.Limits {
			out.Limits[k] = v
		}
	}
	if len(sc.Commands) > 0 {
		out.Commands = make([]host.SchedulerCommandRule, len(sc.Commands))
		for i, r := range sc.Commands {
			out.Commands[i] = host.SchedulerCommandRule{
				Pattern: r.Pattern,
				Class:   string(r.Class),
				Source:  r.Source,
			}
		}
	}
	return out
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
