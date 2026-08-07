package sweep

import (
	"math"
	"sort"
)

// TrialOutcome is one independent trial of the same task/point.
type TrialOutcome struct {
	// Success is true when the trial resolved/passed.
	Success bool
}

// RepeatedTrialMetrics summarizes k independent trials (pass@k family).
//
// Definitions (standard coding-eval usage):
//   - pass@k: probability at least one of k trials succeeds
//     estimated unbiased as 1 - C(n-c, k)/C(n, k) when n>=k (Chen et al.)
//   - pass^k: probability all k trials succeed ≈ (c/n)^k
//   - flakiness: how often repeats disagree — 1 - max(c, n-c)/n when n>0
//     (0 = stable all-pass or all-fail; 1 = 50/50)
type RepeatedTrialMetrics struct {
	Trials    int     `json:"trials"`
	Successes int     `json:"successes"`
	PassAtK   float64 `json:"passAtK"`
	PassHatK  float64 `json:"passHatK"` // pass^k
	Flakiness float64 `json:"flakiness"`
	// ConfidenceN is the trial count used (same as Trials; explicit for reports).
	ConfidenceN int `json:"confidenceN"`
	// K is the k used for pass@k / pass^k (clamped to Trials).
	K int `json:"k"`
}

// ComputeRepeatedTrials derives pass@k, pass^k, flakiness from trial outcomes.
// k<=0 defaults to n (all trials). Empty trials yield zeros.
func ComputeRepeatedTrials(trials []TrialOutcome, k int) RepeatedTrialMetrics {
	n := len(trials)
	if n == 0 {
		return RepeatedTrialMetrics{}
	}
	c := 0
	for _, t := range trials {
		if t.Success {
			c++
		}
	}
	if k <= 0 || k > n {
		k = n
	}
	return RepeatedTrialMetrics{
		Trials:      n,
		Successes:   c,
		PassAtK:     passAtK(n, c, k),
		PassHatK:    passHatK(n, c, k),
		Flakiness:   flakiness(n, c),
		ConfidenceN: n,
		K:           k,
	}
}

// passAtK unbiased estimator: 1 - C(n-c,k)/C(n,k) when n-c >= k, else 1.
func passAtK(n, c, k int) float64 {
	if k <= 0 || n <= 0 {
		return 0
	}
	if c >= n {
		return 1
	}
	if c == 0 {
		return 0
	}
	// If fewer failures than k, cannot draw k failures → pass@k = 1.
	if n-c < k {
		return 1
	}
	// ratio = C(n-c, k) / C(n, k) = prod_{i=0..k-1} (n-c-i)/(n-i)
	ratio := 1.0
	for i := 0; i < k; i++ {
		ratio *= float64(n-c-i) / float64(n-i)
	}
	p := 1 - ratio
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}

func passHatK(n, c, k int) float64 {
	if n <= 0 || k <= 0 {
		return 0
	}
	p := float64(c) / float64(n)
	return math.Pow(p, float64(k))
}

func flakiness(n, c int) float64 {
	if n <= 0 {
		return 0
	}
	maj := c
	if n-c > maj {
		maj = n - c
	}
	return 1 - float64(maj)/float64(n)
}

// AggregateTrialSets computes per-point repeated metrics from named trial sets.
func AggregateTrialSets(sets map[string][]TrialOutcome, k int) map[string]RepeatedTrialMetrics {
	out := make(map[string]RepeatedTrialMetrics, len(sets))
	ids := make([]string, 0, len(sets))
	for id := range sets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		out[id] = ComputeRepeatedTrials(sets[id], k)
	}
	return out
}
