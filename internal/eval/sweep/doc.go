// Package sweep implements E3.5 parameter sweeps (#563).
//
// It runs a fixed eval subset (SWE-bench E3.3 or Terminal-Bench E3.4) once per
// configuration point and writes a comparison summary (pass rate, tokens,
// cost, wall-clock) so dials like compactionThreshold, leanCode, deferTools,
// and effort can be A/B'd without code changes per point.
//
// Config dials are injected as a project-layer .strike/config in each instance
// workspace before strike exec. Effort may also be set via strike exec
// --effort. Runners exclude .strike/ from SWE-bench patch extraction so the
// overlay does not pollute model_patch.
//
// Internal regression signal only — do not publish pass rates in the README.
package sweep
