package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Local pane/1 definition types (docs/plugin-panes.md). Duplicated from the
// plugin package so the TUI boundary stays free of internal/plugin imports.

const (
	paneABI           = "pane/1"
	paneSchemaVersion = 1
	paneModeStatic    = "static"
	paneModeProcess   = "process"

	paneMaxNodes     = 512
	paneMaxDepth     = 16
	paneMaxTextBytes = 4 << 10
	paneMaxTitleLen  = 40
)

type paneDef struct {
	SchemaVersion int               `json:"schemaVersion"`
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Mode          string            `json:"mode"`
	Permissions   paneDefPerms      `json:"permissions"`
	Sizing        paneDefSizing     `json:"sizing"`
	Subscriptions []string          `json:"subscriptions"`
	View          json.RawMessage   `json:"view"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	Timeouts      paneDefTimeouts   `json:"timeouts"`
	Group         string            `json:"group"`
}

type paneDefPerms struct {
	Host    []string `json:"host"`
	FS      string   `json:"fs"`
	Network string   `json:"network"`
	Command string   `json:"command"`
}

type paneDefSizing struct {
	MinWidth        int `json:"minWidth"`
	MinHeight       int `json:"minHeight"`
	PreferredHeight int `json:"preferredHeight"`
	PreferredWidth  int `json:"preferredWidth"`
}

type paneDefTimeouts struct {
	StartMs    int `json:"startMs"`
	RenderMs   int `json:"renderMs"`
	ShutdownMs int `json:"shutdownMs"`
}

func parsePaneDef(data []byte) (paneDef, error) {
	var d paneDef
	if err := json.Unmarshal(data, &d); err != nil {
		return paneDef{}, err
	}
	if d.SchemaVersion != paneSchemaVersion {
		return paneDef{}, fmt.Errorf("unsupported pane schemaVersion %d", d.SchemaVersion)
	}
	d.Title = strings.TrimSpace(d.Title)
	d.Mode = strings.TrimSpace(d.Mode)
	d.ID = strings.TrimSpace(d.ID)
	d.Sizing = clampPaneSizing(d.Sizing)
	d.Timeouts = clampPaneTimeouts(d.Timeouts)
	return d, nil
}

func clampPaneSizing(s paneDefSizing) paneDefSizing {
	if s.MinWidth <= 0 {
		s.MinWidth = 20
	}
	if s.MinWidth < 12 {
		s.MinWidth = 12
	}
	if s.MinWidth > 80 {
		s.MinWidth = 80
	}
	if s.MinHeight <= 0 {
		s.MinHeight = 4
	}
	if s.MinHeight < 3 {
		s.MinHeight = 3
	}
	if s.MinHeight > 40 {
		s.MinHeight = 40
	}
	if s.PreferredHeight <= 0 {
		s.PreferredHeight = 12
	}
	if s.PreferredHeight < s.MinHeight {
		s.PreferredHeight = s.MinHeight
	}
	return s
}

func clampPaneTimeouts(t paneDefTimeouts) paneDefTimeouts {
	if t.StartMs <= 0 {
		t.StartMs = 5000
	}
	if t.StartMs > 15000 {
		t.StartMs = 15000
	}
	if t.ShutdownMs <= 0 {
		t.ShutdownMs = 2000
	}
	if t.ShutdownMs > 5000 {
		t.ShutdownMs = 5000
	}
	if t.RenderMs <= 0 {
		t.RenderMs = 50
	}
	return t
}

func clampPaneTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "plugin"
	}
	if utf8.RuneCountInString(title) <= paneMaxTitleLen {
		return title
	}
	runes := []rune(title)
	return string(runes[:paneMaxTitleLen])
}
