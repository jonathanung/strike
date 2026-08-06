package swebench

import (
	"context"

	"github.com/jonathanung/strike-cli/internal/models"
)

// CostEstimator estimates USD cost from token usage.
type CostEstimator interface {
	Estimate(provider, model string, u Usage) float64
}

// CatalogCost uses models.dev pricing (USD per million tokens).
type CatalogCost struct {
	// Load returns the catalog; defaults to models.Load.
	Load func(ctx context.Context) (models.Catalog, error)
	// catalog cached after first successful load.
	catalog models.Catalog
	loaded  bool
}

// Estimate implements CostEstimator. Returns 0 when pricing is unknown.
func (c *CatalogCost) Estimate(provider, model string, u Usage) float64 {
	if provider == "" || model == "" {
		return 0
	}
	if !c.loaded {
		load := c.Load
		if load == nil {
			load = models.Load
		}
		cat, err := load(context.Background())
		if err == nil {
			c.catalog = cat
		}
		c.loaded = true
	}
	if c.catalog == nil {
		return 0
	}
	info, ok := lookupModel(c.catalog, provider, model)
	if !ok || !info.HasCost {
		return 0
	}
	return costUSD(info.InputCost, info.OutputCost, u)
}

func lookupModel(cat models.Catalog, provider, model string) (models.Info, bool) {
	for _, info := range cat.Infos(provider) {
		if info.ID == model {
			return info, true
		}
	}
	return models.Info{}, false
}

func costUSD(inputPerM, outputPerM float64, u Usage) float64 {
	in := float64(u.Input) / 1e6 * inputPerM
	out := float64(u.Output) / 1e6 * outputPerM
	// Cache tokens: treat cache read/write as input-priced when no separate rate.
	cache := float64(u.CacheRead+u.CacheCreation) / 1e6 * inputPerM
	return in + out + cache
}

// FixedCost always returns the same rate (tests).
type FixedCost struct {
	InputPerM  float64
	OutputPerM float64
}

// Estimate implements CostEstimator.
func (f FixedCost) Estimate(_, _ string, u Usage) float64 {
	return costUSD(f.InputPerM, f.OutputPerM, u)
}
