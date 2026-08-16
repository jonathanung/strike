package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Pane ABI constants (docs/plugin-panes.md).
const (
	PaneABI           = "pane/1"
	PaneSchemaVersion = 1
	PaneModeStatic    = "static"
	PaneModeProcess   = "process"

	CapPanes        = "panes"
	CapPanesProcess = "panes.process"

	// Pane definition / render budgets (host ceilings).
	PaneMaxNodes       = 512
	PaneMaxDepth       = 16
	PaneMaxTextBytes   = 4 << 10
	PaneMaxRenderBytes = 256 << 10
	PaneMaxTitleLen    = 40
	PaneMaxIDLen       = 64
)

// Reserved built-in window ids that plugin panes must not claim.
var reservedPaneIDs = map[string]struct{}{
	"context": {}, "activity": {}, "agents": {}, "visualizer": {},
	"files": {}, "diagnostics": {}, "memory": {}, "issues": {},
	"plans": {}, "markdown": {}, "editor": {}, "pets": {},
	"system": {}, "telemetry": {},
}

// paneIDRE matches plugin-scoped pane slugs (docs/plugin-panes.md §3).
var paneIDRE = regexp.MustCompile(`^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$`)

// PaneEntry is one contributions.panes[] object from the plugin manifest.
type PaneEntry struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	ABI  string `json:"abi"`
}

// PanePermissions is the definition permissions object (§8).
type PanePermissions struct {
	Host    []string `json:"host"`
	FS      string   `json:"fs,omitempty"`
	Network string   `json:"network,omitempty"`
	Command string   `json:"command,omitempty"`
}

// PaneSizing is optional layout hints (§9).
type PaneSizing struct {
	MinWidth        int     `json:"minWidth,omitempty"`
	MinHeight       int     `json:"minHeight,omitempty"`
	PreferredHeight int     `json:"preferredHeight,omitempty"`
	PreferredWidth  int     `json:"preferredWidth,omitempty"`
	Flex            float64 `json:"flex,omitempty"`
	Group           string  `json:"group,omitempty"`
}

// PaneTimeouts are optional process budgets (§10). Definitions may only lower
// host defaults; loaders clamp to ceilings.
type PaneTimeouts struct {
	StartMs    int `json:"startMs,omitempty"`
	RenderMs   int `json:"renderMs,omitempty"`
	ShutdownMs int `json:"shutdownMs,omitempty"`
}

// PaneDefinition is a pane/1 definition file (schemaVersion 1).
type PaneDefinition struct {
	SchemaVersion int               `json:"schemaVersion"`
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Mode          string            `json:"mode"`
	Permissions   PanePermissions   `json:"permissions"`
	Sizing        PaneSizing        `json:"sizing,omitempty"`
	Subscriptions []string          `json:"subscriptions,omitempty"`
	View          json.RawMessage   `json:"view,omitempty"`
	Command       string            `json:"command,omitempty"`
	Args          []string          `json:"args,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	Timeouts      PaneTimeouts      `json:"timeouts,omitempty"`
	Group         string            `json:"group,omitempty"`
}

// Known host data feeds (§7).
var knownPaneFeeds = map[string]struct{}{
	"session.summary": {},
	"usage":           {},
	"agents.roster":   {},
	"clock":           {},
}

// ParsePaneEntry decodes one contributions.panes[] raw object.
func ParsePaneEntry(raw json.RawMessage) (PaneEntry, error) {
	if len(raw) == 0 {
		return PaneEntry{}, fmt.Errorf("empty pane entry")
	}
	var e PaneEntry
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return PaneEntry{}, fmt.Errorf("parse pane entry: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err == nil {
		return PaneEntry{}, fmt.Errorf("parse pane entry: trailing data")
	}
	if err := ValidatePaneID(e.ID); err != nil {
		return PaneEntry{}, err
	}
	if strings.TrimSpace(e.Path) == "" {
		return PaneEntry{}, fmt.Errorf("pane %q: path is required", e.ID)
	}
	if err := validateRelPathSyntax(e.Path); err != nil {
		return PaneEntry{}, fmt.Errorf("pane %q path: %w", e.ID, err)
	}
	abi := strings.TrimSpace(e.ABI)
	if abi == "" {
		return PaneEntry{}, fmt.Errorf("pane %q: abi is required", e.ID)
	}
	if abi == "reserved" {
		return PaneEntry{}, fmt.Errorf("pane %q: abi %q is rejected; use %q", e.ID, abi, PaneABI)
	}
	if abi != PaneABI {
		return PaneEntry{}, fmt.Errorf("pane %q: unsupported abi %q (want %s)", e.ID, abi, PaneABI)
	}
	e.ID = strings.TrimSpace(e.ID)
	e.Path = strings.TrimSpace(e.Path)
	e.ABI = abi
	return e, nil
}

// ValidatePaneID checks pane id grammar and reserved built-in names.
func ValidatePaneID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("pane id is required")
	}
	if len(id) > PaneMaxIDLen {
		return fmt.Errorf("pane id exceeds %d characters", PaneMaxIDLen)
	}
	if !paneIDRE.MatchString(id) {
		return fmt.Errorf("pane id %q is not a valid slug", id)
	}
	if _, reserved := reservedPaneIDs[id]; reserved {
		return fmt.Errorf("pane id %q is reserved for a built-in window", id)
	}
	return nil
}

// ParsePaneDefinition decodes a pane definition JSON/JSONC document.
func ParsePaneDefinition(data []byte) (PaneDefinition, error) {
	stripped, err := stripJSONC(data)
	if err != nil {
		return PaneDefinition{}, err
	}
	stripped = bytesTrimSpace(stripped)
	if len(stripped) == 0 {
		return PaneDefinition{}, fmt.Errorf("empty pane definition")
	}
	var d PaneDefinition
	dec := json.NewDecoder(strings.NewReader(string(stripped)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return PaneDefinition{}, fmt.Errorf("parse pane definition: %w", err)
	}
	if err := dec.Decode(&struct{}{}); err == nil {
		return PaneDefinition{}, fmt.Errorf("parse pane definition: trailing data")
	}
	if err := validatePaneDefinition(d); err != nil {
		return PaneDefinition{}, err
	}
	return d, nil
}

// ReadPaneDefinition loads and parses a definition file under plugin root.
func ReadPaneDefinition(pluginRoot, rel string) (PaneDefinition, string, error) {
	abs, err := ResolveUnderRoot(pluginRoot, rel)
	if err != nil {
		return PaneDefinition{}, "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return PaneDefinition{}, "", err
	}
	d, err := ParsePaneDefinition(data)
	if err != nil {
		return PaneDefinition{}, "", err
	}
	return d, abs, nil
}

func validatePaneDefinition(d PaneDefinition) error {
	if d.SchemaVersion != PaneSchemaVersion {
		if d.SchemaVersion > PaneSchemaVersion {
			return fmt.Errorf("pane schemaVersion %d unsupported (max %d); upgrade Strike", d.SchemaVersion, PaneSchemaVersion)
		}
		return fmt.Errorf("pane schemaVersion must be %d", PaneSchemaVersion)
	}
	if err := ValidatePaneID(d.ID); err != nil {
		return err
	}
	title := strings.TrimSpace(d.Title)
	if title == "" {
		return fmt.Errorf("pane title is required")
	}
	if utf8.RuneCountInString(title) > PaneMaxTitleLen {
		return fmt.Errorf("pane title exceeds %d characters", PaneMaxTitleLen)
	}
	mode := strings.TrimSpace(d.Mode)
	switch mode {
	case PaneModeStatic, PaneModeProcess:
	default:
		return fmt.Errorf("pane mode %q invalid (want static|process)", d.Mode)
	}
	if err := validatePanePermissions(d.Permissions, mode); err != nil {
		return err
	}
	for _, feed := range d.Subscriptions {
		feed = strings.TrimSpace(feed)
		if feed == "" {
			continue
		}
		if _, ok := knownPaneFeeds[feed]; !ok {
			return fmt.Errorf("unknown subscription feed %q", feed)
		}
		if !hostFeedGranted(d.Permissions.Host, feed) {
			return fmt.Errorf("subscription %q not granted in permissions.host", feed)
		}
	}
	if mode == PaneModeStatic {
		if len(d.View) == 0 {
			return fmt.Errorf("static pane requires view")
		}
		if strings.TrimSpace(d.Command) != "" {
			return fmt.Errorf("static pane must not set command")
		}
	}
	if mode == PaneModeProcess {
		if strings.TrimSpace(d.Command) == "" {
			return fmt.Errorf("process pane requires command")
		}
		if err := validateRelPathSyntax(d.Command); err != nil {
			// Allow reviewed absolute paths later via Resolve; relative is the common case.
			if !filepath.IsAbs(d.Command) {
				return fmt.Errorf("process command: %w", err)
			}
		}
	}
	return nil
}

func validatePanePermissions(p PanePermissions, mode string) error {
	fs := strings.TrimSpace(p.FS)
	if fs == "" {
		fs = "none"
	}
	switch fs {
	case "none", "read-workspace", "read-write-workspace":
	default:
		return fmt.Errorf("permissions.fs %q invalid", p.FS)
	}
	if mode == PaneModeStatic && fs != "none" {
		return fmt.Errorf("static pane permissions.fs must be none")
	}
	net := strings.TrimSpace(p.Network)
	if net == "" {
		net = "none"
	}
	switch net {
	case "none":
	case "host-mediated":
		// v1 hosts may reject at load; definition parse still accepts the value
		// so diagnostics can name the grant. Loaders fail closed.
	default:
		return fmt.Errorf("permissions.network %q invalid", p.Network)
	}
	if mode == PaneModeStatic && net != "none" {
		return fmt.Errorf("static pane permissions.network must be none")
	}
	cmd := strings.TrimSpace(p.Command)
	if cmd == "" {
		cmd = "none"
	}
	if cmd != "none" {
		return fmt.Errorf("permissions.command %q rejected in v1 (must be none)", p.Command)
	}
	for _, feed := range p.Host {
		feed = strings.TrimSpace(feed)
		if feed == "" {
			continue
		}
		if _, ok := knownPaneFeeds[feed]; !ok {
			return fmt.Errorf("unknown permissions.host feed %q", feed)
		}
	}
	return nil
}

func hostFeedGranted(host []string, feed string) bool {
	for _, h := range host {
		if strings.TrimSpace(h) == feed {
			return true
		}
	}
	return false
}

// ClampPaneSizing applies ABI defaults and clamps (§9.1).
func ClampPaneSizing(s PaneSizing) PaneSizing {
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
	if s.Flex <= 0 {
		s.Flex = 1
	}
	return s
}

// ClampPaneTimeouts applies defaults and host ceilings (§10.1).
func ClampPaneTimeouts(t PaneTimeouts) PaneTimeouts {
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

// IsProcessPane reports whether a raw contributions.panes entry is process mode.
// Used for trust/executable fingerprinting without full definition load.
func IsProcessPane(pluginRoot string, raw json.RawMessage) bool {
	e, err := ParsePaneEntry(raw)
	if err != nil {
		return false
	}
	d, _, err := ReadPaneDefinition(pluginRoot, e.Path)
	if err != nil {
		// Fail closed: treat unreadable pane as potentially executable.
		return true
	}
	return d.Mode == PaneModeProcess
}

// HasProcessPanes reports whether the manifest contributes any process pane.
func HasProcessPanes(m Manifest, pluginRoot string) bool {
	if m.Format == FormatAPS {
		if skipStrikeCLI(m) || pluginRoot == "" {
			return false
		}
		return strikeCLIHasProcessPanes(pluginRoot)
	}
	for _, raw := range m.Contributions.Panes {
		if IsProcessPane(pluginRoot, raw) {
			return true
		}
	}
	return false
}
