package ui

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/common"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

// Canvas fits body to the terminal rectangle. A solid theme background is
// applied to every cell here, after all views and overlays have composed.
// Wide-neutral historic scripts are padded before cut so a last-chance width
// clamp matches double-cell terminal paint (#689).
func Canvas(th theme.Theme, width, height int, body string) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	rows := strings.Split(body, "\n")
	start := max(0, len(rows)-height)
	inherited := ""
	if start > 0 {
		inherited = activeTerminalState(strings.Join(rows[:start], "\n"))
	}
	background := theme.BackgroundPrefix(th.Resolve().Background)
	out := make([]string, height)
	for i := range out {
		row := ""
		if i+start < len(rows) {
			row = ansi.Cut(common.PadWideGlyphs(rows[i+start]), 0, width)
		}
		rowState := ""
		if i == 0 {
			rowState = inherited
		}
		if background != "" {
			row = background + restoreBackground(rowState+row, background)
		} else {
			row = rowState + row
		}
		padding := width - ansi.StringWidth(row)
		if padding > 0 {
			row += background + strings.Repeat(" ", padding)
		}
		out[i] = row
	}
	return strings.Join(out, "\n")
}

// restoreBackground reapplies Canvas's solid background after SGR that clears
// the background (full reset or default-background). Explicit nested surface
// fills (48 / 40–47 / 100–107) are preserved so solid panel chrome survives
// the final canvas pass. Extended-color payloads are consumed as a group, so
// an RGB component is never mistaken for a later SGR code.
func restoreBackground(s, background string) string {
	if background == "" {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		// Do not interpret a UTF-8 continuation byte as a raw C1 control.
		if _, size := utf8.DecodeRuneInString(s[i:]); size > 1 {
			b.WriteString(s[i : i+size])
			i += size
			continue
		}
		if end, params, ok := csiSequence(s, i); ok {
			b.WriteString(s[i:end])
			if s[end-1] == 'm' && sgrClearsBackground(params) {
				b.WriteString(background)
			}
			i = end
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func csiSequence(s string, start int) (end int, params string, ok bool) {
	paramsStart := start + 1
	if s[start] == '\x1b' {
		if start+1 >= len(s) || s[start+1] != '[' {
			return 0, "", false
		}
		paramsStart++
	} else if s[start] != '\x9b' {
		return 0, "", false
	}
	end = paramsStart
	for end < len(s) && (s[end] < '@' || s[end] > '~') {
		end++
	}
	if end == len(s) {
		return 0, "", false
	}
	return end + 1, s[paramsStart:end], true
}

func activeTerminalState(s string) string {
	var sgr sgrState
	var hyperlink string
	for i := 0; i < len(s); {
		// A raw C1 byte is invalid as a UTF-8 leading byte, while the same byte
		// can be a continuation byte in an ordinary Unicode rune. Advance past
		// valid runes before recognizing byte-oriented terminal controls.
		if _, size := utf8.DecodeRuneInString(s[i:]); size > 1 {
			i += size
			continue
		}
		if end, params, ok := csiSequence(s, i); ok {
			if s[end-1] == 'm' {
				prefix := "\x1b["
				if s[i] == '\x9b' {
					prefix = "\x9b"
				}
				sgr.apply(params, prefix)
			}
			i = end
			continue
		}
		if end, data, ok := oscSequence(s, i); ok {
			if strings.HasPrefix(data, "8;") {
				if _, uri, ok := strings.Cut(data[2:], ";"); ok && uri != "" {
					hyperlink = s[i:end]
				} else {
					hyperlink = ""
				}
			}
			i = end
			continue
		}
		i++
	}
	return sgr.String() + hyperlink
}

func oscSequence(s string, start int) (end int, data string, ok bool) {
	dataStart := start + 1
	if s[start] == '\x1b' {
		if start+1 >= len(s) || s[start+1] != ']' {
			return 0, "", false
		}
		dataStart++
	} else if s[start] != '\x9d' {
		return 0, "", false
	}
	for end = dataStart; end < len(s); end++ {
		if s[end] == '\a' || s[end] == '\x9c' {
			return end + 1, s[dataStart:end], true
		}
		if s[end] == '\x1b' && end+1 < len(s) && s[end+1] == '\\' {
			return end + 2, s[dataStart:end], true
		}
	}
	return 0, "", false
}

type sgrState struct {
	attrs map[string]string
}

func (s *sgrState) apply(params, prefix string) {
	if s.attrs == nil {
		s.attrs = make(map[string]string)
	}
	parts := strings.Split(params, ";")
	if params == "" {
		parts = []string{"0"}
	}
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			part = "0"
		}
		code, ok := sgrCode(part)
		if !ok {
			continue
		}
		key := sgrKey(code)
		if code == 0 {
			clear(s.attrs)
			continue
		}
		if key == "" {
			continue
		}
		if sgrReset(code) {
			delete(s.attrs, key)
			continue
		}
		value := part
		if (code == 38 || code == 48 || code == 58) && !strings.Contains(part, ":") {
			count := 1
			if i+1 < len(parts) && parts[i+1] == "2" {
				count = 5
			} else if i+1 < len(parts) && parts[i+1] == "5" {
				count = 3
			}
			if i+count <= len(parts) {
				value = strings.Join(parts[i:i+count], ";")
				i += count - 1
			}
		}
		s.attrs[key] = prefix + value + "m"
	}
}

func (s sgrState) String() string {
	if len(s.attrs) == 0 {
		return ""
	}
	keys := []string{"intensity", "italic", "underline", "blink", "reverse", "conceal", "strike", "foreground", "background", "underline-color"}
	values := make([]string, 0, len(s.attrs))
	for _, key := range keys {
		if value := s.attrs[key]; value != "" {
			values = append(values, value)
		}
	}
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, "")
}

func sgrKey(code int) string {
	switch code {
	case 1, 2, 22:
		return "intensity"
	case 3, 23:
		return "italic"
	case 4, 21, 24:
		return "underline"
	case 5, 6, 25:
		return "blink"
	case 7, 27:
		return "reverse"
	case 8, 28:
		return "conceal"
	case 9, 29:
		return "strike"
	case 39:
		return "foreground"
	case 49:
		return "background"
	case 59:
		return "underline-color"
	case 38:
		return "foreground"
	case 48:
		return "background"
	case 58:
		return "underline-color"
	}
	if (code >= 30 && code <= 37) || (code >= 90 && code <= 97) {
		return "foreground"
	}
	if (code >= 40 && code <= 47) || (code >= 100 && code <= 107) {
		return "background"
	}
	return ""
}

func sgrCode(param string) (int, bool) {
	code, _, _ := strings.Cut(param, ":")
	value, err := strconv.Atoi(code)
	return value, err == nil
}

func sgrReset(code int) bool {
	return code == 22 || code == 23 || code == 24 || code == 25 || code == 27 || code == 28 || code == 29 || code == 39 || code == 49 || code == 59
}

// sgrClearsBackground reports whether params reset the background back to the
// terminal default (full reset or SGR 49). Explicit background sets are not
// clears — panel surfaces must survive the canvas pass.
func sgrClearsBackground(params string) bool {
	if params == "" {
		return true
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if part == "" {
			part = "0"
		}
		code, ok := sgrCode(part)
		if !ok {
			continue
		}
		if code == 0 || code == 49 {
			return true
		}
		if (code == 38 || code == 48 || code == 58) && !strings.Contains(part, ":") {
			if i+1 < len(parts) && parts[i+1] == "2" {
				i += min(4, len(parts)-i-1)
			} else if i+1 < len(parts) && parts[i+1] == "5" {
				i += min(2, len(parts)-i-1)
			}
		}
	}
	return false
}
