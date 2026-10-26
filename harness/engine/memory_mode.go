package engine

import (
	"fmt"
	"strings"
)

// SetMemoryMode sets the session storage mode. normal reads and writes.
// incognito still loads memory into the prompt. temporary skips that layer.
// Unknown values are rejected and the previous mode is kept.
func (e *Engine) SetMemoryMode(mode string) error {
	if e == nil {
		return fmt.Errorf("memory mode: no engine")
	}
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "normal":
		mode = "normal"
	case "incognito":
		mode = "incognito"
	case "temporary":
		mode = "temporary"
	default:
		return fmt.Errorf("memory mode %q is unknown", strings.TrimSpace(mode))
	}
	e.memModeMu.Lock()
	e.memMode = mode
	e.memModeMu.Unlock()
	return nil
}

// MemoryMode returns normal, incognito, or temporary.
func (e *Engine) MemoryMode() string {
	if e == nil {
		return "normal"
	}
	e.memModeMu.RLock()
	defer e.memModeMu.RUnlock()
	if e.memMode == "" {
		return "normal"
	}
	return e.memMode
}
