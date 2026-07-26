package theme

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// fileDoc is the on-disk JSON shape for a theme. Colors map to Theme roles;
// unset roles fall through Theme.Resolve to Default.
type fileDoc struct {
	Name   string                     `json:"name"`
	ID     string                     `json:"id"`
	Defs   map[string]string          `json:"defs"`
	Colors map[string]json.RawMessage `json:"colors"`
	Border string                     `json:"border"`
	Icons  *fileIcons                 `json:"icons"`
}

type fileIcons struct {
	Prompt, Assistant, Tool, OK, Err, Info, Agent, Bolt, Dot *string
	Cursor, InputCursor, FilterCursor, ToolGuide             *string
	BadgeLeft, BadgeRight, DetailSeparator, Ellipsis         *string
	LogoTopRule, LogoBottomRule, MeterFill, MeterEmpty       *string
}

// Parse decodes a JSON theme document into an Entry. idHint is used when the
// document omits id (typically the file stem). Partial color maps are valid;
// missing roles resolve from Default at apply time.
func Parse(data []byte, idHint string) (Entry, error) {
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return Entry{}, fmt.Errorf("theme json: %w", err)
	}
	id := strings.TrimSpace(doc.ID)
	if id == "" {
		id = strings.TrimSpace(idHint)
	}
	if id == "" {
		return Entry{}, fmt.Errorf("theme id is required")
	}
	if !validThemeID(id) {
		return Entry{}, fmt.Errorf("invalid theme id %q", id)
	}
	name := strings.TrimSpace(doc.Name)
	if name == "" {
		name = id
	}
	defs := map[string]string{}
	for k, v := range doc.Defs {
		defs[k] = strings.TrimSpace(v)
	}
	th := Theme{}
	for role, raw := range doc.Colors {
		if err := applyColor(&th, role, raw, defs); err != nil {
			return Entry{}, fmt.Errorf("colors.%s: %w", role, err)
		}
	}
	switch strings.ToLower(strings.TrimSpace(doc.Border)) {
	case "", "light":
		// default / light left unset so Resolve supplies the light preset
		if doc.Border != "" {
			th.BorderStyle = lightBorderStyle()
		}
	case "heavy":
		th.BorderStyle = heavyBorderStyle()
	default:
		return Entry{}, fmt.Errorf("unknown border %q (want light or heavy)", doc.Border)
	}
	if doc.Icons != nil {
		th.Icons = applyIcons(*doc.Icons)
	}
	return Entry{ID: id, Name: name, Theme: th.Resolve()}, nil
}

func applyColor(th *Theme, role string, raw json.RawMessage, defs map[string]string) error {
	light, dark, transparent, err := decodeColorValue(raw, defs)
	if err != nil {
		return err
	}
	if transparent {
		if role != "background" {
			return fmt.Errorf(`"none" is only valid for background`)
		}
		th.Background = NoBackground()
		return nil
	}
	c := lipgloss.AdaptiveColor{Light: light, Dark: dark}
	switch role {
	case "text":
		th.Text = c
	case "textMuted":
		th.TextMuted = c
	case "accent":
		th.Accent = c
	case "accentAlt":
		th.AccentAlt = c
	case "highlight":
		th.Highlight = c
	case "success":
		th.Success = c
	case "warning":
		th.Warning = c
	case "error":
		th.Error = c
	case "danger":
		th.Danger = c
	case "background":
		th.Background = c
	case "border":
		th.Border = c
	case "borderFocus":
		th.BorderFocus = c
	case "borderMuted":
		th.BorderMuted = c
	case "userLabel":
		th.UserLabel = c
	case "toolLabel":
		th.ToolLabel = c
	case "diffAdded":
		th.DiffAdded = c
	case "diffRemoved":
		th.DiffRemoved = c
	case "overlayScrim":
		th.OverlayScrim = c
	default:
		return fmt.Errorf("unknown color role %q", role)
	}
	return nil
}

func decodeColorValue(raw json.RawMessage, defs map[string]string) (light, dark string, transparent bool, err error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return "", "", false, fmt.Errorf("empty color")
	}
	// Single string: hex, def ref, or "none".
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return resolveColorString(s, defs)
	}
	var pair struct {
		Light string `json:"light"`
		Dark  string `json:"dark"`
	}
	if err := json.Unmarshal(raw, &pair); err != nil {
		return "", "", false, fmt.Errorf("want hex string, def name, \"none\", or {light,dark}")
	}
	if strings.TrimSpace(pair.Light) == "" && strings.TrimSpace(pair.Dark) == "" {
		return "", "", false, fmt.Errorf("light and dark are both empty")
	}
	// Allow one side to fill the other when only one is set.
	if strings.TrimSpace(pair.Light) == "" {
		pair.Light = pair.Dark
	}
	if strings.TrimSpace(pair.Dark) == "" {
		pair.Dark = pair.Light
	}
	l, _, lt, err := resolveColorString(pair.Light, defs)
	if err != nil {
		return "", "", false, fmt.Errorf("light: %w", err)
	}
	d, _, dt, err := resolveColorString(pair.Dark, defs)
	if err != nil {
		return "", "", false, fmt.Errorf("dark: %w", err)
	}
	if lt || dt {
		if !lt || !dt {
			return "", "", false, fmt.Errorf(`"none" must be used for both light and dark`)
		}
		return "", "", true, nil
	}
	return l, d, false, nil
}

func resolveColorString(s string, defs map[string]string) (light, dark string, transparent bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", false, fmt.Errorf("empty color")
	}
	if strings.EqualFold(s, "none") {
		return "", "", true, nil
	}
	// Resolve def refs (non-hex identifiers).
	if !strings.HasPrefix(s, "#") {
		if v, ok := defs[s]; ok {
			s = strings.TrimSpace(v)
		} else if _, err := strconvParseHexish(s); err != nil {
			// bare ANSI slot 0-255 is allowed as a single color token
			if isANSIIndex(s) {
				return s, s, false, nil
			}
			return "", "", false, fmt.Errorf("unknown color ref %q", s)
		}
	}
	if strings.EqualFold(s, "none") {
		return "", "", true, nil
	}
	if err := validateColorToken(s); err != nil {
		return "", "", false, err
	}
	return s, s, false, nil
}

func validateColorToken(s string) error {
	if isANSIIndex(s) {
		return nil
	}
	if !strings.HasPrefix(s, "#") {
		return fmt.Errorf("color %q must be #hex or 0-255", s)
	}
	hex := s[1:]
	switch len(hex) {
	case 3, 4, 6, 8:
	default:
		return fmt.Errorf("hex color %q has invalid length", s)
	}
	for _, r := range hex {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return fmt.Errorf("hex color %q has non-hex digits", s)
		}
	}
	return nil
}

func isANSIIndex(s string) bool {
	if s == "" {
		return false
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
		n = n*10 + int(r-'0')
		if n > 255 {
			return false
		}
	}
	return true
}

func strconvParseHexish(s string) (string, error) {
	if strings.HasPrefix(s, "#") {
		return s, validateColorToken(s)
	}
	return "", fmt.Errorf("not hex")
}

func applyIcons(doc fileIcons) Icons {
	var i Icons
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	set(&i.Prompt, doc.Prompt)
	set(&i.Assistant, doc.Assistant)
	set(&i.Tool, doc.Tool)
	set(&i.OK, doc.OK)
	set(&i.Err, doc.Err)
	set(&i.Info, doc.Info)
	set(&i.Agent, doc.Agent)
	set(&i.Bolt, doc.Bolt)
	set(&i.Dot, doc.Dot)
	set(&i.Cursor, doc.Cursor)
	set(&i.InputCursor, doc.InputCursor)
	set(&i.FilterCursor, doc.FilterCursor)
	set(&i.ToolGuide, doc.ToolGuide)
	set(&i.BadgeLeft, doc.BadgeLeft)
	set(&i.BadgeRight, doc.BadgeRight)
	set(&i.DetailSeparator, doc.DetailSeparator)
	set(&i.Ellipsis, doc.Ellipsis)
	set(&i.LogoTopRule, doc.LogoTopRule)
	set(&i.LogoBottomRule, doc.LogoBottomRule)
	set(&i.MeterFill, doc.MeterFill)
	set(&i.MeterEmpty, doc.MeterEmpty)
	return i
}

func validThemeID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}
