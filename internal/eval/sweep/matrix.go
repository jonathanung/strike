package sweep

import (
	"fmt"
	"sort"
	"strings"
)

// Point is one configuration under test in a sweep matrix.
type Point struct {
	// ID is a filesystem-safe slug (used as subdirectory name).
	ID string `json:"id"`
	// Label is a short human label for tables.
	Label string `json:"label"`
	// Group names the dial family (compaction, leanCode, deferTools, effort).
	Group string `json:"group,omitempty"`
	// Overlay is written as project .strike/config before each instance.
	Overlay Overlay `json:"overlay"`
	// Effort overrides strike exec --effort when non-empty (takes precedence
	// over Overlay.Effort for the CLI flag).
	Effort string `json:"effort,omitempty"`
}

// Matrix is an ordered list of sweep points.
type Matrix []Point

// Builtin matrix names accepted by ResolveMatrix / CLI --matrix.
const (
	MatrixCompaction = "compaction"
	MatrixLeanCode   = "leanCode"
	MatrixDeferTools = "deferTools"
	MatrixEffort     = "effort"
	MatrixAll        = "all"
)

// BuiltinMatrixNames lists matrices in stable CLI help order.
func BuiltinMatrixNames() []string {
	return []string{MatrixCompaction, MatrixLeanCode, MatrixDeferTools, MatrixEffort, MatrixAll}
}

// ResolveMatrix returns a builtin matrix by name.
func ResolveMatrix(name string) (Matrix, error) {
	switch strings.TrimSpace(name) {
	case "", MatrixAll:
		return matrixAll(), nil
	case MatrixCompaction:
		return matrixCompaction(), nil
	case MatrixLeanCode:
		return matrixLeanCode(), nil
	case MatrixDeferTools:
		return matrixDeferTools(), nil
	case MatrixEffort:
		return matrixEffort(), nil
	default:
		return nil, fmt.Errorf("sweep: unknown matrix %q (want %s)", name, strings.Join(BuiltinMatrixNames(), "|"))
	}
}

func matrixCompaction() Matrix {
	return Matrix{
		{
			ID:    "compaction-baseline",
			Label: "threshold=0.70 protect=40k min=20k",
			Group: MatrixCompaction,
			Overlay: Overlay{
				CompactionThreshold: 0.70,
				PruneProtectTokens:  40000,
				PruneMinimumTokens:  20000,
			},
		},
		{
			ID:    "compaction-tight",
			Label: "threshold=0.50 protect=20k min=10k",
			Group: MatrixCompaction,
			Overlay: Overlay{
				CompactionThreshold: 0.50,
				PruneProtectTokens:  20000,
				PruneMinimumTokens:  10000,
			},
		},
		{
			ID:    "compaction-loose",
			Label: "threshold=0.85 protect=60k min=30k",
			Group: MatrixCompaction,
			Overlay: Overlay{
				CompactionThreshold: 0.85,
				PruneProtectTokens:  60000,
				PruneMinimumTokens:  30000,
			},
		},
		{
			ID:    "compaction-aggressive-prune",
			Label: "threshold=0.70 protect=10k min=5k",
			Group: MatrixCompaction,
			Overlay: Overlay{
				CompactionThreshold: 0.70,
				PruneProtectTokens:  10000,
				PruneMinimumTokens:  5000,
			},
		},
	}
}

func matrixLeanCode() Matrix {
	return Matrix{
		{ID: "leanCode-off", Label: "leanCode=off", Group: MatrixLeanCode, Overlay: Overlay{LeanCode: "off"}},
		{ID: "leanCode-lite", Label: "leanCode=lite", Group: MatrixLeanCode, Overlay: Overlay{LeanCode: "lite"}},
		{ID: "leanCode-full", Label: "leanCode=full", Group: MatrixLeanCode, Overlay: Overlay{LeanCode: "full"}},
	}
}

func matrixDeferTools() Matrix {
	return Matrix{
		{ID: "deferTools-off", Label: "deferTools=off", Group: MatrixDeferTools, Overlay: Overlay{DeferTools: "off"}},
		{ID: "deferTools-on", Label: "deferTools=on", Group: MatrixDeferTools, Overlay: Overlay{DeferTools: "on"}},
	}
}

func matrixEffort() Matrix {
	// Effort ladder cost/quality curve — applied via exec --effort.
	levels := []string{"off", "low", "medium", "high"}
	out := make(Matrix, 0, len(levels))
	for _, e := range levels {
		out = append(out, Point{
			ID:     "effort-" + e,
			Label:  "effort=" + e,
			Group:  MatrixEffort,
			Effort: e,
			Overlay: Overlay{
				Effort: e,
			},
		})
	}
	return out
}

func matrixAll() Matrix {
	var out Matrix
	out = append(out, matrixCompaction()...)
	out = append(out, matrixLeanCode()...)
	out = append(out, matrixDeferTools()...)
	out = append(out, matrixEffort()...)
	return out
}

// Validate checks point IDs are unique and non-empty.
func (m Matrix) Validate() error {
	seen := make(map[string]struct{}, len(m))
	for i, p := range m {
		if strings.TrimSpace(p.ID) == "" {
			return fmt.Errorf("sweep: point %d: empty id", i)
		}
		if _, ok := seen[p.ID]; ok {
			return fmt.Errorf("sweep: duplicate point id %q", p.ID)
		}
		seen[p.ID] = struct{}{}
	}
	return nil
}

// IDs returns point ids in matrix order.
func (m Matrix) IDs() []string {
	out := make([]string, len(m))
	for i, p := range m {
		out[i] = p.ID
	}
	return out
}

// FilterByIDs keeps only the named points (order preserved from matrix).
func (m Matrix) FilterByIDs(ids []string) (Matrix, error) {
	if len(ids) == 0 {
		return m, nil
	}
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out Matrix
	for _, p := range m {
		if _, ok := want[p.ID]; ok {
			out = append(out, p)
			delete(want, p.ID)
		}
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for id := range want {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("sweep: unknown point id(s): %s", strings.Join(missing, ", "))
	}
	return out, nil
}
