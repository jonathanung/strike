package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// paneViewNode is one node in the pane/1 view tree.
type paneViewNode struct {
	Type       string          `json:"type"`
	Children   []paneViewNode  `json:"children"`
	Gap        int             `json:"gap"`
	Wrap       bool            `json:"wrap"`
	Text       string          `json:"text"`
	TextFrom   string          `json:"textFrom"`
	Style      string          `json:"style"`
	Truncate   string          `json:"truncate"`
	Entries    []paneKVEntry   `json:"entries"`
	Items      []paneListItem  `json:"items"`
	Selectable bool            `json:"selectable"`
	SelectedID string          `json:"selectedId"`
	Columns    []paneTableCol  `json:"columns"`
	Rows       []paneTableRow  `json:"rows"`
	Label      string          `json:"label"`
	Value      json.RawMessage `json:"value"`
	ValueFrom  string          `json:"valueFrom"`
	Max        json.RawMessage `json:"max"`
	MaxFrom    string          `json:"maxFrom"`
	UnknownMax json.RawMessage `json:"unknownMax"`
	Tone       string          `json:"tone"`
	Flex       int             `json:"flex"`
	Min        int             `json:"min"`
	Hint       string          `json:"hint"`
	Icon       string          `json:"icon"`
	Actions    map[string]any  `json:"actions"`
	Extra      map[string]any  `json:"-"`
}

type paneKVEntry struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	ValueFrom string `json:"valueFrom"`
}

type paneListItem struct {
	ID      string         `json:"id"`
	Label   string         `json:"label"`
	Detail  string         `json:"detail"`
	Icon    string         `json:"icon"`
	Actions map[string]any `json:"actions"`
}

type paneTableCol struct {
	ID     string `json:"id"`
	Header string `json:"header"`
	Width  int    `json:"width"`
}

type paneTableRow struct {
	Cells map[string]string `json:"cells"`
}

// paneDataStore holds latest subscription snapshots keyed by feed id.
type paneDataStore map[string]map[string]any

func parsePaneView(raw json.RawMessage) (paneViewNode, error) {
	if len(raw) == 0 {
		return paneViewNode{}, fmt.Errorf("empty view")
	}
	var n paneViewNode
	if err := json.Unmarshal(raw, &n); err != nil {
		return paneViewNode{}, err
	}
	return n, nil
}

// renderPaneView paints a view tree into width×height cells using theme tokens.
// Panics are recovered by the caller (pluginPaneWindow.view).
func renderPaneView(th theme.Theme, width, height int, root paneViewNode, data paneDataStore) string {
	if width <= 0 {
		return ""
	}
	th = th.Resolve()
	var nodes int
	lines := renderPaneNode(th, width, root, data, 0, &nodes)
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func renderPaneNode(th theme.Theme, width int, n paneViewNode, data paneDataStore, depth int, nodes *int) []string {
	*nodes++
	if *nodes > paneMaxNodes || depth > paneMaxDepth {
		return []string{stylePaneRole(th, "muted", "view truncated")}
	}
	typ := strings.TrimSpace(n.Type)
	switch typ {
	case "column":
		return renderPaneColumn(th, width, n, data, depth, nodes)
	case "row":
		return renderPaneRow(th, width, n, data, depth, nodes)
	case "text":
		return []string{renderPaneText(th, width, n, data)}
	case "markdown":
		// Constrained: plain paragraphs only in v1 TUI host.
		return []string{renderPaneText(th, width, n, data)}
	case "kv":
		return renderPaneKV(th, width, n, data)
	case "list":
		return renderPaneList(th, width, n)
	case "table":
		return renderPaneTable(th, width, n)
	case "meter":
		return []string{renderPaneMeter(th, width, n, data)}
	case "badge":
		return []string{renderPaneBadge(th, n)}
	case "spacer":
		min := n.Min
		if min <= 0 {
			min = 1
		}
		if min > 8 {
			min = 8
		}
		out := make([]string, min)
		for i := range out {
			out[i] = ""
		}
		return out
	case "divider":
		h := th.Resolve().BorderStyle.Horizontal
		if h == "" {
			h = "-"
		}
		rule := strings.Repeat(h, max(1, width))
		return []string{stylePaneRole(th, "muted", rule)}
	case "empty":
		lines := []string{stylePaneRole(th, "muted", sanitizePaneText(n.Text))}
		if h := strings.TrimSpace(n.Hint); h != "" {
			lines = append(lines, stylePaneRole(th, "muted", sanitizePaneText(h)))
		}
		return clampPaneLines(th, lines, width)
	case "":
		return []string{stylePaneRole(th, "muted", "unsupported: (missing type)")}
	default:
		return []string{stylePaneRole(th, "muted", "unsupported: "+typ)}
	}
}

func renderPaneColumn(th theme.Theme, width int, n paneViewNode, data paneDataStore, depth int, nodes *int) []string {
	gap := n.Gap
	if gap < 0 {
		gap = 0
	}
	if gap > 4 {
		gap = 4
	}
	var lines []string
	for i, child := range n.Children {
		if i > 0 && gap > 0 {
			for g := 0; g < gap; g++ {
				lines = append(lines, "")
			}
		}
		lines = append(lines, renderPaneNode(th, width, child, data, depth+1, nodes)...)
	}
	return lines
}

func renderPaneRow(th theme.Theme, width int, n paneViewNode, data paneDataStore, depth int, nodes *int) []string {
	if len(n.Children) == 0 {
		return nil
	}
	gap := n.Gap
	if gap < 0 {
		gap = 0
	}
	if gap > 4 {
		gap = 4
	}
	// Simple equal-width columns; wrap stacks when too narrow.
	nChild := len(n.Children)
	gutter := gap * (nChild - 1)
	if width < nChild*4+gutter || n.Wrap {
		// Fall back to column stack.
		return renderPaneColumn(th, width, n, data, depth, nodes)
	}
	colW := max(1, (width-gutter)/nChild)
	cols := make([][]string, nChild)
	maxH := 0
	for i, child := range n.Children {
		cols[i] = renderPaneNode(th, colW, child, data, depth+1, nodes)
		if len(cols[i]) > maxH {
			maxH = len(cols[i])
		}
	}
	gapStr := strings.Repeat(" ", gap)
	out := make([]string, maxH)
	for row := 0; row < maxH; row++ {
		parts := make([]string, 0, nChild*2)
		for i := 0; i < nChild; i++ {
			if i > 0 {
				parts = append(parts, gapStr)
			}
			cell := ""
			if row < len(cols[i]) {
				cell = cols[i][row]
			}
			// Pad plain width; ANSI-aware pad is approximate.
			pad := colW - ansi.StringWidth(cell)
			if pad > 0 {
				cell += strings.Repeat(" ", pad)
			}
			parts = append(parts, cell)
		}
		out[row] = strings.Join(parts, "")
	}
	return out
}

func renderPaneText(th theme.Theme, width int, n paneViewNode, data paneDataStore) string {
	text := n.Text
	if n.TextFrom != "" {
		text = resolvePanePath(data, n.TextFrom)
	}
	text = sanitizePaneText(text)
	styled := stylePaneRole(th, n.Style, text)
	return truncatePaneLine(styled, width, n.Truncate, th.Resolve().Icons.Ellipsis)
}

func renderPaneKV(th theme.Theme, width int, n paneViewNode, data paneDataStore) []string {
	th = th.Resolve()
	st := th.S()
	dash := th.Icons.DetailSeparator
	lines := make([]string, 0, len(n.Entries))
	for _, e := range n.Entries {
		val := e.Value
		if e.ValueFrom != "" {
			val = resolvePanePath(data, e.ValueFrom)
		}
		if strings.TrimSpace(val) == "" {
			val = dash
		}
		val = sanitizePaneText(val)
		key := sanitizePaneText(e.Key)
		label := st.Muted.Render(key)
		// budget for "key  value"
		gap := themedSpace(th.Spacing.XS)
		budget := max(4, width-ansi.StringWidth(key)-ansi.StringWidth(gap))
		val = truncatePaneLine(st.Text.Render(val), budget, "end", th.Icons.Ellipsis)
		line := label + gap + val
		lines = append(lines, truncatePaneLine(line, width, "end", th.Icons.Ellipsis))
	}
	return lines
}

func renderPaneList(th theme.Theme, width int, n paneViewNode) []string {
	th = th.Resolve()
	items := make([]ui.ListItem, 0, len(n.Items))
	cursor := 0
	for i, it := range n.Items {
		label := sanitizePaneText(it.Label)
		if it.Icon != "" {
			// Closed icon set is host-substituted; unknown names omitted.
			if g := paneIconGlyph(th, it.Icon); g != "" {
				label = g + " " + label
			}
		}
		items = append(items, ui.ListItem{
			Label:   label,
			Detail:  sanitizePaneText(it.Detail),
			Current: n.Selectable && it.ID != "" && it.ID == n.SelectedID,
		})
		if n.Selectable && it.ID == n.SelectedID {
			cursor = i
		}
	}
	if len(items) == 0 {
		return []string{stylePaneRole(th, "muted", "empty")}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  cursor,
		Width:   max(1, width),
		Visible: max(1, len(items)),
		Empty:   "empty",
	})
	return strings.Split(body, "\n")
}

func renderPaneTable(th theme.Theme, width int, n paneViewNode) []string {
	th = th.Resolve()
	st := th.S()
	cols := n.Columns
	if len(cols) == 0 {
		return nil
	}
	if len(cols) > 32 {
		cols = cols[:32]
	}
	// Equal columns when widths omitted.
	colW := make([]int, len(cols))
	fixed := 0
	flexN := 0
	for i, c := range cols {
		if c.Width > 0 {
			colW[i] = c.Width
			fixed += c.Width
		} else {
			flexN++
		}
	}
	remain := width - fixed - max(0, len(cols)-1)
	if flexN > 0 {
		each := max(3, remain/flexN)
		for i := range colW {
			if colW[i] == 0 {
				colW[i] = each
			}
		}
	}
	headerParts := make([]string, len(cols))
	for i, c := range cols {
		headerParts[i] = truncatePaneLine(st.Muted.Render(sanitizePaneText(c.Header)), colW[i], "end", th.Icons.Ellipsis)
		pad := colW[i] - ansi.StringWidth(headerParts[i])
		if pad > 0 {
			headerParts[i] += strings.Repeat(" ", pad)
		}
	}
	lines := []string{strings.Join(headerParts, " ")}
	rows := n.Rows
	if len(rows) > 200 {
		rows = rows[:200]
	}
	for _, r := range rows {
		parts := make([]string, len(cols))
		for i, c := range cols {
			cell := ""
			if r.Cells != nil {
				cell = r.Cells[c.ID]
			}
			parts[i] = truncatePaneLine(st.Text.Render(sanitizePaneText(cell)), colW[i], "end", th.Icons.Ellipsis)
			pad := colW[i] - ansi.StringWidth(parts[i])
			if pad > 0 {
				parts[i] += strings.Repeat(" ", pad)
			}
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	return lines
}

func renderPaneMeter(th theme.Theme, width int, n paneViewNode, data paneDataStore) string {
	th = th.Resolve()
	st := th.S()
	val := paneNumber(n.Value, n.ValueFrom, data)
	maxV := paneNumber(n.Max, n.MaxFrom, data)
	unknown := false
	if len(n.UnknownMax) > 0 {
		var b bool
		if json.Unmarshal(n.UnknownMax, &b) == nil {
			unknown = b
		} else {
			var path string
			if json.Unmarshal(n.UnknownMax, &path) == nil && path != "" {
				// path into data: treat missing/false as unknown
				s := resolvePanePath(data, path)
				unknown = s == "" || s == "false" || s == "0"
			}
		}
	}
	ratio := -1.0
	if !unknown && maxV > 0 {
		ratio = val / maxV
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
	}
	label := sanitizePaneText(n.Label)
	barW := min(12, max(4, width/3))
	if width < 16 {
		barW = 0
	}
	var body string
	if barW > 0 {
		body = ui.Meter(th, barW, ratio)
	}
	pair := formatPaneNumber(val) + "/" + formatPaneNumber(maxV)
	if unknown || maxV <= 0 {
		pair = formatPaneNumber(val) + "/" + th.Icons.DetailSeparator
	}
	gap := themedSpace(th.Spacing.XS)
	out := body
	if out != "" {
		out += gap
	}
	out += st.Text.Render(pair)
	if label != "" {
		out = st.Muted.Render(label) + gap + out
	}
	return truncatePaneLine(out, width, "end", th.Icons.Ellipsis)
}

func renderPaneBadge(th theme.Theme, n paneViewNode) string {
	tone := ui.ToneDefault
	switch strings.ToLower(strings.TrimSpace(n.Tone)) {
	case "accent":
		tone = ui.ToneAccent
	case "success":
		tone = ui.ToneSuccess
	case "warning":
		tone = ui.ToneWarning
	case "error":
		tone = ui.ToneError
	case "neutral", "":
		tone = ui.ToneMuted
	}
	return ui.Badge(th, tone, sanitizePaneText(n.Text))
}

func stylePaneRole(th theme.Theme, role, text string) string {
	th = th.Resolve()
	st := th.S()
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "title":
		return st.Title.Render(text)
	case "muted":
		return st.Muted.Render(text)
	case "accent":
		return st.Accent.Render(text)
	case "success":
		return st.Success.Render(text)
	case "warning":
		return st.Warning.Render(text)
	case "error":
		return st.Error.Render(text)
	case "danger":
		return st.Danger.Render(text)
	default:
		return st.Text.Render(text)
	}
}

func paneIconGlyph(th theme.Theme, name string) string {
	th = th.Resolve()
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "check", "ok":
		return th.Icons.OK
	case "warn", "warning":
		return th.Icons.Info // no dedicated warn glyph; Info is closest muted mark
	case "error", "err":
		return th.Icons.Err
	case "agent":
		return th.Icons.Agent
	case "folder", "file":
		return th.Icons.Dot
	default:
		return ""
	}
}

// sanitizePaneText strips CSI/OSC sequences and clamps payload size.
func sanitizePaneText(s string) string {
	if s == "" {
		return s
	}
	// Strip ESC sequences.
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			// Skip until letter terminator or end.
			i++
			for i < len(s) {
				c := s[i]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '@' {
					break
				}
				i++
			}
			continue
		}
		if s[i] < 0x20 && s[i] != '\t' {
			continue // drop other controls
		}
		b.WriteByte(s[i])
	}
	out := b.String()
	if len(out) > paneMaxTextBytes {
		out = out[:paneMaxTextBytes]
	}
	return out
}

func truncatePaneLine(s string, width int, mode, ellipsis string) string {
	if width <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= width {
		return s
	}
	if mode == "middle" {
		return truncateMiddle(s, width, ellipsis)
	}
	return ansi.Truncate(s, width, ellipsis)
}

func clampPaneLines(th theme.Theme, lines []string, width int) []string {
	ell := th.Resolve().Icons.Ellipsis
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = truncatePaneLine(l, width, "end", ell)
	}
	return out
}

// resolvePanePath walks feed snapshots. Paths are "feed.field" or "feed.nested.field".
// The first path segment may be a feed id containing dots (session.summary.cwd).
func resolvePanePath(data paneDataStore, path string) string {
	path = strings.TrimSpace(path)
	if path == "" || data == nil {
		return ""
	}
	// Try longest feed-id prefix match against known feeds.
	feeds := []string{"session.summary", "agents.roster", "usage", "clock"}
	var feed string
	var rest string
	for _, f := range feeds {
		if path == f {
			feed = f
			break
		}
		if strings.HasPrefix(path, f+".") {
			feed = f
			rest = path[len(f)+1:]
			break
		}
	}
	if feed == "" {
		// Fallback: first segment.
		parts := strings.SplitN(path, ".", 2)
		feed = parts[0]
		if len(parts) == 2 {
			rest = parts[1]
		}
	}
	snap, ok := data[feed]
	if !ok || snap == nil {
		return ""
	}
	if rest == "" {
		return fmt.Sprint(snap)
	}
	cur := any(snap)
	for _, seg := range strings.Split(rest, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur, ok = m[seg]
		if !ok {
			return ""
		}
	}
	switch v := cur.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "true"
		}
		return "false"
	case json.Number:
		return v.String()
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return string(b)
	}
}

func paneNumber(raw json.RawMessage, from string, data paneDataStore) float64 {
	if from != "" {
		s := resolvePanePath(data, from)
		if s == "" {
			return 0
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return f
	}
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		f, _ = strconv.ParseFloat(s, 64)
		return f
	}
	return 0
}

func formatPaneNumber(v float64) string {
	if v == float64(int64(v)) {
		return ui.FormatTokens(int(v))
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

// countPaneNodes walks a view for budget checks.
func countPaneNodes(n paneViewNode) int {
	total := 1
	for _, c := range n.Children {
		total += countPaneNodes(c)
	}
	return total
}
