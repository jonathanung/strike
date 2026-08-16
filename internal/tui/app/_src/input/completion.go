package tui

import (
	"strings"
	"unicode"

	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const (
	completionMaxRows = 6
)

type runeRange struct {
	Start int
	End   int
}

type completionCandidate struct {
	Spec   commandSpec
	Source commandSource
	// Path is set for @file mention candidates (project-relative, slash form).
	Path string
}

type completionState struct {
	Candidates []completionCandidate
	Selected   int
	Source     commandSource
	Replace    runeRange
	rows       int
	// fileMention is true when candidates are @file paths.
	fileMention bool
	// emptyHint explains a zero-result @file popup (ignored trees, no match).
	emptyHint string
}

func commandMatches(catalog []commandSpec, query string) []completionCandidate {
	query = strings.ToLower(strings.TrimPrefix(query, "/"))
	buckets := [3][]completionCandidate{}
	for _, spec := range catalog {
		name := strings.ToLower(strings.TrimPrefix(spec.Name, "/"))
		rank := -1
		switch {
		case name == query:
			rank = 0
		case strings.HasPrefix(name, query):
			rank = 1
		case orderedSubsequence(name, query):
			rank = 2
		}
		if rank >= 0 {
			buckets[rank] = append(buckets[rank], completionCandidate{Spec: spec, Source: spec.Source})
		}
	}
	matches := make([]completionCandidate, 0, len(catalog))
	for _, bucket := range buckets {
		matches = append(matches, bucket...)
	}
	return matches
}

func orderedSubsequence(value, query string) bool {
	if query == "" {
		return true
	}
	needle := []rune(query)
	matched := 0
	for _, r := range value {
		if r == needle[matched] {
			matched++
			if matched == len(needle) {
				return true
			}
		}
	}
	return false
}

func leadingSlashCompletion(value string, cursorRow, cursorCol int, catalog []commandSpec) *completionState {
	if cursorRow != 0 || cursorCol <= 0 {
		return nil
	}
	lines := strings.Split(value, "\n")
	first := []rune(lines[0])
	if len(first) == 0 || first[0] != '/' {
		return nil
	}
	end := 0
	for end < len(first) && !unicode.IsSpace(first[end]) {
		end++
	}
	if cursorCol > end {
		return nil
	}
	queryEnd := min(cursorCol, end)
	matches := commandMatches(catalog, string(first[1:queryEnd]))
	if len(matches) == 0 {
		return nil
	}
	return &completionState{
		Candidates: matches,
		Source:     matches[0].Source,
		Replace:    runeRange{Start: 0, End: end},
	}
}

// atFileCompletion opens @path fuzzy completion when the cursor sits inside an
// @-token that begins after whitespace or at the start of a line. emptyHint is
// shown when paths is empty (still opens the popup so the user knows why).
func atFileCompletion(value string, cursorRow, cursorCol int, paths []string, emptyHint string) *completionState {
	lines := strings.Split(value, "\n")
	if cursorRow < 0 || cursorRow >= len(lines) {
		return nil
	}
	line := []rune(lines[cursorRow])
	if cursorCol < 0 || cursorCol > len(line) {
		return nil
	}
	// Token end is the cursor; walk left for path chars then require '@'.
	end := cursorCol
	start := end
	for start > 0 {
		r := line[start-1]
		if isFileMentionPathRune(r) {
			start--
			continue
		}
		if r == '@' {
			start--
			break
		}
		return nil
	}
	if start >= end || start >= len(line) || line[start] != '@' {
		return nil
	}
	// '@' must be at line start or after whitespace (avoid emails).
	if start > 0 && !unicode.IsSpace(line[start-1]) {
		return nil
	}
	// Cursor must be inside the token (after '@').
	if cursorCol <= start {
		return nil
	}
	query := string(line[start+1 : end])
	// Reject queries with characters we would never insert from the picker.
	for _, r := range query {
		if !isFileMentionPathRune(r) {
			return nil
		}
	}
	matches := make([]completionCandidate, 0, len(paths))
	for _, p := range paths {
		matches = append(matches, completionCandidate{Path: p})
	}
	if len(matches) == 0 && emptyHint == "" {
		return nil
	}
	// Extend replace end through any remaining path runes after the cursor so
	// accepting mid-token rewrites the whole mention.
	tokenEnd := end
	for tokenEnd < len(line) && isFileMentionPathRune(line[tokenEnd]) {
		tokenEnd++
	}
	return &completionState{
		Candidates:  matches,
		Replace:     runeRange{Start: runeOffset(value, cursorRow, start), End: runeOffset(value, cursorRow, tokenEnd)},
		fileMention: true,
		emptyHint:   emptyHint,
	}
}

func isFileMentionPathRune(r rune) bool {
	switch r {
	case '/', '\\', '.', '-', '_', '+', '~':
		return true
	default:
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}
}

// runeOffset returns the rune index into value for (row, col) in its lines.
func runeOffset(value string, row, col int) int {
	if row < 0 {
		return 0
	}
	off := 0
	lines := strings.Split(value, "\n")
	for i := 0; i < len(lines) && i < row; i++ {
		off += len([]rune(lines[i])) + 1
	}
	if row >= len(lines) {
		return off
	}
	line := []rune(lines[row])
	if col > len(line) {
		col = len(line)
	}
	if col < 0 {
		col = 0
	}
	return off + col
}

func (c *completionState) move(delta int) {
	if len(c.Candidates) == 0 {
		return
	}
	c.Selected = (c.Selected + delta + len(c.Candidates)) % len(c.Candidates)
	c.Source = c.Candidates[c.Selected].Source
}

// view renders the inline completion popup as a bordered panel of candidate
// rows above the composer. c.rows (set by reflow) bounds the visible window;
// keyboard handling stays in the app model.
func (c *completionState) view(width, height int, th theme.Theme) string {
	if c == nil {
		return ""
	}
	if height <= 0 || width <= 0 {
		return ""
	}
	if len(c.Candidates) == 0 && c.emptyHint == "" {
		return ""
	}
	popupWidth := width
	th = th.Resolve()
	items := make([]ui.ListItem, len(c.Candidates))
	for i, candidate := range c.Candidates {
		name := candidate.Spec.Name
		detail := candidate.Spec.Description
		if candidate.Path != "" {
			name = "@" + candidate.Path
			detail = "file"
			if strings.HasSuffix(candidate.Path, "/") {
				detail = "folder"
			}
		} else if candidate.Spec.ArgsHint != "" {
			name += themedSpace(th.Spacing.XS) + candidate.Spec.ArgsHint
		}
		items[i] = ui.ListItem{Label: name, Detail: detail}
	}
	minChrome := ui.ChromeMinOuter(th)
	borderless := height < 3 || popupWidth < minChrome
	bodyWidth := max(1, ui.PanelInnerWidth(th, popupWidth))
	bodyHeight := max(0, height-2)
	if borderless {
		bodyWidth = max(1, popupWidth)
		bodyHeight = height
	}
	empty := c.emptyHint
	if empty == "" {
		empty = "no matches"
	}
	visible := bodyHeight
	if len(c.Candidates) > 0 {
		visible = min(bodyHeight, len(c.Candidates))
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  c.Selected,
		Width:   bodyWidth,
		Visible: visible,
		Empty:   empty,
	})
	return ui.Panel(th, ui.PanelOpts{Width: popupWidth, Height: height, Borderless: borderless, Focused: true}, body)
}
