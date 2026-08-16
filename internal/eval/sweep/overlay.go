package sweep

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Overlay is a partial project config written into an instance workspace
// before strike exec. Fields map 1:1 onto internal/product/config.Config JSON keys
// for the dials under test in #563.
type Overlay struct {
	LeanCode            string  `json:"leanCode,omitempty"`
	DeferTools          string  `json:"deferTools,omitempty"`
	CompactionThreshold float64 `json:"compactionThreshold,omitempty"`
	PruneProtectTokens  int     `json:"pruneProtectTokens,omitempty"`
	PruneMinimumTokens  int     `json:"pruneMinimumTokens,omitempty"`
	// Effort is also accepted in project config; runners still pass
	// Point.Effort to strike exec --effort when set.
	Effort string `json:"effort,omitempty"`
}

// Marshal returns stable indented JSON for the overlay (empty object when
// all fields are zero).
func (o Overlay) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// IsZero reports whether the overlay sets no dials.
func (o Overlay) IsZero() bool {
	return o == (Overlay{})
}

// WriteProjectConfig writes overlay JSON to workDir/.strike/config so
// config.Load(workDir) picks it up as the project layer.
func WriteProjectConfig(workDir string, o Overlay) error {
	if workDir == "" {
		return fmt.Errorf("sweep: empty workDir")
	}
	if o.IsZero() {
		return nil
	}
	data, err := o.Marshal()
	if err != nil {
		return err
	}
	return WriteProjectConfigJSON(workDir, data)
}

// WriteProjectConfigJSON writes raw JSON bytes to workDir/.strike/config.
// Used by runners when Config.ProjectConfig is supplied directly.
func WriteProjectConfigJSON(workDir string, raw []byte) error {
	if workDir == "" {
		return fmt.Errorf("sweep: empty workDir")
	}
	if len(raw) == 0 {
		return nil
	}
	// Validate JSON object.
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("sweep: project config json: %w", err)
	}
	dir := filepath.Join(workDir, ".strike")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("sweep: mkdir project config: %w", err)
	}
	path := filepath.Join(dir, "config")
	out := append([]byte(nil), raw...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("sweep: write project config: %w", err)
	}
	return nil
}
